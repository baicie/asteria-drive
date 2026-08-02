package config

import (
	"encoding/json"
	"fmt"
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

type Config struct {
	Environment       string
	Address           string
	AuthMode          string
	Tokens            map[string]Token
	CursorKey         []byte
	MetadataDriver    string
	DatabaseURL       string
	AutoMigrate       bool
	StorageDriver     string
	S3Endpoint        string
	S3Region          string
	S3Bucket          string
	S3AccessKey       string
	S3SecretKey       string
	S3PathStyle       bool
	S3AutoCreate      bool
	S3ChecksumHeaders bool
	MaxFileSize       int64
	PartSize          int64
	UploadTTL         time.Duration
	UploadSignTTL     time.Duration
	DownloadSignTTL   time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	get := func(key, fallback string) string {
		if value, ok := lookup(key); ok && value != "" {
			return value
		}
		return fallback
	}
	cfg := Config{
		Environment: get("ASTERIA_ENV", "development"), Address: get("ASTERIA_SERVER_ADDRESS", "127.0.0.1:8080"),
		AuthMode: get("ASTERIA_AUTH_MODE", "trusted-dev"), MetadataDriver: get("ASTERIA_METADATA_DRIVER", "memory"),
		DatabaseURL: get("ASTERIA_DATABASE_URL", ""), StorageDriver: get("ASTERIA_STORAGE_DRIVER", "memory"),
		S3Endpoint: get("ASTERIA_S3_ENDPOINT", ""), S3Region: get("ASTERIA_S3_REGION", "us-east-1"),
		S3Bucket: get("ASTERIA_S3_BUCKET", "asteria-drive"), S3AccessKey: get("ASTERIA_S3_ACCESS_KEY_ID", ""),
		S3SecretKey: get("ASTERIA_S3_SECRET_ACCESS_KEY", ""),
	}
	var err error
	if cfg.Tokens, err = parseTokens(get("ASTERIA_TRUSTED_TOKENS_JSON", "")); err != nil {
		return Config{}, err
	}
	cfg.CursorKey = []byte(get("ASTERIA_CURSOR_HMAC_KEY", ""))
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
	if cfg.MaxFileSize, err = parseInt64(get("ASTERIA_MAX_FILE_SIZE", "53687091200")); err != nil {
		return Config{}, fmt.Errorf("ASTERIA_MAX_FILE_SIZE: %w", err)
	}
	if cfg.PartSize, err = parseInt64(get("ASTERIA_PART_SIZE", "8388608")); err != nil {
		return Config{}, fmt.Errorf("ASTERIA_PART_SIZE: %w", err)
	}
	for key, target := range map[string]*time.Duration{
		"ASTERIA_UPLOAD_TTL": &cfg.UploadTTL, "ASTERIA_UPLOAD_SIGN_TTL": &cfg.UploadSignTTL,
		"ASTERIA_DOWNLOAD_SIGN_TTL": &cfg.DownloadSignTTL, "ASTERIA_READ_HEADER_TIMEOUT": &cfg.ReadHeaderTimeout,
		"ASTERIA_READ_TIMEOUT": &cfg.ReadTimeout, "ASTERIA_WRITE_TIMEOUT": &cfg.WriteTimeout, "ASTERIA_IDLE_TIMEOUT": &cfg.IdleTimeout,
	} {
		fallback := map[string]string{
			"ASTERIA_UPLOAD_TTL": "24h", "ASTERIA_UPLOAD_SIGN_TTL": "15m", "ASTERIA_DOWNLOAD_SIGN_TTL": "15m",
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
	if c.AuthMode != "trusted-dev" {
		return fmt.Errorf("MVP only supports ASTERIA_AUTH_MODE=trusted-dev")
	}
	if c.Environment == "production" {
		return fmt.Errorf("trusted-dev authentication is forbidden in production")
	}
	if len(c.Tokens) == 0 {
		return fmt.Errorf("ASTERIA_TRUSTED_TOKENS_JSON is required")
	}
	if len(c.CursorKey) < 32 {
		return fmt.Errorf("ASTERIA_CURSOR_HMAC_KEY must contain at least 32 bytes")
	}
	if c.MetadataDriver != "memory" && c.MetadataDriver != "postgres" {
		return fmt.Errorf("ASTERIA_METADATA_DRIVER must be memory or postgres")
	}
	if c.MetadataDriver == "postgres" && c.DatabaseURL == "" {
		return fmt.Errorf("ASTERIA_DATABASE_URL is required for postgres")
	}
	if c.StorageDriver != "memory" && c.StorageDriver != "s3" {
		return fmt.Errorf("ASTERIA_STORAGE_DRIVER must be memory or s3")
	}
	if c.StorageDriver == "s3" && (c.S3Endpoint == "" || c.S3Region == "" || c.S3Bucket == "") {
		return fmt.Errorf("S3 endpoint, region and bucket are required for s3")
	}
	if c.MaxFileSize <= 0 || c.PartSize < 5*1024*1024 || c.PartSize > 5*1024*1024*1024 {
		return fmt.Errorf("file and part size limits are invalid")
	}
	if c.UploadTTL <= 0 || c.UploadSignTTL <= 0 || c.DownloadSignTTL <= 0 || c.UploadSignTTL > time.Hour || c.DownloadSignTTL > time.Hour {
		return fmt.Errorf("session and signing TTL values are invalid")
	}
	if c.ReadHeaderTimeout <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 || c.IdleTimeout <= 0 {
		return fmt.Errorf("HTTP timeout values must be positive")
	}
	return nil
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
