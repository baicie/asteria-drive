package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

func TestRepositoryConcurrentNormalizedNameCreateHasSingleWinner(t *testing.T) {
	repository := NewRepository()
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: "tenant-concurrent", PrincipalID: "principal-concurrent"}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Concurrent Tenant", RootNodeID: "root-concurrent", Now: now,
	})
	assertNoError(t, err)

	const attempts = 32
	start := make(chan struct{})
	results := make(chan error, attempts)
	var workers sync.WaitGroup
	workers.Add(attempts)
	for index := 0; index < attempts; index++ {
		go func(index int) {
			defer workers.Done()
			<-start
			_, createErr := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
				Identity: identity, ID: fmt.Sprintf("concurrent-directory-%02d", index), ParentID: tenant.RootNodeID,
				DisplayName: fmt.Sprintf("Reports %02d", index), NormalizedName: "reports", Now: now.Add(time.Duration(index) * time.Millisecond),
			})
			results <- createErr
		}(index)
	}
	close(start)
	workers.Wait()
	close(results)

	successes, conflicts := 0, 0
	for createErr := range results {
		if createErr == nil {
			successes++
			continue
		}
		switch drive.CodeOf(createErr) {
		case drive.CodeNameConflict:
			conflicts++
		default:
			t.Fatalf("unexpected concurrent create error: code=%s err=%v", drive.CodeOf(createErr), createErr)
		}
	}
	if successes != 1 || conflicts != attempts-1 {
		t.Fatalf("concurrent create outcomes: successes=%d conflicts=%d, want 1 and %d", successes, conflicts, attempts-1)
	}
	children, more, err := repository.ListChildren(ctx, identity, tenant.RootNodeID, drive.CursorPosition{}, attempts)
	assertNoError(t, err)
	if more || len(children) != 1 || children[0].NormalizedName != "reports" {
		t.Fatalf("namespace contains duplicate normalized names: more=%v children=%+v", more, children)
	}
}

func TestRepositoryDirectorySubtreeRecycleRestoreContract(t *testing.T) {
	repository := NewRepository()
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: "tenant-subtree", PrincipalID: "principal-subtree"}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Subtree Tenant", RootNodeID: "root-subtree", Now: now,
	})
	assertNoError(t, err)
	parent, err := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
		Identity: identity, ID: "subtree-parent", ParentID: tenant.RootNodeID,
		DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
	})
	assertNoError(t, err)
	child, err := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
		Identity: identity, ID: "subtree-child", ParentID: parent.ID,
		DisplayName: "Child", NormalizedName: "child", Now: now.Add(2 * time.Minute),
	})
	assertNoError(t, err)
	file, blob := commitMemoryFile(t, repository, identity, child.ID, "subtree", now.Add(3*time.Minute))

	_, downloadedBefore, err := repository.DownloadBlob(ctx, identity, file.ID)
	assertNoError(t, err)
	if downloadedBefore.ID != blob.ID {
		t.Fatalf("download resolved blob %q, want %q", downloadedBefore.ID, blob.ID)
	}
	assertNoError(t, repository.Recycle(ctx, identity, parent.ID, parent.Revision, now.Add(4*time.Minute)))

	for _, nodeID := range []string{parent.ID, child.ID, file.ID} {
		if _, nodeErr := repository.Node(ctx, identity, nodeID, false); drive.CodeOf(nodeErr) != drive.CodeNotFound {
			t.Fatalf("recycled subtree node %q remained visible: code=%s err=%v", nodeID, drive.CodeOf(nodeErr), nodeErr)
		}
	}
	if _, _, downloadErr := repository.DownloadBlob(ctx, identity, file.ID); drive.CodeOf(downloadErr) != drive.CodeNotFound {
		t.Fatalf("recycled subtree file remained downloadable: code=%s err=%v", drive.CodeOf(downloadErr), downloadErr)
	}
	for _, nodeID := range []string{child.ID, file.ID} {
		descendant, nodeErr := repository.Node(ctx, identity, nodeID, true)
		assertNoError(t, nodeErr)
		if descendant.Status != drive.NodeActive || descendant.TrashedRootID != parent.ID {
			t.Fatalf("descendant %q was not attached to recycle root %q: %+v", nodeID, parent.ID, descendant)
		}
	}

	restored, err := repository.Restore(ctx, identity, parent.ID, parent.Revision+1, now.Add(5*time.Minute))
	assertNoError(t, err)
	if restored.Status != drive.NodeActive || restored.ParentID != tenant.RootNodeID {
		t.Fatalf("unexpected restored parent: %+v", restored)
	}
	for _, nodeID := range []string{child.ID, file.ID} {
		descendant, nodeErr := repository.Node(ctx, identity, nodeID, false)
		assertNoError(t, nodeErr)
		if descendant.Status != drive.NodeActive || descendant.TrashedRootID != "" {
			t.Fatalf("restored descendant %q is not visible: %+v", nodeID, descendant)
		}
	}
	_, downloadedAfter, err := repository.DownloadBlob(ctx, identity, file.ID)
	assertNoError(t, err)
	if downloadedAfter.ID != blob.ID {
		t.Fatalf("restored download resolved blob %q, want %q", downloadedAfter.ID, blob.ID)
	}
	assertOnlyChild(t, repository, identity, parent.ID, child.ID)
	assertOnlyChild(t, repository, identity, child.ID, file.ID)
}

func TestRepositoryRestoreChildConflictsWhileOriginalParentIsTrashed(t *testing.T) {
	repository := NewRepository()
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: "tenant-restore-conflict", PrincipalID: "principal-restore-conflict"}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Restore Tenant", RootNodeID: "root-restore-conflict", Now: now,
	})
	assertNoError(t, err)
	parent, err := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
		Identity: identity, ID: "restore-parent", ParentID: tenant.RootNodeID,
		DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
	})
	assertNoError(t, err)
	child, err := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
		Identity: identity, ID: "restore-child", ParentID: parent.ID,
		DisplayName: "Child", NormalizedName: "child", Now: now.Add(2 * time.Minute),
	})
	assertNoError(t, err)

	assertNoError(t, repository.Recycle(ctx, identity, child.ID, child.Revision, now.Add(3*time.Minute)))
	assertNoError(t, repository.Recycle(ctx, identity, parent.ID, parent.Revision, now.Add(4*time.Minute)))
	if _, restoreErr := repository.Restore(ctx, identity, child.ID, child.Revision+1, now.Add(5*time.Minute)); drive.CodeOf(restoreErr) != drive.CodeRestoreConflict {
		t.Fatalf("restore child with trashed original parent: code=%s err=%v", drive.CodeOf(restoreErr), restoreErr)
	}
	stillTrashed, err := repository.Node(ctx, identity, child.ID, true)
	assertNoError(t, err)
	if stillTrashed.Status != drive.NodeTrashed || stillTrashed.TrashedRootID != child.ID {
		t.Fatalf("failed restore changed child recycle root: %+v", stillTrashed)
	}
}

func commitMemoryFile(t *testing.T, repository *Repository, identity drive.Identity, parentID, prefix string, now time.Time) (drive.Node, drive.Blob) {
	t.Helper()
	ctx := context.Background()
	session := drive.UploadSession{
		ID: prefix + "-upload", TenantID: identity.TenantID, PrincipalID: identity.PrincipalID,
		ParentID: parentID, DisplayName: "report.bin", NormalizedName: "report.bin",
		ExpectedSize: 4, MimeType: "application/octet-stream", Bucket: "asteria",
		ObjectKey: "blobs/" + identity.TenantID + "/" + prefix, StorageUploadID: prefix + "-storage-upload",
		Status: drive.UploadCreated, PartSize: 5 * 1024 * 1024, ExpiresAt: now.Add(time.Hour),
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	_, err := repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: session})
	assertNoError(t, err)
	_, err = repository.MarkUploading(ctx, identity, session.ID, now.Add(time.Second))
	assertNoError(t, err)
	const digest = "subtree-completion-digest"
	_, err = repository.BeginComplete(ctx, identity, session.ID, digest, []drive.CompletedPart{
		{PartNumber: 1, ETag: "etag-1", Size: session.ExpectedSize},
	}, now.Add(2*time.Second))
	assertNoError(t, err)
	_, err = repository.MarkObjectCompleted(ctx, identity, session.ID, drive.ObjectInfo{}, now.Add(3*time.Second))
	assertNoError(t, err)

	blob := drive.Blob{
		ID: prefix + "-blob", TenantID: identity.TenantID, Bucket: session.Bucket, ObjectKey: session.ObjectKey,
		Size: session.ExpectedSize, MimeType: session.MimeType, ChecksumStatus: drive.ChecksumUnavailable,
		Status: drive.BlobAvailable, ReferenceCount: 1, CreatedAt: now.Add(4 * time.Second),
	}
	version := drive.FileVersion{
		ID: prefix + "-version", TenantID: identity.TenantID, NodeID: prefix + "-file", BlobID: blob.ID,
		Size: session.ExpectedSize, MimeType: session.MimeType, CreatedBy: identity.PrincipalID, CreatedAt: now.Add(4 * time.Second),
	}
	node := drive.Node{
		ID: version.NodeID, TenantID: identity.TenantID, ParentID: parentID, Kind: drive.NodeFile,
		DisplayName: session.DisplayName, NormalizedName: session.NormalizedName, CurrentVersionID: version.ID,
		Size: session.ExpectedSize, MimeType: session.MimeType, Status: drive.NodeActive, Revision: 1,
		CreatedAt: now.Add(4 * time.Second), UpdatedAt: now.Add(4 * time.Second),
	}
	result, created, err := repository.CommitUpload(ctx, drive.CommitUploadCommand{
		Identity: identity, SessionID: session.ID, Digest: digest, Blob: blob, Version: version, Node: node, Now: now.Add(4 * time.Second),
	})
	assertNoError(t, err)
	if !created {
		t.Fatal("file commit unexpectedly reused an existing result")
	}
	return result.Node, result.Blob
}

func assertOnlyChild(t *testing.T, repository *Repository, identity drive.Identity, parentID, childID string) {
	t.Helper()
	children, more, err := repository.ListChildren(context.Background(), identity, parentID, drive.CursorPosition{}, 10)
	assertNoError(t, err)
	if more || len(children) != 1 || children[0].ID != childID {
		t.Fatalf("children of %q: more=%v children=%+v, want only %q", parentID, more, children, childID)
	}
}
