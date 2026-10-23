//go:build linux

package cicheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestStagingLockLifecycleAndConcurrency(t *testing.T) {
	t.Parallel()
	flock, err := exec.LookPath("flock")
	if err != nil {
		t.Skip("flock is not installed")
	}
	root := t.TempDir()
	lockPath := filepath.Join(root, "staging.lock")
	if err := os.WriteFile(lockPath, nil, 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0o660); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o660 {
		t.Fatalf("lock mode = %o, want 660", info.Mode().Perm())
	}
	if err := exec.Command(flock, "-n", lockPath, "true").Run(); err != nil {
		t.Fatalf("first lock acquisition failed: %v", err)
	}

	holder := exec.Command(flock, "-n", lockPath, "sleep", "2")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _, _ = holder.Process.Wait() })
	time.Sleep(200 * time.Millisecond)
	if err := exec.Command(flock, "-n", lockPath, "true").Run(); err == nil {
		t.Fatal("concurrent lock acquisition unexpectedly succeeded")
	}
	_ = holder.Wait()

	// Reboot simulation: /run is tmpfs, so recreate the lock path and acquire again.
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, nil, 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0o660); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(flock, "-n", lockPath, "true").Run(); err != nil {
		t.Fatalf("lock after simulated reboot failed: %v", err)
	}

	// A symlink is not a valid dispatcher lock path, even if flock could follow it.
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, nil, 0o660); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "symlink.lock")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(symlink)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink setup failed: %v", err)
	}
	if err := exec.Command("bash", "-c", `[[ -f "$1" && ! -L "$1" ]]`, "_", symlink).Run(); err == nil {
		t.Fatal("dispatcher-style lock validation accepted a symlink")
	}
	if info, err := os.Stat(lockPath); err != nil || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		t.Fatalf("lock owner check failed: %v", err)
	}
}
