package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5"
)

// TestBaseline0003DataSurvivesForwardMigration is the R3.1 production-baseline check.
//
// It stands up the schema a deployed installation runs today (migrations 0001-0003 only),
// fills it with the data that installation actually holds -- a tenant, a root directory, a
// committed file with its blob and version, an open upload session with its frozen parts --
// and then applies this branch's migrations on top. Nothing is stubbed: the rows are written
// through the repository where a repository exists and through SQL where the R3 code has no
// writer for a legacy shape.
//
// The question it answers is not "does the DDL parse" but "can a real database be upgraded
// in place without touching, inventing, or losing anything".
func TestBaseline0003DataSurvivesForwardMigration(t *testing.T) {
	connection := isolatedDatabaseURL(t)
	ctx := context.Background()
	if err := applyMigrationsThrough(ctx, connection, 3); err != nil {
		t.Fatalf("apply the deployed 0003 baseline: %v", err)
	}
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(941), PrincipalID: testID(942)}

	conn, err := pgx.Connect(ctx, connection)
	if err != nil {
		t.Fatalf("connect to baseline schema: %v", err)
	}
	defer conn.Close(ctx)

	// A tenant with its root directory, written the way EnsureTenant writes it on the old
	// schema: tenant first, directory second, then the root pointer. The root foreign key is
	// DEFERRABLE INITIALLY DEFERRED, so the pointer can only be set when the directory exists.
	rootID := testID(943)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin baseline tenant write: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant(id, display_name, created_at)
		VALUES($1, 'Baseline', $2)`, identity.TenantID, now); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert baseline tenant: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO file_node(id, tenant_id, parent_id, kind, display_name, normalized_name, size_bytes, status, revision, created_at, updated_at)
		VALUES($1, $2, NULL, 'directory', '根目录', '根目录', 0, 'active', 1, $3, $3)`, rootID, identity.TenantID, now); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert baseline root directory: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE tenant SET root_node_id=$2 WHERE id=$1`, identity.TenantID, rootID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("attach baseline root directory: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit baseline tenant: %v", err)
	}

	// A committed file: node -> current version -> blob, with reference_count 1.
	fileID, blobID, versionID := testID(944), testID(945), testID(946)
	const digest = "u4KAGrZLoDkPvv6uAVbXB3hfd1Ra1b4AVSPiIVppbQo="
	if _, err := conn.Exec(ctx, `
		INSERT INTO blob(id, tenant_id, bucket, object_key, size_bytes, mime_type, checksum_algorithm, checksum_value, checksum_status, status, reference_count, created_at)
		VALUES($1, $2, 'asteria', $3, 5, 'application/octet-stream', 'sha256', $4, 'declared', 'available', 1, $5)`,
		blobID, identity.TenantID, "blobs/"+identity.TenantID+"/legacy", digest, now); err != nil {
		t.Fatalf("insert baseline blob: %v", err)
	}
	// file_node_shape requires a file node to carry a current_version_id, and file_version
	// references the node: a file can only be created as a two-step write inside one
	// transaction, which is exactly what the service does. The version FK is DEFERRABLE
	// INITIALLY DEFERRED, so the forward reference is legal until commit.
	fileTx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin baseline file write: %v", err)
	}
	if _, err := fileTx.Exec(ctx, `
		INSERT INTO file_version(id, tenant_id, node_id, blob_id, size_bytes, mime_type, checksum_algorithm, checksum_value, created_by, created_at)
		VALUES($1, $2, $3, $4, 5, 'application/octet-stream', 'sha256', $5, $6, $7)`,
		versionID, identity.TenantID, fileID, blobID, digest, identity.PrincipalID, now); err != nil {
		_ = fileTx.Rollback(ctx)
		t.Fatalf("insert baseline file version: %v", err)
	}
	if _, err := fileTx.Exec(ctx, `
		INSERT INTO file_node(id, tenant_id, parent_id, kind, display_name, normalized_name, current_version_id, size_bytes, mime_type, status, revision, created_at, updated_at)
		VALUES($1, $2, $3, 'file', 'legacy.bin', 'legacy.bin', $4, 5, 'application/octet-stream', 'active', 1, $5, $5)`,
		fileID, identity.TenantID, rootID, versionID, now); err != nil {
		_ = fileTx.Rollback(ctx)
		t.Fatalf("insert baseline file: %v", err)
	}
	if err := fileTx.Commit(ctx); err != nil {
		t.Fatalf("commit baseline file: %v", err)
	}

	// An open upload session with a frozen completion list. This is the shape that matters:
	// a session created before R2/R3 existed has no signature rows and never will.
	sessionID := testID(947)
	if _, err := conn.Exec(ctx, `
		INSERT INTO upload_session(
			id, tenant_id, principal_id, parent_id, display_name, normalized_name, expected_size, mime_type,
			declared_checksum_algorithm, declared_checksum_value, bucket, object_key, storage_upload_id, status,
			completion_digest, committed_node_id, failure_code, part_size, expires_at, revision, created_at, updated_at)
		VALUES($1, $2, $3, $4, 'pending.bin', 'pending.bin', 5, 'application/octet-stream',
			'sha256', $5, 'asteria', $6, 'storage-legacy', 'completing',
			'legacy-digest', NULL, '', 5242880, $7, 3, $8, $8)`,
		sessionID, identity.TenantID, identity.PrincipalID, rootID, digest,
		"blobs/"+identity.TenantID+"/pending", now.Add(24*time.Hour), now); err != nil {
		t.Fatalf("insert baseline upload session: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO upload_part(tenant_id, upload_session_id, part_number, etag, checksum_algorithm, checksum_value, size_bytes, created_at)
		VALUES($1, $2, 1, '"legacy-etag"', 'sha256', $3, 5, $4)`,
		identity.TenantID, sessionID, digest, now); err != nil {
		t.Fatalf("insert baseline upload part: %v", err)
	}
	conn.Close(ctx)

	// Forward migration to 0005: the operation an operator performs on a live database.
	if err := Migrate(ctx, connection); err != nil {
		t.Fatalf("migrate 0003 baseline to current: %v", err)
	}
	repository, err := New(ctx, connection)
	if err != nil {
		t.Fatalf("open repository after forward migration: %v", err)
	}
	t.Cleanup(repository.Close)

	// 1. The tenant and its root survive and are readable through the current code.
	tenant, err := repository.Tenant(ctx, identity.TenantID)
	if err != nil {
		t.Fatalf("read tenant after migration: %v", err)
	}
	if tenant.RootNodeID != rootID {
		t.Fatalf("root directory changed: got %q want %q", tenant.RootNodeID, rootID)
	}

	// 2. The file still resolves to the same blob and version, with reference_count intact.
	node, blob, err := repository.DownloadBlob(ctx, identity, fileID)
	if err != nil {
		t.Fatalf("resolve committed file after migration: %v", err)
	}
	if node.ID != fileID || node.CurrentVersionID != versionID {
		t.Fatalf("file identity or version changed: %+v", node)
	}
	if blob.ID != blobID || blob.ObjectKey != "blobs/"+identity.TenantID+"/legacy" || blob.ReferenceCount != 1 {
		t.Fatalf("blob reference changed: %+v", blob)
	}

	// 3. The legacy session keeps its frozen completion list, so completion can be replayed
	// without inventing checksums or signatures it never had.
	parts, err := repository.UploadParts(ctx, identity, sessionID)
	if err != nil {
		t.Fatalf("read legacy frozen parts: %v", err)
	}
	if len(parts) != 1 || parts[0].PartNumber != 1 || parts[0].ETag != `"legacy-etag"` || parts[0].Size != 5 {
		t.Fatalf("legacy parts changed by the migration: %+v", parts)
	}
	session, err := repository.Upload(ctx, identity, sessionID)
	if err != nil {
		t.Fatalf("read legacy session: %v", err)
	}
	if session.Status != drive.UploadCompleting || session.CompletionDigest != "legacy-digest" || session.Revision != 3 {
		t.Fatalf("legacy session changed by the migration: %+v", session)
	}

	// 4. A legacy session has no signatures, and that is a valid state rather than a gap:
	// reading them must succeed with an empty result, never error.
	signatures, err := repository.PartSignatures(ctx, identity, sessionID)
	if err != nil {
		t.Fatalf("read signatures for a legacy session: %v", err)
	}
	if len(signatures) != 0 {
		t.Fatalf("migration fabricated signatures for a legacy session: %+v", signatures)
	}

	// 5. The new table exists and the new code can write to it on the upgraded schema.
	if err := repository.RecordPartSignature(ctx, identity, sessionID, drive.PartSignature{
		PartNumber: 2, Size: 5, SHA256: digest,
	}, now); err != nil {
		t.Fatalf("write a signature on the upgraded schema: %v", err)
	}
	signatures, err = repository.PartSignatures(ctx, identity, sessionID)
	if err != nil {
		t.Fatalf("read signatures after write: %v", err)
	}
	if len(signatures) != 1 || signatures[0].PartNumber != 2 || signatures[0].SHA256 != digest {
		t.Fatalf("signature round-trip on the upgraded schema: %+v", signatures)
	}

	// 6. The published migrations were not rewritten. 0001-0003 are recorded under their
	// original names, and the upgrade only appended new versions.
	conn, err = pgx.Connect(ctx, connection)
	if err != nil {
		t.Fatalf("reconnect for migration bookkeeping: %v", err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT version,name FROM asteria_schema_migration ORDER BY version`)
	if err != nil {
		t.Fatalf("read migration bookkeeping: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var version int64
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			t.Fatalf("scan migration row: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration rows: %v", err)
	}
	want := []string{"0001_mvp.sql", "0002_identity_access.sql", "0003_production_readiness.sql",
		"0004_upload_part_checksum.sql", "0005_part_signature_audit_only.sql"}
	if len(names) != len(want) {
		t.Fatalf("migration history = %v, want %v", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("migration history = %v, want %v", names, want)
		}
	}
}
