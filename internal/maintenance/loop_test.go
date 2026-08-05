package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/baicie/asteria-drive/internal/memory"
)

const (
	maintenanceTenant    = "11111111-1111-4111-8111-111111111111"
	maintenancePrincipal = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	maintenanceRoot      = "22222222-2222-4222-8222-222222222222"
	maintenanceUpload    = "33333333-3333-4333-8333-333333333333"
	maintenanceNode      = "44444444-4444-4444-8444-444444444444"
)

func TestRunOnceExpiresAndCleansMultipartUpload(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	repository := memory.NewRepository()
	storage := memory.NewStorage("maintenance")
	seedMaintenanceTenant(t, repository, now)
	storageUploadID, err := storage.CreateMultipart(context.Background(), "uploads/expired", "text/plain", drive.Checksum{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateUpload(context.Background(), drive.CreateUploadCommand{Session: drive.UploadSession{
		ID: maintenanceUpload, TenantID: maintenanceTenant, PrincipalID: maintenancePrincipal, ParentID: maintenanceRoot,
		DisplayName: "expired", NormalizedName: "expired", ExpectedSize: 1, MimeType: "text/plain", Bucket: storage.Bucket(),
		ObjectKey: "uploads/expired", StorageUploadID: storageUploadID, Status: drive.UploadCreated, PartSize: 5 * 1024 * 1024,
		ExpiresAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
	loop := newTestLoop(t, repository, storage, func() time.Time { return now })
	loop.RunOnce(context.Background())
	upload, err := repository.Upload(context.Background(), drive.Identity{TenantID: maintenanceTenant}, maintenanceUpload)
	if err != nil || upload.Status != drive.UploadExpired {
		t.Fatalf("upload=%+v err=%v", upload, err)
	}
	if _, err := storage.SignUploadPart(context.Background(), "uploads/expired", storageUploadID, 1, drive.Checksum{}, time.Minute); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("multipart upload was not cleaned: %v", err)
	}
}

func TestRunOncePurgesExpiredIdempotencyAndRecycleRoot(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	repository := memory.NewRepository()
	storage := memory.NewStorage("maintenance")
	seedMaintenanceTenant(t, repository, now.Add(-2*time.Hour))
	node, err := repository.CreateDirectory(context.Background(), drive.CreateDirectoryCommand{Identity: drive.Identity{TenantID: maintenanceTenant}, ID: maintenanceNode, ParentID: maintenanceRoot, DisplayName: "old", NormalizedName: "old", Now: now.Add(-2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Recycle(context.Background(), drive.Identity{TenantID: maintenanceTenant}, node.ID, node.Revision, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, err = repository.ClaimIdempotency(context.Background(), drive.IdempotencyRequest{
		Identity: drive.Identity{TenantID: maintenanceTenant, PrincipalID: maintenancePrincipal}, Scope: drive.IdempotencyCreateDirectory,
		KeyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RequestDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ClaimToken: "55555555-5555-4555-8555-555555555555", Now: now.Add(-time.Hour), LockedUntil: now.Add(-30 * time.Minute), ExpiresAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	loop := newTestLoop(t, repository, storage, func() time.Time { return now })
	loop.RunOnce(context.Background())
	if entries, _, err := repository.ListRecycle(context.Background(), drive.Identity{TenantID: maintenanceTenant}, drive.CursorPosition{}, 10); err != nil || len(entries) != 0 {
		t.Fatalf("recycle root was not purged: entries=%+v err=%v", entries, err)
	}
	if count, err := repository.DeleteExpiredIdempotency(context.Background(), now, 10); err != nil || count != 0 {
		t.Fatalf("expired idempotency was not removed: count=%d err=%v", count, err)
	}
}

func seedMaintenanceTenant(t *testing.T, repository *memory.Repository, now time.Time) {
	t.Helper()
	if _, err := repository.EnsureTenant(context.Background(), drive.TenantSeed{TenantID: maintenanceTenant, DisplayName: "Maintenance", RootNodeID: maintenanceRoot, Now: now}); err != nil {
		t.Fatal(err)
	}
}

func newTestLoop(t *testing.T, repository *memory.Repository, storage *memory.Storage, now func() time.Time) *Loop {
	t.Helper()
	loop, err := New(Options{Repository: repository, Storage: storage, BatchSize: 10, RecycleRetention: time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return loop
}
