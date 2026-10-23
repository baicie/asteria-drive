package cicheck

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHostReleaseControllerWrapsDeployAndPersistsState(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("host controller integration requires Linux and bash/python3")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	releases := filepath.Join(root, "releases.json")
	if err := os.WriteFile(releases, []byte(`{"v0.1.1":{"compose_sha256":"`+strings.Repeat("a", 64)+`","deploy_script_sha256":"`+strings.Repeat("b", 64)+`","image":"ghcr.io/baicie/asteria-drive@sha256:`+strings.Repeat("c", 64)+`"},"v0.2.0":{"compose_sha256":"`+strings.Repeat("d", 64)+`","deploy_script_sha256":"`+strings.Repeat("e", 64)+`","image":"ghcr.io/baicie/asteria-drive@sha256:`+strings.Repeat("f", 64)+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(runDir, "compose.yaml")
	if err := os.WriteFile(compose, []byte("name: asteria\nservices:\n  api:\n    labels:\n      com.baicie.asteria.release: v0.2.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(runDir, "deploy-staging.sh")
	scriptBody := `#!/usr/bin/env bash
set -euo pipefail
compose="$1"
evidence="$2"
compose_sha="${FIXTURE_COMPOSE_SHA:?}"
target="v0.2.0"
cp "$compose" "$ASTERIA_DEPLOY_ROOT/compose.yaml"
cat >"$evidence" <<JSON
{"schema":"asteria-drive-staging-deployment/v2","status":"success","last_phase":"complete","release_tag":"$target","compose_sha256":"$compose_sha","deployed_image_ref":"${FIXTURE_IMAGE:?}","migration_schema_after":3}
JSON
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	// The controller rejects scripts whose SHA does not match the manifest.
	// Rebuild the manifest with the actual hashes of the fixture files.
	composeHash := sha256FileForTest(t, compose)
	scriptHash := sha256FileForTest(t, script)
	manifest := map[string]map[string]any{
		"v0.1.1": {"compose_sha256": strings.Repeat("a", 64), "deploy_script_sha256": strings.Repeat("b", 64), "image": "ghcr.io/baicie/asteria-drive@sha256:" + strings.Repeat("c", 64)},
		"v0.2.0": {"compose_sha256": composeHash, "deploy_script_sha256": scriptHash, "schema_version": 3, "image": "ghcr.io/baicie/asteria-drive@sha256:" + strings.Repeat("f", 64)},
	}
	encoded, _ := json.Marshal(manifest)
	if err := os.WriteFile(releases, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	controller := filepath.Join("..", "..", "scripts", "staging-release-controller.py")
	command := exec.Command(python, controller, "deploy", "1", "1", strings.Repeat("a", 40), "v0.2.0", runDir)
	command.Env = append(os.Environ(), "ASTERIA_DEPLOY_ROOT="+root, "ASTERIA_CONTROLLER_RELEASES="+releases, "ASTERIA_CONTROLLER_STATE="+filepath.Join(root, "state.json"), "FIXTURE_COMPOSE_SHA="+composeHash, "FIXTURE_IMAGE=ghcr.io/baicie/asteria-drive@sha256:"+strings.Repeat("f", 64))
	output, err := command.CombinedOutput()
	if err != nil {
		state, _ := os.ReadFile(filepath.Join(root, "state.json"))
		evidence, _ := os.ReadFile(filepath.Join(runDir, "deployment-evidence.json"))
		t.Fatalf("controller failed: %v\nstdout/stderr: %s\nstate: %s\nevidence: %s", err, output, state, evidence)
	}
	stateBytes, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		t.Fatal(err)
	}
	if state["state"] != "ACTIVE" || state["target_version"] != "v0.2.0" {
		t.Fatalf("unexpected controller state: %#v", state)
	}
	// Same run replay must be a no-op and must re-read the active compose.
	replay := exec.Command(python, controller, "deploy", "1", "1", strings.Repeat("a", 40), "v0.2.0", runDir)
	replay.Env = command.Env
	if output, err := replay.CombinedOutput(); err != nil {
		t.Fatalf("controller replay failed: %v\n%s", err, output)
	}

	// An interrupted attempt must fail closed rather than rerun a destructive step.
	if err := os.WriteFile(filepath.Join(root, "state.json"), []byte(`{"format":"asteria-drive-staging-controller/v1","run_id":"2","attempt":"1","target_version":"v0.2.0","state":"ATTEMPTING"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := exec.Command(python, controller, "deploy", "2", "1", strings.Repeat("a", 40), "v0.2.0", runDir)
	blocked.Env = command.Env
	if output, err := blocked.CombinedOutput(); err == nil {
		t.Fatalf("interrupted attempt was not blocked: %s", output)
	}
}

func sha256FileForTest(t *testing.T, path string) string {
	t.Helper()
	command := exec.Command("sha256sum", path)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("sha256sum: %v", err)
	}
	return strings.Fields(string(output))[0]
}
