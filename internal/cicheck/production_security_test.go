package cicheck

import (
	"os"
	"strings"
	"testing"
)

func TestProductionSecurityEvidenceContract(t *testing.T) {
	t.Parallel()

	security := readContractFile(t, "../../SECURITY.md")
	for _, required := range []string{
		"private vulnerability reporting",
		"Do not open a",
		"public issue or pull request",
		"docs/security/threat-model.md",
		"docs/security/review-record.md",
	} {
		if !strings.Contains(security, required) {
			t.Errorf("SECURITY.md is missing %q", required)
		}
	}

	threatModel := readContractFile(t, "../../docs/security/threat-model.md")
	for _, threat := range []string{"TM-01", "TM-02", "TM-03", "TM-04", "TM-08", "TM-11", "TM-13", "TM-14"} {
		if !strings.Contains(threatModel, threat) {
			t.Errorf("threat model is missing %s", threat)
		}
	}
	if !strings.Contains(threatModel, "Explicit non-goals") {
		t.Error("threat model must state explicit non-goals")
	}

	controls := readContractFile(t, "../../docs/security/control-matrix.md")
	for _, control := range []string{"IAM-01", "ACL-01", "AUD-01", "REL-01", "RES-01", "RES-02"} {
		if !strings.Contains(controls, control) {
			t.Errorf("control matrix is missing %s", control)
		}
	}

	review := readContractFile(t, "../../docs/security/review-record.md")
	for _, required := range []string{
		"CANDIDATE_COMMIT_SHA", "CANDIDATE_OCI_MANIFEST_DIGEST", "Pending independent approval",
		"high-severity finding blocks approval", "two\nauthorized reviewers",
	} {
		if !strings.Contains(review, required) {
			t.Errorf("security review record is missing %q", required)
		}
	}
}

func TestProductionRecoveryAutomationContract(t *testing.T) {
	t.Parallel()

	backup := readContractFile(t, "../../scripts/backup-postgres.sh")
	for _, required := range []string{
		"PGSERVICEFILE", `--dbname="service=$PGSERVICE"`, "pg_restore --list", "sha256sum",
		"PGSERVICEFILE must have mode 0600", "backup destination already contains",
	} {
		if !strings.Contains(backup, required) {
			t.Errorf("backup automation is missing %q", required)
		}
	}

	restore := readContractFile(t, "../../scripts/restore-postgres.sh")
	for _, required := range []string{
		"ASTERIA_RESTORE_TARGET_KIND", "ASTERIA_RESTORE_CONFIRM", "asteria_restore_*",
		"sha256sum -c", "--single-transaction", "PGSERVICEFILE must have mode 0600",
	} {
		if !strings.Contains(restore, required) {
			t.Errorf("restore automation is missing %q", required)
		}
	}
	if strings.Contains(backup, "ASTERIA_DATABASE_URL") || strings.Contains(restore, "ASTERIA_DATABASE_URL") {
		t.Error("backup and restore automation must not place a password-bearing database URL on the command line")
	}

	runbook := readContractFile(t, "../../docs/operations/backup-and-restore.md")
	for _, required := range []string{
		"isolated", "ASTERIA_MAINTENANCE_ENABLED=false", "asteria-verify-storage",
		"No RPO or RTO is promised", "zero missing objects", "two-person approval",
	} {
		if !strings.Contains(runbook, required) {
			t.Errorf("recovery runbook is missing %q", required)
		}
	}

	dockerfile := readContractFile(t, "../../Dockerfile")
	if !strings.Contains(dockerfile, "/usr/local/bin/asteria-verify-storage") {
		t.Error("production image must include the storage verifier")
	}
}

func readContractFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}
