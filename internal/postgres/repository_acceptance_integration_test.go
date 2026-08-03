package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

func TestRepositoryConcurrentNormalizedNameCreateHasSingleWinner(t *testing.T) {
	repository := integrationRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(1001), PrincipalID: testID(1003)}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Concurrent Tenant", RootNodeID: testID(1002), Now: now,
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
				Identity: identity, ID: testID(1010 + index), ParentID: tenant.RootNodeID,
				DisplayName: "Reports", NormalizedName: "reports", Now: now.Add(time.Duration(index) * time.Millisecond),
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
	repository := integrationRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(1101), PrincipalID: testID(1103)}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Subtree Tenant", RootNodeID: testID(1102), Now: now,
	})
	assertNoError(t, err)
	parent := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: identity, ID: testID(1110), ParentID: tenant.RootNodeID,
		DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
	})
	child := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: identity, ID: testID(1111), ParentID: parent.ID,
		DisplayName: "Child", NormalizedName: "child", Now: now.Add(2 * time.Minute),
	})
	session := newUpload(testID(1120), identity, child.ID, "report.bin", "report.bin", now.Add(3*time.Minute))
	_, err = repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: session})
	assertNoError(t, err)
	const digest = "subtree-completion-digest"
	advanceToObjectCompleted(t, repository, identity, session.ID, digest, now.Add(3*time.Minute))
	result, created, err := repository.CommitUpload(ctx, commitCommand(
		session, identity, testID(1121), testID(1122), testID(1123), digest, now.Add(4*time.Minute),
	))
	assertNoError(t, err)
	if !created {
		t.Fatal("file commit unexpectedly reused an existing result")
	}

	_, downloadedBefore, err := repository.DownloadBlob(ctx, identity, result.Node.ID)
	assertNoError(t, err)
	if downloadedBefore.ID != result.Blob.ID {
		t.Fatalf("download resolved blob %q, want %q", downloadedBefore.ID, result.Blob.ID)
	}
	assertNoError(t, repository.Recycle(ctx, identity, parent.ID, parent.Revision, now.Add(5*time.Minute)))

	for _, nodeID := range []string{parent.ID, child.ID, result.Node.ID} {
		if _, nodeErr := repository.Node(ctx, identity, nodeID, false); drive.CodeOf(nodeErr) != drive.CodeNotFound {
			t.Fatalf("recycled subtree node %q remained visible: code=%s err=%v", nodeID, drive.CodeOf(nodeErr), nodeErr)
		}
	}
	if _, _, downloadErr := repository.DownloadBlob(ctx, identity, result.Node.ID); drive.CodeOf(downloadErr) != drive.CodeNotFound {
		t.Fatalf("recycled subtree file remained downloadable: code=%s err=%v", drive.CodeOf(downloadErr), downloadErr)
	}
	for _, nodeID := range []string{child.ID, result.Node.ID} {
		descendant, nodeErr := repository.Node(ctx, identity, nodeID, true)
		assertNoError(t, nodeErr)
		if descendant.Status != drive.NodeActive || descendant.TrashedRootID != parent.ID {
			t.Fatalf("descendant %q was not attached to recycle root %q: %+v", nodeID, parent.ID, descendant)
		}
	}

	restored, err := repository.Restore(ctx, identity, parent.ID, parent.Revision+1, now.Add(6*time.Minute))
	assertNoError(t, err)
	if restored.Status != drive.NodeActive || restored.ParentID != tenant.RootNodeID {
		t.Fatalf("unexpected restored parent: %+v", restored)
	}
	for _, nodeID := range []string{child.ID, result.Node.ID} {
		descendant, nodeErr := repository.Node(ctx, identity, nodeID, false)
		assertNoError(t, nodeErr)
		if descendant.Status != drive.NodeActive || descendant.TrashedRootID != "" {
			t.Fatalf("restored descendant %q is not visible: %+v", nodeID, descendant)
		}
	}
	_, downloadedAfter, err := repository.DownloadBlob(ctx, identity, result.Node.ID)
	assertNoError(t, err)
	if downloadedAfter.ID != result.Blob.ID {
		t.Fatalf("restored download resolved blob %q, want %q", downloadedAfter.ID, result.Blob.ID)
	}
	assertOnlyPostgresChild(t, repository, identity, parent.ID, child.ID)
	assertOnlyPostgresChild(t, repository, identity, child.ID, result.Node.ID)
}

func TestRepositoryRestoreChildConflictsWhileOriginalParentIsTrashed(t *testing.T) {
	repository := integrationRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(1201), PrincipalID: testID(1203)}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Restore Tenant", RootNodeID: testID(1202), Now: now,
	})
	assertNoError(t, err)
	parent := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: identity, ID: testID(1210), ParentID: tenant.RootNodeID,
		DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
	})
	child := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: identity, ID: testID(1211), ParentID: parent.ID,
		DisplayName: "Child", NormalizedName: "child", Now: now.Add(2 * time.Minute),
	})

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

func assertOnlyPostgresChild(t *testing.T, repository *Repository, identity drive.Identity, parentID, childID string) {
	t.Helper()
	children, more, err := repository.ListChildren(context.Background(), identity, parentID, drive.CursorPosition{}, 10)
	assertNoError(t, err)
	if more || len(children) != 1 || children[0].ID != childID {
		t.Fatalf("children of %q: more=%v children=%+v, want only %q", parentID, more, children, childID)
	}
}
