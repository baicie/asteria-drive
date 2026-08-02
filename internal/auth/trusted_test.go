package auth

import (
	"testing"

	"github.com/baicie/asteria-drive/internal/drive"
)

func TestTrustedAuthenticator(t *testing.T) {
	const token = "a-high-entropy-test-token-000000000000"
	authenticator, err := NewTrusted(map[string]Principal{
		token: {Identity: drive.Identity{TenantID: "tenant", PrincipalID: "principal"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := authenticator.Authenticate("Bearer " + token)
	if err != nil || principal.Identity.TenantID != "tenant" {
		t.Fatalf("authenticate valid token: principal=%+v err=%v", principal, err)
	}
	for _, header := range []string{"", "bearer " + token, "Bearer wrong", "Bearer " + token + " extra"} {
		if _, err := authenticator.Authenticate(header); drive.CodeOf(err) != drive.CodeUnauthenticated {
			t.Fatalf("header %q should be rejected, got %v", header, err)
		}
	}
}
