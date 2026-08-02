package s3store

import (
	"context"
	"mime"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

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
