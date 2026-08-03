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

func TestRepositoryConcurrentMutualDirectoryMovesPreserveAcyclicTree(t *testing.T) {
	repository := integrationRepository(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(1301), PrincipalID: testID(1303)}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Move Tenant", RootNodeID: testID(1302), Now: now,
	})
	assertNoError(t, err)
	left := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: identity, ID: testID(1310), ParentID: tenant.RootNodeID,
		DisplayName: "Left", NormalizedName: "left", Now: now.Add(time.Minute),
	})
	right := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: identity, ID: testID(1311), ParentID: tenant.RootNodeID,
		DisplayName: "Right", NormalizedName: "right", Now: now.Add(2 * time.Minute),
	})

	results := runConcurrentRepositoryActions(
		func() error {
			parentID := right.ID
			_, updateErr := repository.UpdateNode(ctx, drive.UpdateNodeCommand{
				Identity: identity, NodeID: left.ID, ParentID: &parentID,
				ExpectedRevision: left.Revision, Now: now.Add(3 * time.Minute),
			})
			return updateErr
		},
		func() error {
			parentID := left.ID
			_, updateErr := repository.UpdateNode(ctx, drive.UpdateNodeCommand{
				Identity: identity, NodeID: right.ID, ParentID: &parentID,
				ExpectedRevision: right.Revision, Now: now.Add(3 * time.Minute),
			})
			return updateErr
		},
	)
	successes, rejected := 0, 0
	for _, updateErr := range results {
		if updateErr == nil {
			successes++
			continue
		}
		if drive.CodeOf(updateErr) == drive.CodeInvalidRequest {
			rejected++
			continue
		}
		t.Fatalf("unexpected concurrent move error: code=%s err=%v", drive.CodeOf(updateErr), updateErr)
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("mutual move outcomes: successes=%d rejected=%d, want 1 and 1", successes, rejected)
	}

	var ancestorCount int
	err = repository.pool.QueryRow(ctx, `
		WITH RECURSIVE ancestors(id,parent_id) AS (
			SELECT id,parent_id FROM file_node
			WHERE tenant_id=$1 AND id IN ($2,$3)
			UNION
			SELECT parent.id,parent.parent_id
			FROM file_node parent JOIN ancestors child ON parent.id=child.parent_id
			WHERE parent.tenant_id=$1
		)
		SELECT count(*) FROM ancestors`, identity.TenantID, left.ID, right.ID).Scan(&ancestorCount)
	assertNoError(t, err)
	if ancestorCount != 3 {
		t.Fatalf("mutual move left a disconnected cycle: recursive ancestor count=%d, want 3", ancestorCount)
	}
}

func TestRepositoryConcurrentParentRecycleDoesNotCreateActiveOrphans(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		repository := integrationRepository(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
		identity := drive.Identity{TenantID: testID(1401), PrincipalID: testID(1403)}
		tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
			TenantID: identity.TenantID, DisplayName: "Create Race Tenant", RootNodeID: testID(1402), Now: now,
		})
		assertNoError(t, err)
		parent := createDirectory(t, repository, drive.CreateDirectoryCommand{
			Identity: identity, ID: testID(1410), ParentID: tenant.RootNodeID,
			DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
		})

		results := runConcurrentRepositoryActions(
			func() error {
				return repository.Recycle(ctx, identity, parent.ID, parent.Revision, now.Add(2*time.Minute))
			},
			func() error {
				_, createErr := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
					Identity: identity, ID: testID(1411), ParentID: parent.ID,
					DisplayName: "Child", NormalizedName: "child", Now: now.Add(2 * time.Minute),
				})
				return createErr
			},
		)
		assertNoError(t, results[0])
		assertNilOrCode(t, results[1], drive.CodeNotFound)
		assertNoActivePostgresOrphans(t, repository, identity)
	})

	t.Run("move", func(t *testing.T) {
		repository := integrationRepository(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		now := time.Date(2026, 8, 3, 12, 30, 0, 0, time.UTC)
		identity := drive.Identity{TenantID: testID(1501), PrincipalID: testID(1503)}
		tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
			TenantID: identity.TenantID, DisplayName: "Move Race Tenant", RootNodeID: testID(1502), Now: now,
		})
		assertNoError(t, err)
		parent := createDirectory(t, repository, drive.CreateDirectoryCommand{
			Identity: identity, ID: testID(1510), ParentID: tenant.RootNodeID,
			DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
		})
		moving := createDirectory(t, repository, drive.CreateDirectoryCommand{
			Identity: identity, ID: testID(1511), ParentID: tenant.RootNodeID,
			DisplayName: "Moving", NormalizedName: "moving", Now: now.Add(time.Minute),
		})

		results := runConcurrentRepositoryActions(
			func() error {
				return repository.Recycle(ctx, identity, parent.ID, parent.Revision, now.Add(2*time.Minute))
			},
			func() error {
				parentID := parent.ID
				_, updateErr := repository.UpdateNode(ctx, drive.UpdateNodeCommand{
					Identity: identity, NodeID: moving.ID, ParentID: &parentID,
					ExpectedRevision: moving.Revision, Now: now.Add(2 * time.Minute),
				})
				return updateErr
			},
		)
		assertNoError(t, results[0])
		assertNilOrCode(t, results[1], drive.CodeNotFound)
		assertNoActivePostgresOrphans(t, repository, identity)
	})

	t.Run("upload commit", func(t *testing.T) {
		repository := integrationRepository(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		now := time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC)
		identity := drive.Identity{TenantID: testID(1601), PrincipalID: testID(1603)}
		tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
			TenantID: identity.TenantID, DisplayName: "Commit Race Tenant", RootNodeID: testID(1602), Now: now,
		})
		assertNoError(t, err)
		parent := createDirectory(t, repository, drive.CreateDirectoryCommand{
			Identity: identity, ID: testID(1610), ParentID: tenant.RootNodeID,
			DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
		})
		session := newUpload(testID(1620), identity, parent.ID, "report.bin", "report.bin", now.Add(time.Minute))
		_, err = repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: session})
		assertNoError(t, err)
		const digest = "concurrent-parent-recycle"
		advanceToObjectCompleted(t, repository, identity, session.ID, digest, now.Add(2*time.Minute))
		command := commitCommand(session, identity, testID(1621), testID(1622), testID(1623), digest, now.Add(3*time.Minute))

		results := runConcurrentRepositoryActions(
			func() error {
				return repository.Recycle(ctx, identity, parent.ID, parent.Revision, now.Add(3*time.Minute))
			},
			func() error {
				_, _, commitErr := repository.CommitUpload(ctx, command)
				return commitErr
			},
		)
		assertNoError(t, results[0])
		assertNilOrCode(t, results[1], drive.CodeNotFound)
		assertNoActivePostgresOrphans(t, repository, identity)
		if results[1] != nil {
			failed, uploadErr := repository.Upload(ctx, identity, session.ID)
			assertNoError(t, uploadErr)
			if failed.Status != drive.UploadFailed || failed.FailureCode != drive.UploadFailureParentUnavailable {
				t.Fatalf("rejected upload commit was not durably failed: %+v", failed)
			}
		}
	})

	t.Run("restore", func(t *testing.T) {
		repository := integrationRepository(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		now := time.Date(2026, 8, 3, 13, 30, 0, 0, time.UTC)
		identity := drive.Identity{TenantID: testID(1701), PrincipalID: testID(1703)}
		tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
			TenantID: identity.TenantID, DisplayName: "Restore Race Tenant", RootNodeID: testID(1702), Now: now,
		})
		assertNoError(t, err)
		parent := createDirectory(t, repository, drive.CreateDirectoryCommand{
			Identity: identity, ID: testID(1710), ParentID: tenant.RootNodeID,
			DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
		})
		child := createDirectory(t, repository, drive.CreateDirectoryCommand{
			Identity: identity, ID: testID(1711), ParentID: parent.ID,
			DisplayName: "Child", NormalizedName: "child", Now: now.Add(time.Minute),
		})
		assertNoError(t, repository.Recycle(ctx, identity, child.ID, child.Revision, now.Add(2*time.Minute)))

		results := runConcurrentRepositoryActions(
			func() error {
				return repository.Recycle(ctx, identity, parent.ID, parent.Revision, now.Add(3*time.Minute))
			},
			func() error {
				_, restoreErr := repository.Restore(ctx, identity, child.ID, child.Revision+1, now.Add(3*time.Minute))
				return restoreErr
			},
		)
		assertNoError(t, results[0])
		assertNilOrCode(t, results[1], drive.CodeRestoreConflict)
		assertNoActivePostgresOrphans(t, repository, identity)
	})
}

func TestRepositoryConcurrentNestedRecycleRootsRemainIndependent(t *testing.T) {
	repository := integrationRepository(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(1801), PrincipalID: testID(1803)}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Recycle Race Tenant", RootNodeID: testID(1802), Now: now,
	})
	assertNoError(t, err)
	parent := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: identity, ID: testID(1810), ParentID: tenant.RootNodeID,
		DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
	})
	child := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: identity, ID: testID(1811), ParentID: parent.ID,
		DisplayName: "Child", NormalizedName: "child", Now: now.Add(2 * time.Minute),
	})

	results := runConcurrentRepositoryActions(
		func() error {
			return repository.Recycle(ctx, identity, child.ID, child.Revision, now.Add(3*time.Minute))
		},
		func() error {
			return repository.Recycle(ctx, identity, parent.ID, parent.Revision, now.Add(3*time.Minute))
		},
	)
	assertNoError(t, results[1])
	if results[0] == nil {
		assertRecycleRoots(t, listRecycle(t, repository, identity), parent.ID, child.ID)
		return
	}
	assertCode(t, results[0], drive.CodeNotFound)
	assertRecycleRoots(t, listRecycle(t, repository, identity), parent.ID)
	attachedChild, err := repository.Node(ctx, identity, child.ID, true)
	assertNoError(t, err)
	if attachedChild.Status != drive.NodeActive || attachedChild.TrashedRootID != parent.ID {
		t.Fatalf("parent-first recycle did not retain child in the parent recycle tree: %+v", attachedChild)
	}
}

func TestRepositorySequentialPurgeSharedBlobPlansLastEffectiveReference(t *testing.T) {
	repository := integrationRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 14, 30, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(1901), PrincipalID: testID(1903)}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Shared Blob Tenant", RootNodeID: testID(1902), Now: now,
	})
	assertNoError(t, err)
	session := newUpload(testID(1910), identity, tenant.RootNodeID, "source.bin", "source.bin", now.Add(time.Minute))
	_, err = repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: session})
	assertNoError(t, err)
	const digest = "shared-blob-completion"
	advanceToObjectCompleted(t, repository, identity, session.ID, digest, now.Add(2*time.Minute))
	command := commitCommand(session, identity, testID(1911), testID(1912), testID(1913), digest, now.Add(3*time.Minute))
	result, created, err := repository.CommitUpload(ctx, command)
	assertNoError(t, err)
	if !created {
		t.Fatal("shared Blob fixture unexpectedly reused an existing commit")
	}
	sharedVersionID, sharedNodeID := testID(1914), testID(1915)
	addSharedBlobReference(t, repository, command, sharedVersionID, sharedNodeID, now.Add(4*time.Minute))

	assertNoError(t, repository.Recycle(ctx, identity, result.Node.ID, result.Node.Revision, now.Add(5*time.Minute)))
	assertNoError(t, repository.Recycle(ctx, identity, sharedNodeID, 1, now.Add(5*time.Minute)))
	firstPlan, err := repository.PreparePurge(ctx, identity, result.Node.ID, result.Node.Revision+1, now.Add(6*time.Minute))
	assertNoError(t, err)
	if len(firstPlan.Blobs) != 0 {
		t.Fatalf("first purged owner scheduled a still-shared Blob: %+v", firstPlan.Blobs)
	}
	assertNoError(t, repository.FinishPurge(ctx, identity, firstPlan, now.Add(7*time.Minute)))

	lastPlan, err := repository.PreparePurge(ctx, identity, sharedNodeID, 2, now.Add(8*time.Minute))
	assertNoError(t, err)
	if len(lastPlan.Blobs) != 1 || lastPlan.Blobs[0].ID != result.Blob.ID || lastPlan.Blobs[0].Status != drive.BlobPendingDelete {
		t.Fatalf("last effective Blob reference did not schedule deletion: %+v", lastPlan.Blobs)
	}
	var status string
	var referenceCount int64
	assertNoError(t, repository.pool.QueryRow(ctx, `
		SELECT status,reference_count FROM blob WHERE tenant_id=$1 AND id=$2`,
		identity.TenantID, result.Blob.ID).Scan(&status, &referenceCount))
	if status != string(drive.BlobPendingDelete) || referenceCount != 0 {
		t.Fatalf("scheduled shared Blob status=%q reference_count=%d", status, referenceCount)
	}
	assertNoError(t, repository.FinishPurge(ctx, identity, lastPlan, now.Add(9*time.Minute)))
	assertNoError(t, repository.pool.QueryRow(ctx, `
		SELECT status FROM blob WHERE tenant_id=$1 AND id=$2`,
		identity.TenantID, result.Blob.ID).Scan(&status))
	if status != string(drive.BlobDeleted) {
		t.Fatalf("finished shared Blob status=%q, want %q", status, drive.BlobDeleted)
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

func runConcurrentRepositoryActions(actions ...func() error) []error {
	ready := make(chan struct{}, len(actions))
	start := make(chan struct{})
	type actionResult struct {
		index int
		err   error
	}
	results := make(chan actionResult, len(actions))
	var workers sync.WaitGroup
	workers.Add(len(actions))
	for index, action := range actions {
		go func() {
			defer workers.Done()
			ready <- struct{}{}
			<-start
			results <- actionResult{index: index, err: action()}
		}()
	}
	for range actions {
		<-ready
	}
	close(start)
	workers.Wait()
	close(results)

	errors := make([]error, len(actions))
	for result := range results {
		errors[result.index] = result.err
	}
	return errors
}

func assertNilOrCode(t *testing.T, err error, allowed drive.ErrorCode) {
	t.Helper()
	if err != nil && drive.CodeOf(err) != allowed {
		t.Fatalf("error code = %s, want nil or %s (err=%v)", drive.CodeOf(err), allowed, err)
	}
}

func assertNoActivePostgresOrphans(t *testing.T, repository *Repository, identity drive.Identity) {
	t.Helper()
	var orphans int
	err := repository.pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM file_node child
		LEFT JOIN file_node parent
		  ON parent.tenant_id=child.tenant_id AND parent.id=child.parent_id
		WHERE child.tenant_id=$1 AND child.parent_id IS NOT NULL
		  AND child.status='active' AND child.trashed_root_id IS NULL
		  AND (parent.id IS NULL OR parent.status<>'active' OR parent.trashed_root_id IS NOT NULL)`,
		identity.TenantID).Scan(&orphans)
	assertNoError(t, err)
	if orphans != 0 {
		t.Fatalf("namespace contains %d active orphan nodes", orphans)
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
