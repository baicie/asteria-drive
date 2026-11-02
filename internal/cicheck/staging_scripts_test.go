package cicheck

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestStagingHostScriptsParse(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("host script parsing runs under Linux CI")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	scripts := []string{
		"../../scripts/bootstrap-staging-host.sh",
		"../../scripts/deploy-staging.sh",
		"../../scripts/monitor-staging.sh",
		"../../scripts/recover-staging.sh",
	}
	args := append([]string{"-n"}, scripts...)
	if output, err := exec.Command(bash, args...).CombinedOutput(); err != nil {
		t.Fatalf("bash syntax check failed: %v\n%s", err, output)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	if output, err := exec.Command(python, "-m", "py_compile",
		"../../scripts/staging-release-controller.py",
		"../../scripts/staging_runtime_verifier.py",
	).CombinedOutput(); err != nil {
		t.Fatalf("python syntax check failed: %v\n%s", err, output)
	}
}
