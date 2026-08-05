package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Token struct {
	TenantID    string `json:"tenant_id"`
	PrincipalID string `json:"principal_id"`
	TenantName  string `json:"tenant_name"`
}

type OIDCBootstrap struct {
	Issuer      string `json:"issuer"`
	Subject     string `json:"subject"`
	PrincipalID string `json:"principal_id"`
	TenantID    string `json:"tenant_id"`
	TenantName  string `json:"tenant_name"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type Config struct {
	Environment           string
	Address               string
	MetricsAddress        string
	AuthMode              string
	Tokens                map[string]Token
	OIDCIssuer            string
	OIDCClientID          string
	OIDCBootstrap         []OIDCBootstrap
	CursorKey             []byte
	MetadataDriver        string
	DatabaseURL           string
	DatabaseURLFromFile   bool
	AutoMigrate           bool
	StorageDriver         string
	S3Endpoint            string
	S3Region              string
	S3Bucket              string
	S3AccessKey           string
	S3AccessKeyFromFile   bool
	S3SecretKey           string
	S3SecretKeyFromFile   bool
	S3PathStyle           bool
	S3AutoCreate          bool
	S3ChecksumHeaders     bool
	MaxFileSize           int64
	PartSize              int64
	UploadTTL             time.Duration
	UploadSignTTL         time.Duration
	DownloadSignTTL       time.Duration
	MaintenanceEnabled    bool
	MaintenanceInterval   time.Duration
	MaintenanceLease      time.Duration
	MaintenanceStaleAfter time.Duration
	RecycleRetention      time.Duration
	MaintenanceBatchSize  int
	ReadHeaderTimeout     time.Duration
	ReadTimeout           time.Duration
	WriteTimeout          time.Duration
	IdleTimeout           time.Duration
	CursorKeyFromFile     bool
}

func Load() (Config, error) {
	return load(os.LookupEnv)
}

func LoadDatabaseURL() (string, error) {
	environment := "development"
	if value, ok := os.LookupEnv("ASTERIA_ENV"); ok && value != "" {
		environment = value
	}
	if environment != "development" && environment != "production" {
		return "", fmt.Errorf("ASTERIA_ENV must be development or production")
	}
	value, fromFile, err := sourcedValue(os.LookupEnv, "ASTERIA_DATABASE_URL", "", 64*1024)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("ASTERIA_DATABASE_URL or ASTERIA_DATABASE_URL_FILE is required")
	}
	if err := validateDatabaseURL(value, environment == "production", fromFile); err != nil {
		return "", err
	}
	return value, nil
}

func load(lookup func(string) (string, bool)) (Config, error) {
	get := func(key, fallback string) string {
		if value, ok := lookup(key); ok && value != "" {
			return value
		}
		return fallback
	}
	databaseURL, databaseURLFromFile, err := sourcedValue(lookup, "ASTERIA_DATABASE_URL", "", 64*1024)
	if err != nil {
		return Config{}, err
	}
	s3AccessKey, s3AccessKeyFromFile, err := sourcedValue(lookup, "ASTERIA_S3_ACCESS_KEY_ID", "", 64*1024)
	if err != nil {
		return Config{}, err
	}
	s3SecretKey, s3SecretKeyFromFile, err := sourcedValue(lookup, "ASTERIA_S3_SECRET_ACCESS_KEY", "", 64*1024)
	if err != nil {
		return Config{}, err
	}
	cursorKey, cursorKeyFromFile, err := sourcedValue(lookup, "ASTERIA_CURSOR_HMAC_KEY", "", 64*1024)
	if err != nil {
		return Config{}, err
	}
	trustedTokens, _, err := sourcedValue(lookup, "ASTERIA_TRUSTED_TOKENS_JSON", "", 1<<20)
	if err != nil {
		return Config{}, err
	}
	oidcBootstrap, _, err := sourcedValue(lookup, "ASTERIA_OIDC_BOOTSTRAP_JSON", "", 1<<20)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Environment: get("ASTERIA_ENV", "development"), Address: get("ASTERIA_SERVER_ADDRESS", "127.0.0.1:8080"),
		MetricsAddress: get("ASTERIA_METRICS_ADDRESS", "127.0.0.1:9090"),
		AuthMode:       get("ASTERIA_AUTH_MODE", "trusted-dev"), MetadataDriver: get("ASTERIA_METADATA_DRIVER", "memory"),
		DatabaseURL: databaseURL, DatabaseURLFromFile: databaseURLFromFile,
		StorageDriver: get("ASTERIA_STORAGE_DRIVER", "memory"),
		S3Endpoint:    get("ASTERIA_S3_ENDPOINT", ""), S3Region: get("ASTERIA_S3_REGION", "us-east-1"),
		S3Bucket: get("ASTERIA_S3_BUCKET", "asteria-drive"), S3AccessKey: s3AccessKey,
		S3AccessKeyFromFile: s3AccessKeyFromFile, S3SecretKey: s3SecretKey,
		S3SecretKeyFromFile: s3SecretKeyFromFile, CursorKey: []byte(cursorKey),
		CursorKeyFromFile: cursorKeyFromFile,
	}
	if cfg.Tokens, err = parseTokens(trustedTokens); err != nil {
		return Config{}, err
	}
	cfg.OIDCIssuer = get("ASTERIA_OIDC_ISSUER", "")
	cfg.OIDCClientID = get("ASTERIA_OIDC_CLIENT_ID", "")
	if cfg.OIDCBootstrap, err = parseOIDCBootstrap(oidcBootstrap); err != nil {
		return Config{}, err
	}
	if cfg.AutoMigrate, err = parseBool(get("ASTERIA_AUTO_MIGRATE", "false")); err != nil {
		return Config{}, fmt.Errorf("ASTERIA_AUTO_MIGRATE: %w", err)
	}
	if cfg.S3PathStyle, err = parseBool(get("ASTERIA_S3_PATH_STYLE", "true")); err != nil {
		return Config{}, fmt.Errorf("ASTERIA_S3_PATH_STYLE: %w", err)
	}
	if cfg.S3AutoCreate, err = parseBool(get("ASTERIA_S3_AUTO_CREATE_BUCKET", "false")); err != nil {
		return Config{}, fmt.Errorf("ASTERIA_S3_AUTO_CREATE_BUCKET: %w", err)
	}
	if cfg.S3ChecksumHeaders, err = parseBool(get("ASTERIA_S3_CHECKSUM_HEADERS", "false")); err != nil {
		return Config{}, fmt.Errorf("ASTERIA_S3_CHECKSUM_HEADERS: %w", err)
	}
	if cfg.MaintenanceEnabled, err = parseBool(get("ASTERIA_MAINTENANCE_ENABLED", "true")); err != nil {
		return Config{}, fmt.Errorf("ASTERIA_MAINTENANCE_ENABLED: %w", err)
	}
	if cfg.MaintenanceBatchSize, err = strconv.Atoi(get("ASTERIA_MAINTENANCE_BATCH_SIZE", "50")); err != nil {
		return Config{}, fmt.Errorf("ASTERIA_MAINTENANCE_BATCH_SIZE: %w", err)
	}
	if cfg.MaxFileSize, err = parseInt64(get("ASTERIA_MAX_FILE_SIZE", "53687091200")); err != nil {
		return Config{}, fmt.Errorf("ASTERIA_MAX_FILE_SIZE: %w", err)
	}
	if cfg.PartSize, err = parseInt64(get("ASTERIA_PART_SIZE", "8388608")); err != nil {
		return Config{}, fmt.Errorf("ASTERIA_PART_SIZE: %w", err)
	}
	for key, target := range map[string]*time.Duration{
		"ASTERIA_UPLOAD_TTL": &cfg.UploadTTL, "ASTERIA_UPLOAD_SIGN_TTL": &cfg.UploadSignTTL,
		"ASTERIA_DOWNLOAD_SIGN_TTL": &cfg.DownloadSignTTL, "ASTERIA_READ_HEADER_TIMEOUT": &cfg.ReadHeaderTimeout,
		"ASTERIA_MAINTENANCE_INTERVAL": &cfg.MaintenanceInterval, "ASTERIA_MAINTENANCE_LEASE": &cfg.MaintenanceLease,
		"ASTERIA_MAINTENANCE_STALE_AFTER": &cfg.MaintenanceStaleAfter, "ASTERIA_RECYCLE_RETENTION": &cfg.RecycleRetention,
		"ASTERIA_READ_TIMEOUT": &cfg.ReadTimeout, "ASTERIA_WRITE_TIMEOUT": &cfg.WriteTimeout, "ASTERIA_IDLE_TIMEOUT": &cfg.IdleTimeout,
	} {
		fallback := map[string]string{
			"ASTERIA_UPLOAD_TTL": "24h", "ASTERIA_UPLOAD_SIGN_TTL": "15m", "ASTERIA_DOWNLOAD_SIGN_TTL": "15m",
			"ASTERIA_MAINTENANCE_INTERVAL": "1m", "ASTERIA_MAINTENANCE_LEASE": "2m", "ASTERIA_MAINTENANCE_STALE_AFTER": "15m", "ASTERIA_RECYCLE_RETENTION": "720h",
			"ASTERIA_READ_HEADER_TIMEOUT": "5s", "ASTERIA_READ_TIMEOUT": "15s", "ASTERIA_WRITE_TIMEOUT": "30s", "ASTERIA_IDLE_TIMEOUT": "60s",
		}[key]
		*target, err = time.ParseDuration(get(key, fallback))
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", key, err)
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Environment != "development" && c.Environment != "production" {
		return fmt.Errorf("ASTERIA_ENV must be development or production")
	}
	if c.AuthMode != "trusted-dev" && c.AuthMode != "oidc" {
		return fmt.Errorf("ASTERIA_AUTH_MODE must be trusted-dev or oidc")
	}
	if c.AuthMode == "trusted-dev" {
		if c.Environment == "production" {
			return fmt.Errorf("trusted-dev authentication is forbidden in production")
		}
		if len(c.Tokens) == 0 {
			return fmt.Errorf("ASTERIA_TRUSTED_TOKENS_JSON is required")
		}
	} else {
		if c.OIDCIssuer == "" || c.OIDCClientID == "" || len(c.OIDCBootstrap) == 0 {
			return fmt.Errorf("OIDC issuer, client id and bootstrap members are required")
		}
		parsed, err := parseIssuerURL(c.OIDCIssuer)
		if err != nil {
			return err
		}
		if c.Environment == "production" && parsed.Scheme != "https" {
			return fmt.Errorf("production OIDC issuer must use HTTPS")
		}
		seenMembership := make(map[string]struct{}, len(c.OIDCBootstrap))
		seenPrincipal := make(map[string]string, len(c.OIDCBootstrap))
		for _, member := range c.OIDCBootstrap {
			memberIssuer, err := parseIssuerURL(member.Issuer)
			if err != nil {
				return fmt.Errorf("OIDC bootstrap issuer is invalid: %w", err)
			}
			if memberIssuer.String() != parsed.String() {
				return fmt.Errorf("OIDC bootstrap issuer must match ASTERIA_OIDC_ISSUER")
			}
			external := member.Issuer + "\x00" + member.Subject
			membership := member.TenantID + "\x00" + external
			if _, exists := seenMembership[membership]; exists {
				return fmt.Errorf("OIDC bootstrap contains duplicate tenant membership")
			}
			seenMembership[membership] = struct{}{}
			if previous, exists := seenPrincipal[member.PrincipalID]; exists && previous != external {
				return fmt.Errorf("OIDC bootstrap principal id maps to multiple external identities")
			}
			seenPrincipal[member.PrincipalID] = external
		}
		if c.Environment == "production" && (c.MetadataDriver != "postgres" || c.StorageDriver != "s3") {
			return fmt.Errorf("production OIDC mode requires postgres metadata and s3 storage")
		}
	}
	if len(c.CursorKey) < 32 {
		return fmt.Errorf("ASTERIA_CURSOR_HMAC_KEY must contain at least 32 bytes")
	}
	for key, address := range map[string]string{
		"ASTERIA_SERVER_ADDRESS":  c.Address,
		"ASTERIA_METRICS_ADDRESS": c.MetricsAddress,
	} {
		_, port, err := net.SplitHostPort(address)
		portNumber, parseErr := strconv.Atoi(port)
		if err != nil || parseErr != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("%s must be a valid host:port address", key)
		}
	}
	if c.MetadataDriver != "memory" && c.MetadataDriver != "postgres" {
		return fmt.Errorf("ASTERIA_METADATA_DRIVER must be memory or postgres")
	}
	if c.MetadataDriver == "postgres" && c.DatabaseURL == "" {
		return fmt.Errorf("ASTERIA_DATABASE_URL or ASTERIA_DATABASE_URL_FILE is required for postgres")
	}
	if c.MetadataDriver == "postgres" {
		if err := validateDatabaseURL(c.DatabaseURL, c.Environment == "production", c.DatabaseURLFromFile); err != nil {
			return err
		}
	}
	if c.StorageDriver != "memory" && c.StorageDriver != "s3" {
		return fmt.Errorf("ASTERIA_STORAGE_DRIVER must be memory or s3")
	}
	if c.StorageDriver == "s3" && (c.S3Endpoint == "" || c.S3Region == "" || c.S3Bucket == "") {
		return fmt.Errorf("S3 endpoint, region and bucket are required for s3")
	}
	if c.Environment == "production" && c.StorageDriver == "s3" {
		endpoint, err := url.Parse(c.S3Endpoint)
		if err != nil || endpoint.Host == "" || endpoint.Scheme != "https" || endpoint.User != nil {
			return fmt.Errorf("production S3 endpoint must use HTTPS without credentials")
		}
		if c.S3AccessKey != "" && (!c.S3AccessKeyFromFile || !c.S3SecretKeyFromFile) {
			return fmt.Errorf("production static S3 credentials must use *_FILE inputs")
		}
	}
	if c.Environment == "production" {
		if !c.CursorKeyFromFile {
			return fmt.Errorf("production cursor HMAC key must use ASTERIA_CURSOR_HMAC_KEY_FILE")
		}
		if c.AutoMigrate {
			return fmt.Errorf("ASTERIA_AUTO_MIGRATE must be false in production")
		}
		if c.S3AutoCreate {
			return fmt.Errorf("ASTERIA_S3_AUTO_CREATE_BUCKET must be false in production")
		}
	}
	if c.MaxFileSize <= 0 || c.PartSize < 5*1024*1024 || c.PartSize > 5*1024*1024*1024 {
		return fmt.Errorf("file and part size limits are invalid")
	}
	if c.UploadTTL <= 0 || c.UploadSignTTL <= 0 || c.DownloadSignTTL <= 0 || c.UploadSignTTL > time.Hour || c.DownloadSignTTL > time.Hour {
		return fmt.Errorf("session and signing TTL values are invalid")
	}
	if c.MaintenanceInterval <= 0 || c.MaintenanceLease <= 0 || c.MaintenanceStaleAfter <= 0 || c.RecycleRetention <= 0 || c.MaintenanceBatchSize < 1 || c.MaintenanceBatchSize > 1000 {
		return fmt.Errorf("maintenance configuration is invalid")
	}
	if c.ReadHeaderTimeout <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 || c.IdleTimeout <= 0 {
		return fmt.Errorf("HTTP timeout values must be positive")
	}
	return nil
}

func parseIssuerURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("ASTERIA_OIDC_ISSUER must be an HTTP(S) URL without credentials, query, or fragment")
	}
	return parsed, nil
}

func validateDatabaseURL(value string, production, fromFile bool) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return fmt.Errorf("ASTERIA_DATABASE_URL must be a PostgreSQL URL")
	}
	if !production {
		return nil
	}
	if _, hasPassword := parsed.User.Password(); hasPassword && !fromFile {
		return fmt.Errorf("production database credentials must use ASTERIA_DATABASE_URL_FILE")
	}
	if parsed.Query().Get("sslmode") != "verify-full" {
		return fmt.Errorf("production PostgreSQL requires sslmode=verify-full")
	}
	return nil
}

func sourcedValue(lookup func(string) (string, bool), key, fallback string, maxBytes int) (string, bool, error) {
	inline, inlineSet := lookup(key)
	path, fileSet := lookup(key + "_FILE")
	inlineSet = inlineSet && inline != ""
	fileSet = fileSet && path != ""
	if inlineSet && fileSet {
		return "", false, fmt.Errorf("%s and %s_FILE are mutually exclusive", key, key)
	}
	if !fileSet {
		if inlineSet {
			return inline, false, nil
		}
		return fallback, false, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read %s_FILE: %w", key, err)
	}
	if len(contents) == 0 || len(contents) > maxBytes {
		return "", false, fmt.Errorf("%s_FILE must contain between 1 and %d bytes", key, maxBytes)
	}
	value := strings.TrimSuffix(string(contents), "\n")
	value = strings.TrimSuffix(value, "\r")
	if value == "" || strings.ContainsRune(value, '\x00') {
		return "", false, fmt.Errorf("%s_FILE contains an invalid value", key)
	}
	return value, true, nil
}

func parseTokens(value string) (map[string]Token, error) {
	if value == "" {
		return nil, nil
	}
	var tokens map[string]Token
	if err := json.Unmarshal([]byte(value), &tokens); err != nil {
		return nil, fmt.Errorf("ASTERIA_TRUSTED_TOKENS_JSON must be a JSON object: %w", err)
	}
	for secret, token := range tokens {
		if len(secret) < 32 || !validUUID(token.TenantID) || !validUUID(token.PrincipalID) {
			return nil, fmt.Errorf("trusted token entries require a 32-byte token and UUID tenant/principal ids")
		}
		if token.TenantName == "" {
			token.TenantName = "Asteria tenant"
			tokens[secret] = token
		}
	}
	return tokens, nil
}

func parseOIDCBootstrap(value string) ([]OIDCBootstrap, error) {
	if value == "" {
		return nil, nil
	}
	var members []OIDCBootstrap
	if err := json.Unmarshal([]byte(value), &members); err != nil {
		return nil, fmt.Errorf("ASTERIA_OIDC_BOOTSTRAP_JSON must be a JSON array: %w", err)
	}
	for index, member := range members {
		if member.Issuer == "" || member.Subject == "" || !validUUID(member.PrincipalID) || !validUUID(member.TenantID) || !validAccessRole(member.Role) {
			return nil, fmt.Errorf("OIDC bootstrap members require issuer, subject, UUID ids and a valid role")
		}
		if member.TenantName == "" {
			members[index].TenantName = "Asteria tenant"
		}
		if member.DisplayName == "" {
			members[index].DisplayName = member.Subject
		}
	}
	return members, nil
}

func validAccessRole(role string) bool {
	switch role {
	case "owner", "admin", "editor", "viewer":
		return true
	default:
		return false
	}
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, r := range strings.ToLower(value) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func parseBool(value string) (bool, error) { return strconv.ParseBool(value) }

func parseInt64(value string) (int64, error) { return strconv.ParseInt(value, 10, 64) }
