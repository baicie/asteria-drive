package postgres

import (
	"database/sql"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

type scanner interface {
	Scan(...any) error
}

const nodeColumns = `
id::text, tenant_id::text, COALESCE(parent_id::text,''), kind, display_name,
normalized_name, COALESCE(current_version_id::text,''), size_bytes, mime_type,
status, COALESCE(trashed_root_id::text,''), COALESCE(original_parent_id::text,''),
revision, created_at, updated_at, deleted_at`

func scanNode(row scanner) (drive.Node, error) {
	var node drive.Node
	var kind, status string
	var deletedAt sql.NullTime
	err := row.Scan(
		&node.ID, &node.TenantID, &node.ParentID, &kind, &node.DisplayName,
		&node.NormalizedName, &node.CurrentVersionID, &node.Size, &node.MimeType,
		&status, &node.TrashedRootID, &node.OriginalParentID, &node.Revision,
		&node.CreatedAt, &node.UpdatedAt, &deletedAt,
	)
	node.Kind = drive.NodeKind(kind)
	node.Status = drive.NodeStatus(status)
	if deletedAt.Valid {
		t := deletedAt.Time
		node.DeletedAt = &t
	}
	return node, err
}

const uploadColumns = `
id::text, tenant_id::text, principal_id::text, parent_id::text, display_name,
normalized_name, expected_size, mime_type, declared_checksum_algorithm,
declared_checksum_value, bucket, object_key, storage_upload_id, status,
completion_digest, COALESCE(committed_node_id::text,''), failure_code, part_size,
expires_at, revision, created_at, updated_at`

func scanUpload(row scanner) (drive.UploadSession, error) {
	var session drive.UploadSession
	var status string
	err := row.Scan(
		&session.ID, &session.TenantID, &session.PrincipalID, &session.ParentID,
		&session.DisplayName, &session.NormalizedName, &session.ExpectedSize, &session.MimeType,
		&session.DeclaredChecksum.Algorithm, &session.DeclaredChecksum.Value, &session.Bucket,
		&session.ObjectKey, &session.StorageUploadID, &status, &session.CompletionDigest,
		&session.CommittedNodeID, &session.FailureCode, &session.PartSize, &session.ExpiresAt,
		&session.Revision, &session.CreatedAt, &session.UpdatedAt,
	)
	session.Status = drive.UploadStatus(status)
	return session, err
}

const blobColumns = `
id::text, tenant_id::text, bucket, object_key, size_bytes, mime_type,
checksum_algorithm, checksum_value, checksum_status, status, reference_count,
created_at, deleted_at`

func scanBlob(row scanner) (drive.Blob, error) {
	var blob drive.Blob
	var checksumStatus, status string
	var deletedAt sql.NullTime
	err := row.Scan(
		&blob.ID, &blob.TenantID, &blob.Bucket, &blob.ObjectKey, &blob.Size, &blob.MimeType,
		&blob.Checksum.Algorithm, &blob.Checksum.Value, &checksumStatus, &status,
		&blob.ReferenceCount, &blob.CreatedAt, &deletedAt,
	)
	blob.ChecksumStatus = drive.ChecksumStatus(checksumStatus)
	blob.Status = drive.BlobStatus(status)
	if deletedAt.Valid {
		t := deletedAt.Time
		blob.DeletedAt = &t
	}
	return blob, err
}

const versionColumns = `
id::text, tenant_id::text, node_id::text, blob_id::text, size_bytes, mime_type,
checksum_algorithm, checksum_value, created_by::text, created_at`

func scanVersion(row scanner) (drive.FileVersion, error) {
	var version drive.FileVersion
	err := row.Scan(
		&version.ID, &version.TenantID, &version.NodeID, &version.BlobID,
		&version.Size, &version.MimeType, &version.Checksum.Algorithm,
		&version.Checksum.Value, &version.CreatedBy, &version.CreatedAt,
	)
	return version, err
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

var _ = time.Time{}
