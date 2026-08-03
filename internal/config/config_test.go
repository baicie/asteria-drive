package config

import (
	"strings"
	"testing"
)

const validTokens = `{"local-development-token-000000000000":{"tenant_id":"11111111-1111-4111-8111-111111111111","principal_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","tenant_name":"Local"}}`

func TestLoadValidDevelopmentConfiguration(t *testing.T) {
	configuration, err := load(mapLookup(map[string]string{
		"ASTERIA_TRUSTED_TOKENS_JSON": validTokens,
		"ASTERIA_CURSOR_HMAC_KEY":     "test-cursor-hmac-key-at-least-32-bytes",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Environment != "development" || configuration.MetadataDriver != "memory" || configuration.StorageDriver != "memory" {
		t.Fatalf("unexpected defaults: %+v", configuration)
	}
	if configuration.S3ChecksumHeaders {
		t.Fatal("S3 checksum headers must default off for custom MVP endpoints")
	}
}

func TestLoadRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	base := map[string]string{
		"ASTERIA_TRUSTED_TOKENS_JSON": validTokens,
		"ASTERIA_CURSOR_HMAC_KEY":     "test-cursor-hmac-key-at-least-32-bytes",
	}
	tests := []struct {
		name    string
		values  map[string]string
		message string
	}{
		{name: "trusted dev in production", values: map[string]string{"ASTERIA_ENV": "production"}, message: "forbidden"},
		{name: "short cursor key", values: map[string]string{"ASTERIA_CURSOR_HMAC_KEY": "short"}, message: "at least 32 bytes"},
		{name: "short trusted token", values: map[string]string{"ASTERIA_TRUSTED_TOKENS_JSON": `{"short":{"tenant_id":"11111111-1111-4111-8111-111111111111","principal_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}`}, message: "32-byte token"},
		{name: "postgres without URL", values: map[string]string{"ASTERIA_METADATA_DRIVER": "postgres"}, message: "DATABASE_URL"},
		{name: "S3 without endpoint", values: map[string]string{"ASTERIA_STORAGE_DRIVER": "s3"}, message: "endpoint"},
		{name: "negative HTTP timeout", values: map[string]string{"ASTERIA_READ_TIMEOUT": "-1s"}, message: "must be positive"},
		{name: "long signing TTL", values: map[string]string{"ASTERIA_UPLOAD_SIGN_TTL": "2h"}, message: "TTL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := make(map[string]string, len(base)+len(test.values))
			for key, value := range base {
				values[key] = value
			}
			for key, value := range test.values {
				values[key] = value
			}
			_, err := load(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("load error=%v, want message containing %q", err, test.message)
			}
		})
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
