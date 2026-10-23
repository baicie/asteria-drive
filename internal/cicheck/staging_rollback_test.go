package cicheck

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestStagingRollbackWorkflowUsesOnlyApprovedV011Bundle(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../.github/workflows/rollback-staging.yml")
	if err != nil {
		t.Fatalf("read rollback workflow: %v", err)
	}
	text := string(contents)
	for _, forbidden := range []string{"pull_request_target", "ssh-keyscan", "ssh_password", "0.0.0.0:18080", "scp "} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Errorf("rollback workflow contains forbidden value %q", forbidden)
		}
	}
	var workflow workflowDocument
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatalf("parse rollback workflow: %v", err)
	}
	if workflow.Name != "Rollback staging" {
		t.Fatalf("workflow name = %q", workflow.Name)
	}
	if len(workflow.On) != 1 {
		t.Fatalf("rollback triggers = %#v, want workflow_dispatch only", workflow.On)
	}
	if _, ok := workflow.On["workflow_dispatch"]; !ok {
		t.Fatal("rollback must be manually dispatched")
	}
	wantEnv := map[string]string{
		"RELEASE_TAG":    "v0.1.1",
		"RELEASE_COMMIT": "e8d26ded6e9138bbfdeac60f2487c5f835ab61a5",
		"IMAGE":          "ghcr.io/baicie/asteria-drive@sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842",
		"COMPOSE_SHA256": "aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637",
	}
	for key, want := range wantEnv {
		if got := workflow.Env[key]; got != want {
			t.Errorf("rollback env %s = %q, want %q", key, got, want)
		}
	}
	if len(workflow.Jobs) != 1 {
		t.Fatalf("rollback has %d jobs", len(workflow.Jobs))
	}
	job := workflow.Jobs["deploy"]
	if job.Name != "Rollback / staging" || job.Environment != "staging" || job.RunsOn != "ubuntu-24.04" {
		t.Errorf("unexpected rollback job: %#v", job)
	}
	if job.If != "github.ref == 'refs/heads/main' && inputs.confirmation == 'rollback-v0.1.1'" {
		t.Errorf("rollback condition = %q", job.If)
	}
	for _, step := range job.Steps {
		if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
			t.Errorf("rollback uses mutable action %q", step.Uses)
		}
	}
	for _, required := range []string{
		"gh attestation verify",
		"--signer-workflow baicie/asteria-drive/.github/workflows/release.yml",
		"--source-ref \"refs/tags/$RELEASE_TAG\"",
		"--source-digest \"$RELEASE_COMMIT\"",
		"--deny-self-hosted-runners",
		"StrictHostKeyChecking=yes",
		"UserKnownHostsFile=",
		"upload-compose $GITHUB_RUN_ID $GITHUB_RUN_ATTEMPT",
		"upload-script $GITHUB_RUN_ID $GITHUB_RUN_ATTEMPT",
		"deploy $GITHUB_RUN_ID $GITHUB_RUN_ATTEMPT $GITHUB_SHA",
		"infra/docker/staging/releases/v0.1.1/compose.yaml",
		"infra/docker/staging/releases/v0.1.1/deploy-staging.sh",
		"staging-rollback-evidence",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("rollback workflow is missing %q", required)
		}
	}

	compose, err := os.ReadFile("../../infra/docker/staging/releases/v0.1.1/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	composeDigest := fmt.Sprintf("%x", sha256.Sum256(compose))
	if composeDigest != wantEnv["COMPOSE_SHA256"] {
		t.Errorf("v0.1.1 compose digest = %s", composeDigest)
	}
	script, err := os.ReadFile("../../infra/docker/staging/releases/v0.1.1/deploy-staging.sh")
	if err != nil {
		t.Fatal(err)
	}
	scriptDigest := fmt.Sprintf("%x", sha256.Sum256(script))
	if scriptDigest != "cd200fa32fdc2314817308479c99fe6425a481ff3bcd7384a60e145b2ad45044" {
		t.Errorf("v0.1.1 deploy digest = %s", scriptDigest)
	}
}

func TestStagingBootstrapAllowsOnlyTwoReviewedReleasePairs(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../scripts/bootstrap-staging-host.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		`expected_compose_sha256="cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005"`,
		`legacy_compose_sha256="aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637"`,
		`expected_deploy_script_sha256="007eb3a34bc5eff5bdff1db247ef9ed2b396814eac8231a0df71579a3f79138f"`,
		`legacy_deploy_script_sha256="cd200fa32fdc2314817308479c99fe6425a481ff3bcd7384a60e145b2ad45044"`,
		"receive_release_file",
		"ASTERIA_STAGING_CONTROLLER_B64",
		"controller_path=",
		"expected_controller_sha256",
		"staging-releases.json",
		"\"$controller_path\" deploy",
		"compose_actual=",
		"script_actual=",
		`lock_path="${lock_dir}/staging.lock"`,
		`install -d -m 0750 -o root -g "$deploy_group" "$lock_dir"`,
		`install -m 0660 -o root -g "$deploy_group" /dev/null "$lock_path"`,
		"/etc/tmpfiles.d/asteria-drive-staging.conf",
		"validate_lock",
		`exec flock -n "$lock_path"`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("bootstrap is missing %q", required)
		}
	}
	if strings.Contains(text, "0.0.0.0:18080") || strings.Contains(text, "ssh-keyscan") {
		t.Fatal("bootstrap contains an unsafe boundary")
	}
}
