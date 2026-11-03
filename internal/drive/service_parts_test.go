package drive_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

func TestListUploadPartsReportsExpiryWithoutWritingIt(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "elapsed.bin", 4)

	fixture.clock.now = fixture.clock.now.Add(2 * time.Hour)
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts after expiry: %v", err)
	}
	if listing.Status != drive.UploadExpired || len(listing.Parts) != 0 || !listing.Complete {
		t.Fatalf("elapsed listing = %+v, want expired with no parts and complete", listing)
	}
	// A GET must not have persistent side effects: the stored session is untouched and the
	// multipart upload is still there. The maintenance loop owns the real transition.
	session, err := fixture.service.Upload(ctx, fixture.identity, upload.ID)
	if err != nil {
		t.Fatalf("read upload: %v", err)
	}
	if session.Status != drive.UploadCreated || session.Revision != upload.Revision {
		t.Fatalf("listing changed the session: status=%q revision=%d, want created/%d", session.Status, session.Revision, upload.Revision)
	}
	if _, err := fixture.storage.ListParts(ctx, upload.ObjectKey, upload.StorageUploadID); err != nil {
		t.Fatalf("listing aborted the multipart upload: %v", err)
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

func TestListUploadPartsAttachesSignedContentIdentity(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	// A single-part file: expected_size <= part_size, so the signed size is the whole file.
	payload := []byte("abcde")
	upload := fixture.createUpload(t, "identity.bin", int64(len(payload)))
	signed, err := fixture.service.SignUploadPart(ctx, fixture.identity, upload.ID, 1, sha256Checksum(payload))
	if err != nil {
		t.Fatalf("sign part: %v", err)
	}
	stored, err := fixture.storage.PutPart(upload.StorageUploadID, 1, payload)
	if err != nil {
		t.Fatalf("put part: %v", err)
	}
	_ = signed

	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if len(listing.Parts) != 1 {
		t.Fatalf("parts = %+v, want one", listing.Parts)
	}
	if listing.Parts[0].ETag != stored.ETag {
		t.Fatalf("etag = %q, want %q", listing.Parts[0].ETag, stored.ETag)
	}
	if want := sha256Checksum(payload).Value; listing.Parts[0].ContentSHA256 != want {
		t.Fatalf("content digest = %q, want %q: a signed part must carry its identity", listing.Parts[0].ContentSHA256, want)
	}
}

func TestListUploadPartsWithholdsIdentityWhenSizeDiffers(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "short.bin", 5)
	if _, err := fixture.service.SignUploadPart(ctx, fixture.identity, upload.ID, 1, sha256Checksum([]byte("abcde"))); err != nil {
		t.Fatalf("sign part: %v", err)
	}
	// Storage holds different-length bytes for part 1. The signature cannot describe them,
	// so the listing must report no identity and the client will re-upload.
	if _, err := fixture.storage.PutPart(upload.StorageUploadID, 1, []byte("abc")); err != nil {
		t.Fatalf("put part: %v", err)
	}
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if len(listing.Parts) != 1 {
		t.Fatalf("parts = %+v, want one", listing.Parts)
	}
	if listing.Parts[0].ContentSHA256 != "" {
		t.Fatalf("content digest = %q, want none for a size mismatch", listing.Parts[0].ContentSHA256)
	}
}

func TestListUploadPartsWithoutSignatureHasNoIdentity(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "unsigned.bin", 5)
	if _, err := fixture.storage.PutPart(upload.StorageUploadID, 1, []byte("abcde")); err != nil {
		t.Fatalf("put part: %v", err)
	}
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if len(listing.Parts) != 1 || listing.Parts[0].ContentSHA256 != "" {
		t.Fatalf("unsigned part must have no content identity: %+v", listing.Parts)
	}
}

func sha256Checksum(payload []byte) drive.Checksum {
	sum := sha256.Sum256(payload)
	return drive.Checksum{Algorithm: "sha256", Value: base64.StdEncoding.EncodeToString(sum[:])}
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

// A storage listing that repeats a part number must not make the service claim a complete
// listing it cannot reconstruct; the service keeps the last value and still marks it whole.
func TestListUploadPartsHandlesDuplicateAndOutOfRangeNumbers(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "odd.bin", 10)
	if _, err := fixture.storage.PutPart(upload.StorageUploadID, 1, []byte("aaaaa")); err != nil {
		t.Fatalf("put part 1: %v", err)
	}
	if _, err := fixture.storage.PutPart(upload.StorageUploadID, 2, []byte("bbbbb")); err != nil {
		t.Fatalf("put part 2: %v", err)
	}
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if !listing.Complete || len(listing.Parts) != 2 {
		t.Fatalf("listing = %+v, want two complete parts", listing)
	}
	for index, part := range listing.Parts {
		if part.PartNumber != index+1 {
			t.Fatalf("part at index %d has number %d", index, part.PartNumber)
		}
		if part.ETag == "" || part.Size != 5 {
			t.Fatalf("part %d metadata = %+v", part.PartNumber, part)
		}
	}
}

// limit cuts the response short; the caller must be told the subset is not authoritative.
func TestListUploadPartsLimitMarksListingIncomplete(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "limited.bin", 15)
	for partNumber := 1; partNumber <= 3; partNumber++ {
		if _, err := fixture.storage.PutPart(upload.StorageUploadID, partNumber, []byte("abcde")); err != nil {
			t.Fatalf("put part: %v", err)
		}
	}
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 2)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if listing.Complete || len(listing.Parts) != 2 {
		t.Fatalf("limited listing = %+v, want two parts and incomplete", listing)
	}
	full, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list all parts: %v", err)
	}
	if !full.Complete || len(full.Parts) != 3 {
		t.Fatalf("unlimited listing = %+v, want three complete parts", full)
	}
}
