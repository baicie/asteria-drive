package cicheck

import (
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	stagingImage             = "ghcr.io/baicie/asteria-drive@sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842"
	stagingSecretHelperImage = "chrislusf/seaweedfs:3.85@sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a"
)

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
		"deploy-v0.1.1",
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
		"asteria-drive-staging-deployment/v2",
		"type(checked) is not int",
		"type(available) is not int",
		"data_volume_filesystems_verified",
		"postgres_data_filesystem",
		"seaweedfs_data_filesystem",
		"deployment evidence did not prove",
		"storage verifier identity does not match deployment evidence",
		"postgres_tls_verified",
		"postgres_plaintext_rejected",
		"postgres_tls_leaf_san",
		"postgres_tls_dsn_verified",
		"postgres_hba_verified",
		"rollback_attempted",
		"runtime_changed",
		"candidate_cleanup_attempted",
		"failed deployment evidence omitted rollback attempt",
		"deployment evidence fields mismatch",
		"asteria-drive-staging-deployment-collection/v1",
		"staging-deployment.raw.json",
		"staging-deploy.remote-stdout.log",
		"deployment_command_stderr",
		"storage verifier fields mismatch",
		"os.replace(temporary, destination)",
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
	if job.If != "github.ref == 'refs/heads/main' && inputs.confirmation == 'deploy-v0.1.1'" {
		t.Errorf("deploy job condition = %q", job.If)
	}
	if len(job.Permissions) != 0 {
		t.Errorf("deploy job overrides top-level permissions: %#v", job.Permissions)
	}
	deploySteps := make(map[string]workflowStep, len(job.Steps))
	for _, step := range job.Steps {
		deploySteps[step.Name] = step
		if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
			t.Errorf("deployment uses mutable Action reference %q", step.Uses)
		}
	}
	for _, name := range []string{
		"Collect sanitized deployment evidence", "Upload deployment evidence", "Remove remote temporary files",
	} {
		if deploySteps[name].If != `${{ always() }}` {
			t.Errorf("deployment step %q condition = %q, want always()", name, deploySteps[name].If)
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
		"asteria-drive-staging-loopback",
		"-master.volumePreallocate=false",
		"-master.volumeSizeLimitMB=256",
		"-volume.max=8",
		"-volume.minFreeSpace=5GiB",
		"-ip=seaweedfs",
		"-ip.bind=0.0.0.0",
		"read_only: true",
		"no-new-privileges:true",
		"cap_drop:",
		"ASTERIA_TRUSTED_TOKENS_JSON_FILE",
		"ASTERIA_DATABASE_URL_FILE",
		"/var/run/secrets/asteria/database-url-tls",
		"ssl=on",
		"ssl_min_protocol_version=TLSv1.2",
		"hba_file=/var/run/secrets/asteria/pg_hba.conf",
		"sslmode=verify-full",
		"postgres-ca.crt",
		"postgres-server.crt",
		"postgres-server.key",
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

	var compose struct {
		Services map[string]struct {
			Networks    []string          `yaml:"networks"`
			Environment map[string]string `yaml:"environment"`
			Command     []string          `yaml:"command"`
		} `yaml:"services"`
		Networks map[string]struct {
			Internal bool `yaml:"internal"`
		} `yaml:"networks"`
	}
	if err := yaml.Unmarshal(contents, &compose); err != nil {
		t.Fatalf("parse staging Compose file: %v", err)
	}
	for service, want := range map[string][]string{
		"api":       {"backend", "loopback"},
		"seaweedfs": {"backend", "loopback"},
		"postgres":  {"backend"},
	} {
		if got := compose.Services[service].Networks; !reflect.DeepEqual(got, want) {
			t.Errorf("%s networks = %#v, want %#v", service, got, want)
		}
	}
	if !compose.Networks["backend"].Internal || compose.Networks["loopback"].Internal {
		t.Errorf("unexpected staging network isolation: %#v", compose.Networks)
	}
	if compose.Services["api"].Environment["ASTERIA_ENV"] != "development" {
		t.Fatal("staging API must retain the explicit non-production boundary")
	}
	if compose.Services["migrate"].Environment["ASTERIA_ENV"] != "production" {
		t.Fatal("staging migration must exercise production database URL validation")
	}
	for _, service := range []string{"api", "migrate", "verifier"} {
		if got := compose.Services[service].Environment["ASTERIA_DATABASE_URL_FILE"]; got != "/var/run/secrets/asteria/database-url-tls" {
			t.Errorf("%s database URL file = %q", service, got)
		}
	}
	for _, required := range []string{
		"ssl=on", "ssl_min_protocol_version=TLSv1.2",
		"hba_file=/var/run/secrets/asteria/pg_hba.conf",
	} {
		if !slices.Contains(compose.Services["postgres"].Command, required) {
			t.Errorf("PostgreSQL command is missing %q", required)
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
		"staging-postgres-pki", "database-url-tls", "postgres-ca.crt",
		"postgres-server.crt", "postgres-server.key", "pg_hba.conf",
		"verify_hostname postgres", "DNS:postgres", "hostnossl all all 0.0.0.0/0 reject",
		"basicConstraints=critical,CA:TRUE", "keyUsage=critical,keyCertSign,cRLSign",
		"partial; refusing implicit repair or rotation", `"$pki_issuer/ca.key" "0:0" "400"`,
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
		"capacity_guard_verified",
		"capacity-preflight",
		"capacity-postflight",
		"max_disk_used_percent=85",
		"min_disk_available_kib=$((5 * 1024 * 1024))",
		`output="$(df -Pk -- "$path")" || return 1`,
		"docker run --rm --pull never --network none --read-only",
		`--volume "$volume:/capacity:ro"`,
		`docker volume inspect --format '{{ index .Labels "com.docker.compose.project" }}'`,
		`docker volume inspect --format '{{json .Options}}'`,
		`[[ "$driver" == "local" ]] || return 1`,
		`case "$options" in`,
		`null|'{}') ;;`,
		`*) return 1 ;;`,
		"verify_data_volume_filesystems",
		`local code="$?"`,
		`rm -f -- "$tmp_path" || return 1`,
		`report["checked"] < 1`,
		`report["findings"] not in (None, [])`,
		"storage_verifier_checked",
		"run --rm verifier",
		"staging-not-production",
		"server-managed-docker-volumes",
		"verify_postgres_tls",
		"verify_image_identity",
		`docker inspect --format '{{.Config.Image}}'`,
		`docker image inspect --format '{{.Id}}'`,
		"pg_stat_activity a JOIN pg_stat_ssl s",
		"pg_hba_file_rules",
		"sslmode=disable connect_timeout=5",
		"postgres_plaintext_stderr_sha256",
		"postgres_scram_password_verified",
		"rollback_runtime",
		"rollback_attempted",
		"cleanup_candidate_runtime",
		"candidate_cleanup_succeeded",
		"up -d --pull never",
		"previous_compose_available",
		"database-url-tls",
		"app_secret_summary",
		"postgres_secret_summary",
		"--user 65532:65532",
		"--user 0:70",
		`unset app_secret_summary app_password_sha256 app_ca_sha256 app_summary_extra`,
	} {
		if !strings.Contains(string(deploy), required) {
			t.Errorf("staging deployment script is missing %q", required)
		}
	}
	if strings.Contains(string(deploy), "set -x") || strings.Contains(string(bootstrap), "set -x") {
		t.Fatal("staging scripts must never enable shell tracing around secrets")
	}
	if strings.Contains(string(deploy), "< <(df") {
		t.Fatal("staging capacity snapshots must propagate df failures")
	}
	if strings.Contains(string(deploy), "--cap-add") {
		t.Fatal("staging secret preflight must not gain a capability to cross secret ownership boundaries")
	}
	deployText := string(deploy)
	appSummaryStart := strings.Index(deployText, `app_secret_summary="$(docker run`)
	postgresSummaryStart := strings.Index(deployText, `postgres_secret_summary="$(docker run`)
	preflightEnd := -1
	if postgresSummaryStart >= 0 {
		if relativeEnd := strings.Index(deployText[postgresSummaryStart:], `postgres_tls_dsn_verified="true"`); relativeEnd >= 0 {
			preflightEnd = postgresSummaryStart + relativeEnd
		}
	}
	if appSummaryStart < 0 || postgresSummaryStart <= appSummaryStart || preflightEnd < 0 {
		t.Fatal("staging secret preflight ownership sections are missing or out of order")
	}
	appSummary := deployText[appSummaryStart:postgresSummaryStart]
	postgresSummary := deployText[postgresSummaryStart:preflightEnd]
	preflight := deployText[appSummaryStart:preflightEnd]
	if strings.Contains(appSummary, "staging-postgres-secrets") {
		t.Fatal("application-owned preflight must not mount PostgreSQL-owned secrets")
	}
	if strings.Contains(postgresSummary, "staging-app-secrets") {
		t.Fatal("PostgreSQL-owned preflight must not mount application-owned secrets")
	}
	if strings.Contains(preflight, "--user root") || strings.Contains(preflight, "--cap-add") {
		t.Fatal("staging secret preflight must preserve explicit least-privilege users and dropped capabilities")
	}
	if !strings.Contains(preflight, `[[ "$app_password_sha256" == "$postgres_password_sha256" && "$app_ca_sha256" == "$postgres_ca_sha256" ]]`) {
		t.Fatal("staging secret preflight must compare password and CA identities across ownership boundaries")
	}
	if strings.Contains(string(deploy), `"$options" == "{}"`) {
		t.Fatal("staging capacity probe must accept both null and empty Docker volume options")
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
	workflow, err := os.ReadFile("../../.github/workflows/deploy-staging.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflow), composeDigest) {
		t.Errorf("staging workflow does not pin Compose digest %s", composeDigest)
	}
}

func TestStagingCapacityThresholdBoundaries(t *testing.T) {
	t.Parallel()

	const (
		maxUsed      = 85
		minAvailable = int64(5 * 1024 * 1024 * 1024)
	)
	tests := []struct {
		name      string
		used      int
		available int64
		want      bool
	}{
		{name: "exact boundary", used: 85, available: minAvailable, want: true},
		{name: "usage exceeded", used: 86, available: minAvailable, want: false},
		{name: "reserve missed", used: 85, available: minAvailable - 1, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.used <= maxUsed && test.available >= minAvailable
			if got != test.want {
				t.Errorf("capacity decision = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStagingMonitorWorkflowAndScriptTrustBoundary(t *testing.T) {
	t.Parallel()

	workflowContents, err := os.ReadFile("../../.github/workflows/monitor-staging.yml")
	if err != nil {
		t.Fatalf("read staging monitor workflow: %v", err)
	}
	workflowText := string(workflowContents)
	for _, forbidden := range []string{
		"pull_request_target", "ssh-keyscan", "ssh_password", "scp ", "actions/checkout@",
		"ASTERIA_DATABASE_URL", "ASTERIA_CURSOR_HMAC_KEY", "ASTERIA_S3_SECRET_ACCESS_KEY",
	} {
		if strings.Contains(strings.ToLower(workflowText), strings.ToLower(forbidden)) {
			t.Errorf("staging monitor workflow contains forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{
		"23 * * * *",
		"github.ref == 'refs/heads/main'",
		"StrictHostKeyChecking=yes",
		"UserKnownHostsFile=",
		"status $GITHUB_RUN_ID $GITHUB_RUN_ATTEMPT $GITHUB_SHA",
		"asteria-drive-staging-monitor/v2",
		"staging-not-production",
		"capacity_max_disk_used_percent",
		"capacity_min_disk_available_bytes",
		"monitor evidence did not prove",
		"postgres_tls_verified",
		"postgres_plaintext_rejected",
		"postgres_tls_leaf_san",
		"postgres_tls_dsn_verified",
		"postgres_hba_verified",
		"MONITOR_SCRIPT_SHA256",
		"status $GITHUB_RUN_ID $GITHUB_RUN_ATTEMPT $GITHUB_SHA $MONITOR_SCRIPT_SHA256",
		"PostgreSQL certificate expires in under 30 days",
		"monitor evidence fields mismatch",
		"asteria-drive-staging-monitor-collection/v1",
		"staging-monitor.raw.json",
		`>"$raw_evidence" 2>"$raw_stderr"`,
		"os.replace(temporary, sys.argv[2])",
		"PROBE_EXIT",
		"staging-monitor-evidence",
	} {
		if !strings.Contains(workflowText, required) {
			t.Errorf("staging monitor workflow is missing %q", required)
		}
	}
	if count := strings.Count(workflowText, "secrets.ASTERIA_STAGING_SSH_"); count != 5 {
		t.Errorf("monitor workflow references %d staging SSH secrets, want exactly five", count)
	}

	var workflow workflowDocument
	if err := yaml.Unmarshal(workflowContents, &workflow); err != nil {
		t.Fatalf("parse staging monitor workflow: %v", err)
	}
	if workflow.Name != "Monitor staging" {
		t.Errorf("workflow name = %q, want Monitor staging", workflow.Name)
	}
	for _, event := range []string{"workflow_dispatch", "schedule"} {
		if _, ok := workflow.On[event]; !ok {
			t.Errorf("staging monitor trigger %q is missing", event)
		}
	}
	if len(workflow.On) != 2 {
		t.Errorf("staging monitor triggers = %#v, want workflow_dispatch and schedule", workflow.On)
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("staging monitor permissions = %#v", workflow.Permissions)
	}
	if workflow.Concurrency.Group != "asteria-drive-staging" || workflow.Concurrency.CancelInProgress != "false" {
		t.Errorf("unexpected staging monitor concurrency: %#v", workflow.Concurrency)
	}
	if len(workflow.Jobs) != 1 {
		t.Fatalf("staging monitor has %d jobs, want one", len(workflow.Jobs))
	}
	job := workflow.Jobs["monitor"]
	if job.Name != "Monitor / staging" || job.RunsOn != "ubuntu-24.04" || job.Environment != "staging" || job.TimeoutMinutes != 10 {
		t.Errorf("unexpected staging monitor job contract: %#v", job)
	}
	if job.If != "github.ref == 'refs/heads/main'" {
		t.Errorf("monitor job condition = %q", job.If)
	}
	for _, step := range job.Steps {
		if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
			t.Errorf("staging monitor uses mutable Action reference %q", step.Uses)
		}
		if step.Name == "Upload monitor evidence" && step.If != `${{ always() }}` {
			t.Errorf("monitor upload condition = %q, want always()", step.If)
		}
	}

	monitor, err := os.ReadFile("../../scripts/monitor-staging.sh")
	if err != nil {
		t.Fatal(err)
	}
	monitorText := string(monitor)
	for _, required := range []string{
		"sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842",
		"asteria-drive-staging-monitor/v2",
		"status=\"failed\"",
		"trap finish EXIT",
		"max_disk_used_percent=85",
		"min_disk_available_kib=$((5 * 1024 * 1024))",
		`docker volume inspect --format '{{json .Options}}'`,
		`null|'{}') ;;`,
		"docker run --rm --pull never --network none --read-only",
		"verify_loopback_bindings",
		"127.0.0.1:18080/healthz",
		"127.0.0.1:18080/readyz",
		"127.0.0.1:19090/metrics",
		"awk '/^asteria_http_requests_total([ {]|$)/ { found = 1 } END { exit(found ? 0 : 1) }'",
		"staging-not-production",
		"verify_postgres_tls",
		"http-readiness",
		`docker inspect --format '{{.Config.Image}}'`,
		`docker image inspect --format '{{.Id}}'`,
		"pg_stat_activity a JOIN pg_stat_ssl s",
		"sslmode=disable connect_timeout=5",
		"postgres_plaintext_stderr_sha256",
		"postgres_tls_dsn_verified",
		"postgres_hba_verified",
		"pg_hba_file_rules",
		`rm -f -- "$plaintext_stderr"`,
		`-checkend $((30 * 24 * 60 * 60)) -noout >/dev/null`,
	} {
		if !strings.Contains(monitorText, required) {
			t.Errorf("staging monitor script is missing %q", required)
		}
	}
	if strings.Contains(monitorText, "set -x") || regexp.MustCompile(`ASTERIA_(DATABASE|CURSOR|S3|TRUSTED).*FILE`).MatchString(monitorText) {
		t.Fatal("staging monitor must not trace or read application secret files")
	}
	assertStagingSecretOwnershipBoundary(t, monitorText, "monitor")

	bootstrap, err := os.ReadFile("../../scripts/bootstrap-staging-host.sh")
	if err != nil {
		t.Fatal(err)
	}
	monitorDigest := fmt.Sprintf("%x", sha256.Sum256(monitor))
	if workflow.Env["MONITOR_SCRIPT_SHA256"] != monitorDigest {
		t.Errorf("monitor workflow digest = %q, want %s", workflow.Env["MONITOR_SCRIPT_SHA256"], monitorDigest)
	}
	for _, required := range []string{
		"ASTERIA_STAGING_MONITOR_B64",
		"monitor_script_sha256",
		"requested_monitor_sha",
		"status)",
		`[[ "$(stat -c '%u:%g:%a' "$monitor_path")" == "0:0:755" ]]`,
		`[[ "$(sha256sum "$monitor_path" | awk '{print $1}')" == "$monitor_script_sha256" ]]`,
		monitorDigest,
	} {
		if !strings.Contains(string(bootstrap), required) {
			t.Errorf("staging bootstrap monitor contract is missing %q", required)
		}
	}
}

func TestStagingRecoveryWorkflowAndScriptTrustBoundary(t *testing.T) {
	t.Parallel()

	workflowContents, err := os.ReadFile("../../.github/workflows/drill-staging-recovery.yml")
	if err != nil {
		t.Fatalf("read staging recovery workflow: %v", err)
	}
	workflowText := string(workflowContents)
	for _, forbidden := range []string{
		"pull_request_target", "ssh-keyscan", "ssh_password", "scp ",
		"ASTERIA_DATABASE_URL", "ASTERIA_CURSOR_HMAC_KEY", "ASTERIA_S3_SECRET_ACCESS_KEY",
	} {
		if strings.Contains(strings.ToLower(workflowText), strings.ToLower(forbidden)) {
			t.Errorf("staging recovery workflow contains forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{
		"17 4 * * 1",
		"github.ref == 'refs/heads/main'",
		checkoutAction,
		"persist-credentials: false",
		"sha256sum --check --strict",
		"bash -n scripts/recover-staging.sh",
		"StrictHostKeyChecking=yes",
		"UserKnownHostsFile=",
		"recovery $GITHUB_RUN_ID $GITHUB_RUN_ATTEMPT $GITHUB_SHA $RECOVERY_SCRIPT_SHA256",
		"asteria-drive-staging-recovery/v2",
		"staging-recovery-not-production",
		"object_versions_restored",
		"pitr_wal_replayed",
		"recovery evidence fields mismatch",
		"source_postgres_tls_verified",
		"source_postgres_tls_connections",
		"source_postgres_tls_dsn_verified",
		"source_postgres_hba_verified",
		"asteria-drive-staging-recovery-collection/v1",
		"staging-recovery.raw.json",
		`>"$raw_evidence" 2>"$remote_stderr"`,
		"os.replace(temporary, sys.argv[2])",
		"remote-stderr.sha256",
		`rm -f -- "$raw_evidence" "$remote_stderr"`,
		"staging-recovery-evidence",
		"if-no-files-found: error",
	} {
		if !strings.Contains(workflowText, required) {
			t.Errorf("staging recovery workflow is missing %q", required)
		}
	}
	if count := strings.Count(workflowText, "secrets.ASTERIA_STAGING_SSH_"); count != 5 {
		t.Errorf("recovery workflow references %d staging SSH secrets, want exactly five", count)
	}

	var workflow workflowDocument
	if err := yaml.Unmarshal(workflowContents, &workflow); err != nil {
		t.Fatalf("parse staging recovery workflow: %v", err)
	}
	if workflow.Name != "Drill staging recovery" {
		t.Errorf("workflow name = %q, want Drill staging recovery", workflow.Name)
	}
	for _, event := range []string{"workflow_dispatch", "schedule"} {
		if _, ok := workflow.On[event]; !ok {
			t.Errorf("staging recovery trigger %q is missing", event)
		}
	}
	if len(workflow.On) != 2 {
		t.Errorf("staging recovery triggers = %#v, want workflow_dispatch and schedule", workflow.On)
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("staging recovery permissions = %#v", workflow.Permissions)
	}
	if workflow.Concurrency.Group != "asteria-drive-staging" || workflow.Concurrency.CancelInProgress != "false" {
		t.Errorf("unexpected staging recovery concurrency: %#v", workflow.Concurrency)
	}
	if len(workflow.Jobs) != 1 {
		t.Fatalf("staging recovery has %d jobs, want one", len(workflow.Jobs))
	}
	job := workflow.Jobs["recovery"]
	if job.Name != "Recovery drill / staging" || job.RunsOn != "ubuntu-24.04" || job.Environment != "staging" || job.TimeoutMinutes != 30 {
		t.Errorf("unexpected staging recovery job contract: %#v", job)
	}
	if job.If != "github.ref == 'refs/heads/main'" {
		t.Errorf("recovery job condition = %q", job.If)
	}
	for _, step := range job.Steps {
		if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
			t.Errorf("staging recovery uses mutable Action reference %q", step.Uses)
		}
		if step.Name == "Upload recovery evidence" && step.If != `${{ always() }}` {
			t.Errorf("recovery upload condition = %q, want always()", step.If)
		}
	}

	recovery, err := os.ReadFile("../../scripts/recover-staging.sh")
	if err != nil {
		t.Fatal(err)
	}
	recoveryText := string(recovery)
	assertStagingSecretOwnershipBoundary(t, recoveryText, "recovery")
	for _, required := range []string{
		"set -Eeuo pipefail",
		"max_archive_bytes=$((512 * 1024 * 1024))",
		"head -c \"$((max_archive_bytes + 1))\"",
		"pg_dump --username asteria --dbname asteria --format=custom",
		"pg_restore --list",
		"--exit-on-error --single-transaction",
		"docker network create --internal",
		"resource_labels_match volume \"$target_volume\"",
		"--mount \"type=volume,source=$volume,target=/capacity,readonly\"",
		"verify_source_ids",
		"verify_capacity_scope",
		"verify_volume_capacity \"$target_volume\"",
		"restore_capacity_failed=\"true\"",
		"ASTERIA_MAINTENANCE_ENABLED=false",
		"recovered_authenticated_read_succeeded=\"true\"",
		"resource_absent container \"$capacity_container\"",
		"cleanup_resources || code=1",
		"cleanup_verified=\"true\"",
		"object_versions_restored=\"false\"",
		"pitr_wal_replayed=\"false\"",
		"verify-source-postgres-tls",
		"verify_image_identity",
		`docker inspect --format '{{.Config.Image}}'`,
		`docker image inspect --format '{{.Id}}'`,
		"curl --fail --silent --show-error --max-time 5 http://127.0.0.1:18080/readyz >/dev/null",
		"pg_stat_activity a JOIN pg_stat_ssl s",
		"source_postgres_tls_verified=\"true\"",
		"source_postgres_tls_dsn_verified",
		"source_postgres_hba_verified",
		"pg_hba_file_rules",
		"staging-recovery-not-production",
	} {
		if !strings.Contains(recoveryText, required) {
			t.Errorf("staging recovery script is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"set -x", "--publish", "--volume", "ssh-keyscan",
		`if [[ "$cleanup_verified" != "true" ]]`,
	} {
		if strings.Contains(recoveryText, forbidden) {
			t.Errorf("staging recovery script contains forbidden value %q", forbidden)
		}
	}

	recoveryDigest := fmt.Sprintf("%x", sha256.Sum256(recovery))
	if workflow.Env["RECOVERY_SCRIPT_SHA256"] != recoveryDigest {
		t.Errorf("recovery workflow digest = %q, want %s", workflow.Env["RECOVERY_SCRIPT_SHA256"], recoveryDigest)
	}
	bootstrap, err := os.ReadFile("../../scripts/bootstrap-staging-host.sh")
	if err != nil {
		t.Fatal(err)
	}
	bootstrapText := string(bootstrap)
	for _, required := range []string{
		"ASTERIA_STAGING_RECOVERY_B64",
		"recovery_script_sha256",
		"recovery)",
		`[[ "$requested_recovery_sha" == "$recovery_script_sha256" ]]`,
		"install -d -m 0755 -o root -g root /usr/local/libexec",
		`[[ "$(stat -c '%u:%g:%a' "$directory")" == "0:0:755" ]]`,
		`[[ "$(stat -c '%u:%g:%a' "$recovery_path")" == "0:0:755" ]]`,
		`[[ "$(sha256sum "$recovery_path" | awk '{print $1}')" == "$recovery_script_sha256" ]]`,
		recoveryDigest,
	} {
		if !strings.Contains(bootstrapText, required) {
			t.Errorf("staging bootstrap recovery contract is missing %q", required)
		}
	}
}

func assertStagingSecretOwnershipBoundary(t *testing.T, script, name string) {
	t.Helper()

	for _, issue := range stagingSecretOwnershipBoundaryErrors(script, name) {
		t.Error(issue)
	}
}

type stagingSecretProbeContract struct {
	label         string
	assignment    string
	parse         string
	user          string
	volume        string
	mount         string
	manifestBlock string
	secretRead    string
	summaryBlock  string
}

func stagingSecretOwnershipBoundaryErrors(script, name string) []string {
	functionName := map[string]string{
		"monitor":  "verify_postgres_dsn",
		"recovery": "verify_app_secret_metadata",
	}[name]
	if functionName == "" {
		return []string{fmt.Sprintf("unknown staging secret verification contract %q", name)}
	}

	body, ok := shellFunctionBody(script, functionName)
	if !ok {
		return []string{fmt.Sprintf("staging %s secret verification function %s is missing", name, functionName)}
	}
	body = stripShellComments(body)
	appParse := `IFS=' ' read -r app_password_sha256 app_ca_sha256 app_extra <<<"$app_summary"`
	postgresParse := `IFS=' ' read -r postgres_password_sha256 postgres_ca_sha256 postgres_extra <<<"$postgres_summary"`
	appCanonical := `[[ "$app_summary" == "$app_password_sha256 $app_ca_sha256" ]] || return 1`
	postgresCanonical := `[[ "$postgres_summary" == "$postgres_password_sha256 $postgres_ca_sha256" ]] || return 1`
	appValidation := `[[ "$app_password_sha256" =~ ^[0-9a-f]{64}$ && "$app_ca_sha256" =~ ^[0-9a-f]{64}$ && -z "$app_extra" ]] || return 1`
	postgresValidation := `[[ "$postgres_password_sha256" =~ ^[0-9a-f]{64}$ && "$postgres_ca_sha256" =~ ^[0-9a-f]{64}$ && -z "$postgres_extra" ]] || return 1`
	comparison := `[[ "$app_password_sha256" == "$postgres_password_sha256" && "$app_ca_sha256" == "$postgres_ca_sha256" ]] || return 1`
	contracts := []stagingSecretProbeContract{
		{
			label:      "application",
			assignment: `app_summary="$(docker run`,
			parse:      appParse,
			user:       "65532:65532",
			volume:     "asteria-drive-staging-app-secrets",
			mount:      "type=volume,source=asteria-drive-staging-app-secrets,target=/app-secrets,readonly",
			manifestBlock: `for specification in \
        "/app-secrets/database-url-tls 65532:65532:400" \
        "/app-secrets/postgres-ca.crt 65532:65532:400"; do
        path="${specification% *}"
        metadata="${specification##* }"
        [ -s "$path" ] && [ ! -L "$path" ] && [ "$(stat -c "%u:%g:%a" "$path")" = "$metadata" ]
      done`,
			secretRead: `dsn="$(cat /app-secrets/database-url-tls)"`,
			summaryBlock: `printf "%s %s\n" \
        "$(printf "%s" "$password" | sha256sum | awk "{print \$1}")" \
        "$(sha256sum /app-secrets/postgres-ca.crt | awk "{print \$1}")"`,
		},
		{
			label:      "PostgreSQL",
			assignment: `postgres_summary="$(docker run`,
			parse:      postgresParse,
			user:       "0:70",
			volume:     "asteria-drive-staging-postgres-secrets",
			mount:      "type=volume,source=asteria-drive-staging-postgres-secrets,target=/postgres-secrets,readonly",
			manifestBlock: `for specification in \
        "/postgres-secrets/postgres-password 0:0:400" \
        "/postgres-secrets/postgres-ca.crt 0:70:640" \
        "/postgres-secrets/postgres-server.crt 0:70:640" \
        "/postgres-secrets/postgres-server.key 0:70:640" \
        "/postgres-secrets/pg_hba.conf 0:70:640"; do
        path="${specification% *}"
        metadata="${specification##* }"
        [ -s "$path" ] && [ ! -L "$path" ] && [ "$(stat -c "%u:%g:%a" "$path")" = "$metadata" ]
      done`,
			secretRead: `password="$(cat /postgres-secrets/postgres-password)"`,
			summaryBlock: `printf "%s %s\n" \
        "$(printf "%s" "$password" | sha256sum | awk "{print \$1}")" \
        "$(sha256sum /postgres-secrets/postgres-ca.crt | awk "{print \$1}")"`,
		},
	}

	var issues []string
	helperAssignment := `helper_image="` + stagingSecretHelperImage + `"`
	if count := strings.Count(stripShellComments(script), helperAssignment); count != 1 || strings.Index(script, helperAssignment) > strings.Index(script, functionName+"() {") {
		issues = append(issues, fmt.Sprintf("staging %s must bind one reviewed helper image before the secret probes", name))
	}
	expectedDockerRuns := 2
	if name == "recovery" {
		expectedDockerRuns = 3
	}
	if count := strings.Count(body, "docker run"); count != expectedDockerRuns {
		issues = append(issues, fmt.Sprintf("staging %s audits %d literal docker run commands, want %d", name, count, expectedDockerRuns))
	}
	normalizedBody := strings.ReplaceAll(body, "\\\r\n", " ")
	normalizedBody = strings.ReplaceAll(normalizedBody, "\\\n", " ")
	if strings.Contains(normalizedBody, "docker container run") {
		issues = append(issues, fmt.Sprintf("staging %s must not use an unaudited docker container run alias", name))
	}
	for _, contract := range contracts {
		start := strings.Index(body, contract.assignment)
		end := strings.Index(body, contract.parse)
		if start < 0 || end < 0 || end <= start {
			issues = append(issues, fmt.Sprintf("staging %s %s-owned secret probe or summary parse is missing", name, contract.label))
			continue
		}
		if count := strings.Count(body, contract.assignment); count != 1 {
			issues = append(issues, fmt.Sprintf("staging %s has %d %s-owned summary probes, want one", name, count, contract.label))
		}
		probe := body[start:end]
		issues = append(issues, stagingSecretProbeErrors(probe, name, contract)...)
	}

	ordered := []string{
		contracts[0].assignment,
		appParse,
		appCanonical,
		appValidation,
		contracts[1].assignment,
		postgresParse,
		postgresCanonical,
		postgresValidation,
		comparison,
	}
	cursor := 0
	for _, marker := range ordered {
		index := strings.Index(body[cursor:], marker)
		if index < 0 {
			issues = append(issues, fmt.Sprintf("staging %s secret summary contract is missing or out of order: %q", name, marker))
			break
		}
		cursor += index + len(marker)
	}

	// Audit every secret-volume Docker command, including recovery's capacity probe.
	for index, command := range strings.Split(body, "docker run")[1:] {
		secretLiteral := strings.Contains(command, "asteria-drive-staging-") && strings.Contains(command, "-secrets")
		tokens, ok := dockerInvocationTokens(command)
		if !ok {
			if secretLiteral {
				issues = append(issues, fmt.Sprintf("staging %s secret Docker command %d has no bounded invocation header", name, index+1))
			}
			continue
		}
		header := strings.Join(tokens, " ")
		appMounted := strings.Contains(header, contracts[0].volume)
		postgresMounted := strings.Contains(header, contracts[1].volume)
		if !appMounted && !postgresMounted {
			if secretLiteral {
				issues = append(issues, fmt.Sprintf("staging %s secret Docker command %d uses an unexpected secret volume", name, index+1))
			}
			continue
		}
		if appMounted && postgresMounted {
			issues = append(issues, fmt.Sprintf("staging %s secret Docker command %d combines application and PostgreSQL volumes", name, index+1))
			continue
		}
		expected := contracts[0]
		if postgresMounted {
			expected = contracts[1]
		}
		expectedMount := expected.mount
		lifecycle := []string{"--rm"}
		if appMounted && strings.Contains(header, `$capacity_container`) {
			expectedMount = "type=volume,source=asteria-drive-staging-app-secrets,target=/secrets,readonly"
			lifecycle = []string{"--name", `"$capacity_container"`}
		}
		context := fmt.Sprintf("staging %s secret Docker command %d", name, index+1)
		issues = append(issues, stagingSecretDockerHeaderErrors(tokens, expected.user, expectedMount, lifecycle, context)...)
		if count := strings.Count(command, `--entrypoint /bin/sh "$helper_image" -ec '`); count != 1 {
			issues = append(issues, fmt.Sprintf("%s has %d reviewed helper command tails, want one", context, count))
		}
	}
	return issues
}

func stagingSecretProbeErrors(probe, name string, contract stagingSecretProbeContract) []string {
	var issues []string
	tokens, ok := dockerInvocationTokens(probe)
	if !ok {
		return []string{fmt.Sprintf("staging %s %s-owned probe has no bounded Docker invocation header", name, contract.label)}
	}
	context := fmt.Sprintf("staging %s %s-owned probe", name, contract.label)
	issues = append(issues, stagingSecretDockerHeaderErrors(tokens, contract.user, contract.mount, []string{"--rm"}, context)...)
	if count := strings.Count(probe, `--entrypoint /bin/sh "$helper_image" -ec '`); count != 1 {
		issues = append(issues, fmt.Sprintf("%s has %d reviewed helper command tails, want one", context, count))
	}
	if count := strings.Count(probe, contract.manifestBlock); count != 1 {
		issues = append(issues, fmt.Sprintf("staging %s %s-owned probe has %d exact metadata manifest blocks, want one", name, contract.label, count))
	}
	manifestIndex := strings.Index(probe, contract.manifestBlock)
	readIndex := strings.Index(probe, contract.secretRead)
	summaryIndex := strings.Index(probe, contract.summaryBlock)
	if manifestIndex < 0 || readIndex < 0 || summaryIndex < 0 || !(manifestIndex < readIndex && readIndex < summaryIndex) {
		issues = append(issues, fmt.Sprintf("staging %s %s-owned manifest, secret read, and bound hash summary are missing or out of order", name, contract.label))
	}
	return issues
}

func stagingSecretDockerHeaderErrors(tokens []string, user, mount string, lifecycle []string, context string) []string {
	var issues []string
	requiredSequences := [][]string{{"--pull", "never"}, {"--network", "none"}, {"--user", user}, {"--cap-drop", "ALL"}, {"--mount", mount}, {"--security-opt", "no-new-privileges:true"}, {"--log-driver", "none"}, lifecycle}
	for _, required := range requiredSequences {
		if count := countTokenSequence(tokens, required...); count != 1 {
			issues = append(issues, fmt.Sprintf("%s has %d exact %q sequences, want one", context, count, required))
		}
	}
	for _, required := range []string{"--pull", "--read-only", "--user", "--network", "--cap-drop", "--mount", "--security-opt", "--log-driver", lifecycle[0]} {
		if count := countToken(tokens, required); count != 1 {
			issues = append(issues, fmt.Sprintf("%s has %d exact %q tokens, want one", context, count, required))
		}
	}
	oppositeLifecycle := "--name"
	if lifecycle[0] == "--name" {
		oppositeLifecycle = "--rm"
	}
	if countToken(tokens, oppositeLifecycle) != 0 {
		issues = append(issues, fmt.Sprintf("%s contains conflicting lifecycle option %q", context, oppositeLifecycle))
	}
	for _, forbidden := range []string{"--privileged", "--cap-add", "--network=host", "--volume", "--volumes-from", "--use-api-socket"} {
		if countToken(tokens, forbidden) != 0 {
			issues = append(issues, fmt.Sprintf("%s contains forbidden token %q", context, forbidden))
		}
	}
	for _, token := range tokens {
		if strings.HasPrefix(token, "-") && !strings.HasPrefix(token, "--") {
			issues = append(issues, fmt.Sprintf("%s contains forbidden short Docker option %q", context, token))
		}
		if strings.HasPrefix(token, "--pull=") || strings.HasPrefix(token, "--rm=") || strings.HasPrefix(token, "--name=") || strings.HasPrefix(token, "--read-only=") || strings.HasPrefix(token, "--user=") || strings.HasPrefix(token, "--network=") || strings.HasPrefix(token, "--cap-drop=") || strings.HasPrefix(token, "--mount=") || strings.HasPrefix(token, "--volume=") || strings.HasPrefix(token, "--privileged=") || strings.HasPrefix(token, "--cap-add=") || strings.HasPrefix(token, "--volumes-from=") || strings.HasPrefix(token, "--use-api-socket=") || strings.HasPrefix(token, "--security-opt=") || strings.HasPrefix(token, "--log-driver=") {
			issues = append(issues, fmt.Sprintf("%s contains forbidden or non-canonical Docker option %q", context, token))
		}
	}
	return issues
}

func dockerInvocationTokens(command string) ([]string, bool) {
	end := strings.Index(command, "--entrypoint")
	if end < 0 {
		return nil, false
	}
	header := strings.ReplaceAll(command[:end], "\\\r\n", " ")
	header = strings.ReplaceAll(header, "\\\n", " ")
	return strings.Fields(header), true
}

func countToken(tokens []string, token string) int {
	count := 0
	for _, candidate := range tokens {
		if candidate == token {
			count++
		}
	}
	return count
}

func countTokenSequence(tokens []string, sequence ...string) int {
	count := 0
	for index := 0; index+len(sequence) <= len(tokens); index++ {
		if slices.Equal(tokens[index:index+len(sequence)], sequence) {
			count++
		}
	}
	return count
}

func shellFunctionBody(script, name string) (string, bool) {
	marker := name + "() {"
	start := strings.Index(script, marker)
	if start < 0 {
		return "", false
	}
	remainder := script[start+len(marker):]
	end := regexp.MustCompile(`(?m)^}\r?$`).FindStringIndex(remainder)
	if end == nil {
		return "", false
	}
	return remainder[:end[0]], true
}

func replaceShellFunctionOnce(script, name, old, replacement string) string {
	marker := name + "() {"
	start := strings.Index(script, marker)
	if start < 0 {
		return script
	}
	bodyStart := start + len(marker)
	body, ok := shellFunctionBody(script, name)
	if !ok {
		return script
	}
	mutated := strings.Replace(body, old, replacement, 1)
	return script[:bodyStart] + mutated + script[bodyStart+len(body):]
}

func stripShellComments(script string) string {
	lines := strings.Split(script, "\n")
	for lineIndex, line := range lines {
		var quote byte
		escaped := false
		for index := 0; index < len(line); index++ {
			character := line[index]
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' && quote != '\'' {
				escaped = true
				continue
			}
			if quote != 0 {
				if character == quote {
					quote = 0
				}
				continue
			}
			if character == '\'' || character == '"' {
				quote = character
				continue
			}
			if character == '#' && (index == 0 || strings.ContainsRune(" \t;|&()", rune(line[index-1]))) {
				line = line[:index]
				break
			}
		}
		lines[lineIndex] = line
	}
	return strings.Join(lines, "\n")
}

func TestStagingSecretOwnershipBoundaryRejectsRegressions(t *testing.T) {
	t.Parallel()

	appMount := "--mount type=volume,source=asteria-drive-staging-app-secrets,target=/app-secrets,readonly"
	postgresMount := "--mount type=volume,source=asteria-drive-staging-postgres-secrets,target=/postgres-secrets,readonly"
	capacityMount := "--mount type=volume,source=asteria-drive-staging-app-secrets,target=/secrets,readonly"
	appInvocation := `app_summary="$(docker run --rm --pull never --network none --read-only --user 65532:65532`
	appParse := `IFS=' ' read -r app_password_sha256 app_ca_sha256 app_extra <<<"$app_summary"`
	appCanonical := `[[ "$app_summary" == "$app_password_sha256 $app_ca_sha256" ]] || return 1`
	comparison := `[[ "$app_password_sha256" == "$postgres_password_sha256" && "$app_ca_sha256" == "$postgres_ca_sha256" ]] || return 1`
	postgresParse := `IFS=' ' read -r postgres_password_sha256 postgres_ca_sha256 postgres_extra <<<"$postgres_summary"`
	appPasswordHash := `"$(printf "%s" "$password" | sha256sum | awk "{print \$1}")"`
	appCAHash := `"$(sha256sum /app-secrets/postgres-ca.crt | awk "{print \$1}")"`
	helperAssignment := `helper_image="` + stagingSecretHelperImage + `"`
	helperTail := `--entrypoint /bin/sh "$helper_image" -ec '`
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "monitor", path: "../../scripts/monitor-staging.sh"},
		{name: "recovery", path: "../../scripts/recover-staging.sh"},
	} {
		t.Run(test.name, func(t *testing.T) {
			contents, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			script := string(contents)
			functionName := map[string]string{"monitor": "verify_postgres_dsn", "recovery": "verify_app_secret_metadata"}[test.name]
			if issues := stagingSecretOwnershipBoundaryErrors(script, test.name); len(issues) != 0 {
				t.Fatalf("valid %s contract rejected: %v", test.name, issues)
			}
			swappedUsers := strings.ReplaceAll(script, "--user 65532:65532", "--user __APP_OWNER__")
			swappedUsers = strings.ReplaceAll(swappedUsers, "--user 0:70", "--user 65532:65532")
			swappedUsers = strings.ReplaceAll(swappedUsers, "--user __APP_OWNER__", "--user 0:70")
			reordered := strings.Replace(script, comparison, "", 1)
			reordered = strings.Replace(reordered, postgresParse, comparison+"\n  "+postgresParse, 1)
			mutations := map[string]string{
				"true UID swap":                       swappedUsers,
				"default user":                        strings.Replace(script, appInvocation, `app_summary="$(docker run --rm --pull never --network none --read-only`, 1),
				"root user":                           strings.Replace(script, appInvocation, `app_summary="$(docker run --rm --pull never --network none --read-only --user root`, 1),
				"summary rm removed":                  strings.Replace(script, appInvocation, strings.Replace(appInvocation, "--rm ", "", 1), 1),
				"pull policy changed":                 replaceShellFunctionOnce(script, functionName, "--pull never", "--pull always"),
				"log driver changed":                  replaceShellFunctionOnce(script, functionName, "--log-driver none", "--log-driver json-file"),
				"helper image variable changed":       strings.Replace(script, helperAssignment, `helper_image="$postgres_image"`, 1),
				"helper command tail changed":         replaceShellFunctionOnce(script, functionName, helperTail, `--entrypoint /bin/sh "$postgres_image" -ec '`),
				"PG-before-app combined mounts":       strings.Replace(script, appMount, postgresMount+" "+appMount, 1),
				"read-write secret mount":             strings.Replace(script, appMount, strings.TrimSuffix(appMount, "readonly")+"rw", 1),
				"unrelated extra mount":               strings.Replace(script, appMount, appMount+" --mount type=bind,source=/tmp,target=/extra,readonly", 1),
				"privileged equals option":            strings.Replace(script, appMount, appMount+" --privileged=true", 1),
				"cap-add equals option":               strings.Replace(script, appMount, appMount+" --cap-add=ALL", 1),
				"short volume equals option":          strings.Replace(script, appMount, appMount+" -v=/tmp:/extra", 1),
				"short user override":                 strings.Replace(script, appMount, appMount+" -u root", 1),
				"compact short user override":         strings.Replace(script, appMount, appMount+" -uroot", 1),
				"short user equals override":          strings.Replace(script, appMount, appMount+" -u=root", 1),
				"volumes-from override":               strings.Replace(script, appMount, appMount+" --volumes-from asteria-drive-staging-postgres-1:ro", 1),
				"API socket mount":                    strings.Replace(script, appMount, appMount+" --use-api-socket", 1),
				"security option replaced":            replaceShellFunctionOnce(script, functionName, "--security-opt no-new-privileges:true", "--security-opt seccomp=unconfined"),
				"secret volume prefix collision":      strings.Replace(script, "source=asteria-drive-staging-app-secrets,target=/app-secrets", "source=asteria-drive-staging-app-secrets-copy,target=/app-secrets", 1),
				"read-only false":                     strings.Replace(script, "--read-only --user 65532:65532", "--read-only=false --user 65532:65532", 1),
				"flag only in comment":                strings.Replace(script, `app_summary="$(docker run --rm --pull never --network none`, "# --network none\n  "+`app_summary="$(docker run --rm --pull never --network bridge`, 1),
				"metadata only in comment":            strings.Replace(script, `"/app-secrets/database-url-tls 65532:65532:400"`, `"/app-secrets/database-url-tls 65532:65532:401" # "/app-secrets/database-url-tls 65532:65532:400"`, 1),
				"password summary replaced by CA":     strings.Replace(script, appPasswordHash, appCAHash, 1),
				"summary parse only in comment":       strings.Replace(script, appParse, "# "+appParse, 1),
				"multiline summary guard removed":     strings.Replace(script, appCanonical, "[[ -n \"$app_summary\" ]] # "+appCanonical, 1),
				"cross-boundary comparison reordered": reordered,
			}
			if test.name == "recovery" {
				mutations["capacity docker alias"] = replaceShellFunctionOnce(script, "verify_app_secret_metadata", "timeout 30 docker run", "timeout 30 docker container run")
				mutations["capacity split docker command"] = replaceShellFunctionOnce(script, "verify_app_secret_metadata", "timeout 30 docker run", "timeout 30 docker \\\n    run")
				mutations["capacity lifecycle removed"] = replaceShellFunctionOnce(script, "verify_app_secret_metadata", `--name "$capacity_container"`, "")
				mutations["capacity probe extra mount"] = strings.Replace(script, capacityMount, capacityMount+" --mount type=bind,source=/tmp,target=/extra,readonly", 1)
				mutations["capacity probe cap-drop removed"] = replaceShellFunctionOnce(script, "verify_app_secret_metadata", "--user 65532:65532 --read-only --cap-drop ALL", "--user 65532:65532 --read-only")
				mutations["capacity probe read-write secret"] = strings.Replace(script, capacityMount, strings.TrimSuffix(capacityMount, "readonly")+"rw", 1)
			}
			for name, mutated := range mutations {
				t.Run(name, func(t *testing.T) {
					if mutated == script {
						t.Fatal("test mutation did not change the script")
					}
					if issues := stagingSecretOwnershipBoundaryErrors(mutated, test.name); len(issues) == 0 {
						t.Fatalf("mutated %s secret boundary was accepted", test.name)
					}
				})
			}
		})
	}
}

func TestStagingPostgresTLSBooleanTextContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		variable string
	}{
		{name: "deploy", path: "../../scripts/deploy-staging.sh", variable: "tls_all"},
		{name: "monitor", path: "../../scripts/monitor-staging.sh", variable: "tls_all"},
		{name: "recovery", path: "../../scripts/recover-staging.sh", variable: "source_tls_all"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contents, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(contents)
			if !strings.Contains(text, "COALESCE(bool_and(s.ssl), false)::text") {
				t.Fatal("PostgreSQL TLS aggregate must be returned as text")
			}
			if !strings.Contains(text, fmt.Sprintf(`[[ "$%s" == "true"`, test.variable)) {
				t.Fatalf("PostgreSQL boolean text variable %s is not compared with true", test.variable)
			}
			if strings.Contains(text, fmt.Sprintf(`[[ "$%s" == "t"`, test.variable)) {
				t.Fatalf("PostgreSQL boolean text variable %s is compared with psql's non-cast display value", test.variable)
			}
		})
	}
}

func TestStagingDeploymentFilesUseStableLineEndings(t *testing.T) {
	t.Parallel()

	attributes, err := os.ReadFile("../../.gitattributes")
	if err != nil {
		t.Fatal(err)
	}
	text := string(attributes)
	for _, required := range []string{
		"/.github/workflows/deploy-staging.yml text eol=lf",
		"/.github/workflows/drill-staging-recovery.yml text eol=lf",
		"/.github/workflows/monitor-staging.yml text eol=lf",
		"/infra/docker/staging/compose.yaml text eol=lf",
		"/scripts/bootstrap-staging-host.sh text eol=lf",
		"/scripts/deploy-staging.sh text eol=lf",
		"/scripts/monitor-staging.sh text eol=lf",
		"/scripts/recover-staging.sh text eol=lf",
	} {
		if !strings.Contains(text, required) {
			t.Errorf(".gitattributes is missing %q", required)
		}
	}
}
