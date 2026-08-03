package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

const (
	oidcTestTenant    = "11111111-1111-4111-8111-111111111111"
	oidcTestPrincipal = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

type staticOIDCVerifier struct {
	claims OIDCClaims
	err    error
}

func (v staticOIDCVerifier) Verify(_ context.Context, _ string) (OIDCClaims, error) {
	return v.claims, v.err
}

func TestOIDCAuthenticateRequiresTenantMembershipAndAuthorizedParty(t *testing.T) {
	resolver := func(_ context.Context, issuer, subject, tenantID string) (Principal, error) {
		if issuer != "https://issuer.example.test" || subject != "subject-1" || tenantID != oidcTestTenant {
			t.Fatalf("unexpected resolver input: issuer=%q subject=%q tenant=%q", issuer, subject, tenantID)
		}
		return Principal{
			Identity: drive.Identity{TenantID: tenantID, PrincipalID: oidcTestPrincipal},
			Role:     drive.RoleViewer,
		}, nil
	}
	now := time.Now().UTC()
	claims := OIDCClaims{
		Issuer: "https://issuer.example.test", Subject: "subject-1", Audience: []string{"asteria-api"},
		ExpiresAt: now.Add(time.Minute),
	}
	valid, err := NewOIDCWithVerifier(claims.Issuer, "asteria-api", staticOIDCVerifier{claims: claims}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := valid.Authenticate(context.Background(), "Bearer token", oidcTestTenant)
	if err != nil || principal.Role != drive.RoleViewer {
		t.Fatalf("valid OIDC authentication failed: principal=%+v err=%v", principal, err)
	}
	if _, err := valid.Authenticate(context.Background(), "Bearer token", ""); drive.CodeOf(err) != drive.CodeInvalidRequest {
		t.Fatalf("missing tenant selector should be invalid_request, got %v", err)
	}
	claims.Audience = []string{"asteria-api", "another-client"}
	noAuthorizedParty, err := NewOIDCWithVerifier(claims.Issuer, "asteria-api", staticOIDCVerifier{claims: claims}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noAuthorizedParty.Authenticate(context.Background(), "Bearer token", oidcTestTenant); drive.CodeOf(err) != drive.CodeUnauthenticated {
		t.Fatalf("multi-audience token without azp should be rejected, got %v", err)
	}
	claims.AuthorizedParty = "another-client"
	wrongAuthorizedParty, err := NewOIDCWithVerifier(claims.Issuer, "asteria-api", staticOIDCVerifier{claims: claims}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongAuthorizedParty.Authenticate(context.Background(), "Bearer token", oidcTestTenant); drive.CodeOf(err) != drive.CodeUnauthenticated {
		t.Fatalf("token with wrong azp should be rejected, got %v", err)
	}
}

func TestJWKSVerifierValidatesSignatureClaimsAndKeyConstraints(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const kid = "key-1"
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_, _ = fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q}`, issuer, issuer+"/jwks")
		case "/jwks":
			modulus := base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes())
			_, _ = fmt.Fprintf(w, `{"keys":[{"kty":"RSA","kid":%q,"alg":"RS256","use":"sig","n":%q,"e":"AQAB"}]}`, kid, modulus)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	verifier, err := newJWKSVerifier(context.Background(), issuer)
	if err != nil {
		t.Fatal(err)
	}
	validPayload := fmt.Sprintf(`{"iss":%q,"sub":"subject-1","aud":"asteria-api","exp":%d}`, issuer, time.Now().Add(time.Minute).Unix())
	validToken := signRS256(t, privateKey, kid, validPayload)
	claims, err := verifier.Verify(context.Background(), validToken)
	if err != nil || claims.Subject != "subject-1" || !contains(claims.Audience, "asteria-api") {
		t.Fatalf("valid JWT rejected: claims=%+v err=%v", claims, err)
	}
	parts := strings.Split(validToken, ".")
	tamperedSignature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	tamperedSignature[0] ^= 1
	parts[2] = base64.RawURLEncoding.EncodeToString(tamperedSignature)
	if _, err := verifier.Verify(context.Background(), strings.Join(parts, ".")); drive.CodeOf(err) != drive.CodeUnauthenticated {
		t.Fatalf("tampered JWT should be unauthenticated, got %v", err)
	}
	trailingPayload := validPayload + ` {"unexpected":true}`
	if _, err := verifier.Verify(context.Background(), signRS256(t, privateKey, kid, trailingPayload)); drive.CodeOf(err) != drive.CodeUnauthenticated {
		t.Fatalf("JWT with trailing JSON should be unauthenticated, got %v", err)
	}
	unsupported := signJWT(t, privateKey, kid, "HS256", validPayload, []byte("not-a-valid-hmac"))
	if _, err := verifier.Verify(context.Background(), unsupported); drive.CodeOf(err) != drive.CodeUnauthenticated {
		t.Fatalf("unsupported JWT algorithm should be unauthenticated, got %v", err)
	}
	unknownKid := signRS256(t, privateKey, "unknown", validPayload)
	if _, err := verifier.Verify(context.Background(), unknownKid); drive.CodeOf(err) != drive.CodeUnauthenticated {
		t.Fatalf("unknown kid should be unauthenticated, got %v", err)
	}
	server.Close()
	if _, err := verifier.Verify(context.Background(), unknownKid); drive.CodeOf(err) != drive.CodeDependencyUnavailable {
		t.Fatalf("unavailable JWKS provider should be retryable, got %v", err)
	}
}

func TestOIDCRejectsExpiredAndNotYetValidTokens(t *testing.T) {
	resolver := func(_ context.Context, _, _, tenantID string) (Principal, error) {
		return Principal{Identity: drive.Identity{TenantID: tenantID, PrincipalID: oidcTestPrincipal}, Role: drive.RoleOwner}, nil
	}
	now := time.Now().UTC()
	for name, claims := range map[string]OIDCClaims{
		"expired":    {Issuer: "https://issuer.example.test", Subject: "subject", Audience: []string{"client"}, ExpiresAt: now.Add(-time.Second)},
		"not-before": {Issuer: "https://issuer.example.test", Subject: "subject", Audience: []string{"client"}, ExpiresAt: now.Add(time.Minute), NotBefore: timePtr(now.Add(time.Minute))},
	} {
		t.Run(name, func(t *testing.T) {
			oidc, err := NewOIDCWithVerifier("https://issuer.example.test", "client", staticOIDCVerifier{claims: claims}, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := oidc.Authenticate(context.Background(), "Bearer token", oidcTestTenant); drive.CodeOf(err) != drive.CodeUnauthenticated {
				t.Fatalf("invalid time claim should be unauthenticated, got %v", err)
			}
		})
	}
}

func TestVerifyECDSAJWTSignature(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signed := []byte("header.payload")
	digest := sha256.Sum256(signed)
	r, s, err := ecdsa.Sign(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	coordinateSize := (privateKey.Curve.Params().BitSize + 7) / 8
	signature := append(leftPad(r, coordinateSize), leftPad(s, coordinateSize)...)
	key := jwk{
		Kty: "EC", Crv: "P-256",
		X: base64.RawURLEncoding.EncodeToString(leftPad(privateKey.PublicKey.X, coordinateSize)),
		Y: base64.RawURLEncoding.EncodeToString(leftPad(privateKey.PublicKey.Y, coordinateSize)),
	}
	if err := verifyJWTSignature(key, "ES256", signed, signature); err != nil {
		t.Fatalf("valid ECDSA JWT signature rejected: %v", err)
	}
	key.Crv = "P-384"
	if err := verifyJWTSignature(key, "ES256", signed, signature); err == nil {
		t.Fatal("ES256 must reject a P-384 JWK")
	}
}

func signRS256(t *testing.T, key *rsa.PrivateKey, kid, payload string) string {
	t.Helper()
	return signJWT(t, key, kid, "RS256", payload, nil)
}

func signJWT(t *testing.T, key *rsa.PrivateKey, kid, algorithm, payload string, rawSignature []byte) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"alg":%q,"kid":%q}`, algorithm, kid)))
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(payload))
	signed := header + "." + encodedPayload
	if rawSignature == nil {
		digest := sha256.Sum256([]byte(signed))
		var err error
		rawSignature, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(rawSignature)
}

func timePtr(value time.Time) *time.Time { return &value }

func leftPad(value *big.Int, size int) []byte {
	encoded := make([]byte, size)
	value.FillBytes(encoded)
	return encoded
}
