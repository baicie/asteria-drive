package cicheck

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeLogRemovesCredentialsAndSignedURLs(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		`database=postgres://asteria:local-password@127.0.0.1:15432/asteria?sslmode=disable`,
		`Authorization: Bearer live-token-value`,
		`download=http://127.0.0.1:18333/bucket/key?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=local-access&X-Amz-Signature=abcdef`,
		`access=local-access secret=local-secret`,
	}, "\n")
	var output bytes.Buffer
	if err := SanitizeLog(strings.NewReader(input), &output, []string{"local-access", "local-secret", "local-password"}); err != nil {
		t.Fatalf("sanitize log: %v", err)
	}
	sanitized := output.String()
	for _, forbidden := range []string{"postgres://", "local-password", "live-token-value", "X-Amz-", "local-access", "local-secret"} {
		if strings.Contains(sanitized, forbidden) {
			t.Errorf("sanitized log still contains %q: %s", forbidden, sanitized)
		}
	}
	for _, marker := range []string{"[REDACTED_DATABASE_URL]", "Authorization: [REDACTED]", "[REDACTED_SIGNED_URL]"} {
		if !strings.Contains(sanitized, marker) {
			t.Errorf("sanitized log is missing marker %q: %s", marker, sanitized)
		}
	}
}

func TestSanitizeLogPreservesGoTestJSON(t *testing.T) {
	t.Parallel()

	events := []map[string]string{
		{"Action": "output", "Package": "example/server", "Output": "Authorization: Bearer live-token-value\n"},
		{"Action": "output", "Package": "example/server", "Output": "url=postgres://asteria:local-password@127.0.0.1/db?sslmode=disable\n"},
	}
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			t.Fatalf("encode test event: %v", err)
		}
	}

	var output bytes.Buffer
	if err := SanitizeLog(strings.NewReader(input.String()), &output, []string{"local-password"}); err != nil {
		t.Fatalf("sanitize JSON log: %v", err)
	}
	decoder := json.NewDecoder(&output)
	for index := range events {
		var event map[string]string
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("decode sanitized event %d: %v\n%s", index, err, output.String())
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		t.Fatal("sanitized JSON has unexpected trailing event")
	}
	for _, forbidden := range []string{"live-token-value", "local-password", "postgres://"} {
		if strings.Contains(output.String(), forbidden) {
			t.Errorf("sanitized JSON still contains %q: %s", forbidden, output.String())
		}
	}
}
