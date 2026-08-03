package memory

import (
	"context"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

func TestRepositoryFailUploadCompletionContract(t *testing.T) {
	repository := NewRepository()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: "tenant-a", PrincipalID: "principal-a"}
	other := drive.Identity{TenantID: "tenant-b", PrincipalID: "principal-b"}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Tenant A", RootNodeID: "root", Now: now,
	})
	assertNoError(t, err)

	failingSession := memoryUpload("upload-failing", identity, tenant.RootNodeID, "failing.bin", now)
	_, err = repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: failingSession})
	assertNoError(t, err)
	const failingDigest = "digest-failing"
	completing := advanceMemoryUpload(t, repository, identity, failingSession.ID, failingDigest, now.Add(time.Minute))

	_, err = repository.FailUploadCompletion(ctx, other, failingSession.ID, failingDigest, "object_size_mismatch", now.Add(2*time.Minute))
	assertErrorCode(t, err, drive.CodeNotFound)
	unchanged, err := repository.Upload(ctx, identity, failingSession.ID)
	assertNoError(t, err)
	if unchanged.Status != drive.UploadCompleting || unchanged.Revision != completing.Revision {
		t.Fatalf("cross-tenant failure changed upload: %+v", unchanged)
	}

	failedAt := now.Add(3 * time.Minute)
	failed, err := repository.FailUploadCompletion(ctx, identity, failingSession.ID, failingDigest, "object_size_mismatch", failedAt)
	assertNoError(t, err)
	if failed.Status != drive.UploadFailed || failed.FailureCode != "object_size_mismatch" || failed.CompletionDigest != failingDigest {
		t.Fatalf("unexpected failed upload: %+v", failed)
	}
	if failed.Revision != completing.Revision+1 || !failed.UpdatedAt.Equal(failedAt) {
		t.Fatalf("failed upload metadata was not advanced once: completing=%+v failed=%+v", completing, failed)
	}
	persisted, err := repository.Upload(ctx, identity, failingSession.ID)
	assertNoError(t, err)
	if persisted.Status != drive.UploadFailed || persisted.FailureCode != failed.FailureCode {
		t.Fatalf("failure state was not persisted: %+v", persisted)
	}

	retry, err := repository.FailUploadCompletion(ctx, identity, failingSession.ID, failingDigest, "ignored_retry_code", now.Add(4*time.Minute))
	assertNoError(t, err)
	if retry.Status != drive.UploadFailed || retry.FailureCode != failed.FailureCode || retry.Revision != failed.Revision || !retry.UpdatedAt.Equal(failed.UpdatedAt) {
		t.Fatalf("same-digest retry was not idempotent: first=%+v retry=%+v", failed, retry)
	}
	_, err = repository.FailUploadCompletion(ctx, identity, failingSession.ID, "different-digest", "object_size_mismatch", now.Add(5*time.Minute))
	assertErrorCode(t, err, drive.CodeIdempotencyConflict)

	committedSession := memoryUpload("upload-committed", identity, tenant.RootNodeID, "committed.bin", now)
	_, err = repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: committedSession})
	assertNoError(t, err)
	const committedDigest = "digest-committed"
	advanceMemoryUpload(t, repository, identity, committedSession.ID, committedDigest, now.Add(6*time.Minute))
	objectCompleted, err := repository.MarkObjectCompleted(ctx, identity, committedSession.ID, drive.ObjectInfo{}, now.Add(7*time.Minute))
	assertNoError(t, err)
	_, err = repository.FailUploadCompletion(ctx, identity, committedSession.ID, committedDigest, "object_size_mismatch", now.Add(8*time.Minute))
	assertErrorCode(t, err, drive.CodeInvalidState)
	afterRejectedFailure, err := repository.Upload(ctx, identity, committedSession.ID)
	assertNoError(t, err)
	if afterRejectedFailure.Status != drive.UploadObjectCompleted || afterRejectedFailure.Revision != objectCompleted.Revision {
		t.Fatalf("failed transition changed object-completed upload: %+v", afterRejectedFailure)
	}

	command := memoryCommitCommand(committedSession, identity, "blob", "version", "node", committedDigest, now.Add(9*time.Minute))
	result, created, err := repository.CommitUpload(ctx, command)
	assertNoError(t, err)
	if !created || result.Upload.Status != drive.UploadCommitted {
		t.Fatalf("unexpected committed upload: created=%v result=%+v", created, result)
	}
	_, err = repository.FailUploadCompletion(ctx, identity, committedSession.ID, committedDigest, "object_size_mismatch", now.Add(10*time.Minute))
	assertErrorCode(t, err, drive.CodeInvalidState)
	afterCommittedFailure, err := repository.Upload(ctx, identity, committedSession.ID)
	assertNoError(t, err)
	if afterCommittedFailure.Status != drive.UploadCommitted || afterCommittedFailure.Revision != result.Upload.Revision {
		t.Fatalf("failed transition changed committed upload: %+v", afterCommittedFailure)
	}
}

func TestRepositoryNestedRecycleRootsRemainIndependent(t *testing.T) {
	repository := NewRepository()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: "tenant-a", PrincipalID: "principal-a"}

	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Tenant A", RootNodeID: "root", Now: now,
	})
	assertNoError(t, err)
	parent, err := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
		Identity: identity, ID: "parent", ParentID: tenant.RootNodeID,
		DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
	})
	assertNoError(t, err)
	child, err := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
		Identity: identity, ID: "child", ParentID: parent.ID,
		DisplayName: "Child", NormalizedName: "child", Now: now.Add(2 * time.Minute),
	})
	assertNoError(t, err)

	assertNoError(t, repository.Recycle(ctx, identity, child.ID, child.Revision, now.Add(3*time.Minute)))
	assertNoError(t, repository.Recycle(ctx, identity, parent.ID, parent.Revision, now.Add(4*time.Minute)))

	entries := listRecycle(t, repository, identity)
	assertRecycleRoots(t, entries, parent.ID, child.ID)

	restoredParent, err := repository.Restore(ctx, identity, parent.ID, parent.Revision+1, now.Add(5*time.Minute))
	assertNoError(t, err)
	if restoredParent.Status != drive.NodeActive || restoredParent.ParentID != tenant.RootNodeID || restoredParent.TrashedRootID != "" {
		t.Fatalf("unexpected restored parent: %+v", restoredParent)
	}
	trashedChild, err := repository.Node(ctx, identity, child.ID, true)
	assertNoError(t, err)
	if trashedChild.Status != drive.NodeTrashed || trashedChild.TrashedRootID != child.ID || trashedChild.OriginalParentID != parent.ID {
		t.Fatalf("child did not remain an independent recycle root: %+v", trashedChild)
	}
	if _, err := repository.Node(ctx, identity, child.ID, false); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("trashed child was visible after restoring parent: code=%s err=%v", drive.CodeOf(err), err)
	}
	entries = listRecycle(t, repository, identity)
	assertRecycleRoots(t, entries, child.ID)

	restoredChild, err := repository.Restore(ctx, identity, child.ID, child.Revision+1, now.Add(6*time.Minute))
	assertNoError(t, err)
	if restoredChild.Status != drive.NodeActive || restoredChild.ParentID != parent.ID || restoredChild.TrashedRootID != "" || restoredChild.OriginalParentID != "" {
		t.Fatalf("unexpected restored child: %+v", restoredChild)
	}
	children, more, err := repository.ListChildren(ctx, identity, parent.ID, drive.CursorPosition{}, 10)
	assertNoError(t, err)
	if more || len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("restored parent-child relationship is incorrect: more=%v children=%+v", more, children)
	}
	if entries := listRecycle(t, repository, identity); len(entries) != 0 {
		t.Fatalf("recycle bin was not empty after both restores: %+v", entries)
	}
}

func listRecycle(t *testing.T, repository *Repository, identity drive.Identity) []drive.RecycleEntry {
	t.Helper()
	entries, more, err := repository.ListRecycle(context.Background(), identity, drive.CursorPosition{}, 10)
	assertNoError(t, err)
	if more {
		t.Fatal("unexpected recycle pagination")
	}
	return entries
}

func assertRecycleRoots(t *testing.T, entries []drive.RecycleEntry, expected ...string) {
	t.Helper()
	if len(entries) != len(expected) {
		t.Fatalf("recycle roots=%d, want %d: %+v", len(entries), len(expected), entries)
	}
	roots := make(map[string]drive.Node, len(entries))
	for _, entry := range entries {
		roots[entry.Node.ID] = entry.Node
	}
	for _, id := range expected {
		node, ok := roots[id]
		if !ok || node.Status != drive.NodeTrashed || node.TrashedRootID != id {
			t.Fatalf("missing independent recycle root %q: %+v", id, entries)
		}
	}
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func memoryUpload(id string, identity drive.Identity, parentID, displayName string, now time.Time) drive.UploadSession {
	return drive.UploadSession{
		ID: id, TenantID: identity.TenantID, PrincipalID: identity.PrincipalID,
		ParentID: parentID, DisplayName: displayName, NormalizedName: displayName,
		ExpectedSize: 4, MimeType: "application/octet-stream", Bucket: "asteria",
		ObjectKey: "blobs/" + identity.TenantID + "/" + id, StorageUploadID: "storage-" + id,
		Status: drive.UploadCreated, PartSize: 5 * 1024 * 1024, ExpiresAt: now.Add(time.Hour),
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func advanceMemoryUpload(t *testing.T, repository *Repository, identity drive.Identity, sessionID, digest string, now time.Time) drive.UploadSession {
	t.Helper()
	_, err := repository.MarkUploading(context.Background(), identity, sessionID, now)
	assertNoError(t, err)
	session, err := repository.BeginComplete(context.Background(), identity, sessionID, digest, []drive.CompletedPart{
		{PartNumber: 1, ETag: "etag-1", Size: 4},
	}, now.Add(time.Second))
	assertNoError(t, err)
	return session
}

func memoryCommitCommand(session drive.UploadSession, identity drive.Identity, blobID, versionID, nodeID, digest string, now time.Time) drive.CommitUploadCommand {
	return drive.CommitUploadCommand{
		Identity: identity, SessionID: session.ID, Digest: digest, Now: now,
		Blob: drive.Blob{
			ID: blobID, TenantID: identity.TenantID, Bucket: session.Bucket, ObjectKey: session.ObjectKey,
			Size: session.ExpectedSize, MimeType: session.MimeType, ChecksumStatus: drive.ChecksumUnavailable,
			Status: drive.BlobAvailable, ReferenceCount: 1, CreatedAt: now,
		},
		Version: drive.FileVersion{
			ID: versionID, TenantID: identity.TenantID, NodeID: nodeID, BlobID: blobID,
			Size: session.ExpectedSize, MimeType: session.MimeType,
			CreatedBy: identity.PrincipalID, CreatedAt: now,
		},
		Node: drive.Node{
			ID: nodeID, TenantID: identity.TenantID, ParentID: session.ParentID, Kind: drive.NodeFile,
			DisplayName: session.DisplayName, NormalizedName: session.NormalizedName,
			CurrentVersionID: versionID, Size: session.ExpectedSize, MimeType: session.MimeType,
			Status: drive.NodeActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
		},
	}
}

func assertErrorCode(t *testing.T, err error, want drive.ErrorCode) {
	t.Helper()
	if got := drive.CodeOf(err); got != want {
		t.Fatalf("error code = %s, want %s (err=%v)", got, want, err)
	}
}
