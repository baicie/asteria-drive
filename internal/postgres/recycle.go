package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) DownloadBlob(ctx context.Context, identity drive.Identity, nodeID string) (drive.Node, drive.Blob, error) {
	node, err := scanNode(r.pool.QueryRow(ctx, `
		SELECT `+nodeColumns+` FROM file_node
		WHERE tenant_id=$1 AND id=$2 AND kind='file' AND status='active' AND trashed_root_id IS NULL`,
		identity.TenantID, nodeID))
	if err != nil {
		return drive.Node{}, drive.Blob{}, mapError(err, drive.CodeInternal, "could not read file")
	}
	var blobID string
	if err := r.pool.QueryRow(ctx, `
		SELECT blob_id::text FROM file_version WHERE tenant_id=$1 AND id=$2`,
		identity.TenantID, node.CurrentVersionID).Scan(&blobID); err != nil {
		return drive.Node{}, drive.Blob{}, mapError(err, drive.CodeInternal, "could not read file version")
	}
	blob, err := scanBlob(r.pool.QueryRow(ctx, `
		SELECT `+blobColumns+` FROM blob
		WHERE tenant_id=$1 AND id=$2 AND status='available'`, identity.TenantID, blobID))
	if err != nil {
		return drive.Node{}, drive.Blob{}, mapError(err, drive.CodeInternal, "could not read file content")
	}
	return node, blob, nil
}

func (r *Repository) Recycle(ctx context.Context, identity drive.Identity, nodeID string, revision int64, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not recycle node")
	}
	defer tx.Rollback(ctx)
	rootID, err := lockNamespace(ctx, tx, identity.TenantID)
	if err != nil {
		return err
	}
	node, err := scanNode(tx.QueryRow(ctx, `
		SELECT `+nodeColumns+` FROM file_node WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, identity.TenantID, nodeID))
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not read node")
	}
	if node.ID == rootID {
		return drive.E(drive.CodeInvalidRequest, "root directory cannot be recycled")
	}
	if node.Status == drive.NodeTrashed && node.TrashedRootID == node.ID {
		return commit(tx, ctx)
	}
	if node.Status != drive.NodeActive || node.TrashedRootID != "" {
		return drive.E(drive.CodeNotFound, "resource was not found")
	}
	if node.Revision != revision {
		return drive.E(drive.CodeRevisionMismatch, "resource revision does not match")
	}
	if _, err := tx.Exec(ctx, `
		WITH RECURSIVE tree(id) AS (
			SELECT id FROM file_node
			WHERE tenant_id=$1 AND id=$2 AND status='active' AND trashed_root_id IS NULL
			UNION
			SELECT child.id FROM file_node child JOIN tree ON child.parent_id=tree.id
			WHERE child.tenant_id=$1 AND child.status='active' AND child.trashed_root_id IS NULL
		)
		UPDATE file_node n SET
			trashed_root_id=$2,
			status=CASE WHEN n.id=$2 THEN 'trashed' ELSE n.status END,
			original_parent_id=CASE WHEN n.id=$2 THEN n.parent_id ELSE n.original_parent_id END,
			deleted_at=CASE WHEN n.id=$2 THEN $3 ELSE n.deleted_at END,
			revision=CASE WHEN n.id=$2 THEN n.revision+1 ELSE n.revision END,
			updated_at=$3
		WHERE n.tenant_id=$1 AND n.id IN (SELECT id FROM tree)
		  AND n.status='active' AND n.trashed_root_id IS NULL`, identity.TenantID, nodeID, now); err != nil {
		return mapError(err, drive.CodeInternal, "could not recycle node")
	}
	return commit(tx, ctx)
}

func (r *Repository) ListRecycle(ctx context.Context, identity drive.Identity, after drive.CursorPosition, limit int) ([]drive.RecycleEntry, bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+nodeColumns+` FROM file_node
		WHERE tenant_id=$1 AND status='trashed' AND trashed_root_id=id
		  AND ($2='' OR (normalized_name,id) > ($2,$3::uuid))
		ORDER BY normalized_name,id LIMIT $4`, identity.TenantID, after.Name, nullString(after.ID), limit+1)
	if err != nil {
		return nil, false, mapError(err, drive.CodeInternal, "could not list recycle bin")
	}
	defer rows.Close()
	items := make([]drive.RecycleEntry, 0, limit+1)
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, false, mapError(err, drive.CodeInternal, "could not decode recycle entry")
		}
		deletedAt := time.Time{}
		if node.DeletedAt != nil {
			deletedAt = *node.DeletedAt
		}
		items = append(items, drive.RecycleEntry{Node: node, OriginalParentID: node.OriginalParentID, DeletedAt: deletedAt})
	}
	if err := rows.Err(); err != nil {
		return nil, false, mapError(err, drive.CodeInternal, "could not list recycle bin")
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

func (r *Repository) Restore(ctx context.Context, identity drive.Identity, nodeID string, revision int64, now time.Time) (drive.Node, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.Node{}, mapError(err, drive.CodeInternal, "could not restore node")
	}
	defer tx.Rollback(ctx)
	if _, err := lockNamespace(ctx, tx, identity.TenantID); err != nil {
		return drive.Node{}, err
	}
	node, err := scanNode(tx.QueryRow(ctx, `
		SELECT `+nodeColumns+` FROM file_node
		WHERE tenant_id=$1 AND id=$2 AND status='trashed' AND trashed_root_id=id FOR UPDATE`,
		identity.TenantID, nodeID))
	if err != nil {
		return drive.Node{}, mapError(err, drive.CodeInternal, "could not read recycle entry")
	}
	if node.Revision != revision {
		return drive.Node{}, drive.E(drive.CodeRevisionMismatch, "resource revision does not match")
	}
	if node.OriginalParentID == "" {
		return drive.Node{}, drive.E(drive.CodeRestoreConflict, "original location is unavailable")
	}
	if _, err := lockActiveDirectory(ctx, tx, identity.TenantID, node.OriginalParentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return drive.Node{}, drive.E(drive.CodeRestoreConflict, "original location is unavailable")
		}
		return drive.Node{}, mapError(err, drive.CodeInternal, "could not lock restore target")
	}
	var nameExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM file_node
		WHERE tenant_id=$1 AND parent_id=$2 AND normalized_name=$3
		  AND id<>$4 AND status='active' AND trashed_root_id IS NULL)`,
		identity.TenantID, node.OriginalParentID, node.NormalizedName, node.ID).Scan(&nameExists); err != nil {
		return drive.Node{}, mapError(err, drive.CodeInternal, "could not validate restore target")
	}
	if nameExists {
		return drive.Node{}, drive.E(drive.CodeRestoreConflict, "original location is unavailable")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE file_node n SET
			trashed_root_id=NULL,
			status=CASE WHEN n.id=$2 THEN 'active' ELSE n.status END,
			parent_id=CASE WHEN n.id=$2 THEN n.original_parent_id ELSE n.parent_id END,
			original_parent_id=CASE WHEN n.id=$2 THEN NULL ELSE n.original_parent_id END,
			deleted_at=CASE WHEN n.id=$2 THEN NULL ELSE n.deleted_at END,
			revision=CASE WHEN n.id=$2 THEN n.revision+1 ELSE n.revision END,
			updated_at=$3
		WHERE n.tenant_id=$1 AND n.trashed_root_id=$2`, identity.TenantID, nodeID, now); err != nil {
		return drive.Node{}, mapError(err, drive.CodeRestoreConflict, "could not restore node")
	}
	restored, err := scanNode(tx.QueryRow(ctx, `SELECT `+nodeColumns+` FROM file_node WHERE tenant_id=$1 AND id=$2`, identity.TenantID, nodeID))
	if err != nil {
		return drive.Node{}, mapError(err, drive.CodeInternal, "could not read restored node")
	}
	if err := commit(tx, ctx); err != nil {
		return drive.Node{}, err
	}
	return restored, nil
}

func (r *Repository) PreparePurge(ctx context.Context, identity drive.Identity, nodeID string, revision int64, now time.Time) (drive.PurgePlan, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.PurgePlan{}, mapError(err, drive.CodeInternal, "could not prepare purge")
	}
	defer tx.Rollback(ctx)
	if _, err := lockNamespace(ctx, tx, identity.TenantID); err != nil {
		return drive.PurgePlan{}, err
	}
	node, err := scanNode(tx.QueryRow(ctx, `
		SELECT `+nodeColumns+` FROM file_node
		WHERE tenant_id=$1 AND id=$2 AND status IN ('trashed','purging') AND trashed_root_id=id
		FOR UPDATE`, identity.TenantID, nodeID))
	if err != nil {
		return drive.PurgePlan{}, mapError(err, drive.CodeInternal, "could not read recycle entry")
	}
	if node.Revision != revision {
		return drive.PurgePlan{}, drive.E(drive.CodeRevisionMismatch, "resource revision does not match")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE file_node SET status='purging',updated_at=$3
		WHERE tenant_id=$1 AND trashed_root_id=$2`, identity.TenantID, nodeID, now); err != nil {
		return drive.PurgePlan{}, mapError(err, drive.CodeInternal, "could not mark purge tree")
	}
	rows, err := tx.Query(ctx, `
		SELECT `+blobColumns+`
		FROM blob b
		WHERE b.tenant_id=$1 AND b.status<>'deleted'
		  AND b.id IN (
			SELECT v.blob_id FROM file_version v
			WHERE v.tenant_id=b.tenant_id AND v.node_id IN (
				SELECT id FROM file_node WHERE tenant_id=$1 AND trashed_root_id=$2
			)
		  )
		  AND NOT EXISTS(
			SELECT 1
			FROM file_version outside
			JOIN file_node owner
			  ON owner.tenant_id=outside.tenant_id AND owner.id=outside.node_id
			WHERE outside.tenant_id=b.tenant_id AND outside.blob_id=b.id
			  AND owner.status<>'purging'
		  )
		ORDER BY b.id
		FOR UPDATE OF b`, identity.TenantID, nodeID)
	if err != nil {
		return drive.PurgePlan{}, mapError(err, drive.CodeInternal, "could not identify purge blobs")
	}
	blobs := make([]drive.Blob, 0)
	for rows.Next() {
		blob, err := scanBlob(rows)
		if err != nil {
			rows.Close()
			return drive.PurgePlan{}, mapError(err, drive.CodeInternal, "could not decode purge blob")
		}
		blobs = append(blobs, blob)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return drive.PurgePlan{}, mapError(err, drive.CodeInternal, "could not identify purge blobs")
	}
	rows.Close()
	for i := range blobs {
		if _, err := tx.Exec(ctx, `
			UPDATE blob SET status='pending_delete',reference_count=0
			WHERE tenant_id=$1 AND id=$2 AND status<>'deleted'`, identity.TenantID, blobs[i].ID); err != nil {
			return drive.PurgePlan{}, mapError(err, drive.CodeInternal, "could not mark purge blob")
		}
		blobs[i].Status = drive.BlobPendingDelete
		blobs[i].ReferenceCount = 0
	}
	if err := commit(tx, ctx); err != nil {
		return drive.PurgePlan{}, err
	}
	return drive.PurgePlan{RootID: nodeID, Blobs: blobs}, nil
}

func (r *Repository) FinishPurge(ctx context.Context, identity drive.Identity, plan drive.PurgePlan, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not finish purge")
	}
	defer tx.Rollback(ctx)
	var rootExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM file_node WHERE tenant_id=$1 AND id=$2 AND status='purging')`,
		identity.TenantID, plan.RootID).Scan(&rootExists); err != nil {
		return mapError(err, drive.CodeInternal, "could not validate purge plan")
	}
	if !rootExists {
		return drive.E(drive.CodeNotFound, "purge plan was not found")
	}
	for _, blob := range plan.Blobs {
		if _, err := tx.Exec(ctx, `
			UPDATE blob SET status='deleted',deleted_at=$3
			WHERE tenant_id=$1 AND id=$2 AND status='pending_delete'`,
			identity.TenantID, blob.ID, now); err != nil {
			return mapError(err, drive.CodeInternal, "could not finish purge blob")
		}
	}
	return commit(tx, ctx)
}
