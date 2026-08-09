package cicheck

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestContainerBuildUsesPinnedBuilderAndNonRootScratchRuntime(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	text := string(contents)
	for _, required := range []string{
		"FROM golang:1.25.12@sha256:", "FROM scratch", "USER 65532:65532",
		"/usr/local/bin/asteria-server", "/usr/local/bin/asteria-migrate",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Dockerfile is missing %q", required)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "FROM ") && line != "FROM scratch" && !strings.Contains(line, "@sha256:") {
			t.Errorf("container build stage is not digest-pinned: %s", line)
		}
	}
}

func TestKubernetesDeploymentHardeningContract(t *testing.T) {
	t.Parallel()
	documents := readYAMLDocuments(t, "../../infra/kubernetes/base/deployment.yaml")
	if len(documents) != 1 || documents[0]["kind"] != "Deployment" {
		t.Fatalf("expected one Deployment document, got %#v", documents)
	}
	text, err := os.ReadFile("../../infra/kubernetes/base/deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	deployment := normalizeContractText(string(text))
	for _, required := range []string{
		"replicas: 2", "runAsNonRoot: true", "runAsUser: 65532", "type: RuntimeDefault",
		"allowPrivilegeEscalation: false", "readOnlyRootFilesystem: true", "drop:\n                - ALL",
		"readinessProbe:", "livenessProbe:", "startupProbe:", "requests:", "limits:",
		"ASTERIA_DATABASE_URL_FILE", "ASTERIA_CURSOR_HMAC_KEY_FILE", "@sha256:",
	} {
		if !strings.Contains(deployment, required) {
			t.Errorf("Deployment hardening contract is missing %q", required)
		}
	}
	configMap := readContractFile(t, "../../infra/kubernetes/base/config-map.yaml")
	if !strings.Contains(configMap, "ASTERIA_S3_PUBLIC_ENDPOINT: https://") {
		t.Error("production ConfigMap must declare an HTTPS client-visible S3 endpoint")
	}
}

func TestKubernetesNetworkPolicyDefaultsToDeny(t *testing.T) {
	t.Parallel()
	documents := readYAMLDocuments(t, "../../infra/kubernetes/base/network-policy.yaml")
	if len(documents) != 2 {
		t.Fatalf("expected default-deny and required-traffic policies, got %d", len(documents))
	}
	defaultDeny := documents[0]
	if defaultDeny["kind"] != "NetworkPolicy" {
		t.Fatalf("first document is not a NetworkPolicy: %#v", defaultDeny)
	}
	spec, ok := defaultDeny["spec"].(map[string]any)
	if !ok {
		t.Fatalf("default-deny policy has no structured spec: %#v", defaultDeny)
	}
	if _, hasIngress := spec["ingress"]; hasIngress {
		t.Fatal("default-deny policy must omit ingress rules")
	}
	if _, hasEgress := spec["egress"]; hasEgress {
		t.Fatal("default-deny policy must omit egress rules")
	}
}

func TestKubernetesMigrationJobUsesTheHardenedRuntimeContract(t *testing.T) {
	t.Parallel()

	documents := readYAMLDocuments(t, "../../infra/kubernetes/migration/job.yaml")
	if len(documents) != 1 || documents[0]["kind"] != "Job" {
		t.Fatalf("expected one Job document, got %#v", documents)
	}
	contents, err := os.ReadFile("../../infra/kubernetes/migration/job.yaml")
	if err != nil {
		t.Fatal(err)
	}
	job := normalizeContractText(string(contents))
	for _, required := range []string{
		"/usr/local/bin/asteria-migrate", "ASTERIA_ENV", "value: production",
		"ASTERIA_DATABASE_URL_FILE", "runAsNonRoot: true", "runAsUser: 65532",
		"type: RuntimeDefault", "allowPrivilegeEscalation: false",
		"readOnlyRootFilesystem: true", "drop:\n                - ALL", "requests:",
		"limits:", "secretName: asteria-runtime-secrets", "defaultMode: 0400", "@sha256:",
	} {
		if !strings.Contains(job, required) {
			t.Errorf("migration Job hardening contract is missing %q", required)
		}
	}
	if strings.Contains(job, "ASTERIA_DATABASE_URL:") {
		t.Error("migration Job must load the database URL from a mounted file, not an environment value")
	}
}

func readYAMLDocuments(t *testing.T, path string) []map[string]any {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	var documents []map[string]any
	for {
		var document map[string]any
		if err := decoder.Decode(&document); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("parse %s: %v", path, err)
		}
		if len(document) != 0 {
			documents = append(documents, document)
		}
	}
	return documents
}
