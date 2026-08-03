package postgres

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5"
)

const integrationDatabaseEnv = "ASTERIA_TEST_DATABASE_URL"

func TestMigrateIsIdempotent(t *testing.T) {
	connection := isolatedDatabaseURL(t)
	ctx := context.Background()
	if err := Migrate(ctx, connection); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := Migrate(ctx, connection); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	conn, err := pgx.Connect(ctx, connection)
	if err != nil {
		t.Fatalf("connect to migrated schema: %v", err)
	}
	defer conn.Close(ctx)
	var migrations, tables int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM asteria_schema_migration`).Scan(&migrations); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema=current_schema()
		  AND table_name IN ('tenant','file_node','blob','file_version','upload_session','upload_part')`,
	).Scan(&tables); err != nil {
		t.Fatalf("count MVP tables: %v", err)
	}
	if migrations != 1 || tables != 6 {
		t.Fatalf("unexpected migration result: migrations=%d tables=%d", migrations, tables)
	}
}

func TestRepositoryNamespaceAndUploadContract(t *testing.T) {
	repository := integrationRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	tenantA := drive.Identity{TenantID: testID(1), PrincipalID: testID(3)}
	tenantB := drive.Identity{TenantID: testID(4), PrincipalID: testID(6)}

	seedA := drive.TenantSeed{TenantID: tenantA.TenantID, DisplayName: "Tenant A", RootNodeID: testID(2), Now: now}
	seedB := drive.TenantSeed{TenantID: tenantB.TenantID, DisplayName: "Tenant B", RootNodeID: testID(5), Now: now}
	first, err := repository.EnsureTenant(ctx, seedA)
	assertNoError(t, err)
	second, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: tenantA.TenantID, DisplayName: "ignored", RootNodeID: testID(99), Now: now.Add(time.Minute),
	})
	assertNoError(t, err)
	if first.RootNodeID != seedA.RootNodeID || second.RootNodeID != first.RootNodeID {
		t.Fatalf("tenant initialization was not idempotent: first=%+v second=%+v", first, second)
	}
	_, err = repository.EnsureTenant(ctx, seedB)
	assertNoError(t, err)

	projects := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: tenantA, ID: testID(10), ParentID: first.RootNodeID,
		DisplayName: "Projects", NormalizedName: "projects", Now: now.Add(time.Minute),
	})
	_, err = repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
		Identity: tenantA, ID: testID(11), ParentID: first.RootNodeID,
		DisplayName: "PROJECTS", NormalizedName: "projects", Now: now.Add(2 * time.Minute),
	})
	assertCode(t, err, drive.CodeNameConflict)
	_, err = repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
		Identity: tenantB, ID: testID(12), ParentID: projects.ID,
		DisplayName: "foreign", NormalizedName: "foreign", Now: now.Add(2 * time.Minute),
	})
	assertCode(t, err, drive.CodeNotFound)

	child := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: tenantA, ID: testID(13), ParentID: projects.ID,
		DisplayName: "Child", NormalizedName: "child", Now: now.Add(3 * time.Minute),
	})
	childID := child.ID
	_, err = repository.UpdateNode(ctx, drive.UpdateNodeCommand{
		Identity: tenantA, NodeID: projects.ID, ParentID: &childID,
		ExpectedRevision: projects.Revision, Now: now.Add(4 * time.Minute),
	})
	assertCode(t, err, drive.CodeInvalidRequest)
	_, err = repository.UpdateNode(ctx, drive.UpdateNodeCommand{
		Identity: tenantB, NodeID: projects.ID, ExpectedRevision: projects.Revision,
		Now: now.Add(4 * time.Minute),
	})
	assertCode(t, err, drive.CodeNotFound)

	session := newUpload(testID(20), tenantA, projects.ID, "report.bin", "report.bin", now.Add(5*time.Minute))
	created, err := repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: session})
	assertNoError(t, err)
	if created.Status != drive.UploadCreated || created.Revision != 1 {
		t.Fatalf("unexpected created upload: %+v", created)
	}

	_, err = repository.MarkUploading(ctx, tenantB, session.ID, now.Add(6*time.Minute))
	assertCode(t, err, drive.CodeNotFound)
	unchanged, err := repository.Upload(ctx, tenantA, session.ID)
	assertNoError(t, err)
	if unchanged.Status != drive.UploadCreated || unchanged.Revision != 1 {
		t.Fatalf("cross-tenant transition changed upload: %+v", unchanged)
	}
	_, err = repository.MarkObjectCompleted(ctx, tenantB, session.ID, drive.ObjectInfo{}, now.Add(6*time.Minute))
	assertCode(t, err, drive.CodeNotFound)
	_, err = repository.MarkObjectCompleted(ctx, tenantA, session.ID, drive.ObjectInfo{}, now.Add(6*time.Minute))
	assertCode(t, err, drive.CodeInvalidState)

	completing := advanceUpload(t, repository, tenantA, session.ID, "digest-report", now.Add(7*time.Minute))
	_, err = repository.AbortUpload(ctx, tenantA, session.ID, drive.UploadAborted, now.Add(8*time.Minute))
	assertCode(t, err, drive.CodeInvalidState)
	_, err = repository.MarkObjectCompleted(ctx, tenantB, session.ID, drive.ObjectInfo{}, now.Add(8*time.Minute))
	assertCode(t, err, drive.CodeNotFound)
	objectCompleted, err := repository.MarkObjectCompleted(ctx, tenantA, session.ID, drive.ObjectInfo{}, now.Add(8*time.Minute))
	assertNoError(t, err)
	if completing.Status != drive.UploadCompleting || objectCompleted.Status != drive.UploadObjectCompleted {
		t.Fatalf("unexpected upload transition: completing=%s object=%s", completing.Status, objectCompleted.Status)
	}
	_, err = repository.AbortUpload(ctx, tenantA, session.ID, drive.UploadAborted, now.Add(8*time.Minute))
	assertCode(t, err, drive.CodeInvalidState)

	command := commitCommand(session, tenantA, testID(21), testID(22), testID(23), "digest-report", now.Add(9*time.Minute))
	result, createdResult, err := repository.CommitUpload(ctx, command)
	assertNoError(t, err)
	if !createdResult || result.Node.ID != command.Node.ID || result.Upload.Status != drive.UploadCommitted {
		t.Fatalf("unexpected first commit: created=%v result=%+v", createdResult, result)
	}
	retry, createdResult, err := repository.CommitUpload(ctx, command)
	assertNoError(t, err)
	if createdResult || retry.Node.ID != result.Node.ID || retry.Blob.ID != result.Blob.ID || retry.Version.ID != result.Version.ID {
		t.Fatalf("commit retry returned another result: created=%v retry=%+v", createdResult, retry)
	}
	_, _, err = repository.CommitUpload(ctx, drive.CommitUploadCommand{
		Identity: tenantB, SessionID: session.ID, Digest: command.Digest,
	})
	assertCode(t, err, drive.CodeNotFound)

	var nodes, versions, blobs int
	if err := repository.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM file_node WHERE id=$1),
			(SELECT count(*) FROM file_version WHERE id=$2),
			(SELECT count(*) FROM blob WHERE id=$3)`,
		command.Node.ID, command.Version.ID, command.Blob.ID,
	).Scan(&nodes, &versions, &blobs); err != nil {
		t.Fatalf("read committed rows: %v", err)
	}
	if nodes != 1 || versions != 1 || blobs != 1 {
		t.Fatalf("circular FK commit was incomplete: nodes=%d versions=%d blobs=%d", nodes, versions, blobs)
	}
}

func TestRepositoryFailUploadCompletionContract(t *testing.T) {
	repository := integrationRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 12, 30, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(301), PrincipalID: testID(303)}
	other := drive.Identity{TenantID: testID(304), PrincipalID: testID(306)}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Tenant A", RootNodeID: testID(302), Now: now,
	})
	assertNoError(t, err)
	_, err = repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: other.TenantID, DisplayName: "Tenant B", RootNodeID: testID(305), Now: now,
	})
	assertNoError(t, err)

	failingSession := newUpload(testID(310), identity, tenant.RootNodeID, "failing.bin", "failing.bin", now)
	_, err = repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: failingSession})
	assertNoError(t, err)
	const failingDigest = "digest-failing"
	completing := advanceUpload(t, repository, identity, failingSession.ID, failingDigest, now.Add(time.Minute))

	_, err = repository.FailUploadCompletion(ctx, other, failingSession.ID, failingDigest, "object_size_mismatch", now.Add(2*time.Minute))
	assertCode(t, err, drive.CodeNotFound)
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
	assertCode(t, err, drive.CodeIdempotencyConflict)

	committedSession := newUpload(testID(320), identity, tenant.RootNodeID, "committed.bin", "committed.bin", now)
	_, err = repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: committedSession})
	assertNoError(t, err)
	const committedDigest = "digest-committed"
	advanceUpload(t, repository, identity, committedSession.ID, committedDigest, now.Add(6*time.Minute))
	objectCompleted, err := repository.MarkObjectCompleted(ctx, identity, committedSession.ID, drive.ObjectInfo{}, now.Add(7*time.Minute))
	assertNoError(t, err)
	_, err = repository.FailUploadCompletion(ctx, identity, committedSession.ID, committedDigest, "object_size_mismatch", now.Add(8*time.Minute))
	assertCode(t, err, drive.CodeInvalidState)
	afterRejectedFailure, err := repository.Upload(ctx, identity, committedSession.ID)
	assertNoError(t, err)
	if afterRejectedFailure.Status != drive.UploadObjectCompleted || afterRejectedFailure.Revision != objectCompleted.Revision {
		t.Fatalf("failed transition changed object-completed upload: %+v", afterRejectedFailure)
	}

	command := commitCommand(committedSession, identity, testID(321), testID(322), testID(323), committedDigest, now.Add(9*time.Minute))
	result, created, err := repository.CommitUpload(ctx, command)
	assertNoError(t, err)
	if !created || result.Upload.Status != drive.UploadCommitted {
		t.Fatalf("unexpected committed upload: created=%v result=%+v", created, result)
	}
	_, err = repository.FailUploadCompletion(ctx, identity, committedSession.ID, committedDigest, "object_size_mismatch", now.Add(10*time.Minute))
	assertCode(t, err, drive.CodeInvalidState)
	afterCommittedFailure, err := repository.Upload(ctx, identity, committedSession.ID)
	assertNoError(t, err)
	if afterCommittedFailure.Status != drive.UploadCommitted || afterCommittedFailure.Revision != result.Upload.Revision {
		t.Fatalf("failed transition changed committed upload: %+v", afterCommittedFailure)
	}
}

func TestRepositoryCommitRollbackAndPurgeContract(t *testing.T) {
	repository := integrationRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(101), PrincipalID: testID(103)}
	other := drive.Identity{TenantID: testID(104), PrincipalID: testID(106)}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Tenant A", RootNodeID: testID(102), Now: now,
	})
	assertNoError(t, err)
	_, err = repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: other.TenantID, DisplayName: "Tenant B", RootNodeID: testID(105), Now: now,
	})
	assertNoError(t, err)

	firstSession := newUpload(testID(110), identity, tenant.RootNodeID, "duplicate.bin", "duplicate.bin", now.Add(time.Minute))
	secondSession := newUpload(testID(120), identity, tenant.RootNodeID, "DUPLICATE.bin", "duplicate.bin", now.Add(time.Minute))
	_, err = repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: firstSession})
	assertNoError(t, err)
	_, err = repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: secondSession})
	assertNoError(t, err)
	advanceToObjectCompleted(t, repository, identity, firstSession.ID, "digest-first", now.Add(2*time.Minute))
	advanceToObjectCompleted(t, repository, identity, secondSession.ID, "digest-second", now.Add(2*time.Minute))
	firstCommand := commitCommand(firstSession, identity, testID(111), testID(112), testID(113), "digest-first", now.Add(3*time.Minute))
	_, _, err = repository.CommitUpload(ctx, firstCommand)
	assertNoError(t, err)
	secondCommand := commitCommand(secondSession, identity, testID(121), testID(122), testID(123), "digest-second", now.Add(3*time.Minute))
	_, _, err = repository.CommitUpload(ctx, secondCommand)
	assertCode(t, err, drive.CodeNameConflict)

	var secondBlobs, secondVersions, secondNodes int
	if err := repository.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM blob WHERE id=$1),
			(SELECT count(*) FROM file_version WHERE id=$2),
			(SELECT count(*) FROM file_node WHERE id=$3)`,
		secondCommand.Blob.ID, secondCommand.Version.ID, secondCommand.Node.ID,
	).Scan(&secondBlobs, &secondVersions, &secondNodes); err != nil {
		t.Fatalf("read rolled-back rows: %v", err)
	}
	if secondBlobs != 0 || secondVersions != 0 || secondNodes != 0 {
		t.Fatalf("failed commit leaked rows: blobs=%d versions=%d nodes=%d", secondBlobs, secondVersions, secondNodes)
	}
	failedSession, err := repository.Upload(ctx, identity, secondSession.ID)
	assertNoError(t, err)
	if failedSession.Status != drive.UploadObjectCompleted {
		t.Fatalf("failed commit changed session state: %+v", failedSession)
	}

	sharedVersionID, sharedNodeID := testID(130), testID(131)
	addSharedBlobReference(t, repository, firstCommand, sharedVersionID, sharedNodeID, now.Add(4*time.Minute))
	if err := repository.Recycle(ctx, identity, firstCommand.Node.ID, firstCommand.Node.Revision, now.Add(4*time.Minute)); err != nil {
		t.Fatalf("recycle committed file: %v", err)
	}
	_, err = repository.PreparePurge(ctx, other, firstCommand.Node.ID, firstCommand.Node.Revision+1, now.Add(5*time.Minute))
	assertCode(t, err, drive.CodeNotFound)
	protectedPlan, err := repository.PreparePurge(ctx, identity, firstCommand.Node.ID, firstCommand.Node.Revision+1, now.Add(5*time.Minute))
	assertNoError(t, err)
	if len(protectedPlan.Blobs) != 0 {
		t.Fatalf("blob with an outside reference was scheduled for deletion: %+v", protectedPlan.Blobs)
	}
	var protectedStatus string
	if err := repository.pool.QueryRow(ctx, `SELECT status FROM blob WHERE id=$1`, firstCommand.Blob.ID).Scan(&protectedStatus); err != nil {
		t.Fatalf("read protected blob: %v", err)
	}
	if protectedStatus != string(drive.BlobAvailable) {
		t.Fatalf("protected blob status=%q, want %q", protectedStatus, drive.BlobAvailable)
	}
	removeSharedBlobReference(t, repository, firstCommand.Blob.ID, sharedVersionID, sharedNodeID)

	plan, err := repository.PreparePurge(ctx, identity, firstCommand.Node.ID, firstCommand.Node.Revision+1, now.Add(6*time.Minute))
	assertNoError(t, err)
	if len(plan.Blobs) != 1 || plan.Blobs[0].ID != firstCommand.Blob.ID || plan.Blobs[0].Status != drive.BlobPendingDelete {
		t.Fatalf("unexpected purge plan: %+v", plan)
	}
	retryPlan, err := repository.PreparePurge(ctx, identity, firstCommand.Node.ID, firstCommand.Node.Revision+1, now.Add(7*time.Minute))
	assertNoError(t, err)
	if len(retryPlan.Blobs) != 1 || retryPlan.Blobs[0].ID != firstCommand.Blob.ID {
		t.Fatalf("purge preparation was not retryable: %+v", retryPlan)
	}
	if err := repository.FinishPurge(ctx, other, plan, now.Add(8*time.Minute)); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("cross-tenant finish purge code = %s, err=%v", drive.CodeOf(err), err)
	}
	if err := repository.FinishPurge(ctx, identity, plan, now.Add(8*time.Minute)); err != nil {
		t.Fatalf("finish purge: %v", err)
	}
	var blobStatus string
	var deletedAt *time.Time
	if err := repository.pool.QueryRow(ctx, `SELECT status,deleted_at FROM blob WHERE id=$1`, firstCommand.Blob.ID).Scan(&blobStatus, &deletedAt); err != nil {
		t.Fatalf("read purged blob: %v", err)
	}
	if blobStatus != string(drive.BlobDeleted) || deletedAt == nil {
		t.Fatalf("purged blob status=%q deleted_at=%v", blobStatus, deletedAt)
	}
	finalPlan, err := repository.PreparePurge(ctx, identity, firstCommand.Node.ID, firstCommand.Node.Revision+1, now.Add(9*time.Minute))
	assertNoError(t, err)
	if len(finalPlan.Blobs) != 0 {
		t.Fatalf("deleted blob was scheduled again: %+v", finalPlan.Blobs)
	}
}

func TestRepositoryNestedRecycleRootsRemainIndependent(t *testing.T) {
	repository := integrationRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(201), PrincipalID: testID(203)}

	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Tenant A", RootNodeID: testID(202), Now: now,
	})
	assertNoError(t, err)
	parent := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: identity, ID: testID(210), ParentID: tenant.RootNodeID,
		DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
	})
	child := createDirectory(t, repository, drive.CreateDirectoryCommand{
		Identity: identity, ID: testID(211), ParentID: parent.ID,
		DisplayName: "Child", NormalizedName: "child", Now: now.Add(2 * time.Minute),
	})

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

func addSharedBlobReference(t *testing.T, repository *Repository, source drive.CommitUploadCommand, versionID, nodeID string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	tx, err := repository.pool.Begin(ctx)
	assertNoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		INSERT INTO file_version(
			id,tenant_id,node_id,blob_id,size_bytes,mime_type,checksum_algorithm,checksum_value,created_by,created_at
		) VALUES($1,$2,$3,$4,$5,$6,'','',$7,$8)`,
		versionID, source.Identity.TenantID, nodeID, source.Blob.ID, source.Blob.Size,
		source.Blob.MimeType, source.Identity.PrincipalID, now)
	assertNoError(t, err)
	_, err = tx.Exec(ctx, `
		INSERT INTO file_node(
			id,tenant_id,parent_id,kind,display_name,normalized_name,current_version_id,
			size_bytes,mime_type,status,revision,created_at,updated_at
		) VALUES($1,$2,$3,'file','survivor.bin','survivor.bin',$4,$5,$6,'active',1,$7,$7)`,
		nodeID, source.Identity.TenantID, source.Node.ParentID, versionID,
		source.Blob.Size, source.Blob.MimeType, now)
	assertNoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE blob SET reference_count=reference_count+1 WHERE id=$1`, source.Blob.ID)
	assertNoError(t, err)
	assertNoError(t, tx.Commit(ctx))
}

func removeSharedBlobReference(t *testing.T, repository *Repository, blobID, versionID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := repository.pool.Begin(ctx)
	assertNoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`)
	assertNoError(t, err)
	_, err = tx.Exec(ctx, `DELETE FROM file_node WHERE id=$1`, nodeID)
	assertNoError(t, err)
	_, err = tx.Exec(ctx, `DELETE FROM file_version WHERE id=$1`, versionID)
	assertNoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE blob SET reference_count=reference_count-1 WHERE id=$1`, blobID)
	assertNoError(t, err)
	assertNoError(t, tx.Commit(ctx))
}

func integrationRepository(t *testing.T) *Repository {
	t.Helper()
	connection := isolatedDatabaseURL(t)
	ctx := context.Background()
	if err := Migrate(ctx, connection); err != nil {
		t.Fatalf("migrate integration schema: %v", err)
	}
	repository, err := New(ctx, connection)
	if err != nil {
		t.Fatalf("open integration repository: %v", err)
	}
	t.Cleanup(repository.Close)
	return repository
}

func isolatedDatabaseURL(t *testing.T) string {
	t.Helper()
	base := os.Getenv(integrationDatabaseEnv)
	if base == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", integrationDatabaseEnv)
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("%s must be a PostgreSQL URL", integrationDatabaseEnv)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to integration database: %v", err)
	}
	schema := fmt.Sprintf("asteria_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		admin.Close(ctx)
		t.Fatalf("create integration schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`)
		_ = admin.Close(context.Background())
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func createDirectory(t *testing.T, repository *Repository, command drive.CreateDirectoryCommand) drive.Node {
	t.Helper()
	node, err := repository.CreateDirectory(context.Background(), command)
	assertNoError(t, err)
	return node
}

func newUpload(id string, identity drive.Identity, parentID, displayName, normalizedName string, now time.Time) drive.UploadSession {
	return drive.UploadSession{
		ID: id, TenantID: identity.TenantID, PrincipalID: identity.PrincipalID,
		ParentID: parentID, DisplayName: displayName, NormalizedName: normalizedName,
		ExpectedSize: 4, MimeType: "application/octet-stream", Bucket: "asteria",
		ObjectKey: "blobs/" + identity.TenantID + "/" + id, StorageUploadID: "storage-" + id,
		Status: drive.UploadCreated, PartSize: 5 * 1024 * 1024, ExpiresAt: now.Add(time.Hour),
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func advanceUpload(t *testing.T, repository *Repository, identity drive.Identity, sessionID, digest string, now time.Time) drive.UploadSession {
	t.Helper()
	_, err := repository.MarkUploading(context.Background(), identity, sessionID, now)
	assertNoError(t, err)
	session, err := repository.BeginComplete(context.Background(), identity, sessionID, digest, []drive.CompletedPart{
		{PartNumber: 1, ETag: "etag-1", Size: 4},
	}, now.Add(time.Second))
	assertNoError(t, err)
	return session
}

func advanceToObjectCompleted(t *testing.T, repository *Repository, identity drive.Identity, sessionID, digest string, now time.Time) {
	t.Helper()
	advanceUpload(t, repository, identity, sessionID, digest, now)
	_, err := repository.MarkObjectCompleted(context.Background(), identity, sessionID, drive.ObjectInfo{}, now.Add(2*time.Second))
	assertNoError(t, err)
}

func commitCommand(session drive.UploadSession, identity drive.Identity, blobID, versionID, nodeID, digest string, now time.Time) drive.CommitUploadCommand {
	checksum := drive.Checksum{}
	return drive.CommitUploadCommand{
		Identity: identity, SessionID: session.ID, Digest: digest, Now: now,
		Blob: drive.Blob{
			ID: blobID, TenantID: identity.TenantID, Bucket: session.Bucket, ObjectKey: session.ObjectKey,
			Size: session.ExpectedSize, MimeType: session.MimeType, Checksum: checksum,
			ChecksumStatus: drive.ChecksumUnavailable, Status: drive.BlobAvailable,
			ReferenceCount: 1, CreatedAt: now,
		},
		Version: drive.FileVersion{
			ID: versionID, TenantID: identity.TenantID, NodeID: nodeID, BlobID: blobID,
			Size: session.ExpectedSize, MimeType: session.MimeType, Checksum: checksum,
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

func assertCode(t *testing.T, err error, want drive.ErrorCode) {
	t.Helper()
	if got := drive.CodeOf(err); got != want {
		t.Fatalf("error code = %s, want %s (err=%v)", got, want, err)
	}
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func testID(number int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", number)
}
