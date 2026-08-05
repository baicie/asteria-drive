package cicheck

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	checkoutAction         = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
	setupGoAction          = "actions/setup-go@d35c59abb061a4a6fb18e82ac0862c26744d6ab5"
	setupNodeAction        = "actions/setup-node@820762786026740c76f36085b0efc47a31fe5020"
	uploadArtifactAction   = "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"
	dependencyReviewAction = "actions/dependency-review-action@a1d282b36b6f3519aa1f3fc636f609c47dddb294"
	codeQLInitAction       = "github/codeql-action/init@f205ea1c3313d32999d8d6a48b4f6530d4437b38"
	codeQLAnalyzeAction    = "github/codeql-action/analyze@f205ea1c3313d32999d8d6a48b4f6530d4437b38"
)

var (
	pinnedAction    = regexp.MustCompile(`^[^@[:space:]]+@[0-9a-f]{40}$`)
	secretReference = regexp.MustCompile(`(?i)\bsecrets\b`)
)

type workflowDocument struct {
	Name        string               `yaml:"name"`
	On          map[string]yaml.Node `yaml:"on"`
	Permissions map[string]string    `yaml:"permissions"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress string `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Env  map[string]string      `yaml:"env"`
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Name           string            `yaml:"name"`
	If             string            `yaml:"if"`
	RunsOn         string            `yaml:"runs-on"`
	TimeoutMinutes int               `yaml:"timeout-minutes"`
	Permissions    map[string]string `yaml:"permissions"`
	Env            map[string]any    `yaml:"env"`
	Uses           string            `yaml:"uses"`
	Secrets        yaml.Node         `yaml:"secrets"`
	Steps          []workflowStep    `yaml:"steps"`
}

type workflowStep struct {
	Name string         `yaml:"name"`
	If   string         `yaml:"if"`
	Run  string         `yaml:"run"`
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
}

func TestCIWorkflowTrustBoundaryAndStableJobs(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	workflowText := string(contents)
	if strings.Contains(workflowText, "pull_request_target") {
		t.Fatal("CI must use pull_request, never pull_request_target")
	}
	if secretReference.MatchString(workflowText) {
		t.Fatal("initial PR checks must not read repository secrets")
	}

	var workflow workflowDocument
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}

	if workflow.Name != "CI" {
		t.Fatalf("workflow name = %q, want CI", workflow.Name)
	}
	for _, event := range []string{"pull_request", "push", "workflow_dispatch"} {
		if _, ok := workflow.On[event]; !ok {
			t.Errorf("workflow trigger %q is missing", event)
		}
	}
	if len(workflow.On) != 3 {
		t.Errorf("workflow has unexpected triggers: %#v", workflow.On)
	}
	var pushConfig struct {
		Branches []string `yaml:"branches"`
	}
	pushNode := workflow.On["push"]
	if err := pushNode.Decode(&pushConfig); err != nil {
		t.Fatalf("decode push trigger: %v", err)
	}
	if !reflect.DeepEqual(pushConfig.Branches, []string{"main"}) {
		t.Errorf("push branches = %#v, want only main", pushConfig.Branches)
	}
	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		t.Fatalf("top-level permissions = %#v, want only contents: read", workflow.Permissions)
	}
	if workflow.Concurrency.Group != `${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}` {
		t.Errorf("unexpected concurrency group %q", workflow.Concurrency.Group)
	}
	if workflow.Concurrency.CancelInProgress != `${{ github.event_name == 'pull_request' }}` {
		t.Errorf("unexpected concurrency cancellation rule %q", workflow.Concurrency.CancelInProgress)
	}

	expectedEnvironment := map[string]string{
		"GOTOOLCHAIN":        "local",
		"GO_MIN_VERSION":     "1.25.12",
		"GO_CURRENT_VERSION": "1.26.5",
		"NODE_VERSION":       "24.16.0",
	}
	if !reflect.DeepEqual(workflow.Env, expectedEnvironment) {
		t.Errorf("workflow env = %#v, want %#v", workflow.Env, expectedEnvironment)
	}

	expectedJobs := map[string]struct {
		name           string
		timeoutMinutes int
		actions        []string
		commands       []string
		goVersion      string
	}{
		"quality": {
			name: "CI / quality", timeoutMinutes: 15,
			actions:   []string{checkoutAction, setupGoAction, uploadArtifactAction},
			goVersion: `${{ env.GO_MIN_VERSION }}`,
			commands: []string{
				"go mod verify",
				"gofmt -l .",
				"go test -json -coverprofile=coverage.out -count=1 ./...",
				"go vet ./...",
				"go build ./...",
				`git diff --check "${{ github.event.pull_request.base.sha }}...${{ github.sha }}"`,
				`git diff --check "${{ github.event.before }}...${{ github.sha }}"`,
				"git diff --check HEAD^ HEAD",
			},
		},
		"race": {
			name: "CI / race", timeoutMinutes: 15,
			actions:   []string{checkoutAction, setupGoAction},
			goVersion: `${{ env.GO_CURRENT_VERSION }}`,
			commands:  []string{"go test -race ./... -count=1"},
		},
		"integration": {
			name: "CI / integration", timeoutMinutes: 25,
			actions:   []string{checkoutAction, setupGoAction, uploadArtifactAction},
			goVersion: `${{ env.GO_CURRENT_VERSION }}`,
			commands: []string{
				"docker compose config --quiet",
				"docker compose up -d --wait --wait-timeout 180",
				"docker compose ps",
				"go test -json -p=1 -count=1 -timeout=15m",
				"./internal/postgres ./internal/s3store ./internal/server",
				"> integration-test.raw.json",
				"go run ./tools/verify-integration-report",
				"-manifest .github/integration-tests.json",
				"-input integration-test.raw.json",
				"go run ./tools/sanitize-ci-log",
				"docker compose logs --no-color",
				"trap 'rm -f integration-test.raw.json' EXIT",
				"docker compose down -v --remove-orphans",
			},
		},
		"api-contract": {
			name: "CI / api-contract", timeoutMinutes: 10,
			actions:   []string{checkoutAction, setupGoAction, setupNodeAction},
			goVersion: `${{ env.GO_MIN_VERSION }}`,
			commands: []string{
				"npm ci --ignore-scripts",
				"npm run lint:actions",
				"npm run lint:openapi",
				"git show \"$BASE_SHA:docs/openapi.yaml\"",
				"go run ./tools/verify-openapi-compat",
				"OpenAPI changes require an accompanying ADR",
				"go test ./internal/server -run '^TestOpenAPIOperationsMatchRegisteredRoutes$' -count=1",
			},
		},
	}
	if len(workflow.Jobs) != len(expectedJobs) {
		t.Fatalf("workflow has %d jobs, want %d", len(workflow.Jobs), len(expectedJobs))
	}
	for jobID, expected := range expectedJobs {
		job, ok := workflow.Jobs[jobID]
		if !ok {
			t.Errorf("required job %q is missing", jobID)
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
		if len(job.Permissions) != 0 {
			t.Errorf("job %q overrides read-only workflow permissions: %#v", jobID, job.Permissions)
		}
		if job.Uses != "" || job.Secrets.Kind != 0 {
			t.Errorf("job %q must not call a reusable workflow or pass secrets", jobID)
		}

		var actions []string
		var commands []string
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
				if (jobID == "quality" || jobID == "api-contract") && fmt.Sprint(step.With["fetch-depth"]) != "0" {
					t.Errorf("%s checkout must fetch history for event-aware diff", jobID)
				}
			case setupGoAction:
				if fmt.Sprint(step.With["go-version"]) != expected.goVersion || step.With["cache"] != true {
					t.Errorf("job %q has unexpected setup-go inputs: %#v", jobID, step.With)
				}
			case setupNodeAction:
				if fmt.Sprint(step.With["node-version"]) != `${{ env.NODE_VERSION }}` || fmt.Sprint(step.With["cache"]) != "npm" {
					t.Errorf("api-contract has unexpected setup-node inputs: %#v", step.With)
				}
			}
		}
		if !reflect.DeepEqual(actions, expected.actions) {
			t.Errorf("job %q actions = %#v, want %#v", jobID, actions, expected.actions)
		}
		joinedCommands := strings.Join(commands, "\n")
		for _, command := range expected.commands {
			if !strings.Contains(joinedCommands, command) {
				t.Errorf("job %q is missing required command %q", jobID, command)
			}
		}
		if jobID == "race" && fmt.Sprint(job.Env["CGO_ENABLED"]) != "1" {
			t.Errorf("race job must enable CGO")
		}
		if jobID == "integration" {
			assertIntegrationJobContract(t, job)
		}
	}
}

func assertIntegrationJobContract(t *testing.T, job workflowJob) {
	t.Helper()

	expectedEnvironment := map[string]any{
		"COMPOSE_PROJECT_NAME":       `asteria-ci-${{ github.run_id }}-${{ github.run_attempt }}`,
		"ASTERIA_TEST_DATABASE_URL":  "postgres://asteria:local-asteria-password@127.0.0.1:15432/asteria?sslmode=disable",
		"ASTERIA_TEST_S3_ENDPOINT":   "http://127.0.0.1:18333",
		"ASTERIA_TEST_S3_ACCESS_KEY": "asteria-local-access",
		"ASTERIA_TEST_S3_SECRET_KEY": "asteria-local-secret-not-for-production",
		"ASTERIA_TEST_S3_REGION":     "us-east-1",
		"ASTERIA_TEST_S3_BUCKET":     `asteria-ci-${{ github.run_id }}-${{ github.run_attempt }}`,
	}
	if !reflect.DeepEqual(job.Env, expectedEnvironment) {
		t.Errorf("integration env = %#v, want %#v", job.Env, expectedEnvironment)
	}

	steps := make(map[string]workflowStep, len(job.Steps))
	for _, step := range job.Steps {
		if _, duplicate := steps[step.Name]; duplicate {
			t.Fatalf("integration job repeats step name %q", step.Name)
		}
		steps[step.Name] = step
	}
	for _, stepName := range []string{
		"Sanitize integration evidence",
		"Remove isolated dependencies",
		"Upload integration evidence",
	} {
		step, ok := steps[stepName]
		if !ok {
			t.Errorf("integration job is missing step %q", stepName)
			continue
		}
		if step.If != `${{ always() }}` {
			t.Errorf("integration step %q condition = %q, want always()", stepName, step.If)
		}
	}

	validation := steps["Validate integration environment"].Run
	for variable := range expectedEnvironment {
		if variable == "COMPOSE_PROJECT_NAME" {
			continue
		}
		if !strings.Contains(validation, variable) {
			t.Errorf("integration environment validation omits %s", variable)
		}
	}
	if !strings.Contains(validation, `${!variable:-}`) {
		t.Error("integration environment validation must reject empty variables")
	}

	upload := steps["Upload integration evidence"]
	if upload.Uses != uploadArtifactAction {
		t.Errorf("integration evidence uses %q, want pinned upload-artifact", upload.Uses)
	}
	if fmt.Sprint(upload.With["name"]) != "integration-evidence" ||
		fmt.Sprint(upload.With["if-no-files-found"]) != "ignore" ||
		fmt.Sprint(upload.With["retention-days"]) != "7" {
		t.Errorf("integration upload has unexpected inputs: %#v", upload.With)
	}
	uploadPaths := strings.Fields(fmt.Sprint(upload.With["path"]))
	if !reflect.DeepEqual(uploadPaths, []string{"integration-test.json", "compose.log"}) {
		t.Errorf("integration upload paths = %#v, want only sanitized evidence", uploadPaths)
	}
}
