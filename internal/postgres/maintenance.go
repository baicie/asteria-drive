package postgres

import (
	"context"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

const maintenanceUploadColumns = `
u.id::text, u.tenant_id::text, u.principal_id::text, u.parent_id::text, u.display_name,
u.normalized_name, u.expected_size, u.mime_type, u.declared_checksum_algorithm,
u.declared_checksum_value, u.bucket, u.object_key, u.storage_upload_id, u.status,
u.completion_digest, COALESCE(u.committed_node_id::text,''), u.failure_code, u.part_size,
u.expires_at, u.revision, u.created_at, u.updated_at`

func (r *Repository) ClaimUploadsForMaintenance(ctx context.Context, owner string, now, staleBefore, leaseUntil time.Time, limit int) ([]drive.UploadMaintenanceClaim, error) {
	rows, err := r.pool.Query(ctx, `
		WITH candidate AS (
			SELECT tenant_id, id FROM upload_session
			WHERE (maintenance_lease_until IS NULL OR maintenance_lease_until <= $1)
			  AND (maintenance_not_before IS NULL OR maintenance_not_before <= $1)
			  AND (
				(status IN ('created','uploading') AND expires_at <= $1)
				OR (status IN ('completing','object_completed') AND updated_at <= $2)
				OR cleanup_status = 'pending'
				OR maintenance_error_code <> ''
			  )
			ORDER BY maintenance_not_before NULLS FIRST, expires_at, id
			LIMIT $3 FOR UPDATE SKIP LOCKED
		)
		UPDATE upload_session u SET maintenance_owner=$4, maintenance_lease_until=$5
		FROM candidate c WHERE u.tenant_id=c.tenant_id AND u.id=c.id
		RETURNING `+maintenanceUploadColumns+`, u.cleanup_status, u.maintenance_attempts`, now, staleBefore, limit, owner, leaseUntil)
	if err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not claim upload maintenance")
	}
	defer rows.Close()
	claims := make([]drive.UploadMaintenanceClaim, 0, limit)
	for rows.Next() {
		claim, err := scanMaintenanceUpload(rows)
		if err != nil {
			return nil, mapError(err, drive.CodeInternal, "could not decode upload maintenance claim")
		}
		claim.Owner = owner
		claims = append(claims, claim)
	}
	return claims, mapError(rows.Err(), drive.CodeInternal, "could not claim upload maintenance")
}

func scanMaintenanceUpload(row scanner) (drive.UploadMaintenanceClaim, error) {
	var claim drive.UploadMaintenanceClaim
	var status, cleanup string
	err := row.Scan(
		&claim.Upload.ID, &claim.Upload.TenantID, &claim.Upload.PrincipalID, &claim.Upload.ParentID,
		&claim.Upload.DisplayName, &claim.Upload.NormalizedName, &claim.Upload.ExpectedSize, &claim.Upload.MimeType,
		&claim.Upload.DeclaredChecksum.Algorithm, &claim.Upload.DeclaredChecksum.Value, &claim.Upload.Bucket,
		&claim.Upload.ObjectKey, &claim.Upload.StorageUploadID, &status, &claim.Upload.CompletionDigest,
		&claim.Upload.CommittedNodeID, &claim.Upload.FailureCode, &claim.Upload.PartSize, &claim.Upload.ExpiresAt,
		&claim.Upload.Revision, &claim.Upload.CreatedAt, &claim.Upload.UpdatedAt, &cleanup, &claim.Attempts,
	)
	claim.Upload.Status = drive.UploadStatus(status)
	claim.CleanupPending = cleanup == "pending"
	return claim, err
}

func (r *Repository) FinishUploadMaintenance(ctx context.Context, owner, uploadID string, status drive.UploadStatus, cleanupComplete bool, retryAt time.Time, errorCode string, now time.Time) error {
	cleanupStatus := "pending"
	if cleanupComplete {
		cleanupStatus = "complete"
	}
	var notBefore any
	if !retryAt.IsZero() {
		notBefore = retryAt
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE upload_session SET status=$4, cleanup_status=$5,
			maintenance_owner=NULL, maintenance_lease_until=NULL,
			maintenance_not_before=$6,
			maintenance_attempts=CASE WHEN $7='' THEN maintenance_attempts ELSE maintenance_attempts+1 END,
			maintenance_error_code=$7, revision=revision+1, updated_at=$8
		WHERE id=$1 AND maintenance_owner=$2`, uploadID, owner, now, status, cleanupStatus, notBefore, errorCode, now)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not finish upload maintenance")
	}
	if tag.RowsAffected() != 1 {
		return drive.E(drive.CodeInvalidState, "upload maintenance claim is no longer owned")
	}
	return nil
}

func (r *Repository) ClaimRecycleForMaintenance(ctx context.Context, owner string, cutoff, now, leaseUntil time.Time, limit int) ([]drive.RecycleMaintenanceClaim, error) {
	rows, err := r.pool.Query(ctx, `
		WITH candidate AS (
			SELECT tenant_id, id, revision FROM file_node
			WHERE status IN ('trashed','purging') AND trashed_root_id=id AND deleted_at <= $1
			  AND (maintenance_lease_until IS NULL OR maintenance_lease_until <= $2)
			  AND (maintenance_not_before IS NULL OR maintenance_not_before <= $2)
			ORDER BY maintenance_not_before NULLS FIRST, deleted_at, id
			LIMIT $3 FOR UPDATE SKIP LOCKED
		)
		UPDATE file_node n SET status='purging', maintenance_owner=$4, maintenance_lease_until=$5
		FROM candidate c WHERE n.tenant_id=c.tenant_id AND n.id=c.id
		RETURNING n.tenant_id::text, n.id::text, n.revision`, cutoff, now, limit, owner, leaseUntil)
	if err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not claim recycle maintenance")
	}
	defer rows.Close()
	claims := make([]drive.RecycleMaintenanceClaim, 0, limit)
	for rows.Next() {
		claim := drive.RecycleMaintenanceClaim{Owner: owner}
		if err := rows.Scan(&claim.Identity.TenantID, &claim.RootID, &claim.Revision); err != nil {
			return nil, mapError(err, drive.CodeInternal, "could not decode recycle maintenance claim")
		}
		claims = append(claims, claim)
	}
	return claims, mapError(rows.Err(), drive.CodeInternal, "could not claim recycle maintenance")
}

func (r *Repository) ReleaseRecycleMaintenance(ctx context.Context, owner, rootID string, retryAt time.Time, errorCode string, now time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE file_node SET maintenance_owner=NULL, maintenance_lease_until=NULL,
			maintenance_not_before=$3, maintenance_attempts=maintenance_attempts+1,
			maintenance_error_code=$4, updated_at=$5
		WHERE id=$1 AND maintenance_owner=$2`, rootID, owner, retryAt, errorCode, now)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not release recycle maintenance")
	}
	if tag.RowsAffected() != 1 {
		return drive.E(drive.CodeInvalidState, "recycle maintenance claim is no longer owned")
	}
	return nil
}

func (r *Repository) DeleteExpiredIdempotency(ctx context.Context, now time.Time, limit int) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		WITH candidate AS (
			SELECT ctid FROM idempotency_record WHERE expires_at <= $1
			ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM idempotency_record WHERE ctid IN (SELECT ctid FROM candidate)`, now, limit)
	if err != nil {
		return 0, mapError(err, drive.CodeInternal, "could not delete expired idempotency records")
	}
	return int(tag.RowsAffected()), nil
}
