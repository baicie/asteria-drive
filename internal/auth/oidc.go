package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

const TenantHeader = "X-Tenant-ID"

type OIDCClaims struct {
	Issuer          string
	Subject         string
	Audience        []string
	AuthorizedParty string
	ExpiresAt       time.Time
	NotBefore       *time.Time
}

type OIDCTokenVerifier interface {
	Verify(context.Context, string) (OIDCClaims, error)
}

type OIDCResolver func(context.Context, string, string, string) (Principal, error)

type OIDC struct {
	issuer  string
	client  string
	verify  OIDCTokenVerifier
	resolve OIDCResolver
}

func NewOIDC(ctx context.Context, issuer, clientID string, resolve OIDCResolver) (*OIDC, error) {
	if err := validateOIDCConfig(issuer, clientID, resolve); err != nil {
		return nil, err
	}
	verifier, err := newJWKSVerifier(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &OIDC{issuer: issuer, client: clientID, verify: verifier, resolve: resolve}, nil
}

func NewOIDCWithVerifier(issuer, clientID string, verifier OIDCTokenVerifier, resolve OIDCResolver) (*OIDC, error) {
	if err := validateOIDCConfig(issuer, clientID, resolve); err != nil {
		return nil, err
	}
	if verifier == nil {
		return nil, drive.E(drive.CodeInvalidRequest, "OIDC token verifier is required")
	}
	return &OIDC{issuer: issuer, client: clientID, verify: verifier, resolve: resolve}, nil
}

func validateOIDCConfig(issuer, clientID string, resolve OIDCResolver) error {
	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return drive.E(drive.CodeInvalidRequest, "OIDC issuer must be an HTTP(S) URL")
	}
	if clientID == "" {
		return drive.E(drive.CodeInvalidRequest, "OIDC client id is required")
	}
	if resolve == nil {
		return drive.E(drive.CodeInvalidRequest, "OIDC principal resolver is required")
	}
	return nil
}

func (a *OIDC) Authenticate(ctx context.Context, header, tenantID string) (Principal, error) {
	raw, err := bearerToken(header)
	if err != nil {
		return Principal{}, err
	}
	if !drive.ValidID(tenantID) {
		return Principal{}, drive.E(drive.CodeInvalidRequest, "a valid tenant selector is required")
	}
	claims, err := a.verify.Verify(ctx, raw)
	if err != nil {
		return Principal{}, err
	}
	if claims.Issuer != a.issuer || claims.Subject == "" || !contains(claims.Audience, a.client) ||
		(claims.AuthorizedParty != "" && claims.AuthorizedParty != a.client) ||
		(len(claims.Audience) > 1 && claims.AuthorizedParty != a.client) {
		return Principal{}, drive.E(drive.CodeUnauthenticated, "a valid OIDC token is required")
	}
	if claims.ExpiresAt.IsZero() || !time.Now().UTC().Before(claims.ExpiresAt) {
		return Principal{}, drive.E(drive.CodeUnauthenticated, "OIDC token has expired")
	}
	if claims.NotBefore != nil && time.Now().UTC().Add(30*time.Second).Before(*claims.NotBefore) {
		return Principal{}, drive.E(drive.CodeUnauthenticated, "OIDC token is not active")
	}
	principal, err := a.resolve(ctx, claims.Issuer, claims.Subject, tenantID)
	if err != nil {
		return Principal{}, err
	}
	if principal.Identity.TenantID != tenantID || !drive.ValidID(principal.Identity.PrincipalID) || !drive.ValidAccessRole(principal.Role) {
		return Principal{}, drive.E(drive.CodeForbidden, "identity is not authorized for this tenant")
	}
	return principal, nil
}

func (a *OIDC) Middleware(onError func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := a.Authenticate(r.Context(), r.Header.Get("Authorization"), r.Header.Get(TenantHeader))
			if err != nil {
				onError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

func bearerToken(header string) (string, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || len(header) == len(prefix) || len(header)-len(prefix) > 1<<20 || strings.ContainsAny(header[len(prefix):], " \t\r\n") {
		return "", drive.E(drive.CodeUnauthenticated, "a valid bearer token is required")
	}
	return header[len(prefix):], nil
}

type discoveryDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

type jwksVerifier struct {
	issuer string
	client *http.Client
	jwks   string

	mu          sync.Mutex
	keys        map[string]jwk
	refreshedAt time.Time
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func newJWKSVerifier(ctx context.Context, issuer string) (*jwksVerifier, error) {
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 {
			return nil
		}
		origin := via[0].URL
		if req.URL.Scheme != origin.Scheme || req.URL.Host != origin.Host {
			return errors.New("OIDC discovery redirect changed origin")
		}
		if len(via) >= 5 {
			return errors.New("OIDC discovery redirect limit exceeded")
		}
		return nil
	}}
	discoveryURL := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create OIDC discovery request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("OIDC discovery returned HTTP %d", response.StatusCode)
	}
	var document discoveryDocument
	if err := decodeLimited(response.Body, &document); err != nil {
		return nil, fmt.Errorf("decode OIDC discovery: %w", err)
	}
	if document.Issuer != issuer || document.JWKSURI == "" {
		return nil, fmt.Errorf("OIDC discovery does not match configured issuer")
	}
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("configured OIDC issuer is invalid: %w", err)
	}
	jwksURL, err := url.Parse(document.JWKSURI)
	if err != nil || jwksURL.Host == "" || (jwksURL.Scheme != "https" && jwksURL.Scheme != "http") || jwksURL.User != nil || jwksURL.Fragment != "" || issuerURL.Scheme == "https" && jwksURL.Scheme != "https" {
		return nil, fmt.Errorf("OIDC discovery returned an invalid JWKS URL")
	}
	verifier := &jwksVerifier{issuer: issuer, client: client, jwks: document.JWKSURI}
	if _, err := verifier.loadKeys(ctx); err != nil {
		return nil, fmt.Errorf("load OIDC signing keys: %w", err)
	}
	return verifier, nil
}

func (v *jwksVerifier) Verify(ctx context.Context, raw string) (OIDCClaims, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token is malformed")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token is malformed")
	}
	headerBytes, err := decodeURL(parts[0])
	if err != nil {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token header is malformed")
	}
	payloadBytes, err := decodeURL(parts[1])
	if err != nil {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token payload is malformed")
	}
	signature, err := decodeURL(parts[2])
	if err != nil {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token signature is malformed")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Kid == "" {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token header is invalid")
	}
	var payload struct {
		Issuer          string          `json:"iss"`
		Subject         string          `json:"sub"`
		Audience        json.RawMessage `json:"aud"`
		AuthorizedParty string          `json:"azp"`
		ExpiresAt       json.Number     `json:"exp"`
		NotBefore       json.RawMessage `json:"nbf"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payloadBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token claims are invalid")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token claims are invalid")
	}
	audience, err := parseAudience(payload.Audience)
	if err != nil || payload.Issuer != v.issuer || payload.Subject == "" || payload.ExpiresAt == "" {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token claims are invalid")
	}
	expiresAt, err := parseUnixClaim(payload.ExpiresAt)
	if err != nil {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token expiry is invalid")
	}
	var notBefore *time.Time
	if len(payload.NotBefore) > 0 {
		if bytes.Equal(bytes.TrimSpace(payload.NotBefore), []byte("null")) {
			return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token not-before is invalid")
		}
		value, err := parseUnixRawClaim(payload.NotBefore)
		if err != nil {
			return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token not-before is invalid")
		}
		notBefore = &value
	}
	key, err := v.key(ctx, header.Kid)
	if err != nil {
		return OIDCClaims{}, err
	}
	if key.Alg != "" && key.Alg != header.Alg {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token algorithm is not allowed")
	}
	if err := verifyJWTSignature(key, header.Alg, []byte(parts[0]+"."+parts[1]), signature); err != nil {
		return OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "OIDC token signature is invalid")
	}
	return OIDCClaims{Issuer: payload.Issuer, Subject: payload.Subject, Audience: audience,
		AuthorizedParty: payload.AuthorizedParty, ExpiresAt: expiresAt, NotBefore: notBefore}, nil
}

func (v *jwksVerifier) key(ctx context.Context, kid string) (jwk, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if key, ok := v.keys[kid]; ok && time.Since(v.refreshedAt) < 10*time.Minute {
		return key, nil
	}
	keys, err := v.loadKeysLocked(ctx)
	if err != nil {
		return jwk{}, err
	}
	key, ok := keys[kid]
	if !ok {
		return jwk{}, drive.E(drive.CodeUnauthenticated, "OIDC token key is unknown")
	}
	return key, nil
}

func (v *jwksVerifier) loadKeys(ctx context.Context) (map[string]jwk, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.loadKeysLocked(ctx)
}

func (v *jwksVerifier) loadKeysLocked(ctx context.Context) (map[string]jwk, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwks, nil)
	if err != nil {
		return nil, drive.Retryable(drive.CodeDependencyUnavailable, "OIDC key request could not be created", err)
	}
	response, err := v.client.Do(request)
	if err != nil {
		return nil, drive.Retryable(drive.CodeDependencyUnavailable, "OIDC signing keys are unavailable", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, drive.Retryable(drive.CodeDependencyUnavailable, "OIDC signing keys returned an unexpected status", fmt.Errorf("HTTP %d", response.StatusCode))
	}
	var document struct {
		Keys []jwk `json:"keys"`
	}
	if err := decodeLimited(response.Body, &document); err != nil {
		return nil, drive.Retryable(drive.CodeDependencyUnavailable, "OIDC signing keys are invalid", err)
	}
	keys := make(map[string]jwk, len(document.Keys))
	for _, key := range document.Keys {
		if key.Kid == "" || key.Use != "" && key.Use != "sig" {
			continue
		}
		keys[key.Kid] = key
	}
	if len(keys) == 0 {
		return nil, drive.Retryable(drive.CodeDependencyUnavailable, "OIDC provider returned no signing keys", nil)
	}
	v.keys = keys
	v.refreshedAt = time.Now().UTC()
	return keys, nil
}

func verifyJWTSignature(key jwk, algorithm string, signed, signature []byte) error {
	var hashFunc func() hash.Hash
	switch algorithm {
	case "RS256", "ES256":
		hashFunc = sha256.New
	case "RS384", "ES384":
		hashFunc = sha512.New384
	case "RS512", "ES512":
		hashFunc = sha512.New
	default:
		return errors.New("unsupported JWT algorithm")
	}
	if strings.HasPrefix(algorithm, "RS") && key.Kty != "RSA" {
		return errors.New("RSA JWT algorithm requires an RSA key")
	}
	if strings.HasPrefix(algorithm, "ES") && key.Kty != "EC" {
		return errors.New("ECDSA JWT algorithm requires an EC key")
	}
	if strings.HasPrefix(algorithm, "ES") {
		expectedCurve := map[string]string{"ES256": "P-256", "ES384": "P-384", "ES512": "P-521"}[algorithm]
		if key.Crv != expectedCurve {
			return errors.New("ECDSA JWT algorithm and curve do not match")
		}
	}
	digest := hashFunc()
	_, _ = digest.Write(signed)
	sum := digest.Sum(nil)
	if strings.HasPrefix(algorithm, "RS") {
		publicKey, err := rsaKey(key)
		if err != nil {
			return err
		}
		return rsa.VerifyPKCS1v15(publicKey, hashFor(algorithm), sum, signature)
	}
	publicKey, err := ecdsaKey(key)
	if err != nil {
		return err
	}
	if len(signature) != 2*((publicKey.Curve.Params().BitSize+7)/8) {
		return errors.New("invalid ECDSA JWT signature size")
	}
	half := len(signature) / 2
	return boolError(ecdsa.Verify(publicKey, sum, new(big.Int).SetBytes(signature[:half]), new(big.Int).SetBytes(signature[half:])))
}

func rsaKey(key jwk) (*rsa.PublicKey, error) {
	modulus, err := decodeURL(key.N)
	if err != nil || key.Kty != "RSA" || len(modulus) < 256 {
		return nil, errors.New("invalid RSA JWK")
	}
	modulusValue := new(big.Int).SetBytes(modulus)
	if modulusValue.Sign() <= 0 || modulusValue.Bit(0) == 0 {
		return nil, errors.New("invalid RSA modulus")
	}
	exponentBytes, err := decodeURL(key.E)
	if err != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
		return nil, errors.New("invalid RSA exponent")
	}
	exponent := 0
	for _, value := range exponentBytes {
		exponent = exponent<<8 | int(value)
	}
	if exponent < 3 || exponent%2 == 0 {
		return nil, errors.New("invalid RSA exponent")
	}
	return &rsa.PublicKey{N: modulusValue, E: exponent}, nil
}

func ecdsaKey(key jwk) (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	switch key.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, errors.New("invalid ECDSA curve")
	}
	x, err := decodeURL(key.X)
	if err != nil {
		return nil, err
	}
	y, err := decodeURL(key.Y)
	if err != nil || key.Kty != "EC" {
		return nil, errors.New("invalid ECDSA JWK")
	}
	coordinateSize := (curve.Params().BitSize + 7) / 8
	if len(x) != coordinateSize || len(y) != coordinateSize {
		return nil, errors.New("invalid ECDSA point size")
	}
	publicKey := &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	if !curve.IsOnCurve(publicKey.X, publicKey.Y) {
		return nil, errors.New("ECDSA point is not on curve")
	}
	return publicKey, nil
}

func hashFor(algorithm string) crypto.Hash {
	switch algorithm {
	case "RS256":
		return crypto.SHA256
	case "RS384":
		return crypto.SHA384
	default:
		return crypto.SHA512
	}
}

func boolError(valid bool) error {
	if !valid {
		return errors.New("invalid ECDSA JWT signature")
	}
	return nil
}

func parseAudience(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("missing audience")
	}
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one}, nil
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil || len(many) == 0 {
		return nil, errors.New("invalid audience")
	}
	return many, nil
}

func parseUnixClaim(value json.Number) (time.Time, error) {
	seconds, err := strconv.ParseFloat(string(value), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < -62135596800 || seconds > 253402300799 {
		return time.Time{}, errors.New("invalid NumericDate")
	}
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*1e9)).UTC(), nil
}

func parseUnixRawClaim(raw json.RawMessage) (time.Time, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value json.Number
	if err := decoder.Decode(&value); err != nil {
		return time.Time{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return time.Time{}, err
	}
	return parseUnixClaim(value)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func decodeURL(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}

func decodeLimited(reader io.Reader, destination any) error {
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if len(data) > limit {
		return errors.New("JSON document exceeds the allowed size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("JSON document contains trailing values")
		}
		return err
	}
	return nil
}
