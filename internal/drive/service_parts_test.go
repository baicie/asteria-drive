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
	return newPartsFixtureWithStorage(t, clock, nil)
}

// newPartsFixtureWithStorage lets a case decide what the service sees as its storage
// backend. The repository and the direct handles stay on the real memory storage so a test
// can seed parts, while the service may be given a wrapper that reports a different shape.
func newPartsFixtureWithStorage(t *testing.T, clock *fixedClock, wrap func(*memory.Storage) drive.StorageProvider) partsFixture {
	t.Helper()
	if clock == nil {
		// A far-future fixed clock keeps every TTL meaningful without sleeping.
		clock = &fixedClock{now: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)}
	}
	repository := memory.NewRepository()
	storage := memory.NewStorage("list-parts-test")
	var serviceStorage drive.StorageProvider = storage
	if wrap != nil {
		serviceStorage = wrap(storage)
	}
	cursor, err := drive.NewCursorCodec([]byte("test-cursor-key-that-is-at-least-32-bytes"))
	if err != nil {
		t.Fatalf("create cursor codec: %v", err)
	}
	service, err := drive.NewService(drive.ServiceOptions{
		Repository: repository, Storage: serviceStorage, Cursor: cursor, Clock: clock,
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

// A stored part is reusable on the strength of what storage reports about the bytes it
// accepted, never on the digest a client declared. Here storage holds exactly the signed
// payload, so the two agree and the part is reusable.
func TestListUploadPartsReportsStorageChecksum(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	// A single-part file: expected_size <= part_size, so the signed size is the whole file.
	payload := []byte("abcde")
	upload := fixture.createUpload(t, "identity.bin", int64(len(payload)))
	if _, err := fixture.service.SignUploadPart(ctx, fixture.identity, upload.ID, 1, sha256Checksum(payload)); err != nil {
		t.Fatalf("sign part: %v", err)
	}
	stored, err := fixture.storage.PutPart(upload.StorageUploadID, 1, payload)
	if err != nil {
		t.Fatalf("put part: %v", err)
	}

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
	if want := sha256Checksum(payload); listing.Parts[0].StorageChecksum != want {
		t.Fatalf("storage checksum = %+v, want %+v", listing.Parts[0].StorageChecksum, want)
	}
	if !listing.Parts[0].Reusable {
		t.Fatal("a part storage checksums at the expected size must be reusable")
	}
}

// The R3 regression. Signing part 1 with B's digest does not change what storage holds:
// A's bytes stay exactly where they were. The listing must keep describing A, and the part
// must stay non-reusable for B, so the only outcome of resuming with B is a re-upload.
func TestListUploadPartsIgnoresSignatureThatStorageNeverReceived(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	payloadA := []byte("AAAAA")
	payloadB := []byte("BBBBB")
	upload := fixture.createUpload(t, "resign.bin", int64(len(payloadA)))
	stored, err := fixture.storage.PutPart(upload.StorageUploadID, 1, payloadA)
	if err != nil {
		t.Fatalf("put part: %v", err)
	}
	// A second signature for the same part number: the client promised B, then the PUT was
	// interrupted. The control plane records the promise; storage still holds A.
	if _, err := fixture.service.SignUploadPart(ctx, fixture.identity, upload.ID, 1, sha256Checksum(payloadB)); err != nil {
		t.Fatalf("re-sign part: %v", err)
	}

	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if len(listing.Parts) != 1 {
		t.Fatalf("parts = %+v, want one", listing.Parts)
	}
	if got := listing.Parts[0].StorageChecksum; got != sha256Checksum(payloadA) {
		t.Fatalf("storage checksum = %+v, want A's %+v: a signature storage never received must not describe the stored part",
			got, sha256Checksum(payloadA))
	}
	if listing.Parts[0].StorageChecksum == sha256Checksum(payloadB) {
		t.Fatal("listing reported the declared B digest for bytes storage never accepted")
	}
	if listing.Parts[0].ETag != stored.ETag {
		t.Fatalf("etag = %q, want the stored %q", listing.Parts[0].ETag, stored.ETag)
	}
}

// Storage accepted bytes of the wrong length for this part number. Nothing about them can
// be checked against the session geometry, so they must be re-sent.
func TestListUploadPartsMarksSizeMismatchForReupload(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixture(t, nil)
	upload := fixture.createUpload(t, "short.bin", 5)
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
	if listing.Parts[0].Reusable {
		t.Fatalf("part of %d bytes cannot satisfy a %d-byte part: %+v", listing.Parts[0].Size, 5, listing.Parts[0])
	}
}

// A backend that reports no checksum at all gives resume nothing to verify against. That is
// the SeaweedFS 3.85 behaviour, and the safe answer is to re-upload.
func TestListUploadPartsWithoutStorageChecksumIsNotReusable(t *testing.T) {
	ctx := context.Background()
	// A backend that reports no checksum (SeaweedFS 3.85) gives resume nothing to verify.
	fixture := newPartsFixtureWithStorage(t, nil, func(storage *memory.Storage) drive.StorageProvider {
		return unattestedStorage{StorageProvider: storage}
	})
	upload := fixture.createUpload(t, "unsigned.bin", 5)
	if _, err := fixture.storage.PutPart(upload.StorageUploadID, 1, []byte("abcde")); err != nil {
		t.Fatalf("put part: %v", err)
	}
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if len(listing.Parts) != 1 || listing.Parts[0].Reusable {
		t.Fatalf("a part without a storage checksum must not be reusable: %+v", listing.Parts)
	}
	if listing.Parts[0].StorageChecksum.Algorithm != "" {
		t.Fatalf("no checksum may be invented for an unattested part: %+v", listing.Parts[0])
	}
}

// Re-PUTting the same part number leaves storage describing more than one candidate, and
// which one completion will use is decided by the ETag in the completion request. There is
// no single stored identity to verify, so resume must re-upload.
func TestListUploadPartsDuplicatePartNumbersAreNotReusable(t *testing.T) {
	ctx := context.Background()
	// The real memory backend collapses a re-PUT into one entry, so the duplicate shape is
	// produced by a wrapper that reports what SeaweedFS actually returns.
	fixture := newPartsFixtureWithStorage(t, nil, func(storage *memory.Storage) drive.StorageProvider {
		return duplicateStorage{StorageProvider: storage}
	})
	upload := fixture.createUpload(t, "duplicate.bin", 5)
	if _, err := fixture.storage.PutPart(upload.StorageUploadID, 1, []byte("AAAAA")); err != nil {
		t.Fatalf("put part: %v", err)
	}
	if _, err := fixture.storage.PutPart(upload.StorageUploadID, 1, []byte("BBBBB")); err != nil {
		t.Fatalf("re-put part: %v", err)
	}
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts with duplicate entries: %v", err)
	}
	if len(listing.Parts) != 1 || listing.Parts[0].Reusable {
		t.Fatalf("an ambiguous part number must not be reusable: %+v", listing.Parts)
	}
}

// A backend can report a checksum it never validated. The listing must keep that checksum
// visible for diagnostics but must not call the part reusable, because nothing proves the
// reported digest describes an accepted PUT or that a mismatched PUT was rejected. This is
// the provider-level half of ADR-0029; the s3store flag is the deployment-level half.
func TestListUploadPartsDoesNotTrustAnUnattestedReportedChecksum(t *testing.T) {
	ctx := context.Background()
	fixture := newPartsFixtureWithStorage(t, nil, func(storage *memory.Storage) drive.StorageProvider {
		return reportedButUnattestedStorage{StorageProvider: storage}
	})
	payload := []byte("abcde")
	upload := fixture.createUpload(t, "reported.bin", int64(len(payload)))
	if _, err := fixture.storage.PutPart(upload.StorageUploadID, 1, payload); err != nil {
		t.Fatalf("put part: %v", err)
	}
	listing, err := fixture.service.ListUploadParts(ctx, fixture.identity, upload.ID, 0)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if len(listing.Parts) != 1 {
		t.Fatalf("parts = %+v, want one", listing.Parts)
	}
	if want := sha256Checksum(payload); listing.Parts[0].StorageChecksum != want {
		t.Fatalf("reported checksum = %+v, want %+v", listing.Parts[0].StorageChecksum, want)
	}
	if listing.Parts[0].ChecksumAttested {
		t.Fatal("a provider that did not attest its checksum must not be recorded as attested")
	}
	if listing.Parts[0].Reusable {
		t.Fatal("an unattested reported checksum must not make a part skippable on resume")
	}
}

// reportedButUnattestedStorage reports a real checksum over the bytes it holds, but does not
// claim the backend ever validated a signed part PUT against it.
type reportedButUnattestedStorage struct {
	drive.StorageProvider
}

func (s reportedButUnattestedStorage) ListParts(ctx context.Context, key, uploadID string) ([]drive.StoredPart, error) {
	parts, err := s.StorageProvider.ListParts(ctx, key, uploadID)
	if err != nil {
		return nil, err
	}
	for index := range parts {
		parts[index].ChecksumAttested = false
	}
	return parts, nil
}

// unattestedStorage behaves like a backend that never reports per-part checksums.
type unattestedStorage struct {
	drive.StorageProvider
}

func (s unattestedStorage) ListParts(ctx context.Context, key, uploadID string) ([]drive.StoredPart, error) {
	parts, err := s.StorageProvider.ListParts(ctx, key, uploadID)
	if err != nil {
		return nil, err
	}
	for index := range parts {
		parts[index].StorageChecksum = drive.Checksum{}
	}
	return parts, nil
}

// duplicateStorage reports a second entry for every part, as SeaweedFS does after a re-PUT.
type duplicateStorage struct {
	drive.StorageProvider
}

func (s duplicateStorage) ListParts(ctx context.Context, key, uploadID string) ([]drive.StoredPart, error) {
	parts, err := s.StorageProvider.ListParts(ctx, key, uploadID)
	if err != nil {
		return nil, err
	}
	duplicated := make([]drive.StoredPart, 0, len(parts)*2)
	for _, part := range parts {
		duplicated = append(duplicated, part)
		duplicated = append(duplicated, drive.StoredPart{
			PartNumber: part.PartNumber, ETag: part.ETag + "-earlier", Size: part.Size,
			StorageChecksum: part.StorageChecksum,
		})
	}
	return duplicated, nil
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

// A backend that answers with an out-of-range or empty-ETag entry must not inject anything
// into the listing, and the listing stays complete for the parts it can describe.
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
