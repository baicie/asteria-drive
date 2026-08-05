package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) CreateUpload(ctx context.Context, command drive.CreateUploadCommand) (drive.UploadSession, error) {
	session := command.Session
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not create upload")
	}
	defer tx.Rollback(ctx)
	var parentExists, nameExists bool
	if err := tx.QueryRow(ctx, `
		SELECT
			EXISTS(SELECT 1 FROM file_node WHERE tenant_id=$1 AND id=$2 AND kind='directory'
				AND status='active' AND trashed_root_id IS NULL),
			EXISTS(SELECT 1 FROM file_node WHERE tenant_id=$1 AND parent_id=$2 AND normalized_name=$3
				AND status='active' AND trashed_root_id IS NULL)`,
		session.TenantID, session.ParentID, session.NormalizedName).Scan(&parentExists, &nameExists); err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not validate upload")
	}
	if !parentExists {
		return drive.UploadSession{}, drive.E(drive.CodeNotFound, "parent directory was not found")
	}
	if nameExists {
		return drive.UploadSession{}, drive.E(drive.CodeNameConflict, "an active item with this name already exists")
	}
	created, err := scanUpload(tx.QueryRow(ctx, `
		INSERT INTO upload_session(
			id,tenant_id,principal_id,parent_id,display_name,normalized_name,expected_size,mime_type,
			declared_checksum_algorithm,declared_checksum_value,bucket,object_key,storage_upload_id,status,
			completion_digest,committed_node_id,failure_code,part_size,expires_at,revision,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'',NULL,'',$15,$16,1,$17,$17)
		RETURNING `+uploadColumns,
		session.ID, session.TenantID, session.PrincipalID, session.ParentID, session.DisplayName,
		session.NormalizedName, session.ExpectedSize, session.MimeType,
		session.DeclaredChecksum.Algorithm, session.DeclaredChecksum.Value, session.Bucket,
		session.ObjectKey, session.StorageUploadID, session.Status, session.PartSize,
		session.ExpiresAt, session.CreatedAt))
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not create upload")
	}
	if err := completeIdempotency(ctx, tx, command.Idempotency, created.ID, session.CreatedAt); err != nil {
		return drive.UploadSession{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return drive.UploadSession{}, err
	}
	return created, nil
}

func (r *Repository) Upload(ctx context.Context, identity drive.Identity, id string) (drive.UploadSession, error) {
	session, err := scanUpload(r.pool.QueryRow(ctx, `
		SELECT `+uploadColumns+` FROM upload_session WHERE tenant_id=$1 AND id=$2`, identity.TenantID, id))
	return session, mapError(err, drive.CodeInternal, "could not read upload")
}

func (r *Repository) MarkUploading(ctx context.Context, identity drive.Identity, id string, now time.Time) (drive.UploadSession, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not mark upload active")
	}
	defer tx.Rollback(ctx)
	session, err := scanUpload(tx.QueryRow(ctx, `
		SELECT `+uploadColumns+` FROM upload_session
		WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, identity.TenantID, id))
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not read upload")
	}
	if session.Status == drive.UploadUploading {
		if err := commit(tx, ctx); err != nil {
			return drive.UploadSession{}, err
		}
		return session, nil
	}
	if session.Status != drive.UploadCreated {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload session cannot transition to uploading")
	}
	session, err = scanUpload(tx.QueryRow(ctx, `
		UPDATE upload_session SET status='uploading',revision=revision+1,updated_at=$3
		WHERE tenant_id=$1 AND id=$2
		RETURNING `+uploadColumns, identity.TenantID, id, now))
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not mark upload active")
	}
	if err := commit(tx, ctx); err != nil {
		return drive.UploadSession{}, err
	}
	return session, nil
}

func (r *Repository) BeginComplete(ctx context.Context, identity drive.Identity, id, digest string, parts []drive.CompletedPart, now time.Time) (drive.UploadSession, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not begin upload completion")
	}
	defer tx.Rollback(ctx)
	session, err := scanUpload(tx.QueryRow(ctx, `
		SELECT `+uploadColumns+` FROM upload_session
		WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, identity.TenantID, id))
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not read upload")
	}
	if session.CompletionDigest != "" && session.CompletionDigest != digest {
		return drive.UploadSession{}, drive.E(drive.CodeIdempotencyConflict, "completion payload differs from the first request")
	}
	if session.Status == drive.UploadCommitted || session.Status == drive.UploadObjectCompleted || session.Status == drive.UploadCompleting {
		if err := commit(tx, ctx); err != nil {
			return drive.UploadSession{}, err
		}
		return session, nil
	}
	if session.Status != drive.UploadCreated && session.Status != drive.UploadUploading {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload session cannot be completed")
	}
	if !now.Before(session.ExpiresAt) {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload session has expired")
	}
	for _, part := range parts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO upload_part(
				tenant_id,upload_session_id,part_number,etag,checksum_algorithm,checksum_value,size_bytes,created_at
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT(tenant_id,upload_session_id,part_number) DO UPDATE SET
				etag=EXCLUDED.etag,checksum_algorithm=EXCLUDED.checksum_algorithm,
				checksum_value=EXCLUDED.checksum_value,size_bytes=EXCLUDED.size_bytes
			WHERE upload_part.etag=EXCLUDED.etag
			  AND upload_part.checksum_algorithm=EXCLUDED.checksum_algorithm
			  AND upload_part.checksum_value=EXCLUDED.checksum_value`,
			identity.TenantID, id, part.PartNumber, part.ETag, part.Checksum.Algorithm,
			part.Checksum.Value, part.Size, now); err != nil {
			return drive.UploadSession{}, mapError(err, drive.CodeIdempotencyConflict, "upload part differs from the first request")
		}
	}
	session, err = scanUpload(tx.QueryRow(ctx, `
		UPDATE upload_session SET status='completing',completion_digest=$3,
			revision=revision+1,updated_at=$4
		WHERE tenant_id=$1 AND id=$2
		RETURNING `+uploadColumns, identity.TenantID, id, digest, now))
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not begin upload completion")
	}
	if err := commit(tx, ctx); err != nil {
		return drive.UploadSession{}, err
	}
	return session, nil
}

func (r *Repository) FailUploadCompletion(ctx context.Context, identity drive.Identity, id, digest, failureCode string, now time.Time) (drive.UploadSession, error) {
	if failureCode == "" {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidRequest, "upload failure code is required")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not fail upload completion")
	}
	defer tx.Rollback(ctx)
	session, err := scanUpload(tx.QueryRow(ctx, `
		SELECT `+uploadColumns+` FROM upload_session
		WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, identity.TenantID, id))
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not read upload")
	}
	if session.CompletionDigest != digest {
		return drive.UploadSession{}, drive.E(drive.CodeIdempotencyConflict, "completion payload differs from the first request")
	}
	if session.Status == drive.UploadFailed {
		if err := commit(tx, ctx); err != nil {
			return drive.UploadSession{}, err
		}
		return session, nil
	}
	if session.Status != drive.UploadCompleting {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload completion cannot transition to failed")
	}
	session, err = scanUpload(tx.QueryRow(ctx, `
		UPDATE upload_session SET status='failed',failure_code=$4,cleanup_status='pending',revision=revision+1,updated_at=$5
		WHERE tenant_id=$1 AND id=$2 AND completion_digest=$3
		RETURNING `+uploadColumns, identity.TenantID, id, digest, failureCode, now))
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not fail upload completion")
	}
	if err := commit(tx, ctx); err != nil {
		return drive.UploadSession{}, err
	}
	return session, nil
}

func (r *Repository) MarkObjectCompleted(ctx context.Context, identity drive.Identity, id string, _ drive.ObjectInfo, now time.Time) (drive.UploadSession, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not mark upload object complete")
	}
	defer tx.Rollback(ctx)
	session, err := scanUpload(tx.QueryRow(ctx, `
		SELECT `+uploadColumns+` FROM upload_session
		WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, identity.TenantID, id))
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not read upload")
	}
	if session.Status == drive.UploadObjectCompleted || session.Status == drive.UploadCommitted {
		if err := commit(tx, ctx); err != nil {
			return drive.UploadSession{}, err
		}
		return session, nil
	}
	if session.Status != drive.UploadCompleting {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload object cannot be marked complete")
	}
	session, err = scanUpload(tx.QueryRow(ctx, `
		UPDATE upload_session SET status='object_completed',revision=revision+1,updated_at=$3
		WHERE tenant_id=$1 AND id=$2
		RETURNING `+uploadColumns, identity.TenantID, id, now))
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not mark upload object complete")
	}
	if err := commit(tx, ctx); err != nil {
		return drive.UploadSession{}, err
	}
	return session, nil
}

func (r *Repository) CommitUpload(ctx context.Context, command drive.CommitUploadCommand) (drive.CompleteResult, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.CompleteResult{}, false, mapError(err, drive.CodeInternal, "could not commit upload")
	}
	defer tx.Rollback(ctx)
	if _, err := lockNamespace(ctx, tx, command.Identity.TenantID); err != nil {
		return drive.CompleteResult{}, false, err
	}
	session, err := scanUpload(tx.QueryRow(ctx, `
		SELECT `+uploadColumns+` FROM upload_session
		WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.Identity.TenantID, command.SessionID))
	if err != nil {
		return drive.CompleteResult{}, false, mapError(err, drive.CodeInternal, "could not read upload")
	}
	if session.CompletionDigest != command.Digest {
		return drive.CompleteResult{}, false, drive.E(drive.CodeIdempotencyConflict, "completion payload differs from the first request")
	}
	if session.Status == drive.UploadCommitted {
		result, err := loadCompleteResult(ctx, tx, session)
		if err != nil {
			return drive.CompleteResult{}, false, err
		}
		if err := commit(tx, ctx); err != nil {
			return drive.CompleteResult{}, false, err
		}
		return result, false, nil
	}
	if session.Status != drive.UploadObjectCompleted {
		return drive.CompleteResult{}, false, drive.E(drive.CodeInvalidState, "upload object is not completed")
	}
	if _, err := lockActiveDirectory(ctx, tx, command.Identity.TenantID, session.ParentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return rejectUploadCommit(ctx, tx, session, drive.UploadFailureParentUnavailable, command.Now,
				drive.E(drive.CodeNotFound, "parent directory was not found"))
		}
		return drive.CompleteResult{}, false, mapError(err, drive.CodeInternal, "could not lock upload parent")
	}
	var nameExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM file_node
		WHERE tenant_id=$1 AND parent_id=$2 AND normalized_name=$3
		  AND status='active' AND trashed_root_id IS NULL)`,
		command.Identity.TenantID, session.ParentID, session.NormalizedName).Scan(&nameExists); err != nil {
		return drive.CompleteResult{}, false, mapError(err, drive.CodeInternal, "could not validate upload name")
	}
	if nameExists {
		return rejectUploadCommit(ctx, tx, session, drive.UploadFailureNameConflict, command.Now,
			drive.E(drive.CodeNameConflict, "an active item with this name already exists"))
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO blob(
			id,tenant_id,bucket,object_key,size_bytes,mime_type,checksum_algorithm,checksum_value,
			checksum_status,status,reference_count,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		command.Blob.ID, command.Blob.TenantID, command.Blob.Bucket, command.Blob.ObjectKey,
		command.Blob.Size, command.Blob.MimeType, command.Blob.Checksum.Algorithm,
		command.Blob.Checksum.Value, command.Blob.ChecksumStatus, command.Blob.Status,
		command.Blob.ReferenceCount, command.Blob.CreatedAt); err != nil {
		return drive.CompleteResult{}, false, mapError(err, drive.CodeInternal, "could not create blob")
	}
	// Both sides are deferred foreign keys: inserting the version first lets the file node satisfy its shape check.
	if _, err := tx.Exec(ctx, `
		INSERT INTO file_version(
			id,tenant_id,node_id,blob_id,size_bytes,mime_type,checksum_algorithm,checksum_value,created_by,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		command.Version.ID, command.Version.TenantID, command.Version.NodeID, command.Version.BlobID,
		command.Version.Size, command.Version.MimeType, command.Version.Checksum.Algorithm,
		command.Version.Checksum.Value, command.Version.CreatedBy, command.Version.CreatedAt); err != nil {
		return drive.CompleteResult{}, false, mapError(err, drive.CodeInternal, "could not create file version")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO file_node(
			id,tenant_id,parent_id,kind,display_name,normalized_name,current_version_id,
			size_bytes,mime_type,status,revision,created_at,updated_at
		) VALUES($1,$2,$3,'file',$4,$5,$6,$7,$8,'active',1,$9,$9)`,
		command.Node.ID, command.Node.TenantID, command.Node.ParentID, command.Node.DisplayName,
		command.Node.NormalizedName, command.Node.CurrentVersionID, command.Node.Size,
		command.Node.MimeType, command.Node.CreatedAt); err != nil {
		return drive.CompleteResult{}, false, mapError(err, drive.CodeInternal, "could not create file node")
	}
	session, err = scanUpload(tx.QueryRow(ctx, `
		UPDATE upload_session SET status='committed',committed_node_id=$3,
			revision=revision+1,updated_at=$4
		WHERE tenant_id=$1 AND id=$2
		RETURNING `+uploadColumns,
		command.Identity.TenantID, command.SessionID, command.Node.ID, command.Now))
	if err != nil {
		return drive.CompleteResult{}, false, mapError(err, drive.CodeInternal, "could not commit upload session")
	}
	if err := appendAuditTx(ctx, tx, command.Identity.TenantID, command.Identity.PrincipalID, "upload.committed", "node", command.Node.ID, command.Now, map[string]string{"upload_id": session.ID, "kind": string(command.Node.Kind)}); err != nil {
		return drive.CompleteResult{}, false, err
	}
	if err := commit(tx, ctx); err != nil {
		return drive.CompleteResult{}, false, err
	}
	return drive.CompleteResult{Upload: session, Node: command.Node, Blob: command.Blob, Version: command.Version}, true, nil
}

func rejectUploadCommit(
	ctx context.Context,
	tx pgx.Tx,
	session drive.UploadSession,
	failureCode string,
	now time.Time,
	resultErr error,
) (drive.CompleteResult, bool, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE upload_session SET status='failed',failure_code=$4,cleanup_status='pending',revision=revision+1,updated_at=$5
		WHERE tenant_id=$1 AND id=$2 AND completion_digest=$3 AND status='object_completed'`,
		session.TenantID, session.ID, session.CompletionDigest, failureCode, now); err != nil {
		return drive.CompleteResult{}, false, mapError(err, drive.CodeInternal, "could not reject upload commit")
	}
	if err := commit(tx, ctx); err != nil {
		return drive.CompleteResult{}, false, err
	}
	return drive.CompleteResult{}, false, resultErr
}

func loadCompleteResult(ctx context.Context, tx pgx.Tx, session drive.UploadSession) (drive.CompleteResult, error) {
	node, err := scanNode(tx.QueryRow(ctx, `
		SELECT `+nodeColumns+` FROM file_node WHERE tenant_id=$1 AND id=$2`, session.TenantID, session.CommittedNodeID))
	if err != nil {
		return drive.CompleteResult{}, mapError(err, drive.CodeInternal, "could not read committed file")
	}
	version, err := scanVersion(tx.QueryRow(ctx, `
		SELECT `+versionColumns+` FROM file_version WHERE tenant_id=$1 AND id=$2`, session.TenantID, node.CurrentVersionID))
	if err != nil {
		return drive.CompleteResult{}, mapError(err, drive.CodeInternal, "could not read committed version")
	}
	blob, err := scanBlob(tx.QueryRow(ctx, `
		SELECT `+blobColumns+` FROM blob WHERE tenant_id=$1 AND id=$2`, session.TenantID, version.BlobID))
	if err != nil {
		return drive.CompleteResult{}, mapError(err, drive.CodeInternal, "could not read committed blob")
	}
	return drive.CompleteResult{Upload: session, Node: node, Blob: blob, Version: version}, nil
}

func (r *Repository) AbortUpload(ctx context.Context, identity drive.Identity, id string, target drive.UploadStatus, now time.Time) (drive.UploadSession, error) {
	if target != drive.UploadAborted && target != drive.UploadExpired && target != drive.UploadFailed {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "invalid upload terminal state")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not abort upload")
	}
	defer tx.Rollback(ctx)
	session, err := scanUpload(tx.QueryRow(ctx, `
		SELECT `+uploadColumns+` FROM upload_session WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, identity.TenantID, id))
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not read upload")
	}
	if session.Status == drive.UploadCommitted {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "committed upload cannot be aborted")
	}
	if session.Status == drive.UploadAborted || session.Status == drive.UploadExpired || session.Status == drive.UploadFailed {
		if err := commit(tx, ctx); err != nil {
			return drive.UploadSession{}, err
		}
		return session, nil
	}
	if session.Status != drive.UploadCreated && session.Status != drive.UploadUploading {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload session cannot be aborted while completing")
	}
	session, err = scanUpload(tx.QueryRow(ctx, `
		UPDATE upload_session SET status=$3,cleanup_status='pending',revision=revision+1,updated_at=$4
		WHERE tenant_id=$1 AND id=$2 RETURNING `+uploadColumns,
		identity.TenantID, id, target, now))
	if err != nil {
		return drive.UploadSession{}, mapError(err, drive.CodeInternal, "could not abort upload")
	}
	if err := commit(tx, ctx); err != nil {
		return drive.UploadSession{}, err
	}
	return session, nil
}

func (r *Repository) MarkUploadCleanupComplete(ctx context.Context, identity drive.Identity, id string, now time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE upload_session SET cleanup_status='complete',maintenance_not_before=NULL,
			maintenance_error_code='',updated_at=$3
		WHERE tenant_id=$1 AND id=$2 AND status IN ('aborted','expired','failed')`, identity.TenantID, id, now)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not complete upload cleanup")
	}
	if tag.RowsAffected() != 1 {
		return drive.E(drive.CodeInvalidState, "upload cleanup cannot be completed")
	}
	return nil
}

func (r *Repository) ExpiredUploads(ctx context.Context, now time.Time, limit int) ([]drive.UploadSession, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+uploadColumns+` FROM upload_session
		WHERE expires_at <= $1 AND status IN ('created','uploading','completing','object_completed')
		ORDER BY expires_at,id LIMIT $2`, now, limit)
	if err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not list expired uploads")
	}
	defer rows.Close()
	result := make([]drive.UploadSession, 0)
	for rows.Next() {
		session, err := scanUpload(rows)
		if err != nil {
			return nil, mapError(err, drive.CodeInternal, "could not decode expired upload")
		}
		result = append(result, session)
	}
	return result, mapError(rows.Err(), drive.CodeInternal, "could not list expired uploads")
}
