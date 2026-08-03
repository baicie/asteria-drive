package s3store

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

func TestProviderSeaweedFSContract(t *testing.T) {
	endpoint := os.Getenv("ASTERIA_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set ASTERIA_TEST_S3_ENDPOINT to run the SeaweedFS contract test")
	}

	accessKey := requireIntegrationEnv(t, "ASTERIA_TEST_S3_ACCESS_KEY")
	secretKey := requireIntegrationEnv(t, "ASTERIA_TEST_S3_SECRET_KEY")
	region := envOrDefault("ASTERIA_TEST_S3_REGION", "us-east-1")
	bucket := envOrDefault("ASTERIA_TEST_S3_BUCKET", "asteria-contract")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	provider, err := New(ctx, Options{
		Endpoint:         endpoint,
		Region:           region,
		Bucket:           bucket,
		AccessKey:        accessKey,
		SecretKey:        secretKey,
		UsePathStyle:     true,
		AutoCreateBucket: true,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if err := provider.Ready(ctx); err != nil {
		t.Fatalf("provider should be ready: %v", err)
	}

	key := fmt.Sprintf("contract/%d.bin", time.Now().UTC().UnixNano())
	firstPart := bytes.Repeat([]byte("0123456789abcdef"), 5*1024*1024/16)
	secondPart := []byte("asteria-seaweedfs-final-part")
	want := append(append([]byte(nil), firstPart...), secondPart...)

	uploadID, err := provider.CreateMultipart(ctx, key, "application/octet-stream", drive.Checksum{})
	if err != nil {
		t.Fatalf("create multipart upload: %v", err)
	}
	uploadCompleted := false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if !uploadCompleted {
			if err := provider.AbortMultipart(cleanupCtx, key, uploadID); err != nil {
				t.Errorf("abort multipart upload during cleanup: %v", err)
			}
		}
		if err := provider.DeleteObject(cleanupCtx, key); err != nil {
			t.Errorf("delete contract-test object: %v", err)
		}
	})

	httpClient := &http.Client{Timeout: 30 * time.Second}
	partBodies := [][]byte{firstPart, secondPart}
	completedParts := make([]drive.CompletedPart, 0, len(partBodies))
	for index, body := range partBodies {
		partNumber := index + 1
		signed, err := provider.SignUploadPart(ctx, key, uploadID, partNumber, drive.Checksum{}, 5*time.Minute)
		if err != nil {
			t.Fatalf("sign upload part %d: %v", partNumber, err)
		}
		response := doSignedRequest(t, ctx, httpClient, signed.Method, signed.URL, signed.RequiredHeaders, body, "")
		responseBody := readAndClose(t, response)
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			t.Fatalf("upload part %d returned %s: %s", partNumber, response.Status, responseBody)
		}
		etag := response.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("upload part %d returned no ETag", partNumber)
		}
		completedParts = append(completedParts, drive.CompletedPart{
			PartNumber: partNumber,
			ETag:       etag,
			Size:       int64(len(body)),
		})
	}

	object, err := provider.CompleteMultipart(ctx, key, uploadID, completedParts)
	if err != nil {
		t.Fatalf("complete multipart upload: %v", err)
	}
	uploadCompleted = true
	assertObjectInfo(t, object, bucket, key, int64(len(want)))

	stat, err := provider.StatObject(ctx, key)
	if err != nil {
		t.Fatalf("stat completed object: %v", err)
	}
	assertObjectInfo(t, stat, bucket, key, int64(len(want)))

	download, err := provider.SignDownload(ctx, key, "contract sample.bin", 5*time.Minute)
	if err != nil {
		t.Fatalf("sign object download: %v", err)
	}
	downloadRequest, err := http.NewRequest(download.Method, download.URL, nil)
	if err != nil {
		t.Fatalf("parse signed download URL: %v", err)
	}
	if checksumMode := downloadRequest.URL.Query().Get("X-Amz-Checksum-Mode"); checksumMode != "" {
		t.Fatalf("signed SeaweedFS download unexpectedly requested checksum mode %q", checksumMode)
	}
	fullResponse := doSignedRequest(t, ctx, httpClient, download.Method, download.URL, nil, nil, "")
	fullBody := readAndClose(t, fullResponse)
	if fullResponse.StatusCode != http.StatusOK {
		t.Fatalf("full download returned %s (signed headers %q): %s", fullResponse.Status,
			fullResponse.Request.URL.Query().Get("X-Amz-SignedHeaders"), fullBody)
	}
	if disposition := fullResponse.Header.Get("Content-Disposition"); disposition != `attachment; filename="contract sample.bin"` {
		t.Fatalf("Content-Disposition = %q, want attachment filename", disposition)
	}
	if !bytes.Equal(fullBody, want) {
		t.Fatalf("full download mismatch: got %d bytes, want %d", len(fullBody), len(want))
	}

	const rangeStart, rangeEnd = 17, 61
	rangeHeader := fmt.Sprintf("bytes=%d-%d", rangeStart, rangeEnd)
	rangeResponse := doSignedRequest(t, ctx, httpClient, download.Method, download.URL, nil, nil, rangeHeader)
	rangeBody := readAndClose(t, rangeResponse)
	if rangeResponse.StatusCode != http.StatusPartialContent {
		t.Fatalf("range download returned %s: %s", rangeResponse.Status, rangeBody)
	}
	wantRange := want[rangeStart : rangeEnd+1]
	if !bytes.Equal(rangeBody, wantRange) {
		t.Fatalf("range download mismatch: got %d bytes, want %d", len(rangeBody), len(wantRange))
	}
	wantContentRange := fmt.Sprintf("bytes %d-%d/%d", rangeStart, rangeEnd, len(want))
	if got := rangeResponse.Header.Get("Content-Range"); got != wantContentRange {
		t.Fatalf("Content-Range = %q, want %q", got, wantContentRange)
	}
}

func doSignedRequest(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	method string,
	url string,
	headers map[string]string,
	body []byte,
	byteRange string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create signed %s request: %v", method, err)
	}
	for name, value := range headers {
		if http.CanonicalHeaderKey(name) == "Host" {
			request.Host = value
			continue
		}
		request.Header.Set(name, value)
	}
	if byteRange != "" {
		request.Header.Set("Range", byteRange)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("execute signed %s request: %v", method, err)
	}
	return response
}

func readAndClose(t *testing.T, response *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if closeErr := response.Body.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return body
}

func assertObjectInfo(t *testing.T, object drive.ObjectInfo, bucket, key string, size int64) {
	t.Helper()
	if object.Bucket != bucket || object.ObjectKey != key || object.Size != size {
		t.Fatalf("unexpected object info: got bucket=%q key=%q size=%d, want bucket=%q key=%q size=%d",
			object.Bucket, object.ObjectKey, object.Size, bucket, key, size)
	}
	if object.ETag == "" {
		t.Fatal("object info returned no ETag")
	}
}

func requireIntegrationEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required when ASTERIA_TEST_S3_ENDPOINT is set", name)
	}
	return value
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
