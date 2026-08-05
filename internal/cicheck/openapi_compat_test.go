package cicheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const compatibilityFixture = `openapi: 3.1.0
info:
  title: Fixture
  version: 1.0.0
paths:
  /files:
    get:
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                type: object
                required: [id]
                properties:
                  id:
                    type: string
                  name:
                    type: string
`

func TestVerifyOpenAPICompatibilityAllowsAdditiveChanges(t *testing.T) {
	directory := t.TempDir()
	base := filepath.Join(directory, "base.yaml")
	current := filepath.Join(directory, "current.yaml")
	if err := os.WriteFile(base, []byte(compatibilityFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	added := compatibilityFixture + "  /healthz:\n    get:\n      responses:\n        '200':\n          description: ok\n"
	if err := os.WriteFile(current, []byte(added), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyOpenAPICompatibility(base, current); err != nil {
		t.Fatalf("additive change rejected: %v", err)
	}
}

func TestVerifyOpenAPICompatibilityRejectsRemovedProperty(t *testing.T) {
	directory := t.TempDir()
	base := filepath.Join(directory, "base.yaml")
	current := filepath.Join(directory, "current.yaml")
	if err := os.WriteFile(base, []byte(compatibilityFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	removed := strings.Replace(compatibilityFixture, "                  name:\n                    type: string\n", "", 1)
	if err := os.WriteFile(current, []byte(removed), 0o644); err != nil {
		t.Fatal(err)
	}
	err := VerifyOpenAPICompatibility(base, current)
	if err == nil || !strings.Contains(err.Error(), "property name was removed") {
		t.Fatalf("removed property error = %v", err)
	}
}

func TestVerifyOpenAPICompatibilityRejectsRemovedOperation(t *testing.T) {
	directory := t.TempDir()
	base := filepath.Join(directory, "base.yaml")
	current := filepath.Join(directory, "current.yaml")
	if err := os.WriteFile(base, []byte(compatibilityFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	removed := strings.Replace(compatibilityFixture, "    get:", "    post:", 1)
	if err := os.WriteFile(current, []byte(removed), 0o644); err != nil {
		t.Fatal(err)
	}
	err := VerifyOpenAPICompatibility(base, current)
	if err == nil || !strings.Contains(err.Error(), "operation GET /files was removed") {
		t.Fatalf("removed operation error = %v", err)
	}
}
