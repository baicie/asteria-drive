package cicheck

import (
	"encoding/json"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

type composeDocument struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image string `yaml:"image"`
}

type seaweedS3Config struct {
	Identities []struct {
		Credentials []struct {
			AccessKey string `json:"accessKey"`
			SecretKey string `json:"secretKey"`
		} `json:"credentials"`
	} `json:"identities"`
}

func TestComposeRequiredServiceImagesArePinned(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Fatalf("read Compose configuration: %v", err)
	}

	var compose composeDocument
	if err := yaml.Unmarshal(contents, &compose); err != nil {
		t.Fatalf("parse Compose configuration: %v", err)
	}

	expectedImages := map[string]string{
		"postgres":  "postgres:17.5-alpine@sha256:6567bca8d7bc8c82c5922425a0baee57be8402df92bae5eacad5f01ae9544daa",
		"seaweedfs": "chrislusf/seaweedfs:3.85@sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a",
	}
	for serviceName, expectedImage := range expectedImages {
		service, ok := compose.Services[serviceName]
		if !ok {
			t.Errorf("required Compose service %q is missing", serviceName)
			continue
		}
		if service.Image != expectedImage {
			t.Errorf("Compose service %q image = %q, want %q", serviceName, service.Image, expectedImage)
		}
	}
}

func TestIntegrationWorkflowUsesConfiguredSeaweedFSCredentials(t *testing.T) {
	t.Parallel()

	workflowContents, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	var workflow workflowDocument
	if err := yaml.Unmarshal(workflowContents, &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	integration, ok := workflow.Jobs["integration"]
	if !ok {
		t.Fatal("CI workflow is missing integration job")
	}
	accessKey, accessKeyOK := integration.Env["ASTERIA_TEST_S3_ACCESS_KEY"].(string)
	secretKey, secretKeyOK := integration.Env["ASTERIA_TEST_S3_SECRET_KEY"].(string)
	if !accessKeyOK || !secretKeyOK || accessKey == "" || secretKey == "" {
		t.Fatalf("integration S3 credentials are missing: %#v", integration.Env)
	}

	configContents, err := os.ReadFile("../../infra/seaweedfs/s3.json")
	if err != nil {
		t.Fatalf("read SeaweedFS S3 configuration: %v", err)
	}
	var config seaweedS3Config
	if err := json.Unmarshal(configContents, &config); err != nil {
		t.Fatalf("parse SeaweedFS S3 configuration: %v", err)
	}
	for _, identity := range config.Identities {
		for _, credential := range identity.Credentials {
			if credential.AccessKey == accessKey && credential.SecretKey == secretKey {
				return
			}
		}
	}
	t.Fatal("integration S3 credentials do not match any configured SeaweedFS identity")
}
