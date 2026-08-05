package config

import (
	"os"
	"path/filepath"
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

func TestLoadMaintenanceDefaultsAndRejectsInvalidBatch(t *testing.T) {
	configuration, err := load(mapLookup(map[string]string{
		"ASTERIA_TRUSTED_TOKENS_JSON": validTokens,
		"ASTERIA_CURSOR_HMAC_KEY":     "test-cursor-hmac-key-at-least-32-bytes",
	}))
	if err != nil || !configuration.MaintenanceEnabled || configuration.MaintenanceBatchSize != 50 {
		t.Fatalf("maintenance defaults: config=%+v err=%v", configuration, err)
	}
	_, err = load(mapLookup(map[string]string{
		"ASTERIA_TRUSTED_TOKENS_JSON":    validTokens,
		"ASTERIA_CURSOR_HMAC_KEY":        "test-cursor-hmac-key-at-least-32-bytes",
		"ASTERIA_MAINTENANCE_BATCH_SIZE": "1001",
	}))
	if err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("invalid maintenance batch error=%v", err)
	}
}

func TestLoadValidOIDCConfiguration(t *testing.T) {
	configuration, err := load(mapLookup(map[string]string{
		"ASTERIA_AUTH_MODE":           "oidc",
		"ASTERIA_OIDC_ISSUER":         "http://issuer.example.test",
		"ASTERIA_OIDC_CLIENT_ID":      "asteria-api",
		"ASTERIA_OIDC_BOOTSTRAP_JSON": `[{"issuer":"http://issuer.example.test","subject":"user-1","principal_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","tenant_id":"11111111-1111-4111-8111-111111111111","tenant_name":"Tenant","display_name":"User","role":"owner"}]`,
		"ASTERIA_CURSOR_HMAC_KEY":     "test-cursor-hmac-key-at-least-32-bytes",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.AuthMode != "oidc" || len(configuration.OIDCBootstrap) != 1 || configuration.OIDCBootstrap[0].Role != "owner" {
		t.Fatalf("unexpected OIDC configuration: %+v", configuration)
	}
}

func TestLoadAllowsOnePrincipalInMultipleTenants(t *testing.T) {
	configuration, err := load(mapLookup(map[string]string{
		"ASTERIA_AUTH_MODE":      "oidc",
		"ASTERIA_OIDC_ISSUER":    "https://issuer.example.test",
		"ASTERIA_OIDC_CLIENT_ID": "asteria-api",
		"ASTERIA_OIDC_BOOTSTRAP_JSON": `[{
			"issuer":"https://issuer.example.test","subject":"user-1","principal_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","tenant_id":"11111111-1111-4111-8111-111111111111","role":"viewer"
		},{
			"issuer":"https://issuer.example.test","subject":"user-1","principal_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","tenant_id":"22222222-2222-4222-8222-222222222222","role":"editor"
		}]`,
		"ASTERIA_CURSOR_HMAC_KEY": "test-cursor-hmac-key-at-least-32-bytes",
	}))
	if err != nil || len(configuration.OIDCBootstrap) != 2 {
		t.Fatalf("one principal should be allowed in two tenants: config=%+v err=%v", configuration, err)
	}
}

func TestLoadValidProductionOIDCConfiguration(t *testing.T) {
	databaseFile := writeConfigFile(t, "database-url", "postgres://asteria:password@db.example.test/asteria?sslmode=verify-full\n")
	cursorFile := writeConfigFile(t, "cursor-key", "test-cursor-hmac-key-at-least-32-bytes\n")
	configuration, err := load(mapLookup(map[string]string{
		"ASTERIA_ENV":                  "production",
		"ASTERIA_AUTH_MODE":            "oidc",
		"ASTERIA_OIDC_ISSUER":          "https://issuer.example.test",
		"ASTERIA_OIDC_CLIENT_ID":       "asteria-api",
		"ASTERIA_OIDC_BOOTSTRAP_JSON":  `[{"issuer":"https://issuer.example.test","subject":"user-1","principal_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","tenant_id":"11111111-1111-4111-8111-111111111111","role":"owner"}]`,
		"ASTERIA_METADATA_DRIVER":      "postgres",
		"ASTERIA_DATABASE_URL_FILE":    databaseFile,
		"ASTERIA_STORAGE_DRIVER":       "s3",
		"ASTERIA_S3_ENDPOINT":          "https://s3.example.test",
		"ASTERIA_S3_REGION":            "us-east-1",
		"ASTERIA_S3_BUCKET":            "asteria",
		"ASTERIA_CURSOR_HMAC_KEY_FILE": cursorFile,
	}))
	if err != nil || configuration.Environment != "production" || configuration.AuthMode != "oidc" {
		t.Fatalf("valid production OIDC configuration rejected: config=%+v err=%v", configuration, err)
	}
}

func TestLoadReadsSecretFilesAndRejectsAmbiguousSources(t *testing.T) {
	cursorFile := writeConfigFile(t, "cursor-key", "file-cursor-hmac-key-at-least-32-bytes\r\n")
	configuration, err := load(mapLookup(map[string]string{
		"ASTERIA_TRUSTED_TOKENS_JSON":  validTokens,
		"ASTERIA_CURSOR_HMAC_KEY_FILE": cursorFile,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if string(configuration.CursorKey) != "file-cursor-hmac-key-at-least-32-bytes" || !configuration.CursorKeyFromFile {
		t.Fatalf("cursor secret file was not loaded correctly: from_file=%v value=%q", configuration.CursorKeyFromFile, configuration.CursorKey)
	}
	_, err = load(mapLookup(map[string]string{
		"ASTERIA_TRUSTED_TOKENS_JSON":  validTokens,
		"ASTERIA_CURSOR_HMAC_KEY":      "inline-cursor-hmac-key-at-least-32-bytes",
		"ASTERIA_CURSOR_HMAC_KEY_FILE": cursorFile,
	}))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("ambiguous secret sources error=%v", err)
	}
}

func TestProductionRejectsWeakOrInlineSecretConfiguration(t *testing.T) {
	cursorFile := writeConfigFile(t, "cursor-key", "test-cursor-hmac-key-at-least-32-bytes")
	databaseFile := writeConfigFile(t, "database-url", "postgres://asteria:password@db.example.test/asteria?sslmode=verify-full")
	base := map[string]string{
		"ASTERIA_ENV":                  "production",
		"ASTERIA_AUTH_MODE":            "oidc",
		"ASTERIA_OIDC_ISSUER":          "https://issuer.example.test",
		"ASTERIA_OIDC_CLIENT_ID":       "asteria-api",
		"ASTERIA_OIDC_BOOTSTRAP_JSON":  `[{"issuer":"https://issuer.example.test","subject":"user-1","principal_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","tenant_id":"11111111-1111-4111-8111-111111111111","role":"owner"}]`,
		"ASTERIA_METADATA_DRIVER":      "postgres",
		"ASTERIA_DATABASE_URL_FILE":    databaseFile,
		"ASTERIA_STORAGE_DRIVER":       "s3",
		"ASTERIA_S3_ENDPOINT":          "https://s3.example.test",
		"ASTERIA_S3_REGION":            "us-east-1",
		"ASTERIA_S3_BUCKET":            "asteria",
		"ASTERIA_CURSOR_HMAC_KEY_FILE": cursorFile,
	}
	tests := []struct {
		name    string
		values  map[string]string
		message string
	}{
		{name: "inline database password", values: map[string]string{"ASTERIA_DATABASE_URL_FILE": "", "ASTERIA_DATABASE_URL": "postgres://asteria:password@db.example.test/asteria?sslmode=verify-full"}, message: "DATABASE_URL_FILE"},
		{name: "weak database TLS", values: map[string]string{"ASTERIA_DATABASE_URL_FILE": writeConfigFile(t, "weak-database-url", "postgres://asteria@db.example.test/asteria?sslmode=require")}, message: "verify-full"},
		{name: "inline cursor key", values: map[string]string{"ASTERIA_CURSOR_HMAC_KEY_FILE": "", "ASTERIA_CURSOR_HMAC_KEY": "test-cursor-hmac-key-at-least-32-bytes"}, message: "HMAC key"},
		{name: "automatic migration", values: map[string]string{"ASTERIA_AUTO_MIGRATE": "true"}, message: "must be false"},
		{name: "automatic bucket creation", values: map[string]string{"ASTERIA_S3_AUTO_CREATE_BUCKET": "true"}, message: "must be false"},
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
		{name: "unknown auth mode", values: map[string]string{"ASTERIA_AUTH_MODE": "basic"}, message: "trusted-dev or oidc"},
		{name: "invalid metrics address", values: map[string]string{"ASTERIA_METRICS_ADDRESS": "not-an-address"}, message: "METRICS_ADDRESS"},
		{name: "OIDC without bootstrap", values: map[string]string{"ASTERIA_AUTH_MODE": "oidc", "ASTERIA_OIDC_ISSUER": "https://issuer.example.test", "ASTERIA_OIDC_CLIENT_ID": "asteria-api"}, message: "bootstrap"},
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

func writeConfigFile(t *testing.T, name, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}
