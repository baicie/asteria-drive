package recovery

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

type fakeCatalog struct {
	items []BlobRecord
}

func (c *fakeCatalog) SchemaVersion(context.Context) (int64, error) { return 3, nil }
func (c *fakeCatalog) Close()                                       {}

func (c *fakeCatalog) ListAvailableBlobs(_ context.Context, after BlobCursor, limit int) ([]BlobRecord, error) {
	items := append([]BlobRecord(nil), c.items...)
	sort.Slice(items, func(i, j int) bool {
		return items[i].TenantID < items[j].TenantID ||
			(items[i].TenantID == items[j].TenantID && items[i].ID < items[j].ID)
	})
	result := make([]BlobRecord, 0, limit)
	for _, item := range items {
		if after.TenantID != "" && (item.TenantID < after.TenantID || item.TenantID == after.TenantID && item.ID <= after.BlobID) {
			continue
		}
		result = append(result, item)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

type fakeStorage struct {
	drive.StorageProvider
	bucket  string
	objects map[string]drive.ObjectInfo
	errors  map[string]error
}

func (s *fakeStorage) Bucket() string { return s.bucket }

func (s *fakeStorage) StatObject(_ context.Context, key string) (drive.ObjectInfo, error) {
	if err := s.errors[key]; err != nil {
		return drive.ObjectInfo{}, err
	}
	return s.objects[key], nil
}

func TestVerifierReportsIntegrityFindingsWithoutObjectKeys(t *testing.T) {
	t.Parallel()
	const tenant = "11111111-1111-4111-8111-111111111111"
	checksum := drive.Checksum{Algorithm: "sha256", Value: "YWJj"}
	items := []BlobRecord{
		{ID: "00000000-0000-4000-8000-000000000001", TenantID: tenant, Bucket: "private", ObjectKey: "secret/healthy", Size: 3},
		{ID: "00000000-0000-4000-8000-000000000002", TenantID: tenant, Bucket: "private", ObjectKey: "secret/missing", Size: 3},
		{ID: "00000000-0000-4000-8000-000000000003", TenantID: tenant, Bucket: "private", ObjectKey: "secret/size", Size: 3},
		{ID: "00000000-0000-4000-8000-000000000004", TenantID: tenant, Bucket: "private", ObjectKey: "secret/checksum", Size: 3, Checksum: checksum, ChecksumStatus: drive.ChecksumVerified},
		{ID: "00000000-0000-4000-8000-000000000005", TenantID: tenant, Bucket: "other", ObjectKey: "secret/bucket", Size: 3},
	}
	storage := &fakeStorage{
		bucket: "private",
		objects: map[string]drive.ObjectInfo{
			"secret/healthy":  {Bucket: "private", Size: 3},
			"secret/size":     {Bucket: "private", Size: 4},
			"secret/checksum": {Bucket: "private", Size: 3, Checksum: drive.Checksum{Algorithm: "sha256", Value: "ZGVm"}, ChecksumStatus: drive.ChecksumVerified},
		},
		errors: map[string]error{"secret/missing": drive.E(drive.CodeNotFound, "missing")},
	}
	verifier, err := NewVerifier(Options{
		Catalog: &fakeCatalog{items: items}, Storage: storage,
		BatchSize: 2, Concurrency: 3, MaxFindings: 3,
		Now: func() time.Time { return time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := verifier.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Verified || report.Checked != 5 || report.Healthy != 1 || !report.Truncated || len(report.Findings) != 3 {
		t.Fatalf("unexpected report: %+v", report)
	}
	for _, code := range []string{"object_missing", "size_mismatch", "checksum_mismatch", "bucket_mismatch"} {
		if report.Counts[code] != 1 {
			t.Errorf("finding count %s=%d, want 1", code, report.Counts[code])
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret/") {
		t.Fatalf("verification report leaked an object key: %s", encoded)
	}
}

func TestVerifierRejectsUnsafeLimits(t *testing.T) {
	t.Parallel()
	_, err := NewVerifier(Options{
		Catalog: &fakeCatalog{}, Storage: &fakeStorage{}, Concurrency: 257,
	})
	if drive.CodeOf(err) != drive.CodeInvalidRequest {
		t.Fatalf("invalid concurrency error=%v", err)
	}
}
