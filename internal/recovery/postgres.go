package recovery

import (
	"context"
	"fmt"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresCatalog struct {
	pool *pgxpool.Pool
}

func NewPostgresCatalog(ctx context.Context, connection string) (*PostgresCatalog, error) {
	pool, err := pgxpool.New(ctx, connection)
	if err != nil {
		return nil, fmt.Errorf("create recovery catalog pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect recovery catalog: %w", err)
	}
	return &PostgresCatalog{pool: pool}, nil
}

func (c *PostgresCatalog) Close() { c.pool.Close() }

func (c *PostgresCatalog) SchemaVersion(ctx context.Context) (int64, error) {
	var version int64
	if err := c.pool.QueryRow(ctx, `SELECT COALESCE(MAX(version),0) FROM asteria_schema_migration`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

func (c *PostgresCatalog) ListAvailableBlobs(ctx context.Context, after BlobCursor, limit int) ([]BlobRecord, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT id::text,tenant_id::text,bucket,object_key,size_bytes,
		       checksum_algorithm,checksum_value,checksum_status
		FROM blob
		WHERE status='available'
		  AND ($1='' OR (tenant_id,id) > (NULLIF($1,'')::uuid,$2::uuid))
		ORDER BY tenant_id,id
		LIMIT $3`, after.TenantID, nullUUID(after.BlobID), limit)
	if err != nil {
		return nil, fmt.Errorf("list available blobs: %w", err)
	}
	defer rows.Close()
	items := make([]BlobRecord, 0, limit)
	for rows.Next() {
		var item BlobRecord
		var checksumStatus string
		if err := rows.Scan(&item.ID, &item.TenantID, &item.Bucket, &item.ObjectKey, &item.Size,
			&item.Checksum.Algorithm, &item.Checksum.Value, &checksumStatus); err != nil {
			return nil, fmt.Errorf("decode available blob: %w", err)
		}
		item.ChecksumStatus = drive.ChecksumStatus(checksumStatus)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate available blobs: %w", err)
	}
	return items, nil
}

func nullUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

var _ Catalog = (*PostgresCatalog)(nil)
