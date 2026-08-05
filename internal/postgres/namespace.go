package postgres

import (
	"context"
	"errors"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) EnsureTenant(ctx context.Context, seed drive.TenantSeed) (drive.Tenant, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.Tenant{}, mapError(err, drive.CodeInternal, "could not initialize tenant")
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant(id,display_name,created_at)
		VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, seed.TenantID, seed.DisplayName, seed.Now); err != nil {
		return drive.Tenant{}, mapError(err, drive.CodeInternal, "could not initialize tenant")
	}
	var tenant drive.Tenant
	var rootID *string
	if err := tx.QueryRow(ctx, `
		SELECT id::text,display_name,root_node_id::text,created_at
		FROM tenant WHERE id=$1 FOR UPDATE`, seed.TenantID).Scan(&tenant.ID, &tenant.DisplayName, &rootID, &tenant.CreatedAt); err != nil {
		return drive.Tenant{}, mapError(err, drive.CodeInternal, "could not read tenant")
	}
	if rootID == nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO file_node(
				id,tenant_id,parent_id,kind,display_name,normalized_name,current_version_id,
				size_bytes,mime_type,status,revision,created_at,updated_at
			) VALUES($1,$2,NULL,'directory','','',NULL,0,'','active',1,$3,$3)`,
			seed.RootNodeID, seed.TenantID, seed.Now); err != nil {
			return drive.Tenant{}, mapError(err, drive.CodeInternal, "could not create tenant root")
		}
		if _, err := tx.Exec(ctx, `UPDATE tenant SET root_node_id=$2 WHERE id=$1`, seed.TenantID, seed.RootNodeID); err != nil {
			return drive.Tenant{}, mapError(err, drive.CodeInternal, "could not attach tenant root")
		}
		tenant.RootNodeID = seed.RootNodeID
	} else {
		tenant.RootNodeID = *rootID
	}
	if err := commit(tx, ctx); err != nil {
		return drive.Tenant{}, err
	}
	return tenant, nil
}

func (r *Repository) Tenant(ctx context.Context, tenantID string) (drive.Tenant, error) {
	var tenant drive.Tenant
	err := r.pool.QueryRow(ctx, `
		SELECT id::text,display_name,root_node_id::text,created_at
		FROM tenant WHERE id=$1 AND root_node_id IS NOT NULL`, tenantID,
	).Scan(&tenant.ID, &tenant.DisplayName, &tenant.RootNodeID, &tenant.CreatedAt)
	return tenant, mapError(err, drive.CodeInternal, "could not read tenant")
}

func (r *Repository) CreateDirectory(ctx context.Context, command drive.CreateDirectoryCommand) (drive.Node, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.Node{}, mapError(err, drive.CodeInternal, "could not create directory")
	}
	defer tx.Rollback(ctx)
	if _, err := lockNamespace(ctx, tx, command.Identity.TenantID); err != nil {
		return drive.Node{}, err
	}
	if _, err := lockActiveDirectory(ctx, tx, command.Identity.TenantID, command.ParentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return drive.Node{}, drive.E(drive.CodeNotFound, "parent directory was not found")
		}
		return drive.Node{}, mapError(err, drive.CodeInternal, "could not lock parent directory")
	}
	node, err := scanNode(tx.QueryRow(ctx, `
		INSERT INTO file_node(
			id,tenant_id,parent_id,kind,display_name,normalized_name,current_version_id,
			size_bytes,mime_type,status,revision,created_at,updated_at
		)
		VALUES($1,$2,$6,'directory',$3,$4,NULL,0,'','active',1,$5,$5)
		RETURNING `+nodeColumns,
		command.ID, command.Identity.TenantID, command.DisplayName, command.NormalizedName,
		command.Now, command.ParentID,
	))
	if err != nil {
		return drive.Node{}, mapError(err, drive.CodeInternal, "could not create directory")
	}
	if err := completeIdempotency(ctx, tx, command.Idempotency, node.ID, command.Now); err != nil {
		return drive.Node{}, err
	}
	if err := appendAuditTx(ctx, tx, command.Identity.TenantID, command.Identity.PrincipalID, "node.created", "node", node.ID, command.Now, map[string]string{"kind": string(node.Kind), "parent_id": node.ParentID}); err != nil {
		return drive.Node{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return drive.Node{}, err
	}
	return node, nil
}

func (r *Repository) Node(ctx context.Context, identity drive.Identity, id string, includeTrashed bool) (drive.Node, error) {
	query := `SELECT ` + nodeColumns + ` FROM file_node WHERE tenant_id=$1 AND id=$2`
	if !includeTrashed {
		query += ` AND status='active' AND trashed_root_id IS NULL`
	}
	node, err := scanNode(r.pool.QueryRow(ctx, query, identity.TenantID, id))
	return node, mapError(err, drive.CodeInternal, "could not read node")
}

func (r *Repository) ListChildren(ctx context.Context, identity drive.Identity, parentID string, after drive.CursorPosition, limit int) ([]drive.Node, bool, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM file_node WHERE tenant_id=$1 AND id=$2 AND kind='directory'
			AND status='active' AND trashed_root_id IS NULL
		)`, identity.TenantID, parentID).Scan(&exists); err != nil {
		return nil, false, mapError(err, drive.CodeInternal, "could not read parent directory")
	}
	if !exists {
		return nil, false, drive.E(drive.CodeNotFound, "parent directory was not found")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+nodeColumns+`
		FROM file_node
		WHERE tenant_id=$1 AND parent_id=$2 AND status='active' AND trashed_root_id IS NULL
		  AND ($3='' OR (normalized_name,id) > ($3,$4::uuid))
		ORDER BY normalized_name,id
		LIMIT $5`, identity.TenantID, parentID, after.Name, nullString(after.ID), limit+1)
	if err != nil {
		return nil, false, mapError(err, drive.CodeInternal, "could not list directory")
	}
	defer rows.Close()
	items := make([]drive.Node, 0, limit+1)
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, false, mapError(err, drive.CodeInternal, "could not decode directory item")
		}
		items = append(items, node)
	}
	if err := rows.Err(); err != nil {
		return nil, false, mapError(err, drive.CodeInternal, "could not list directory")
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

func (r *Repository) UpdateNode(ctx context.Context, command drive.UpdateNodeCommand) (drive.Node, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.Node{}, mapError(err, drive.CodeInternal, "could not update node")
	}
	defer tx.Rollback(ctx)
	rootID, err := lockNamespace(ctx, tx, command.Identity.TenantID)
	if err != nil {
		return drive.Node{}, err
	}
	node, err := scanNode(tx.QueryRow(ctx, `
		SELECT `+nodeColumns+` FROM file_node
		WHERE tenant_id=$1 AND id=$2 AND status='active' AND trashed_root_id IS NULL
		FOR UPDATE`, command.Identity.TenantID, command.NodeID))
	if err != nil {
		return drive.Node{}, mapError(err, drive.CodeInternal, "could not read node")
	}
	if node.ID == rootID {
		return drive.Node{}, drive.E(drive.CodeInvalidRequest, "root directory cannot be changed")
	}
	if node.Revision != command.ExpectedRevision {
		return drive.Node{}, drive.E(drive.CodeRevisionMismatch, "resource revision does not match")
	}
	parentID := node.ParentID
	if command.ParentID != nil {
		parentID = *command.ParentID
		if _, err := lockActiveDirectory(ctx, tx, command.Identity.TenantID, parentID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return drive.Node{}, drive.E(drive.CodeNotFound, "target directory was not found")
			}
			return drive.Node{}, mapError(err, drive.CodeInternal, "could not lock target directory")
		}
		if node.Kind == drive.NodeDirectory {
			var createsCycle bool
			if err := tx.QueryRow(ctx, `
				WITH RECURSIVE ancestors(id,parent_id) AS (
					SELECT id,parent_id FROM file_node WHERE tenant_id=$1 AND id=$2
					UNION
					SELECT parent.id,parent.parent_id
					FROM file_node parent JOIN ancestors a ON parent.id=a.parent_id
					WHERE parent.tenant_id=$1
				)
				SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=$3)`,
				command.Identity.TenantID, parentID, node.ID).Scan(&createsCycle); err != nil {
				return drive.Node{}, mapError(err, drive.CodeInternal, "could not validate directory move")
			}
			if createsCycle {
				return drive.Node{}, drive.E(drive.CodeInvalidRequest, "directory cannot be moved into itself or a descendant")
			}
		}
	}
	displayName, normalizedName := node.DisplayName, node.NormalizedName
	if command.DisplayName != nil {
		displayName, normalizedName = *command.DisplayName, *command.NormalizedName
	}
	updated, err := scanNode(tx.QueryRow(ctx, `
		UPDATE file_node SET parent_id=$3,display_name=$4,normalized_name=$5,
			revision=revision+1,updated_at=$6
		WHERE tenant_id=$1 AND id=$2 AND revision=$7
		RETURNING `+nodeColumns,
		command.Identity.TenantID, node.ID, parentID, displayName, normalizedName, command.Now, command.ExpectedRevision))
	if err != nil {
		return drive.Node{}, mapError(err, drive.CodeInternal, "could not update node")
	}
	if err := appendAuditTx(ctx, tx, command.Identity.TenantID, command.Identity.PrincipalID, "node.updated", "node", updated.ID, command.Now, map[string]string{"parent_id": updated.ParentID}); err != nil {
		return drive.Node{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return drive.Node{}, err
	}
	return updated, nil
}
