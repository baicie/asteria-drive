package cicheck

import (
	"os"
	"strings"
	"testing"
)

func TestRepositoryIntegrationManifestCoverage(t *testing.T) {
	t.Parallel()

	file, err := os.Open("../../.github/integration-tests.json")
	if err != nil {
		t.Fatalf("open repository integration manifest: %v", err)
	}
	defer file.Close()
	manifest, err := LoadIntegrationManifest(file)
	if err != nil {
		t.Fatalf("load repository integration manifest: %v", err)
	}

	expectedCounts := map[string]int{
		"github.com/baicie/asteria-drive/internal/postgres": 16,
		"github.com/baicie/asteria-drive/internal/s3store":  1,
		"github.com/baicie/asteria-drive/internal/server":   2,
	}
	if len(manifest.Packages) != len(expectedCounts) {
		t.Fatalf("repository manifest has %d packages, want %d", len(manifest.Packages), len(expectedCounts))
	}
	for _, requirement := range manifest.Packages {
		expected, ok := expectedCounts[requirement.Package]
		if !ok {
			t.Errorf("repository manifest contains unexpected package %q", requirement.Package)
			continue
		}
		if len(requirement.Tests) != expected {
			t.Errorf("repository manifest package %q has %d tests, want %d", requirement.Package, len(requirement.Tests), expected)
		}
	}
}

func TestVerifyIntegrationReportRequiresPassAndRejectsSkip(t *testing.T) {
	t.Parallel()

	manifestJSON := `{"packages":[{"package":"example/postgres","tests":["TestRepository","TestMigration"]},{"package":"example/s3","tests":["TestStorage"]}]}`
	manifest, err := LoadIntegrationManifest(strings.NewReader(manifestJSON))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}

	passing := strings.Join([]string{
		`{"Action":"run","Package":"example/postgres","Test":"TestRepository"}`,
		`{"Action":"pass","Package":"example/postgres","Test":"TestRepository"}`,
		`{"Action":"pass","Package":"example/postgres","Test":"TestMigration"}`,
		`{"Action":"pass","Package":"example/postgres"}`,
		`{"Action":"pass","Package":"example/s3","Test":"TestStorage"}`,
		`{"Action":"pass","Package":"example/s3"}`,
	}, "\n")
	if err := VerifyIntegrationReport(strings.NewReader(passing), manifest); err != nil {
		t.Fatalf("verify passing report: %v", err)
	}

	for name, report := range map[string]string{
		"skip":            strings.Replace(passing, `{"Action":"pass","Package":"example/s3","Test":"TestStorage"}`, `{"Action":"skip","Package":"example/s3","Test":"TestStorage"}`, 1),
		"missing":         strings.Replace(passing, `{"Action":"pass","Package":"example/postgres","Test":"TestMigration"}`+"\n", "", 1),
		"package failure": strings.Replace(passing, `{"Action":"pass","Package":"example/postgres"}`, `{"Action":"fail","Package":"example/postgres"}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyIntegrationReport(strings.NewReader(report), manifest); err == nil {
				t.Fatal("verification unexpectedly accepted invalid evidence")
			}
		})
	}
}

func TestLoadIntegrationManifestRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]string{
		"empty":             `{}`,
		"unknown field":     `{"packages":[],"other":true}`,
		"duplicate package": `{"packages":[{"package":"example/p","tests":["TestA"]},{"package":"example/p","tests":["TestB"]}]}`,
		"duplicate test":    `{"packages":[{"package":"example/p","tests":["TestA","TestA"]}]}`,
		"trailing value":    `{"packages":[{"package":"example/p","tests":["TestA"]}]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadIntegrationManifest(strings.NewReader(input)); err == nil {
				t.Fatal("manifest unexpectedly passed validation")
			}
		})
	}
}
