package buildinfo

import "testing"

func TestString(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, Date
	t.Cleanup(func() {
		Version, Commit, Date = oldVersion, oldCommit, oldDate
	})

	Version = "v0.2.0"
	Commit = "0123456789abcdef"
	Date = "2026-08-05T00:00:00Z"
	if got, want := String("asteria-server"), "asteria-server v0.2.0 (commit 0123456789abcdef, built 2026-08-05T00:00:00Z)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
