package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/baicie/asteria-drive/internal/drive"
)

type Principal struct {
	Identity          drive.Identity
	TenantDisplayName string
	Role              drive.AccessRole
}

type Authenticator interface {
	Middleware(func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler
}

type ExternalIdentity struct {
	Issuer  string
	Subject string
}

type ExternalAuthenticator interface {
	ExternalMiddleware(func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler
}

type trustedEntry struct {
	hash      [sha256.Size]byte
	principal Principal
}

type Trusted struct {
	entries []trustedEntry
}

func NewTrusted(tokens map[string]Principal) (*Trusted, error) {
	if len(tokens) == 0 {
		return nil, drive.E(drive.CodeInvalidRequest, "at least one trusted token is required")
	}
	authenticator := &Trusted{entries: make([]trustedEntry, 0, len(tokens))}
	for token, principal := range tokens {
		if len(token) < 32 || principal.Identity.TenantID == "" || principal.Identity.PrincipalID == "" {
			return nil, drive.E(drive.CodeInvalidRequest, "trusted tokens must be high entropy and map to tenant and principal ids")
		}
		if principal.Role == "" {
			principal.Role = drive.RoleOwner
		}
		if !drive.ValidAccessRole(principal.Role) {
			return nil, drive.E(drive.CodeInvalidRequest, "trusted token role is invalid")
		}
		authenticator.entries = append(authenticator.entries, trustedEntry{hash: sha256.Sum256([]byte(token)), principal: principal})
	}
	return authenticator, nil
}

func (p Principal) HasPermission(permission drive.Permission) bool {
	_, ok := drive.PermissionsForRole(p.Role)[permission]
	return ok
}

func (a *Trusted) Authenticate(header string) (Principal, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || len(header) == len(prefix) || strings.ContainsAny(header[len(prefix):], " \t\r\n") {
		return Principal{}, drive.E(drive.CodeUnauthenticated, "a valid bearer token is required")
	}
	candidate := sha256.Sum256([]byte(header[len(prefix):]))
	matched := -1
	for i := range a.entries {
		if subtle.ConstantTimeCompare(candidate[:], a.entries[i].hash[:]) == 1 {
			matched = i
		}
	}
	if matched < 0 {
		return Principal{}, drive.E(drive.CodeUnauthenticated, "a valid bearer token is required")
	}
	return a.entries[matched].principal, nil
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, principal)
}

func FromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(contextKey{}).(Principal)
	return principal, ok
}

func (a *Trusted) Middleware(onError func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := a.Authenticate(r.Header.Get("Authorization"))
			if err != nil {
				onError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

func (a *Trusted) ExternalMiddleware(onError func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := a.Authenticate(r.Header.Get("Authorization"))
			if err != nil {
				onError(w, r, err)
				return
			}
			identity := ExternalIdentity{Issuer: "trusted-dev", Subject: principal.Identity.PrincipalID}
			next.ServeHTTP(w, r.WithContext(WithExternalIdentity(r.Context(), identity)))
		})
	}
}

type externalContextKey struct{}

func WithExternalIdentity(ctx context.Context, identity ExternalIdentity) context.Context {
	return context.WithValue(ctx, externalContextKey{}, identity)
}

func ExternalFromContext(ctx context.Context) (ExternalIdentity, bool) {
	identity, ok := ctx.Value(externalContextKey{}).(ExternalIdentity)
	return identity, ok
}

var _ ExternalAuthenticator = (*Trusted)(nil)
