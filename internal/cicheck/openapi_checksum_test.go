package cicheck

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

type openAPIChecksumVariant struct {
	Type                 string         `yaml:"type"`
	AdditionalProperties bool           `yaml:"additionalProperties"`
	MaxProperties        int            `yaml:"maxProperties"`
	Required             []string       `yaml:"required"`
	Properties           map[string]any `yaml:"properties"`
}

func TestOpenAPIChecksumVariantsAreMutuallyExclusive(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Components struct {
			Schemas map[string]struct {
				OneOf []openAPIChecksumVariant `yaml:"oneOf"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	checksum, ok := document.Components.Schemas["Checksum"]
	if !ok {
		t.Fatal("OpenAPI Checksum schema is missing")
	}
	if len(checksum.OneOf) != 3 {
		t.Fatalf("Checksum oneOf variants = %d, want 3", len(checksum.OneOf))
	}
	empty, explicitEmpty, sha256 := checksum.OneOf[0], checksum.OneOf[1], checksum.OneOf[2]
	if empty.Type != "object" || empty.AdditionalProperties || empty.MaxProperties != 0 {
		t.Fatalf("empty Checksum variant = %#v", empty)
	}
	for name, variant := range map[string]openAPIChecksumVariant{
		"explicit empty": explicitEmpty,
		"sha256":         sha256,
	} {
		if variant.Type != "object" || variant.AdditionalProperties {
			t.Errorf("%s Checksum variant = %#v", name, variant)
		}
		if len(variant.Required) != 2 || variant.Required[0] != "algorithm" || variant.Required[1] != "value" {
			t.Errorf("%s Checksum required = %#v", name, variant.Required)
		}
		if variant.Properties["algorithm"] == nil || variant.Properties["value"] == nil {
			t.Errorf("%s Checksum properties = %#v", name, variant.Properties)
		}
	}
	algorithm, _ := explicitEmpty.Properties["algorithm"].(map[string]any)
	value, _ := explicitEmpty.Properties["value"].(map[string]any)
	if algorithm["const"] != "" || value["const"] != "" {
		t.Fatalf("explicit empty Checksum constants = %#v / %#v", algorithm, value)
	}
	shaAlgorithm, _ := sha256.Properties["algorithm"].(map[string]any)
	shaValue, _ := sha256.Properties["value"].(map[string]any)
	if shaAlgorithm["const"] != "sha256" || shaValue["pattern"] != "^[A-Za-z0-9+/]{43}=$" {
		t.Fatalf("sha256 Checksum constants = %#v / %#v", shaAlgorithm, shaValue)
	}
}
