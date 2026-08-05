package drive_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/baicie/asteria-drive/internal/memory"
)

type idempotencyTestClock struct{ now time.Time }

func (c idempotencyTestClock) Now() time.Time { return c.now }

type recordingIdempotencyRepository struct {
	*memory.Repository
	mu       sync.Mutex
	claims   []drive.IdempotencyRequest
	releases []drive.IdempotencyRequest
}

func newRecordingIdempotencyRepository() *recordingIdempotencyRepository {
	return &recordingIdempotencyRepository{Repository: memory.NewRepository()}
}

func (r *recordingIdempotencyRepository) ClaimIdempotency(ctx context.Context, request drive.IdempotencyRequest) (drive.IdempotencyRecord, error) {
	record, err := r.Repository.ClaimIdempotency(ctx, request)
	r.mu.Lock()
	r.claims = append(r.claims, request)
	r.mu.Unlock()
	return record, err
}

func (r *recordingIdempotencyRepository) ReleaseIdempotency(ctx context.Context, request drive.IdempotencyRequest) error {
	err := r.Repository.ReleaseIdempotency(ctx, request)
	r.mu.Lock()
	r.releases = append(r.releases, request)
	r.mu.Unlock()
	return err
}

func (r *recordingIdempotencyRepository) recordedClaims() []drive.IdempotencyRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]drive.IdempotencyRequest(nil), r.claims...)
}

func (r *recordingIdempotencyRepository) recordedReleases() []drive.IdempotencyRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]drive.IdempotencyRequest(nil), r.releases...)
}

type multipartCreateProbe struct {
	*memory.Storage
	createCalls atomic.Int32
	abortCalls  atomic.Int32
	failFirst   atomic.Bool
	failure     error
	entered     chan struct{}
	proceed     chan struct{}
}

func (s *multipartCreateProbe) CreateMultipart(ctx context.Context, key, mimeType string, checksum drive.Checksum) (string, error) {
	call := s.createCalls.Add(1)
	if call == 1 && s.entered != nil {
		close(s.entered)
		select {
		case <-s.proceed:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if s.failure != nil && s.failFirst.CompareAndSwap(true, false) {
		return "", s.failure
	}
	return s.Storage.CreateMultipart(ctx, key, mimeType, checksum)
}

func (s *multipartCreateProbe) AbortMultipart(ctx context.Context, key, uploadID string) error {
	s.abortCalls.Add(1)
	return s.Storage.AbortMultipart(ctx, key, uploadID)
}

type failFirstDirectoryRepository struct {
	*recordingIdempotencyRepository
	failFirst atomic.Bool
}

func (r *failFirstDirectoryRepository) CreateDirectory(ctx context.Context, command drive.CreateDirectoryCommand) (drive.Node, error) {
	if r.failFirst.CompareAndSwap(true, false) {
		return drive.Node{}, drive.E(drive.CodeNameConflict, "simulated directory conflict")
	}
	return r.Repository.CreateDirectory(ctx, command)
}

type failFirstUploadRepository struct {
	*recordingIdempotencyRepository
	failFirst atomic.Bool
}

func (r *failFirstUploadRepository) CreateUpload(ctx context.Context, command drive.CreateUploadCommand) (drive.UploadSession, error) {
	if r.failFirst.CompareAndSwap(true, false) {
		return drive.UploadSession{}, drive.E(drive.CodeNameConflict, "simulated upload conflict")
	}
	return r.Repository.CreateUpload(ctx, command)
}

func TestCreateDirectoryIdempotencyReplaysAndRejectsChangedRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := newRecordingIdempotencyRepository()
	service, identity, tenant := newIdempotencyTestService(t, repository, memory.NewStorage("directory-idempotency"))
	key := "directory-create-raw-key-never-store-this-value"

	first, err := service.CreateDirectoryWithIdempotency(ctx, identity, tenant.RootNodeID, "Reports", key)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed {
		t.Fatal("first directory creation was marked as replayed")
	}
	second, err := service.CreateDirectoryWithIdempotency(ctx, identity, tenant.RootNodeID, "Reports", key)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.Node.ID != first.Node.ID {
		t.Fatalf("directory replay=%t id=%s, want replay=true id=%s", second.Replayed, second.Node.ID, first.Node.ID)
	}
	if _, err := service.CreateDirectoryWithIdempotency(ctx, identity, tenant.RootNodeID, "Changed", key); drive.CodeOf(err) != drive.CodeIdempotencyConflict {
		t.Fatalf("changed request code=%s err=%v, want idempotency_conflict", drive.CodeOf(err), err)
	}

	claims := repository.recordedClaims()
	if len(claims) != 3 {
		t.Fatalf("claim calls=%d, want 3", len(claims))
	}
	assertHashedIdempotencyClaim(t, claims[0], key, struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
	}{ParentID: tenant.RootNodeID, Name: "Reports"})
	if claims[1].KeyHash != claims[0].KeyHash || claims[1].RequestDigest != claims[0].RequestDigest {
		t.Fatal("identical replay did not use identical idempotency digests")
	}
	if claims[2].KeyHash != claims[0].KeyHash || claims[2].RequestDigest == claims[0].RequestDigest {
		t.Fatal("changed request did not retain the key digest and change the request digest")
	}
}

func TestCreateUploadIdempotencyReplaysWithoutCreatingAnotherMultipartUpload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := newRecordingIdempotencyRepository()
	storage := &multipartCreateProbe{Storage: memory.NewStorage("upload-idempotency")}
	service, identity, tenant := newIdempotencyTestService(t, repository, storage)
	input := drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "archive.bin", Size: 1024, MimeType: "application/octet-stream",
	}

	first, err := service.CreateUploadWithIdempotency(ctx, identity, input, "upload-create-key")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateUploadWithIdempotency(ctx, identity, input, "upload-create-key")
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || !second.Replayed {
		t.Fatalf("first replay=%t second replay=%t, want false and true", first.Replayed, second.Replayed)
	}
	if second.Upload.ID != first.Upload.ID || second.Upload.StorageUploadID != first.Upload.StorageUploadID {
		t.Fatalf("upload replay returned a different session: first=%+v second=%+v", first.Upload, second.Upload)
	}
	if calls := storage.createCalls.Load(); calls != 1 {
		t.Fatalf("CreateMultipart calls=%d, want 1", calls)
	}
}

func TestCreateUploadClaimsIdempotencyBeforeCallingStorage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := newRecordingIdempotencyRepository()
	storage := &multipartCreateProbe{
		Storage: memory.NewStorage("blocking-upload-idempotency"),
		entered: make(chan struct{}),
		proceed: make(chan struct{}),
	}
	service, identity, tenant := newIdempotencyTestService(t, repository, storage)
	input := drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "blocked.bin", Size: 12, MimeType: "application/octet-stream",
	}
	key := "blocked-upload-key"
	type createResult struct {
		output drive.CreateUploadOutput
		err    error
	}
	result := make(chan createResult, 1)
	go func() {
		output, err := service.CreateUploadWithIdempotency(ctx, identity, input, key)
		result <- createResult{output: output, err: err}
	}()

	select {
	case <-storage.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("CreateMultipart was not reached")
	}
	if len(repository.recordedClaims()) != 1 {
		t.Fatal("storage was entered before the first idempotency claim completed")
	}
	_, err := service.CreateUploadWithIdempotency(ctx, identity, input, key)
	var domainErr *drive.Error
	if drive.CodeOf(err) != drive.CodeDependencyUnavailable || !errors.As(err, &domainErr) || !domainErr.Retryable {
		close(storage.proceed)
		t.Fatalf("concurrent request error=%v, want retryable dependency_unavailable", err)
	}
	if calls := storage.createCalls.Load(); calls != 1 {
		close(storage.proceed)
		t.Fatalf("CreateMultipart calls while first request blocked=%d, want 1", calls)
	}
	close(storage.proceed)
	select {
	case completed := <-result:
		if completed.err != nil || completed.output.Replayed {
			t.Fatalf("first request output=%+v err=%v", completed.output, completed.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first upload creation did not finish")
	}
}

func TestCreateIdempotencyReleasesClaimsAfterDeterministicFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("storage create", func(t *testing.T) {
		repository := newRecordingIdempotencyRepository()
		storage := &multipartCreateProbe{
			Storage: memory.NewStorage("failed-storage-idempotency"),
			failure: drive.E(drive.CodeInvalidRequest, "simulated deterministic storage rejection"),
		}
		storage.failFirst.Store(true)
		service, identity, tenant := newIdempotencyTestService(t, repository, storage)
		input := drive.CreateUploadInput{
			ParentID: tenant.RootNodeID, Name: "storage-retry.bin", Size: 8, MimeType: "application/octet-stream",
		}
		key := "storage-failure-key"

		if _, err := service.CreateUploadWithIdempotency(ctx, identity, input, key); drive.CodeOf(err) != drive.CodeInvalidRequest {
			t.Fatalf("first creation code=%s err=%v, want invalid_request", drive.CodeOf(err), err)
		}
		second, err := service.CreateUploadWithIdempotency(ctx, identity, input, key)
		if err != nil {
			t.Fatalf("retry after deterministic storage failure: %v", err)
		}
		if second.Replayed {
			t.Fatal("retry after released storage failure was marked replayed")
		}
		if len(repository.recordedReleases()) != 1 {
			t.Fatalf("release calls=%d, want 1", len(repository.recordedReleases()))
		}
	})

	t.Run("directory create", func(t *testing.T) {
		recorded := newRecordingIdempotencyRepository()
		repository := &failFirstDirectoryRepository{recordingIdempotencyRepository: recorded}
		repository.failFirst.Store(true)
		service, identity, tenant := newIdempotencyTestService(t, repository, memory.NewStorage("failed-directory-idempotency"))
		key := "directory-failure-key"

		if _, err := service.CreateDirectoryWithIdempotency(ctx, identity, tenant.RootNodeID, "Retry", key); drive.CodeOf(err) != drive.CodeNameConflict {
			t.Fatalf("first creation code=%s err=%v, want name_conflict", drive.CodeOf(err), err)
		}
		second, err := service.CreateDirectoryWithIdempotency(ctx, identity, tenant.RootNodeID, "Retry", key)
		if err != nil {
			t.Fatalf("retry after deterministic directory failure: %v", err)
		}
		if second.Replayed {
			t.Fatal("retry after released directory failure was marked replayed")
		}
		if len(recorded.recordedReleases()) != 1 {
			t.Fatalf("release calls=%d, want 1", len(recorded.recordedReleases()))
		}
	})

	t.Run("upload record create", func(t *testing.T) {
		recorded := newRecordingIdempotencyRepository()
		repository := &failFirstUploadRepository{recordingIdempotencyRepository: recorded}
		repository.failFirst.Store(true)
		storage := &multipartCreateProbe{Storage: memory.NewStorage("failed-upload-record-idempotency")}
		service, identity, tenant := newIdempotencyTestService(t, repository, storage)
		input := drive.CreateUploadInput{
			ParentID: tenant.RootNodeID, Name: "record-retry.bin", Size: 8, MimeType: "application/octet-stream",
		}
		key := "upload-record-failure-key"

		if _, err := service.CreateUploadWithIdempotency(ctx, identity, input, key); drive.CodeOf(err) != drive.CodeNameConflict {
			t.Fatalf("first creation code=%s err=%v, want name_conflict", drive.CodeOf(err), err)
		}
		second, err := service.CreateUploadWithIdempotency(ctx, identity, input, key)
		if err != nil {
			t.Fatalf("retry after deterministic upload record failure: %v", err)
		}
		if second.Replayed {
			t.Fatal("retry after released upload record failure was marked replayed")
		}
		if len(recorded.recordedReleases()) != 1 {
			t.Fatalf("release calls=%d, want 1", len(recorded.recordedReleases()))
		}
		if calls := storage.abortCalls.Load(); calls != 1 {
			t.Fatalf("AbortMultipart calls=%d, want 1", calls)
		}
	})
}

func newIdempotencyTestService(t *testing.T, repository drive.Repository, storage drive.StorageProvider) (*drive.Service, drive.Identity, drive.Tenant) {
	t.Helper()
	cursor, err := drive.NewCursorCodec([]byte("idempotency-service-test-cursor-key-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := drive.NewService(drive.ServiceOptions{
		Repository:           repository,
		Storage:              storage,
		Cursor:               cursor,
		Clock:                idempotencyTestClock{now: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)},
		IdempotencyClaimTTL:  time.Minute,
		IdempotencyRetention: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := drive.Identity{
		TenantID:    "91919191-9191-4191-8191-919191919191",
		PrincipalID: "92929292-9292-4292-8292-929292929292",
	}
	tenant, err := service.EnsureTenant(context.Background(), identity.TenantID, "Idempotency test")
	if err != nil {
		t.Fatal(err)
	}
	return service, identity, tenant
}

func assertHashedIdempotencyClaim(t *testing.T, claim drive.IdempotencyRequest, rawKey string, request any) {
	t.Helper()
	keyHash := sha256.Sum256([]byte(rawKey))
	expectedKeyHash := hex.EncodeToString(keyHash[:])
	if claim.KeyHash != expectedKeyHash || !drive.ValidSHA256Hex(claim.KeyHash) {
		t.Fatalf("key digest=%q, want lowercase SHA-256 %q", claim.KeyHash, expectedKeyHash)
	}
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestHash := sha256.Sum256(encodedRequest)
	expectedRequestDigest := hex.EncodeToString(requestHash[:])
	if claim.RequestDigest != expectedRequestDigest || !drive.ValidSHA256Hex(claim.RequestDigest) {
		t.Fatalf("request digest=%q, want lowercase SHA-256 %q", claim.RequestDigest, expectedRequestDigest)
	}
	persisted, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), rawKey) {
		t.Fatalf("raw idempotency key was passed to persistence: %s", persisted)
	}
}
