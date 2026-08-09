package s3store

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/baicie/asteria-drive/internal/drive"
)

func TestMapErrorClassifiesS3MissingResources(t *testing.T) {
	tests := []struct {
		name      string
		code      string
		wantCode  drive.ErrorCode
		retryable bool
	}{
		{name: "missing bucket", code: "NoSuchBucket", wantCode: drive.CodeDependencyUnavailable, retryable: true},
		{name: "missing key", code: "NoSuchKey", wantCode: drive.CodeNotFound},
		{name: "missing upload", code: "NoSuchUpload", wantCode: drive.CodeNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiErr := &smithy.GenericAPIError{Code: tt.code, Message: "simulated S3 response"}
			err := mapError(apiErr, "object storage operation failed")
			if got := drive.CodeOf(err); got != tt.wantCode {
				t.Fatalf("error code = %q, want %q", got, tt.wantCode)
			}
			var domainErr *drive.Error
			if !errors.As(err, &domainErr) {
				t.Fatalf("mapped error type = %T, want *drive.Error", err)
			}
			if domainErr.Retryable != tt.retryable {
				t.Fatalf("retryable = %v, want %v", domainErr.Retryable, tt.retryable)
			}
		})
	}
}

func TestBucketNotFoundClassificationIsScopedToBucketDiscovery(t *testing.T) {
	missingBucket := &smithy.GenericAPIError{Code: "NoSuchBucket", Message: "simulated S3 response"}
	if isNotFound(missingBucket) {
		t.Fatal("NoSuchBucket must not be treated as a missing object or upload")
	}
	if !isBucketNotFound(missingBucket) {
		t.Fatal("NoSuchBucket must trigger the auto-create bucket flow")
	}

	for _, code := range []string{"NotFound", "404"} {
		t.Run(code, func(t *testing.T) {
			err := &smithy.GenericAPIError{Code: code, Message: "simulated HeadBucket response"}
			if !isBucketNotFound(err) {
				t.Fatalf("%s must trigger the auto-create bucket flow", code)
			}
		})
	}
	if isBucketNotFound(&smithy.GenericAPIError{Code: "AccessDenied", Message: "simulated S3 response"}) {
		t.Fatal("AccessDenied must not trigger the auto-create bucket flow")
	}
}

func TestCustomEndpointPresignedDownloadOmitsOptionalChecksumMode(t *testing.T) {
	provider, err := New(context.Background(), Options{
		Endpoint:     "http://127.0.0.1:8333",
		Region:       "us-east-1",
		Bucket:       "asteria-test",
		AccessKey:    "test-access-key",
		SecretKey:    "test-secret-key",
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	const filename = "contract sample.bin"
	signed, err := provider.SignDownload(context.Background(), "tenant/object", filename, time.Minute)
	if err != nil {
		t.Fatalf("sign download: %v", err)
	}
	parsed, err := url.Parse(signed.URL)
	if err != nil {
		t.Fatalf("parse signed URL: %v", err)
	}
	if parsed.Scheme != "http" || parsed.Host != "127.0.0.1:8333" {
		t.Fatalf("default presign endpoint = %s://%s, want http://127.0.0.1:8333", parsed.Scheme, parsed.Host)
	}
	query := parsed.Query()
	if checksumMode := query.Get("X-Amz-Checksum-Mode"); checksumMode != "" {
		t.Fatalf("optional checksum mode = %q, want it omitted for S3-compatible endpoints", checksumMode)
	}
	wantDisposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	if disposition := query.Get("response-content-disposition"); disposition != wantDisposition {
		t.Fatalf("response content disposition = %q, want %q", disposition, wantDisposition)
	}
}

func TestCustomEndpointChecksumHeadersAreCapabilityGated(t *testing.T) {
	checksum := drive.Checksum{Algorithm: "sha256", Value: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	for _, enabled := range []bool{false, true} {
		provider, err := New(context.Background(), Options{
			Endpoint: "http://127.0.0.1:8333", Region: "us-east-1", Bucket: "asteria-test",
			AccessKey: "test-access-key", SecretKey: "test-secret-key", UsePathStyle: true,
			EnableChecksumHeaders: enabled,
		})
		if err != nil {
			t.Fatalf("create provider: %v", err)
		}
		signed, err := provider.SignUploadPart(context.Background(), "tenant/object", "upload-id", 1, checksum, time.Minute)
		if err != nil {
			t.Fatalf("sign upload part: %v", err)
		}
		hasChecksumHeader := false
		for name := range signed.RequiredHeaders {
			if strings.EqualFold(name, "x-amz-checksum-sha256") {
				hasChecksumHeader = true
			}
		}
		parsed, err := url.Parse(signed.URL)
		if err != nil {
			t.Fatalf("parse upload URL: %v", err)
		}
		hasChecksum := hasChecksumHeader || parsed.Query().Get("X-Amz-Checksum-Sha256") != ""
		if hasChecksum != enabled {
			t.Fatalf("checksum binding present=%v with capability enabled=%v; headers=%v query=%v", hasChecksum, enabled, signed.RequiredHeaders, parsed.Query())
		}
	}
}

func TestPublicEndpointIsUsedOnlyForPresigning(t *testing.T) {
	var controlRequests atomic.Int32
	controlEndpoint := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		controlRequests.Add(1)
		if request.Method != http.MethodHead || request.URL.Path != "/asteria-test" {
			t.Errorf("control request = %s %s, want HEAD /asteria-test", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		response.WriteHeader(http.StatusOK)
	}))
	defer controlEndpoint.Close()

	provider, err := New(context.Background(), Options{
		Endpoint: controlEndpoint.URL, PublicEndpoint: "https://objects.example.test",
		Region: "us-east-1", Bucket: "asteria-test", AccessKey: "test-access-key",
		SecretKey: "test-secret-key", UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if err := provider.Ready(context.Background()); err != nil {
		t.Fatalf("check control endpoint: %v", err)
	}
	if got := controlRequests.Load(); got != 1 {
		t.Fatalf("control requests = %d, want 1", got)
	}

	download, err := provider.SignDownload(context.Background(), "tenant/object", "sample.bin", time.Minute)
	if err != nil {
		t.Fatalf("sign download: %v", err)
	}
	upload, err := provider.SignUploadPart(context.Background(), "tenant/object", "upload-id", 1, drive.Checksum{}, time.Minute)
	if err != nil {
		t.Fatalf("sign upload part: %v", err)
	}
	for name, rawURL := range map[string]string{"download": download.URL, "upload": upload.URL} {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse %s URL: %v", name, err)
		}
		if parsed.Scheme != "https" || parsed.Host != "objects.example.test" {
			t.Fatalf("%s endpoint = %s://%s, want https://objects.example.test", name, parsed.Scheme, parsed.Host)
		}
		if parsed.Query().Get("X-Amz-Signature") == "" {
			t.Fatalf("%s URL has no signature", name)
		}
	}
	if got := controlRequests.Load(); got != 1 {
		t.Fatalf("presigning sent a network request: control requests = %d", got)
	}
}
