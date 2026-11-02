package drive_test

import (
	"context"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/baicie/asteria-drive/internal/memory"
)

const (
	partsTenantID    = "11111111-1111-4111-8111-111111111111"
	partsPrincipalID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

type partsFixture struct {
	service    *drive.Service
	repository *memory.Repository
	storage    *memory.Storage
	identity   drive.Identity
	tenant     drive.Tenant
	clock      *fixedClock
}

func newPartsFixture(t *testing.T, clock *fixedClock) partsFixture {
	t.Helper()
	if clock == nil {
		// A far-future fixed clock keeps every TTL meaningful without sleeping.
		clock = &fixedClock{now: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)}
	}
	repository := memory.NewRepository()
	storage := memory.NewStorage("list-parts-test")
	cursor, err := drive.NewCursorCodec([]byte("test-cursor-key-that-is-at-least-32-bytes"))
	if err != nil {
		t.Fatalf("create cursor codec: %v", err)
	}
	service, err := drive.NewService(drive.ServiceOptions{
		Repository: repository, Storage: storage, Cursor: cursor, Clock: clock,
		UploadTTL: time.Hour, UploadSignTTL: time.Minute, DownloadSignTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	identity := drive.Identity{TenantID: partsTenantID, PrincipalID: partsPrincipalID}
	tenant, err := service.EnsureTenant(context.Background(), identity.TenantID, "List parts test")
	if err != nil {
		t.Fatalf("ensure tenant: %v", err)
	}
	return partsFixture{service: service, repository: repository, storage: storage, identity: identity, tenant: tenant, clock: clock}
}

func (f partsFixture) createUpload(t *testing.T, name string, size int64) drive.UploadSession {
	t.Helper()
	upload, err := f.service.CreateUpload(context.Background(), f.identity, drive.CreateUploadInput{
		ParentID: f.tenant.RootNodeID, Name: name, Size: size, MimeType: "application/octet-stream",
	})
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	return upload
}

func TestListUploadPartsMergesStorageAndControlPlane(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "resume.bin", 12)
	if _, err := fixture.storage.PutPart(upload.StorageUploadID, 2, []byte("world")); err != nil {
		t.Fatalf("put part: %v", err)
	}
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if listing.UploadID != upload.ID || listing.Status != drive.UploadCreated || len(listing.Parts) != 1 {
		t.Fatalf("unexpected listing: %+v", listing)
	}
	if listing.Parts[0].PartNumber != 2 || listing.Parts[0].Size != 5 || listing.Parts[0].ETag == "" {
		t.Fatalf("unexpected part metadata: %+v", listing.Parts[0])
	}
	if !listing.Complete {
		t.Fatal("a listing within the limit must be marked complete")
	}
}

func TestListUploadPartsRejectsForeignTenantAndInvalidID(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "foreign.bin", 4)
	foreign := drive.Identity{TenantID: "99999999-9999-4999-8999-999999999999", PrincipalID: partsPrincipalID}
	if _, err := fixture.service.ListUploadParts(ctx, foreign, upload.ID, 0); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("foreign tenant listing error = %v, want not_found", err)
	}
	if _, err := fixture.service.ListUploadParts(ctx, fixture.identity, "not-a-uuid", 0); drive.CodeOf(err) != drive.CodeInvalidRequest {
		t.Fatalf("invalid id listing error = %v, want invalid_request", err)
	}
}

func TestListUploadPartsDoesNotSignOrMutate(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "untouched.bin", 4)
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if len(listing.Parts) != 0 {
		t.Fatalf("fresh session returned parts: %+v", listing.Parts)
	}
	session, err := fixture.service.Upload(ctx, fixture.identity, upload.ID)
	if err != nil {
		t.Fatalf("read upload: %v", err)
	}
	if session.Status != drive.UploadCreated || session.Revision != upload.Revision {
		t.Fatalf("listing must not transition the session: %+v", session)
	}
}

func TestListUploadPartsExpiresAnElapsedSession(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "elapsed.bin", 4)

	fixture.clock.now = fixture.clock.now.Add(2 * time.Hour)
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts after expiry: %v", err)
	}
	if listing.Status != drive.UploadExpired || len(listing.Parts) != 0 {
		t.Fatalf("elapsed listing = %+v, want expired with no parts", listing)
	}
	// The transition must be persistent: a second read sees the terminal status.
	session, err := fixture.service.Upload(ctx, fixture.identity, upload.ID)
	if err != nil {
		t.Fatalf("read upload: %v", err)
	}
	if session.Status != drive.UploadExpired {
		t.Fatalf("session status = %q, want expired", session.Status)
	}
}

func TestListUploadPartsReportsMissingStorageUploadAsInvalidState(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "gone.bin", 4)
	if err := fixture.storage.AbortMultipart(ctx, upload.ObjectKey, upload.StorageUploadID); err != nil {
		t.Fatalf("abort multipart: %v", err)
	}
	if _, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0); drive.CodeOf(err) != drive.CodeInvalidState {
		t.Fatalf("missing multipart error = %v, want invalid_state", err)
	}
}

func TestListUploadPartsReturnsTerminalSessionsWithoutStorage(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "cancelled.bin", 4)
	if err := fixture.service.AbortUpload(ctx, fixture.identity, upload.ID); err != nil {
		t.Fatalf("abort upload: %v", err)
	}
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts for aborted session: %v", err)
	}
	if listing.Status != drive.UploadAborted || len(listing.Parts) != 0 {
		t.Fatalf("aborted listing = %+v", listing)
	}
}

func TestListUploadPartsFrozenCompletionTakesPrecedence(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "frozen.bin", 5)
	stored, err := fixture.storage.PutPart(upload.StorageUploadID, 1, []byte("hello"))
	if err != nil {
		t.Fatalf("put part: %v", err)
	}
	parts := []drive.CompletedPart{{PartNumber: 1, ETag: stored.ETag, Size: stored.Size}}
	if _, err := fixture.service.CompleteUpload(ctx, fixture.identity, upload.ID, parts); err != nil {
		t.Fatalf("complete upload: %v", err)
	}
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts for committed session: %v", err)
	}
	if listing.Status != drive.UploadCommitted || len(listing.Parts) != 1 || listing.Parts[0].ETag != stored.ETag {
		t.Fatalf("committed listing = %+v", listing)
	}
}
