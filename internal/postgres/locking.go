package postgres

import (
	"context"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5"
)

func lockNamespace(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	var rootID string
	err := tx.QueryRow(ctx, `
		SELECT root_node_id::text FROM tenant
		WHERE id=$1 AND root_node_id IS NOT NULL
		FOR NO KEY UPDATE`, tenantID).Scan(&rootID)
	return rootID, mapError(err, drive.CodeInternal, "could not lock tenant namespace")
}

func lockActiveDirectory(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) (drive.Node, error) {
	return scanNode(tx.QueryRow(ctx, `
		SELECT `+nodeColumns+` FROM file_node
		WHERE tenant_id=$1 AND id=$2 AND kind='directory'
		  AND status='active' AND trashed_root_id IS NULL
		FOR UPDATE`, tenantID, nodeID))
}
