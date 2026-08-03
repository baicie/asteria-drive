package drive_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/baicie/asteria-drive/internal/memory"
)

type uncertainCompletionStorage struct {
	*memory.Storage
	failFirst atomic.Bool
}

type retryingAbortStorage struct {
	*memory.Storage
	failFirst  atomic.Bool
	abortCalls atomic.Int32
}

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

type rejectingCreateRepository struct{ *memory.Repository }

type transitioningAbortRepository struct {
	*memory.Repository
	abortResult drive.UploadSession
}

func (r *transitioningAbortRepository) AbortUpload(context.Context, drive.Identity, string, drive.UploadStatus, time.Time) (drive.UploadSession, error) {
	return r.abortResult, nil
}

func (r *rejectingCreateRepository) CreateUpload(context.Context, drive.CreateUploadCommand) (drive.UploadSession, error) {
	return drive.UploadSession{}, context.Canceled
}

type contextAwareAbortStorage struct {
	*memory.Storage
	lastUploadID string
	lastKey      string
	aborted      bool
}

type unavailableChecksumStorage struct{ *memory.Storage }

type mismatchedVerifiedChecksumStorage struct{ *memory.Storage }

type missingCompletionStorage struct{ *memory.Storage }

func (s *missingCompletionStorage) CompleteMultipart(context.Context, string, string, []drive.CompletedPart) (drive.ObjectInfo, error) {
	return drive.ObjectInfo{}, drive.E(drive.CodeNotFound, "multipart upload was not found")
}

func (s *missingCompletionStorage) StatObject(context.Context, string) (drive.ObjectInfo, error) {
	return drive.ObjectInfo{}, drive.E(drive.CodeNotFound, "object was not found")
}

func (s *mismatchedVerifiedChecksumStorage) CreateMultipart(ctx context.Context, key, mimeType string, _ drive.Checksum) (string, error) {
	return s.Storage.CreateMultipart(ctx, key, mimeType, drive.Checksum{})
}

func (s *mismatchedVerifiedChecksumStorage) StatObject(ctx context.Context, key string) (drive.ObjectInfo, error) {
	object, err := s.Storage.StatObject(ctx, key)
	if err != nil {
		return drive.ObjectInfo{}, err
	}
	digest := sha256.Sum256([]byte("different object"))
	object.Checksum = drive.Checksum{Algorithm: "sha256", Value: base64.StdEncoding.EncodeToString(digest[:])}
	object.ChecksumStatus = drive.ChecksumVerified
	return object, nil
}

func (s *unavailableChecksumStorage) StatObject(ctx context.Context, key string) (drive.ObjectInfo, error) {
	object, err := s.Storage.StatObject(ctx, key)
	object.Checksum = drive.Checksum{}
	object.ChecksumStatus = drive.ChecksumUnavailable
	return object, err
}

type retryingDeleteStorage struct {
	*memory.Storage
	failFirst   atomic.Bool
	deleteCalls atomic.Int32
}

func (s *retryingDeleteStorage) DeleteObject(ctx context.Context, key string) error {
	s.deleteCalls.Add(1)
	if s.failFirst.CompareAndSwap(true, false) {
		return drive.Retryable(drive.CodeDependencyUnavailable, "simulated object delete failure", context.DeadlineExceeded)
	}
	return s.Storage.DeleteObject(ctx, key)
}

func (s *contextAwareAbortStorage) CreateMultipart(ctx context.Context, key, mimeType string, checksum drive.Checksum) (string, error) {
	uploadID, err := s.Storage.CreateMultipart(ctx, key, mimeType, checksum)
	s.lastUploadID = uploadID
	s.lastKey = key
	return uploadID, err
}

func (s *contextAwareAbortStorage) AbortMultipart(ctx context.Context, key, uploadID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.aborted = true
	return s.Storage.AbortMultipart(ctx, key, uploadID)
}

func (s *retryingAbortStorage) AbortMultipart(ctx context.Context, key, uploadID string) error {
	s.abortCalls.Add(1)
	if s.failFirst.CompareAndSwap(true, false) {
		return drive.Retryable(drive.CodeDependencyUnavailable, "simulated abort failure", context.DeadlineExceeded)
	}
	return s.Storage.AbortMultipart(ctx, key, uploadID)
}

func (s *uncertainCompletionStorage) CompleteMultipart(ctx context.Context, key, uploadID string, parts []drive.CompletedPart) (drive.ObjectInfo, error) {
	object, err := s.Storage.CompleteMultipart(ctx, key, uploadID, parts)
	if err == nil && s.failFirst.CompareAndSwap(true, false) {
		return drive.ObjectInfo{}, drive.Retryable(drive.CodeDependencyUnavailable, "simulated unknown completion result", context.DeadlineExceeded)
	}
	return object, err
}

func TestCompleteUploadReconcilesUnknownResultAndConcurrentRetries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := memory.NewRepository()
	storage := &uncertainCompletionStorage{Storage: memory.NewStorage("completion-test")}
	storage.failFirst.Store(true)
	cursor, err := drive.NewCursorCodec([]byte("completion-test-cursor-key-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := drive.NewService(drive.ServiceOptions{
		Repository: repository, Storage: storage, Cursor: cursor,
		UploadTTL: time.Hour, UploadSignTTL: time.Minute, DownloadSignTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := drive.Identity{
		TenantID: "11111111-1111-4111-8111-111111111111", PrincipalID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Completion test")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := service.CreateDirectory(ctx, identity, tenant.RootNodeID, "Uploads")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
		ParentID: directory.ID, Name: "result.bin", Size: 12, MimeType: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := storage.PutPart(upload.StorageUploadID, 1, []byte("hello world!"))
	if err != nil {
		t.Fatal(err)
	}

	const retryCount = 8
	results := make(chan drive.CompleteOutput, retryCount)
	errors := make(chan error, retryCount)
	var wait sync.WaitGroup
	for range retryCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{part})
			if err != nil {
				errors <- err
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Errorf("completion retry failed: %v", err)
	}
	var fileID string
	firstCount, resultCount := 0, 0
	for result := range results {
		resultCount++
		if result.First {
			firstCount++
		}
		if fileID == "" {
			fileID = result.Node.ID
		} else if result.Node.ID != fileID {
			t.Errorf("completion returned different file IDs: %s and %s", fileID, result.Node.ID)
		}
	}
	if resultCount != retryCount || firstCount != 1 {
		t.Fatalf("completion results=%d first_results=%d, want %d and 1", resultCount, firstCount, retryCount)
	}
	page, err := service.ListChildren(ctx, identity, directory.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != fileID {
		t.Fatalf("completion created unexpected namespace results: %+v", page.Items)
	}
}

func TestCompleteUploadKeepsUnknownMissingResultRetryable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := memory.NewRepository()
	storage := &missingCompletionStorage{Storage: memory.NewStorage("missing-completion-test")}
	service := completionService(t, repository, storage, "missing-completion-cursor-key-at-least-32-bytes")
	identity := drive.Identity{
		TenantID: "12121212-1212-4212-8212-121212121212", PrincipalID: "abababab-abab-4bab-8bab-abababababab",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Missing completion test")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "unknown.bin", Size: 7, MimeType: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := storage.Storage.PutPart(upload.StorageUploadID, 1, []byte("unknown"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{part})
	var domainErr *drive.Error
	if drive.CodeOf(err) != drive.CodeDependencyUnavailable || !errors.As(err, &domainErr) || !domainErr.Retryable {
		t.Fatalf("completion error=%v, want retryable dependency_unavailable", err)
	}
	persisted, err := service.Upload(ctx, identity, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != drive.UploadCompleting {
		t.Fatalf("upload status=%s, want completing", persisted.Status)
	}
}

func TestCompleteUploadNameConflictPersistsFailureAndRetriesObjectCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := memory.NewRepository()
	storage := &retryingDeleteStorage{Storage: memory.NewStorage("name-conflict-cleanup-test")}
	storage.failFirst.Store(true)
	service := completionService(t, repository, storage, "name-conflict-cleanup-cursor-key-at-least-32-bytes")
	identity := drive.Identity{
		TenantID: "13131313-1313-4313-8313-131313131313", PrincipalID: "bcbcbcbc-bcbc-4cbc-8cbc-bcbcbcbcbcbc",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Name conflict cleanup test")
	if err != nil {
		t.Fatal(err)
	}
	create := func() drive.UploadSession {
		upload, createErr := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
			ParentID: tenant.RootNodeID, Name: "same.bin", Size: 4, MimeType: "application/octet-stream",
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return upload
	}
	firstUpload, secondUpload := create(), create()
	firstPart, err := storage.PutPart(firstUpload.StorageUploadID, 1, []byte("one!"))
	if err != nil {
		t.Fatal(err)
	}
	secondPart, err := storage.PutPart(secondUpload.StorageUploadID, 1, []byte("two!"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CompleteUpload(ctx, identity, firstUpload.ID, []drive.CompletedPart{firstPart})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteUpload(ctx, identity, secondUpload.ID, []drive.CompletedPart{secondPart}); drive.CodeOf(err) != drive.CodeNameConflict {
		t.Fatalf("second completion code=%s err=%v, want name_conflict", drive.CodeOf(err), err)
	}
	failed, err := service.Upload(ctx, identity, secondUpload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != drive.UploadFailed || failed.FailureCode != drive.UploadFailureNameConflict {
		t.Fatalf("failed upload=%+v", failed)
	}
	if _, _, exists := storage.Object(secondUpload.ObjectKey); !exists {
		t.Fatal("simulated failed cleanup unexpectedly removed the conflicting object")
	}
	if err := service.AbortUpload(ctx, identity, secondUpload.ID); err != nil {
		t.Fatalf("retry failed-object cleanup: %v", err)
	}
	if storage.deleteCalls.Load() != 2 {
		t.Fatalf("delete calls=%d, want 2", storage.deleteCalls.Load())
	}
	if _, _, exists := storage.Object(secondUpload.ObjectKey); exists {
		t.Fatal("failed upload object remains after DELETE retry")
	}
	if _, _, err := repository.DownloadBlob(ctx, identity, first.Node.ID); err != nil {
		t.Fatalf("conflict cleanup affected the committed file: %v", err)
	}
}

func TestCompleteUploadFailsWhenParentWasRecycledAndDeletesObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := memory.NewRepository()
	storage := memory.NewStorage("parent-recycled-completion-test")
	service := completionService(t, repository, storage, "parent-recycled-cursor-key-at-least-32-bytes")
	identity := drive.Identity{
		TenantID: "14141414-1414-4414-8414-141414141414", PrincipalID: "cdcdcdcd-cdcd-4dcd-8dcd-cdcdcdcdcdcd",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Parent recycled completion test")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := service.CreateDirectory(ctx, identity, tenant.RootNodeID, "Parent")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
		ParentID: parent.ID, Name: "orphan.bin", Size: 6, MimeType: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := storage.PutPart(upload.StorageUploadID, 1, []byte("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Recycle(ctx, identity, parent.ID, parent.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{part}); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("completion code=%s err=%v, want not_found", drive.CodeOf(err), err)
	}
	failed, err := service.Upload(ctx, identity, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != drive.UploadFailed || failed.FailureCode != drive.UploadFailureParentUnavailable {
		t.Fatalf("failed upload=%+v", failed)
	}
	if _, _, exists := storage.Object(upload.ObjectKey); exists {
		t.Fatal("completed object remained after parent admission failure")
	}
}

func TestCommittedCompletionReplaySurvivesRecycleAndPurge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := memory.NewRepository()
	storage := memory.NewStorage("committed-replay-test")
	service := completionService(t, repository, storage, "committed-replay-cursor-key-at-least-32-bytes")
	identity := drive.Identity{
		TenantID: "15151515-1515-4515-8515-151515151515", PrincipalID: "dededede-dede-4ede-8ede-dededededede",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Committed replay test")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("persisted")
	upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "persisted.bin", Size: int64(len(body)), MimeType: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := storage.PutPart(upload.StorageUploadID, 1, body)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{part})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Recycle(ctx, identity, completed.Node.ID, completed.Node.Revision); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{part})
	if err != nil {
		t.Fatalf("replay after recycle: %v", err)
	}
	if replayed.First || replayed.Node.ID != completed.Node.ID {
		t.Fatalf("replay after recycle=%+v, want file %s", replayed, completed.Node.ID)
	}
	if err := service.Purge(ctx, identity, completed.Node.ID, completed.Node.Revision+1); err != nil {
		t.Fatal(err)
	}
	replayed, err = service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{part})
	if err != nil {
		t.Fatalf("replay after purge: %v", err)
	}
	if replayed.First || replayed.Node.ID != completed.Node.ID {
		t.Fatalf("replay after purge=%+v, want file %s", replayed, completed.Node.ID)
	}
}

func TestAbortUploadPersistsTerminalStateAndRetriesStorageCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := memory.NewRepository()
	storage := &retryingAbortStorage{Storage: memory.NewStorage("abort-test")}
	storage.failFirst.Store(true)
	cursor, err := drive.NewCursorCodec([]byte("abort-test-cursor-key-that-is-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := drive.NewService(drive.ServiceOptions{Repository: repository, Storage: storage, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	identity := drive.Identity{
		TenantID: "22222222-2222-4222-8222-222222222222", PrincipalID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Abort test")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "abort.bin", Size: 1, MimeType: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AbortUpload(ctx, identity, upload.ID); drive.CodeOf(err) != drive.CodeDependencyUnavailable {
		t.Fatalf("first abort code=%s err=%v, want dependency_unavailable", drive.CodeOf(err), err)
	}
	aborted, err := service.Upload(ctx, identity, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if aborted.Status != drive.UploadAborted {
		t.Fatalf("upload status=%s after cleanup failure, want aborted", aborted.Status)
	}
	if _, err := storage.Storage.SignUploadPart(ctx, upload.ObjectKey, upload.StorageUploadID, 1, drive.Checksum{}, time.Minute); err != nil {
		t.Fatalf("first failed cleanup unexpectedly removed multipart upload: %v", err)
	}
	if err := service.AbortUpload(ctx, identity, upload.ID); err != nil {
		t.Fatalf("retry abort: %v", err)
	}
	if storage.abortCalls.Load() != 2 {
		t.Fatalf("abort calls=%d, want 2", storage.abortCalls.Load())
	}
	if _, err := storage.Storage.SignUploadPart(ctx, upload.ObjectKey, upload.StorageUploadID, 1, drive.Checksum{}, time.Minute); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("multipart upload still exists after retry: code=%s err=%v", drive.CodeOf(err), err)
	}
}

func TestAbortUploadUsesAtomicTransitionResultForCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := &transitioningAbortRepository{Repository: memory.NewRepository()}
	storage := memory.NewStorage("abort-transition-test")
	service := completionService(t, repository, storage, "abort-transition-cursor-key-at-least-32-bytes")
	identity := drive.Identity{
		TenantID: "16161616-1616-4616-8616-161616161616", PrincipalID: "efefefef-efef-4fef-8fef-efefefefefef",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Abort transition test")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "transition.bin", Size: 6, MimeType: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := storage.PutPart(upload.StorageUploadID, 1, []byte("object"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.CompleteMultipart(ctx, upload.ObjectKey, upload.StorageUploadID, []drive.CompletedPart{part}); err != nil {
		t.Fatal(err)
	}
	repository.abortResult = upload
	repository.abortResult.Status = drive.UploadFailed
	repository.abortResult.FailureCode = drive.UploadFailureParentUnavailable
	if err := service.AbortUpload(ctx, identity, upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, exists := storage.Object(upload.ObjectKey); exists {
		t.Fatal("abort cleanup used the stale pre-transition state and left a completed object")
	}
}

func TestExpiredUploadIsMarkedAndMultipartIsAborted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := memory.NewRepository()
	storage := memory.NewStorage("expiry-test")
	clock := &fixedClock{now: time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)}
	cursor, err := drive.NewCursorCodec([]byte("expiry-test-cursor-key-that-is-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := drive.NewService(drive.ServiceOptions{
		Repository: repository, Storage: storage, Cursor: cursor, Clock: clock, UploadTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := drive.Identity{
		TenantID: "33333333-3333-4333-8333-333333333333", PrincipalID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Expiry test")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "expired.bin", Size: 1, MimeType: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if _, err := service.SignUploadPart(ctx, identity, upload.ID, 1, drive.Checksum{}); drive.CodeOf(err) != drive.CodeInvalidState {
		t.Fatalf("expired sign code=%s err=%v, want invalid_state", drive.CodeOf(err), err)
	}
	expired, err := service.Upload(ctx, identity, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Status != drive.UploadExpired {
		t.Fatalf("expired upload status=%s, want expired", expired.Status)
	}
	if _, err := storage.SignUploadPart(ctx, upload.ObjectKey, upload.StorageUploadID, 1, drive.Checksum{}, time.Minute); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("expired multipart upload still exists: code=%s err=%v", drive.CodeOf(err), err)
	}
}

func TestCreateUploadUsesDetachedContextForCompensatingAbort(t *testing.T) {
	t.Parallel()
	baseRepository := memory.NewRepository()
	repository := &rejectingCreateRepository{Repository: baseRepository}
	storage := &contextAwareAbortStorage{Storage: memory.NewStorage("create-compensation-test")}
	cursor, err := drive.NewCursorCodec([]byte("create-compensation-cursor-key-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := drive.NewService(drive.ServiceOptions{Repository: repository, Storage: storage, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	identity := drive.Identity{
		TenantID: "44444444-4444-4444-8444-444444444444", PrincipalID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
	}
	tenant, err := service.EnsureTenant(context.Background(), identity.TenantID, "Create compensation test")
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.CreateUpload(canceled, identity, drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "compensate.bin", Size: 1, MimeType: "application/octet-stream",
	})
	if err == nil {
		t.Fatal("create upload unexpectedly succeeded")
	}
	if !storage.aborted {
		t.Fatal("multipart upload was not compensated after repository failure")
	}
	if _, err := storage.Storage.SignUploadPart(context.Background(), storage.lastKey, storage.lastUploadID, 1, drive.Checksum{}, time.Minute); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("compensated multipart upload still exists: code=%s err=%v", drive.CodeOf(err), err)
	}
}

func TestCompleteUploadRetainsDeclaredChecksumWithoutClaimingVerification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := memory.NewRepository()
	storage := &unavailableChecksumStorage{Storage: memory.NewStorage("declared-checksum-test")}
	cursor, err := drive.NewCursorCodec([]byte("declared-checksum-cursor-key-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := drive.NewService(drive.ServiceOptions{Repository: repository, Storage: storage, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	identity := drive.Identity{
		TenantID: "55555555-5555-4555-8555-555555555555", PrincipalID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Declared checksum test")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("checksum declared by the client")
	digest := sha256.Sum256(body)
	declared := drive.Checksum{Algorithm: "sha256", Value: base64.StdEncoding.EncodeToString(digest[:])}
	upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "checksum.bin", Size: int64(len(body)),
		MimeType: "application/octet-stream", Checksum: declared,
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := storage.PutPart(upload.StorageUploadID, 1, body)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{part})
	if err != nil {
		t.Fatal(err)
	}
	_, blob, err := repository.DownloadBlob(ctx, identity, result.Node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blob.ChecksumStatus != drive.ChecksumDeclared || blob.Checksum != declared {
		t.Fatalf("blob checksum=%+v status=%s, want declared %+v", blob.Checksum, blob.ChecksumStatus, declared)
	}
}

func TestCompleteUploadPersistsVerifiedChecksum(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := memory.NewRepository()
	storage := memory.NewStorage("verified-checksum-test")
	service := completionService(t, repository, storage, "verified-checksum-cursor-key-at-least-32-bytes")
	identity := drive.Identity{
		TenantID: "77777777-7777-4777-8777-777777777777", PrincipalID: "11111111-aaaa-4aaa-8aaa-111111111111",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Verified checksum test")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("verified checksum")
	digest := sha256.Sum256(body)
	declared := drive.Checksum{Algorithm: "sha256", Value: base64.StdEncoding.EncodeToString(digest[:])}
	upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "verified.bin", Size: int64(len(body)),
		MimeType: "application/octet-stream", Checksum: declared,
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := storage.PutPart(upload.StorageUploadID, 1, body)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{part})
	if err != nil {
		t.Fatal(err)
	}
	_, blob, err := repository.DownloadBlob(ctx, identity, result.Node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blob.ChecksumStatus != drive.ChecksumVerified || blob.Checksum != declared {
		t.Fatalf("blob checksum=%+v status=%s, want verified %+v", blob.Checksum, blob.ChecksumStatus, declared)
	}
}

func TestCompleteUploadDeterministicStorageRejectionFailsAndAborts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := memory.NewRepository()
	storage := memory.NewStorage("rejected-completion-test")
	service := completionService(t, repository, storage, "rejected-completion-cursor-key-at-least-32-bytes")
	identity := drive.Identity{
		TenantID: "88888888-8888-4888-8888-888888888888", PrincipalID: "22222222-aaaa-4aaa-8aaa-222222222222",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Rejected completion test")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("bad etag")
	upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "rejected.bin", Size: int64(len(body)), MimeType: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := storage.PutPart(upload.StorageUploadID, 1, body)
	if err != nil {
		t.Fatal(err)
	}
	wrong := part
	wrong.ETag = `"wrong"`
	if _, err := service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{wrong}); drive.CodeOf(err) != drive.CodeInvalidRequest {
		t.Fatalf("completion code=%s err=%v, want invalid_request", drive.CodeOf(err), err)
	}
	failed, err := service.Upload(ctx, identity, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != drive.UploadFailed || failed.FailureCode != "storage_rejected" {
		t.Fatalf("failed session=%+v", failed)
	}
	if _, err := storage.SignUploadPart(ctx, upload.ObjectKey, upload.StorageUploadID, 1, drive.Checksum{}, time.Minute); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("rejected multipart was not aborted: code=%s err=%v", drive.CodeOf(err), err)
	}
	if _, err := service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{wrong}); drive.CodeOf(err) != drive.CodeInvalidState {
		t.Fatalf("same failed completion code=%s err=%v, want invalid_state", drive.CodeOf(err), err)
	}
	if _, err := service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{part}); drive.CodeOf(err) != drive.CodeIdempotencyConflict {
		t.Fatalf("changed failed completion code=%s err=%v, want idempotency_conflict", drive.CodeOf(err), err)
	}
	page, err := service.ListChildren(ctx, identity, tenant.RootNodeID, "", 50)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("failed completion became visible: items=%+v err=%v", page.Items, err)
	}
}

func TestCompleteUploadValidationFailureDeletesUncommittedObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tests := []struct {
		name         string
		storage      func() drive.StorageProvider
		expectedSize int64
		checksum     func([]byte) drive.Checksum
		failureCode  string
	}{
		{
			name: "size mismatch", storage: func() drive.StorageProvider { return memory.NewStorage("size-mismatch-test") },
			expectedSize: 99, checksum: func([]byte) drive.Checksum { return drive.Checksum{} }, failureCode: "size_mismatch",
		},
		{
			name: "verified checksum mismatch", storage: func() drive.StorageProvider {
				return &mismatchedVerifiedChecksumStorage{Storage: memory.NewStorage("checksum-mismatch-test")}
			},
			expectedSize: 0, checksum: func(body []byte) drive.Checksum {
				digest := sha256.Sum256(body)
				return drive.Checksum{Algorithm: "sha256", Value: base64.StdEncoding.EncodeToString(digest[:])}
			}, failureCode: "checksum_mismatch",
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := test.storage()
			var baseStorage *memory.Storage
			switch value := storage.(type) {
			case *memory.Storage:
				baseStorage = value
			case *mismatchedVerifiedChecksumStorage:
				baseStorage = value.Storage
			default:
				t.Fatalf("unexpected storage type %T", storage)
			}
			repository := memory.NewRepository()
			service := completionService(t, repository, storage, "validation-failure-cursor-key-at-least-32-bytes")
			identity := drive.Identity{
				TenantID:    fmt.Sprintf("99999999-9999-4999-8999-%012d", index+1),
				PrincipalID: fmt.Sprintf("33333333-aaaa-4aaa-8aaa-%012d", index+1),
			}
			tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Validation failure test")
			if err != nil {
				t.Fatal(err)
			}
			body := []byte("completed but invalid")
			expectedSize := test.expectedSize
			if expectedSize == 0 {
				expectedSize = int64(len(body))
			}
			upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
				ParentID: tenant.RootNodeID, Name: "invalid.bin", Size: expectedSize,
				MimeType: "application/octet-stream", Checksum: test.checksum(body),
			})
			if err != nil {
				t.Fatal(err)
			}
			part, err := baseStorage.PutPart(upload.StorageUploadID, 1, body)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{part}); drive.CodeOf(err) != drive.CodeInvalidRequest {
				t.Fatalf("completion code=%s err=%v, want invalid_request", drive.CodeOf(err), err)
			}
			failed, err := service.Upload(ctx, identity, upload.ID)
			if err != nil {
				t.Fatal(err)
			}
			if failed.Status != drive.UploadFailed || failed.FailureCode != test.failureCode {
				t.Fatalf("failed session=%+v", failed)
			}
			if _, _, exists := baseStorage.Object(upload.ObjectKey); exists {
				t.Fatal("invalid completed object was not deleted")
			}
		})
	}
}

func completionService(t *testing.T, repository drive.Repository, storage drive.StorageProvider, cursorKey string) *drive.Service {
	t.Helper()
	cursor, err := drive.NewCursorCodec([]byte(cursorKey))
	if err != nil {
		t.Fatal(err)
	}
	service, err := drive.NewService(drive.ServiceOptions{Repository: repository, Storage: storage, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestPurgeRemainsInvisibleAndRetriesObjectDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := memory.NewRepository()
	storage := &retryingDeleteStorage{Storage: memory.NewStorage("purge-retry-test")}
	storage.failFirst.Store(true)
	cursor, err := drive.NewCursorCodec([]byte("purge-retry-cursor-key-that-is-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := drive.NewService(drive.ServiceOptions{Repository: repository, Storage: storage, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	identity := drive.Identity{
		TenantID: "66666666-6666-4666-8666-666666666666", PrincipalID: "ffffffff-ffff-4fff-8fff-ffffffffffff",
	}
	tenant, err := service.EnsureTenant(ctx, identity.TenantID, "Purge retry test")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("purge me")
	upload, err := service.CreateUpload(ctx, identity, drive.CreateUploadInput{
		ParentID: tenant.RootNodeID, Name: "purge.bin", Size: int64(len(body)), MimeType: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := storage.PutPart(upload.StorageUploadID, 1, body)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.CompleteUpload(ctx, identity, upload.ID, []drive.CompletedPart{part})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Recycle(ctx, identity, completed.Node.ID, completed.Node.Revision); err != nil {
		t.Fatal(err)
	}
	if err := service.Purge(ctx, identity, completed.Node.ID, completed.Node.Revision+1); drive.CodeOf(err) != drive.CodeDependencyUnavailable {
		t.Fatalf("first purge code=%s err=%v, want dependency_unavailable", drive.CodeOf(err), err)
	}
	if _, err := service.Node(ctx, identity, completed.Node.ID, drive.NodeFile); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("purging file became visible: code=%s err=%v", drive.CodeOf(err), err)
	}
	if _, _, exists := storage.Object(upload.ObjectKey); !exists {
		t.Fatal("failed delete unexpectedly removed object")
	}
	if err := service.Purge(ctx, identity, completed.Node.ID, completed.Node.Revision+1); err != nil {
		t.Fatalf("retry purge: %v", err)
	}
	if _, _, exists := storage.Object(upload.ObjectKey); exists {
		t.Fatal("retry purge left object in storage")
	}
	if err := service.Purge(ctx, identity, completed.Node.ID, completed.Node.Revision+1); err != nil {
		t.Fatalf("idempotent purge retry: %v", err)
	}
	if storage.deleteCalls.Load() != 2 {
		t.Fatalf("delete calls=%d, want 2", storage.deleteCalls.Load())
	}
}
