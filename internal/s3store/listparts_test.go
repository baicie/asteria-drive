package s3store

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baicie/asteria-drive/internal/drive"
)

// fakeListPartsServer answers the S3 ListParts XML protocol, including pagination, so the
// provider's cursor loop is tested without Docker or SeaweedFS.
func fakeListPartsServer(t *testing.T, pages []string, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := int(requests.Add(1)) - 1
		if r.Method != http.MethodGet || r.URL.Query().Get("uploadId") == "" {
			t.Errorf("unexpected ListParts request: %s %s", r.Method, r.URL)
		}
		if call >= len(pages) {
			http.Error(w, "unexpected extra ListParts page", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, pages[call])
	}))
	t.Cleanup(server.Close)
	return server
}

func TestListPartsFollowsS3Pagination(t *testing.T) {
	var requests atomic.Int32
	server := fakeListPartsServer(t, []string{
		`<?xml version="1.0" encoding="UTF-8"?>
<ListPartsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Bucket>test-bucket</Bucket><Key>tenant/object</Key><UploadId>upload-1</UploadId>
  <IsTruncated>true</IsTruncated><NextPartNumberMarker>2</NextPartNumberMarker>
  <Part><PartNumber>2</PartNumber><ETag>"etag-2"</ETag><Size>5</Size></Part>
  <Part><PartNumber>1</PartNumber><ETag>"etag-1"</ETag><Size>5</Size></Part>
</ListPartsResult>`,
		`<?xml version="1.0" encoding="UTF-8"?>
<ListPartsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Bucket>test-bucket</Bucket><Key>tenant/object</Key><UploadId>upload-1</UploadId>
  <IsTruncated>false</IsTruncated>
  <Part><PartNumber>3</PartNumber><ETag>"etag-3"</ETag><Size>7</Size></Part>
</ListPartsResult>`,
	}, &requests)

	provider, err := New(context.Background(), Options{
		Endpoint: server.URL, Region: "us-east-1", Bucket: "test-bucket",
		AccessKey: "test-access-key", SecretKey: "test-secret-key", UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	parts, err := provider.ListParts(context.Background(), "tenant/object", "upload-1")
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("ListParts requests = %d, want 2 (one page each)", got)
	}
	if len(parts) != 3 || parts[0].PartNumber != 1 || parts[1].PartNumber != 2 || parts[2].PartNumber != 3 {
		t.Fatalf("parts are not sorted by part number: %+v", parts)
	}
	if parts[2].ETag != `"etag-3"` || parts[2].Size != 7 {
		t.Fatalf("unexpected third part: %+v", parts[2])
	}
}

func TestListPartsMapsMissingUploadToNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>NoSuchUpload</Code><Message>no such upload</Message></Error>`)
	}))
	t.Cleanup(server.Close)

	provider, err := New(context.Background(), Options{
		Endpoint: server.URL, Region: "us-east-1", Bucket: "test-bucket",
		AccessKey: "test-access-key", SecretKey: "test-secret-key", UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	_, err = provider.ListParts(context.Background(), "tenant/object", "missing")
	if drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("missing upload error = %v, want not_found", err)
	}
	if strings.Contains(err.Error(), "test-secret-key") {
		t.Fatal("mapped error leaked S3 credentials")
	}
}
