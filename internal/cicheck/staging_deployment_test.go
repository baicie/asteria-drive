package cicheck

import (
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const stagingImage = "ghcr.io/baicie/asteria-drive@sha256:f5da244cba2055764a8caae7b9e9a752cc8f07356c0d7ae6397a6a7992e0cccc"

func TestStagingDeploymentWorkflowTrustBoundary(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../.github/workflows/deploy-staging.yml")
	if err != nil {
		t.Fatalf("read staging deployment workflow: %v", err)
	}
	text := string(contents)
	for _, forbidden := range []string{
		"pull_request_target", "ssh-keyscan", "ssh_password", "ASTERIA_DATABASE_URL",
		"ASTERIA_CURSOR_HMAC_KEY", "ASTERIA_S3_SECRET_ACCESS_KEY", "0.0.0.0:18080", "scp ",
	} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Errorf("staging deployment workflow contains forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{
		stagingImage,
		"github.ref == 'refs/heads/main'",
		"gh attestation verify",
		"--signer-workflow baicie/asteria-drive/.github/workflows/release.yml",
		"--source-ref \"refs/tags/$RELEASE_TAG\"",
		"--source-digest \"$RELEASE_COMMIT\"",
		"--deny-self-hosted-runners",
		"StrictHostKeyChecking=yes",
		"UserKnownHostsFile=",
		"prepare $GITHUB_RUN_ID $GITHUB_RUN_ATTEMPT",
		"upload-compose $GITHUB_RUN_ID $GITHUB_RUN_ATTEMPT",
		"upload-script $GITHUB_RUN_ID $GITHUB_RUN_ATTEMPT",
		"fetch $GITHUB_RUN_ID $GITHUB_RUN_ATTEMPT $artifact",
		"COMPOSE_SHA256",
		"asteria-drive-staging-deployment/v1",
		"type(checked) is not int",
		"deployment evidence did not prove",
		"storage verifier identity does not match deployment evidence",
		"scripts/deploy-staging.sh",
		"deployment-evidence.json",
		"storage-verifier.json",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("staging deployment workflow is missing %q", required)
		}
	}

	var workflow workflowDocument
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatalf("parse staging deployment workflow: %v", err)
	}
	if workflow.Name != "Deploy staging" {
		t.Errorf("workflow name = %q, want Deploy staging", workflow.Name)
	}
	if len(workflow.On) != 1 {
		t.Fatalf("workflow triggers = %#v, want only workflow_dispatch", workflow.On)
	}
	if _, ok := workflow.On["workflow_dispatch"]; !ok {
		t.Fatal("staging deployment must be manually dispatched")
	}
	expectedPermissions := map[string]string{
		"contents": "read", "attestations": "read", "packages": "read",
	}
	if !reflect.DeepEqual(workflow.Permissions, expectedPermissions) {
		t.Errorf("workflow permissions = %#v, want %#v", workflow.Permissions, expectedPermissions)
	}
	if workflow.Concurrency.Group != "asteria-drive-staging" || workflow.Concurrency.CancelInProgress != "false" {
		t.Errorf("unexpected staging concurrency: %#v", workflow.Concurrency)
	}
	if len(workflow.Jobs) != 1 {
		t.Fatalf("workflow has %d jobs, want one", len(workflow.Jobs))
	}
	job := workflow.Jobs["deploy"]
	if job.Name != "Deploy / staging" || job.RunsOn != "ubuntu-24.04" || job.Environment != "staging" || job.TimeoutMinutes != 20 {
		t.Errorf("unexpected deploy job contract: %#v", job)
	}
	if len(job.Permissions) != 0 {
		t.Errorf("deploy job overrides top-level permissions: %#v", job.Permissions)
	}
	for _, step := range job.Steps {
		if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
			t.Errorf("deployment uses mutable Action reference %q", step.Uses)
		}
	}
	if count := strings.Count(text, "secrets.ASTERIA_STAGING_SSH_"); count != 5 {
		t.Errorf("workflow references %d staging SSH secrets, want exactly five", count)
	}
	if regexp.MustCompile(`(?i)secrets\.[A-Z0-9_]*(DATABASE|CURSOR|S3|TOKEN|PASSWORD)`).MatchString(text) {
		t.Fatal("application or password secrets must remain on the server")
	}
}

func TestStagingComposePinsImagesAndKeepsPortsOnLoopback(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../infra/docker/staging/compose.yaml")
	if err != nil {
		t.Fatalf("read staging Compose file: %v", err)
	}
	text := string(contents)
	for _, required := range []string{
		stagingImage,
		"127.0.0.1:18080:8080",
		"127.0.0.1:18333:8333",
		"127.0.0.1:19090:9090",
		"internal: true",
		"read_only: true",
		"no-new-privileges:true",
		"cap_drop:",
		"ASTERIA_TRUSTED_TOKENS_JSON_FILE",
		"ASTERIA_DATABASE_URL_FILE",
		"ASTERIA_CURSOR_HMAC_KEY_FILE",
		"staging-not-production",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("staging Compose file is missing %q", required)
		}
	}
	for _, forbidden := range []string{"0.0.0.0:18080", ":latest", "sha256:" + strings.Repeat("0", 64)} {
		if strings.Contains(text, forbidden) {
			t.Errorf("staging Compose file contains forbidden value %q", forbidden)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "image:") && !strings.Contains(line, "@sha256:") && line != "image: *asteria-image" {
			t.Errorf("staging image is not digest-pinned: %s", line)
		}
	}
}

func TestStagingScriptsKeepSecretsServerSideAndEmitEvidence(t *testing.T) {
	t.Parallel()

	bootstrap, err := os.ReadFile("../../scripts/bootstrap-staging-host.sh")
	if err != nil {
		t.Fatal(err)
	}
	deploy, err := os.ReadFile("../../scripts/deploy-staging.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"restrict,command=\"%s\"", "SSH_ORIGINAL_COMMAND", "upload-compose", "upload-script",
		"receive_file", "sha256sum", "openssl rand", "docker volume create",
		"refusing to rotate implicitly", "require_metadata", "chmod 0400",
	} {
		if !strings.Contains(string(bootstrap), required) {
			t.Errorf("staging bootstrap is missing %q", required)
		}
	}
	for _, required := range []string{
		stagingImage,
		"docker compose",
		"run --rm migrate",
		"/readyz",
		"Authorization: Bearer",
		"upload-download-smoke",
		"upload_download_smoke_succeeded",
		"metrics_scrape_succeeded",
		`local code="$?"`,
		`rm -f -- "$tmp_path" || return 1`,
		`report["checked"] < 1`,
		`report["findings"] not in (None, [])`,
		"storage_verifier_checked",
		"run --rm verifier",
		"staging-not-production",
		"server-managed-docker-volumes",
	} {
		if !strings.Contains(string(deploy), required) {
			t.Errorf("staging deployment script is missing %q", required)
		}
	}
	if strings.Contains(string(deploy), "set -x") || strings.Contains(string(bootstrap), "set -x") {
		t.Fatal("staging scripts must never enable shell tracing around secrets")
	}
	if strings.Contains(string(bootstrap), "restrict %s") {
		t.Fatal("staging deploy key must use the root-owned forced-command dispatcher")
	}

	compose, err := os.ReadFile("../../infra/docker/staging/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	composeDigest := fmt.Sprintf("%x", sha256.Sum256(compose))
	deployDigest := fmt.Sprintf("%x", sha256.Sum256(deploy))
	for _, digest := range []string{composeDigest, deployDigest} {
		if !strings.Contains(string(bootstrap), digest) {
			t.Errorf("staging bootstrap dispatcher does not pin reviewed file digest %s", digest)
		}
	}
	if !strings.Contains(string(deploy), composeDigest) {
		t.Errorf("staging deployment script does not pin Compose digest %s", composeDigest)
	}
}
