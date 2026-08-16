package cicheck

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSecurityWorkflowTrustBoundaryAndJobs(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../.github/workflows/security.yml")
	if err != nil {
		t.Fatalf("read security workflow: %v", err)
	}
	workflowText := string(contents)
	if strings.Contains(workflowText, "pull_request_target") {
		t.Fatal("security workflow must use pull_request, never pull_request_target")
	}
	if secretReference.MatchString(workflowText) {
		t.Fatal("security workflow must not read repository secrets")
	}

	var workflow workflowDocument
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatalf("parse security workflow: %v", err)
	}
	if workflow.Name != "Security" {
		t.Fatalf("workflow name = %q, want Security", workflow.Name)
	}
	for _, event := range []string{"pull_request", "push", "schedule", "workflow_dispatch"} {
		if _, ok := workflow.On[event]; !ok {
			t.Errorf("security workflow trigger %q is missing", event)
		}
	}
	if len(workflow.On) != 4 {
		t.Errorf("security workflow has unexpected triggers: %#v", workflow.On)
	}
	var pushConfig struct {
		Branches []string `yaml:"branches"`
	}
	pushNode := workflow.On["push"]
	if err := pushNode.Decode(&pushConfig); err != nil {
		t.Fatalf("decode security push trigger: %v", err)
	}
	if !reflect.DeepEqual(pushConfig.Branches, []string{"main"}) {
		t.Errorf("security push branches = %#v, want only main", pushConfig.Branches)
	}
	var schedules []struct {
		Cron string `yaml:"cron"`
	}
	scheduleNode := workflow.On["schedule"]
	if err := scheduleNode.Decode(&schedules); err != nil {
		t.Fatalf("decode security schedule: %v", err)
	}
	if len(schedules) != 1 || schedules[0].Cron != "17 3 * * 1" {
		t.Errorf("security schedule = %#v, want weekly Monday 03:17 UTC", schedules)
	}
	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		t.Fatalf("security top-level permissions = %#v, want only contents: read", workflow.Permissions)
	}
	if workflow.Concurrency.Group != `${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}` {
		t.Errorf("unexpected security concurrency group %q", workflow.Concurrency.Group)
	}
	if workflow.Concurrency.CancelInProgress != `${{ github.event_name == 'pull_request' }}` {
		t.Errorf("unexpected security cancellation rule %q", workflow.Concurrency.CancelInProgress)
	}
	if !reflect.DeepEqual(workflow.Env, map[string]string{
		"GOTOOLCHAIN":        "local",
		"GO_MIN_VERSION":     "1.25.13",
		"GO_CURRENT_VERSION": "1.26.6",
	}) {
		t.Errorf("security env = %#v, want fixed Go policy", workflow.Env)
	}

	expectedJobs := map[string]struct {
		name           string
		timeoutMinutes int
		actions        []string
	}{
		"govulncheck": {
			name: "Security / govulncheck", timeoutMinutes: 15,
			actions: []string{checkoutAction, setupGoAction, setupGoAction},
		},
		"dependency-review": {
			name: "Security / dependency-review", timeoutMinutes: 10,
			actions: []string{dependencyReviewAction},
		},
		"codeql": {
			name: "Security / codeql", timeoutMinutes: 25,
			actions: []string{checkoutAction, setupGoAction, codeQLInitAction, codeQLAnalyzeAction},
		},
	}
	if len(workflow.Jobs) != len(expectedJobs) {
		t.Fatalf("security workflow has %d jobs, want %d", len(workflow.Jobs), len(expectedJobs))
	}
	for jobID, expected := range expectedJobs {
		job, ok := workflow.Jobs[jobID]
		if !ok {
			t.Errorf("required security job %q is missing", jobID)
			continue
		}
		if job.Name != expected.name {
			t.Errorf("job %q name = %q, want %q", jobID, job.Name, expected.name)
		}
		if job.RunsOn != "ubuntu-24.04" {
			t.Errorf("job %q runner = %q, want ubuntu-24.04", jobID, job.RunsOn)
		}
		if job.TimeoutMinutes != expected.timeoutMinutes {
			t.Errorf("job %q timeout = %d, want %d", jobID, job.TimeoutMinutes, expected.timeoutMinutes)
		}
		if job.Uses != "" || job.Secrets.Kind != 0 {
			t.Errorf("job %q must not call a reusable workflow or pass secrets", jobID)
		}
		if jobID == "dependency-review" && job.If != `${{ github.event_name == 'pull_request' }}` {
			t.Errorf("dependency review condition = %q, want pull requests only", job.If)
		}
		if jobID != "dependency-review" && job.If != "" {
			t.Errorf("job %q has unexpected condition %q", jobID, job.If)
		}

		var actions []string
		var commands []string
		var setupGoVersions []string
		for _, step := range job.Steps {
			if step.Run != "" {
				commands = append(commands, step.Run)
			}
			if step.Uses == "" {
				continue
			}
			actions = append(actions, step.Uses)
			if !pinnedAction.MatchString(step.Uses) {
				t.Errorf("job %q uses mutable Action reference %q", jobID, step.Uses)
			}
			switch step.Uses {
			case checkoutAction:
				if step.With["persist-credentials"] != false {
					t.Errorf("job %q checkout must set persist-credentials: false", jobID)
				}
			case setupGoAction:
				version := fmt.Sprint(step.With["go-version"])
				if step.With["cache"] != true {
					t.Errorf("job %q has unexpected setup-go inputs: %#v", jobID, step.With)
				}
				if jobID == "govulncheck" {
					setupGoVersions = append(setupGoVersions, version)
					if version != `${{ env.GO_MIN_VERSION }}` && version != `${{ env.GO_CURRENT_VERSION }}` {
						t.Errorf("job %q has unsupported Go version %q", jobID, version)
					}
				} else if version != `${{ env.GO_CURRENT_VERSION }}` {
					t.Errorf("job %q has unexpected Go version %q", jobID, version)
				}
			case dependencyReviewAction:
				if fmt.Sprint(step.With["fail-on-severity"]) != "high" {
					t.Errorf("dependency review has unexpected inputs: %#v", step.With)
				}
			case codeQLInitAction:
				if fmt.Sprint(step.With["languages"]) != "go" || fmt.Sprint(step.With["build-mode"]) != "autobuild" {
					t.Errorf("CodeQL init has unexpected inputs: %#v", step.With)
				}
			case codeQLAnalyzeAction:
				if fmt.Sprint(step.With["category"]) != "/language:go" {
					t.Errorf("CodeQL analyze has unexpected inputs: %#v", step.With)
				}
			}
		}
		if !reflect.DeepEqual(actions, expected.actions) {
			t.Errorf("job %q actions = %#v, want %#v", jobID, actions, expected.actions)
		}
		if jobID == "govulncheck" {
			if !reflect.DeepEqual(setupGoVersions, []string{`${{ env.GO_MIN_VERSION }}`, `${{ env.GO_CURRENT_VERSION }}`}) {
				t.Errorf("job %q Go setup versions = %#v, want minimum then current", jobID, setupGoVersions)
			}
			govulncheckCommand := "go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./..."
			if strings.Count(strings.Join(commands, "\n"), govulncheckCommand) != 2 {
				t.Errorf("job %q must run govulncheck@v1.1.4 against both Go lanes", jobID)
			}
		}
	}

	codeQLPermissions := workflow.Jobs["codeql"].Permissions
	if !reflect.DeepEqual(codeQLPermissions, map[string]string{
		"actions":         "read",
		"contents":        "read",
		"security-events": "write",
	}) {
		t.Errorf("CodeQL permissions = %#v, want read-only plus security-events: write", codeQLPermissions)
	}
	dependencyPermissions := workflow.Jobs["dependency-review"].Permissions
	if !reflect.DeepEqual(dependencyPermissions, map[string]string{
		"contents":      "read",
		"pull-requests": "read",
	}) {
		t.Errorf("dependency review permissions = %#v, want contents/pull-requests read", dependencyPermissions)
	}
}
