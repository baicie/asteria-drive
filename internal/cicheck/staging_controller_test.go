package cicheck

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestHostReleaseControllerFaultInjection(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("host release controller tests run under Linux CI")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	command := exec.Command(python, "-m", "unittest", "discover", "-s", "scripts/tests", "-v")
	command.Dir = "../.."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("controller fault-injection suite failed: %v\n%s", err, output)
	}
	text := string(output)
	for _, name := range []string{
		"test_compose_changed_but_old_image",
		"test_compose_switched_but_old_container_image",
		"test_compose_correct_but_api_stopped",
		"test_compose_correct_but_schema_mismatch",
		"test_active_compose_drift_after_success_rejected_on_replay",
		"test_api_stopped",
		"test_schema_mismatch",
		"test_rollback_succeeded_but_runtime_not_restored",
		"test_compensation_requires_runtime_reverification",
		"test_compensation_accepts_early_rollback_without_deployed_image",
		"test_compensation_with_wrong_previous_image_is_recovery",
		"test_injected_wrong_run_id",
		"test_injected_wrong_workflow_sha",
		"test_injected_wrong_source_commit",
		"test_injected_wrong_evidence_image",
		"test_injected_wrong_evidence_compose",
		"test_replay_runtime_drift_rejected",
		"test_state_permissions_rejected",
		"test_interrupted_attempt_blocked",
		"test_real_adapter_calls_docker_db_and_readiness",
		"test_port_binding_mismatch_rejected",
		"test_command_timeout_is_verification_error",
	} {
		if !strings.Contains(text, name) {
			t.Errorf("fault-injection report is missing %s", name)
		}
	}
	if !strings.Contains(text, "OK") {
		t.Fatalf("fault-injection suite did not report OK:\n%s", text)
	}
}
