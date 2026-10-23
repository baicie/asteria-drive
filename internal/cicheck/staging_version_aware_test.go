package cicheck

import (
	"os"
	"strings"
	"testing"
)

func TestVersionAwareMonitorAndRecoveryUseOnlyApprovedVersions(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		path      string
		oldHash   string
		newHash   string
		selection string
	}{
		{
			path:      "../../.github/workflows/monitor-staging.yml",
			oldHash:   "3ef39670c2a5a90b9aee4bf926c7dd6b40938038d861457b0ed92ba48765c279",
			newHash:   "d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69",
			selection: "SELECTED_MONITOR_SHA256",
		},
		{
			path:      "../../.github/workflows/drill-staging-recovery.yml",
			oldHash:   "3b2bb836852893548f1f1d64602745c67b02a307f0c8ad5fcf17c9c5a41f459a",
			newHash:   "91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4",
			selection: "SELECTED_RECOVERY_SHA256",
		},
	} {
		contents, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatalf("read %s: %v", test.path, err)
		}
		text := string(contents)
		for _, required := range []string{
			"for candidate in v0.2.0 v0.1.1; do",
			"no approved staging",
			"SELECTED_VERSION",
			test.selection,
			test.oldHash,
			test.newHash,
		} {
			if !strings.Contains(text, required) {
				t.Errorf("%s is missing %q", test.path, required)
			}
		}
		if strings.Contains(text, "for candidate in") && strings.Contains(text, "AllowAny") {
			t.Errorf("%s contains an allow-any marker", test.path)
		}
	}
}
