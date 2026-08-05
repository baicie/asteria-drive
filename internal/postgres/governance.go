package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) DeleteMember(ctx context.Context, command drive.DeleteMemberCommand) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not delete member")
	}
	defer tx.Rollback(ctx)
	if err := lockTenant(ctx, tx, command.TenantID); err != nil {
		return err
	}
	if err := authorizeTenantManager(ctx, tx, command.TenantID, command.ActorPrincipalID, command.ActorRole); err != nil {
		return err
	}
	var targetRole, targetStatus string
	if err := tx.QueryRow(ctx, `
		SELECT role,status FROM tenant_member
		WHERE tenant_id=$1 AND principal_id=$2 FOR UPDATE`, command.TenantID, command.PrincipalID).
		Scan(&targetRole, &targetStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return drive.E(drive.CodeNotFound, "tenant member was not found")
		}
		return mapError(err, drive.CodeInternal, "could not lock member")
	}
	if command.ActorRole == drive.RoleAdmin && drive.AccessRole(targetRole) == drive.RoleOwner {
		return drive.E(drive.CodeForbidden, "admins cannot delete owners")
	}
	if drive.AccessRole(targetRole) == drive.RoleOwner && drive.MemberStatus(targetStatus) == drive.MemberStatusActive {
		var remaining int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM tenant_member
			WHERE tenant_id=$1 AND role='owner' AND status='active' AND principal_id<>$2`,
			command.TenantID, command.PrincipalID).Scan(&remaining); err != nil {
			return mapError(err, drive.CodeInternal, "could not count owners")
		}
		if remaining == 0 {
			return drive.E(drive.CodeInvalidState, "the last active owner cannot be removed")
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tenant_member WHERE tenant_id=$1 AND principal_id=$2`, command.TenantID, command.PrincipalID); err != nil {
		return mapError(err, drive.CodeInternal, "could not delete member")
	}
	if err := appendAuditTx(ctx, tx, command.TenantID, command.ActorPrincipalID, "tenant.member.deleted", "principal", command.PrincipalID, command.Now, map[string]string{"role": targetRole}); err != nil {
		return err
	}
	return commit(tx, ctx)
}

func (r *Repository) CreateInvitation(ctx context.Context, command drive.CreateInvitationCommand) (drive.TenantInvitation, error) {
	if !drive.ValidID(command.ID) || !drive.ValidID(command.TenantID) || !drive.ValidID(command.ActorPrincipalID) ||
		!drive.ValidAccessRole(command.ActorRole) || !validInvitationIdentity(command.Issuer, command.Subject) || len(command.DisplayName) > 256 || len(command.TokenHash) != 64 ||
		!command.ExpiresAt.After(command.Now) || !drive.ValidAccessRole(command.Role) {
		return drive.TenantInvitation{}, drive.E(drive.CodeInvalidRequest, "invitation is invalid")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.TenantInvitation{}, mapError(err, drive.CodeInternal, "could not create invitation")
	}
	defer tx.Rollback(ctx)
	if err := lockTenant(ctx, tx, command.TenantID); err != nil {
		return drive.TenantInvitation{}, err
	}
	if err := authorizeTenantManager(ctx, tx, command.TenantID, command.ActorPrincipalID, command.ActorRole); err != nil {
		return drive.TenantInvitation{}, err
	}
	if command.ActorRole == drive.RoleAdmin && command.Role == drive.RoleOwner {
		return drive.TenantInvitation{}, drive.E(drive.CodeForbidden, "admins cannot invite owners")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_invitation(
			id,tenant_id,issuer,subject,display_name,role,token_hash,status,
			created_by,expires_at,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,'pending',$8,$9,$10,$10)`,
		command.ID, command.TenantID, command.Issuer, command.Subject, command.DisplayName,
		command.Role, command.TokenHash, command.ActorPrincipalID, command.ExpiresAt, command.Now); err != nil {
		return drive.TenantInvitation{}, mapError(err, drive.CodeNameConflict, "could not create invitation")
	}
	if err := appendAuditTx(ctx, tx, command.TenantID, command.ActorPrincipalID, "tenant.invitation.created", "invitation", command.ID, command.Now, map[string]string{"role": string(command.Role)}); err != nil {
		return drive.TenantInvitation{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return drive.TenantInvitation{}, err
	}
	return drive.TenantInvitation{
		ID: command.ID, TenantID: command.TenantID, Issuer: command.Issuer,
		Subject: command.Subject, DisplayName: command.DisplayName, Role: command.Role,
		Status: drive.InvitationPending, CreatedBy: command.ActorPrincipalID,
		ExpiresAt: command.ExpiresAt, CreatedAt: command.Now, UpdatedAt: command.Now,
	}, nil
}

func (r *Repository) ListInvitations(ctx context.Context, tenantID string, status drive.InvitationStatus, limit int) ([]drive.TenantInvitation, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text,issuer,subject,display_name,role,
		       CASE WHEN status='pending' AND expires_at<=now() THEN 'expired' ELSE status END,
		       COALESCE(accepted_principal_id::text,''),expires_at,accepted_at,revoked_at,
		       created_by::text,created_at,updated_at
		FROM tenant_invitation
		WHERE tenant_id=$1 AND (
			$2='' OR CASE WHEN status='pending' AND expires_at<=now() THEN 'expired' ELSE status END=$2)
		ORDER BY created_at DESC,id DESC LIMIT $3`, tenantID, status, limit)
	if err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not list invitations")
	}
	defer rows.Close()
	items := make([]drive.TenantInvitation, 0, limit)
	for rows.Next() {
		var item drive.TenantInvitation
		var role, itemStatus string
		if err := rows.Scan(&item.ID, &item.Issuer, &item.Subject, &item.DisplayName, &role, &itemStatus,
			&item.AcceptedPrincipalID, &item.ExpiresAt, &item.AcceptedAt, &item.RevokedAt,
			&item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, mapError(err, drive.CodeInternal, "could not read invitation")
		}
		item.TenantID, item.Role, item.Status = tenantID, drive.AccessRole(role), drive.InvitationStatus(itemStatus)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not list invitations")
	}
	return items, nil
}

func (r *Repository) AcceptInvitation(ctx context.Context, command drive.AcceptInvitationCommand) (drive.TenantInvitation, drive.PrincipalRecord, error) {
	if len(command.TokenHash) != 64 || !drive.ValidID(command.CandidatePrincipalID) || !validInvitationIdentity(command.Issuer, command.Subject) {
		return drive.TenantInvitation{}, drive.PrincipalRecord{}, drive.E(drive.CodeInvalidRequest, "invitation acceptance is invalid")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.TenantInvitation{}, drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not accept invitation")
	}
	defer tx.Rollback(ctx)
	invitation, err := selectInvitationForUpdate(ctx, tx, command.TokenHash)
	if err != nil {
		return drive.TenantInvitation{}, drive.PrincipalRecord{}, err
	}
	if invitation.Issuer != command.Issuer || invitation.Subject != command.Subject {
		return drive.TenantInvitation{}, drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "invitation identity does not match the authenticated identity")
	}
	if invitation.Status == drive.InvitationAccepted {
		member, err := selectMember(ctx, tx, invitation.TenantID, invitation.AcceptedPrincipalID)
		if err != nil {
			return invitation, drive.PrincipalRecord{}, err
		}
		if member.Issuer != command.Issuer || member.Subject != command.Subject {
			return drive.TenantInvitation{}, drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "invitation identity does not match the accepted member")
		}
		return invitation, member, commit(tx, ctx)
	}
	if invitation.Status != drive.InvitationPending {
		return invitation, drive.PrincipalRecord{}, drive.E(drive.CodeInvalidState, "invitation is not pending")
	}
	if !invitation.ExpiresAt.After(command.Now) {
		if _, err := tx.Exec(ctx, `UPDATE tenant_invitation SET status='expired',updated_at=$2 WHERE id=$1`, invitation.ID, command.Now); err != nil {
			return invitation, drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not expire invitation")
		}
		invitation.Status, invitation.UpdatedAt = drive.InvitationExpired, command.Now
		if err := commit(tx, ctx); err != nil {
			return invitation, drive.PrincipalRecord{}, err
		}
		return invitation, drive.PrincipalRecord{}, drive.E(drive.CodeInvalidState, "invitation has expired")
	}
	if err := lockTenant(ctx, tx, invitation.TenantID); err != nil {
		return invitation, drive.PrincipalRecord{}, err
	}
	principalID := ""
	err = tx.QueryRow(ctx, `SELECT id::text FROM principal WHERE issuer=$1 AND subject=$2 FOR UPDATE`, command.Issuer, command.Subject).Scan(&principalID)
	if errors.Is(err, pgx.ErrNoRows) {
		principalID = command.CandidatePrincipalID
		if _, err := tx.Exec(ctx, `
			INSERT INTO principal(id,issuer,subject,display_name,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$5)`, principalID, command.Issuer, command.Subject, invitation.DisplayName, command.Now); err != nil {
			return invitation, drive.PrincipalRecord{}, mapError(err, drive.CodeNameConflict, "could not create invitation principal")
		}
	} else if err != nil {
		return invitation, drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not resolve invitation principal")
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_member WHERE tenant_id=$1 AND principal_id=$2)`, invitation.TenantID, principalID).Scan(&exists); err != nil {
		return invitation, drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not check invitation membership")
	}
	if exists {
		return invitation, drive.PrincipalRecord{}, drive.E(drive.CodeInvalidState, "the authenticated identity is already a tenant member")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_member(tenant_id,principal_id,role,status,created_at,updated_at)
		VALUES($1,$2,$3,'active',$4,$4)`, invitation.TenantID, principalID, invitation.Role, command.Now); err != nil {
		return invitation, drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not create invitation membership")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tenant_invitation SET status='accepted',accepted_principal_id=$2,
		accepted_at=$3,updated_at=$3 WHERE id=$1`, invitation.ID, principalID, command.Now); err != nil {
		return invitation, drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not accept invitation")
	}
	if err := appendAuditTx(ctx, tx, invitation.TenantID, principalID, "tenant.invitation.accepted", "invitation", invitation.ID, command.Now, map[string]string{"principal_id": principalID}); err != nil {
		return invitation, drive.PrincipalRecord{}, err
	}
	member, err := selectMember(ctx, tx, invitation.TenantID, principalID)
	if err != nil {
		return invitation, drive.PrincipalRecord{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return invitation, drive.PrincipalRecord{}, err
	}
	invitation.Status, invitation.AcceptedPrincipalID = drive.InvitationAccepted, principalID
	invitation.AcceptedAt, invitation.UpdatedAt = &command.Now, command.Now
	return invitation, member, nil
}

func (r *Repository) RevokeInvitation(ctx context.Context, command drive.RevokeInvitationCommand) (drive.TenantInvitation, error) {
	if !drive.ValidID(command.TenantID) || !drive.ValidID(command.InvitationID) || !drive.ValidID(command.ActorPrincipalID) || !drive.ValidAccessRole(command.ActorRole) {
		return drive.TenantInvitation{}, drive.E(drive.CodeInvalidRequest, "invitation revocation is invalid")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.TenantInvitation{}, mapError(err, drive.CodeInternal, "could not revoke invitation")
	}
	defer tx.Rollback(ctx)
	if err := lockTenant(ctx, tx, command.TenantID); err != nil {
		return drive.TenantInvitation{}, err
	}
	if err := authorizeTenantManager(ctx, tx, command.TenantID, command.ActorPrincipalID, command.ActorRole); err != nil {
		return drive.TenantInvitation{}, err
	}
	var tokenHash string
	if err := tx.QueryRow(ctx, `SELECT token_hash FROM tenant_invitation WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.InvitationID).Scan(&tokenHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return drive.TenantInvitation{}, drive.E(drive.CodeNotFound, "invitation was not found")
		}
		return drive.TenantInvitation{}, mapError(err, drive.CodeInternal, "could not lock invitation")
	}
	invitation, err := selectInvitationForUpdate(ctx, tx, tokenHash)
	if err != nil {
		return drive.TenantInvitation{}, err
	}
	if invitation.Status != drive.InvitationPending || !invitation.ExpiresAt.After(command.Now) {
		return drive.TenantInvitation{}, drive.E(drive.CodeInvalidState, "invitation is not pending")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tenant_invitation SET status='revoked',revoked_at=$2,updated_at=$2 WHERE id=$1`, invitation.ID, command.Now); err != nil {
		return drive.TenantInvitation{}, mapError(err, drive.CodeInternal, "could not revoke invitation")
	}
	if err := appendAuditTx(ctx, tx, command.TenantID, command.ActorPrincipalID, "tenant.invitation.revoked", "invitation", invitation.ID, command.Now, nil); err != nil {
		return drive.TenantInvitation{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return drive.TenantInvitation{}, err
	}
	invitation.Status, invitation.RevokedAt, invitation.UpdatedAt = drive.InvitationRevoked, &command.Now, command.Now
	return invitation, nil
}

func (r *Repository) CreateGroup(ctx context.Context, command drive.CreateGroupCommand) (drive.TenantGroup, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.TenantGroup{}, mapError(err, drive.CodeInternal, "could not create group")
	}
	defer tx.Rollback(ctx)
	if err := lockTenant(ctx, tx, command.TenantID); err != nil {
		return drive.TenantGroup{}, err
	}
	if err := authorizeTenantManager(ctx, tx, command.TenantID, command.ActorPrincipalID, command.ActorRole); err != nil {
		return drive.TenantGroup{}, err
	}
	group := drive.TenantGroup{
		ID: command.ID, TenantID: command.TenantID, DisplayName: command.DisplayName,
		NormalizedName: command.NormalizedName, CreatedBy: command.ActorPrincipalID,
		CreatedAt: command.Now, UpdatedAt: command.Now,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_group(id,tenant_id,display_name,normalized_name,created_by,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$6)`, group.ID, group.TenantID, group.DisplayName,
		group.NormalizedName, group.CreatedBy, group.CreatedAt); err != nil {
		return drive.TenantGroup{}, mapError(err, drive.CodeNameConflict, "group name already exists")
	}
	if err := appendAuditTx(ctx, tx, group.TenantID, command.ActorPrincipalID, "tenant.group.created", "group", group.ID, command.Now, map[string]string{"name": group.DisplayName}); err != nil {
		return drive.TenantGroup{}, err
	}
	return group, commit(tx, ctx)
}

func (r *Repository) ListGroups(ctx context.Context, tenantID string) ([]drive.TenantGroup, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text,tenant_id::text,display_name,normalized_name,created_by::text,created_at,updated_at
		FROM tenant_group WHERE tenant_id=$1 ORDER BY normalized_name,id`, tenantID)
	if err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not list groups")
	}
	defer rows.Close()
	items := []drive.TenantGroup{}
	for rows.Next() {
		var group drive.TenantGroup
		if err := rows.Scan(&group.ID, &group.TenantID, &group.DisplayName, &group.NormalizedName,
			&group.CreatedBy, &group.CreatedAt, &group.UpdatedAt); err != nil {
			return nil, mapError(err, drive.CodeInternal, "could not read group")
		}
		items = append(items, group)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not list groups")
	}
	return items, nil
}

func (r *Repository) UpdateGroup(ctx context.Context, command drive.UpdateGroupCommand) (drive.TenantGroup, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.TenantGroup{}, mapError(err, drive.CodeInternal, "could not update group")
	}
	defer tx.Rollback(ctx)
	if err := lockTenant(ctx, tx, command.TenantID); err != nil {
		return drive.TenantGroup{}, err
	}
	if err := authorizeTenantManager(ctx, tx, command.TenantID, command.ActorPrincipalID, command.ActorRole); err != nil {
		return drive.TenantGroup{}, err
	}
	var group drive.TenantGroup
	err = tx.QueryRow(ctx, `
		UPDATE tenant_group SET display_name=$3,normalized_name=$4,updated_at=$5
		WHERE tenant_id=$1 AND id=$2
		RETURNING id::text,tenant_id::text,display_name,normalized_name,created_by::text,created_at,updated_at`,
		command.TenantID, command.GroupID, command.DisplayName, command.NormalizedName, command.Now).
		Scan(&group.ID, &group.TenantID, &group.DisplayName, &group.NormalizedName,
			&group.CreatedBy, &group.CreatedAt, &group.UpdatedAt)
	if err != nil {
		return drive.TenantGroup{}, mapError(err, drive.CodeNameConflict, "could not update group")
	}
	if err := appendAuditTx(ctx, tx, group.TenantID, command.ActorPrincipalID, "tenant.group.updated", "group", group.ID, command.Now, map[string]string{"name": group.DisplayName}); err != nil {
		return drive.TenantGroup{}, err
	}
	return group, commit(tx, ctx)
}

func (r *Repository) DeleteGroup(ctx context.Context, command drive.DeleteGroupCommand) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not delete group")
	}
	defer tx.Rollback(ctx)
	if err := lockTenant(ctx, tx, command.TenantID); err != nil {
		return err
	}
	if err := authorizeTenantManager(ctx, tx, command.TenantID, command.ActorPrincipalID, command.ActorRole); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM tenant_group WHERE tenant_id=$1 AND id=$2`, command.TenantID, command.GroupID)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not delete group")
	}
	if tag.RowsAffected() == 0 {
		return drive.E(drive.CodeNotFound, "group was not found")
	}
	if err := appendAuditTx(ctx, tx, command.TenantID, command.ActorPrincipalID, "tenant.group.deleted", "group", command.GroupID, command.Now, nil); err != nil {
		return err
	}
	return commit(tx, ctx)
}

func (r *Repository) AddGroupMember(ctx context.Context, command drive.GroupMemberCommand) error {
	return r.changeGroupMember(ctx, command, true)
}

func (r *Repository) RemoveGroupMember(ctx context.Context, command drive.GroupMemberCommand) error {
	return r.changeGroupMember(ctx, command, false)
}

func (r *Repository) changeGroupMember(ctx context.Context, command drive.GroupMemberCommand, add bool) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not change group member")
	}
	defer tx.Rollback(ctx)
	if err := lockTenant(ctx, tx, command.TenantID); err != nil {
		return err
	}
	if err := authorizeTenantManager(ctx, tx, command.TenantID, command.ActorPrincipalID, command.ActorRole); err != nil {
		return err
	}
	var groupExists, memberExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_group WHERE tenant_id=$1 AND id=$2)`, command.TenantID, command.GroupID).Scan(&groupExists); err != nil {
		return mapError(err, drive.CodeInternal, "could not check group")
	}
	if !groupExists {
		return drive.E(drive.CodeNotFound, "group was not found")
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_member WHERE tenant_id=$1 AND principal_id=$2)`, command.TenantID, command.PrincipalID).Scan(&memberExists); err != nil {
		return mapError(err, drive.CodeInternal, "could not check group member")
	}
	if !memberExists {
		return drive.E(drive.CodeNotFound, "tenant member was not found")
	}
	action := "tenant.group.member_added"
	var changed int64
	if add {
		tag, execErr := tx.Exec(ctx, `
			INSERT INTO tenant_group_member(tenant_id,group_id,principal_id,added_by,created_at)
			VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, command.TenantID, command.GroupID,
			command.PrincipalID, command.ActorPrincipalID, command.Now)
		err, changed = execErr, tag.RowsAffected()
	} else {
		action = "tenant.group.member_removed"
		tag, execErr := tx.Exec(ctx, `
			DELETE FROM tenant_group_member WHERE tenant_id=$1 AND group_id=$2 AND principal_id=$3`,
			command.TenantID, command.GroupID, command.PrincipalID)
		err, changed = execErr, tag.RowsAffected()
	}
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not change group member")
	}
	if changed > 0 {
		if err := appendAuditTx(ctx, tx, command.TenantID, command.ActorPrincipalID, action, "group", command.GroupID, command.Now, map[string]string{"principal_id": command.PrincipalID}); err != nil {
			return err
		}
	}
	return commit(tx, ctx)
}

func (r *Repository) ListGroupMembers(ctx context.Context, tenantID, groupID string) ([]drive.PrincipalRecord, error) {
	var groupExists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_group WHERE tenant_id=$1 AND id=$2)`, tenantID, groupID).Scan(&groupExists); err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not check group")
	}
	if !groupExists {
		return nil, drive.E(drive.CodeNotFound, "group was not found")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT p.id::text,p.issuer,p.subject,p.display_name,t.display_name,tm.role,tm.status
		FROM tenant_group_member gm
		JOIN tenant_member tm ON tm.tenant_id=gm.tenant_id AND tm.principal_id=gm.principal_id
		JOIN principal p ON p.id=tm.principal_id
		JOIN tenant t ON t.id=tm.tenant_id
		WHERE gm.tenant_id=$1 AND gm.group_id=$2 ORDER BY p.id`, tenantID, groupID)
	if err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not list group members")
	}
	defer rows.Close()
	items := []drive.PrincipalRecord{}
	for rows.Next() {
		var item drive.PrincipalRecord
		var role, status string
		if err := rows.Scan(&item.Identity.PrincipalID, &item.Issuer, &item.Subject, &item.DisplayName,
			&item.TenantDisplayName, &role, &status); err != nil {
			return nil, mapError(err, drive.CodeInternal, "could not read group member")
		}
		item.Identity.TenantID, item.Role, item.Status = tenantID, drive.AccessRole(role), drive.MemberStatus(status)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not list group members")
	}
	return items, nil
}

func (r *Repository) ListNodeACL(ctx context.Context, tenantID, nodeID string) ([]drive.NodeACLEntry, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM file_node WHERE tenant_id=$1 AND id=$2)`, tenantID, nodeID).Scan(&exists); err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not check node")
	}
	if !exists {
		return nil, drive.E(drive.CodeNotFound, "node was not found")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text,tenant_id::text,node_id::text,subject_type,
		       COALESCE(principal_id::text,group_id::text),role,created_by::text,created_at,updated_at
		FROM node_acl WHERE tenant_id=$1 AND node_id=$2 ORDER BY subject_type,COALESCE(principal_id,group_id),id`, tenantID, nodeID)
	if err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not list node ACL")
	}
	defer rows.Close()
	items := []drive.NodeACLEntry{}
	for rows.Next() {
		var item drive.NodeACLEntry
		var subjectType, role string
		if err := rows.Scan(&item.ID, &item.TenantID, &item.NodeID, &subjectType, &item.SubjectID,
			&role, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, mapError(err, drive.CodeInternal, "could not read node ACL")
		}
		item.SubjectType, item.Role = drive.ACLSubjectType(subjectType), drive.ACLRole(role)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not list node ACL")
	}
	return items, nil
}

func (r *Repository) SetNodeACL(ctx context.Context, command drive.SetNodeACLCommand) (drive.NodeACLEntry, error) {
	if !drive.ValidACLSubjectType(command.SubjectType) || !drive.ValidACLRole(command.Role) {
		return drive.NodeACLEntry{}, drive.E(drive.CodeInvalidRequest, "ACL is invalid")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.NodeACLEntry{}, mapError(err, drive.CodeInternal, "could not set node ACL")
	}
	defer tx.Rollback(ctx)
	if err := lockTenant(ctx, tx, command.TenantID); err != nil {
		return drive.NodeACLEntry{}, err
	}
	if err := authorizeNodeTx(ctx, tx, command.TenantID, command.ActorPrincipalID, command.ActorRole, command.NodeID, drive.NodeCapabilityManageACL); err != nil {
		return drive.NodeACLEntry{}, err
	}
	principalID, groupID := any(nil), any(nil)
	if command.SubjectType == drive.ACLSubjectPrincipal {
		principalID = command.SubjectID
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_member WHERE tenant_id=$1 AND principal_id=$2)`, command.TenantID, command.SubjectID).Scan(&exists); err != nil {
			return drive.NodeACLEntry{}, mapError(err, drive.CodeInternal, "could not check ACL principal")
		}
		if !exists {
			return drive.NodeACLEntry{}, drive.E(drive.CodeNotFound, "ACL principal was not found")
		}
	} else {
		groupID = command.SubjectID
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_group WHERE tenant_id=$1 AND id=$2)`, command.TenantID, command.SubjectID).Scan(&exists); err != nil {
			return drive.NodeACLEntry{}, mapError(err, drive.CodeInternal, "could not check ACL group")
		}
		if !exists {
			return drive.NodeACLEntry{}, drive.E(drive.CodeNotFound, "ACL group was not found")
		}
	}
	var item drive.NodeACLEntry
	var subjectType, role string
	if command.SubjectType == drive.ACLSubjectPrincipal {
		err = tx.QueryRow(ctx, `
			INSERT INTO node_acl(id,tenant_id,node_id,subject_type,principal_id,group_id,role,created_by,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
			ON CONFLICT (tenant_id,node_id,principal_id) WHERE subject_type='principal'
			DO UPDATE SET role=EXCLUDED.role,updated_at=EXCLUDED.updated_at
			RETURNING id::text,tenant_id::text,node_id::text,subject_type,
			          COALESCE(principal_id::text,group_id::text),role,created_by::text,created_at,updated_at`,
			command.ID, command.TenantID, command.NodeID, command.SubjectType, principalID, groupID,
			command.Role, command.ActorPrincipalID, command.Now).
			Scan(&item.ID, &item.TenantID, &item.NodeID, &subjectType, &item.SubjectID,
				&role, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt)
	} else {
		err = tx.QueryRow(ctx, `
			INSERT INTO node_acl(id,tenant_id,node_id,subject_type,principal_id,group_id,role,created_by,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
			ON CONFLICT (tenant_id,node_id,group_id) WHERE subject_type='group'
			DO UPDATE SET role=EXCLUDED.role,updated_at=EXCLUDED.updated_at
			RETURNING id::text,tenant_id::text,node_id::text,subject_type,
			          COALESCE(principal_id::text,group_id::text),role,created_by::text,created_at,updated_at`,
			command.ID, command.TenantID, command.NodeID, command.SubjectType, principalID, groupID,
			command.Role, command.ActorPrincipalID, command.Now).
			Scan(&item.ID, &item.TenantID, &item.NodeID, &subjectType, &item.SubjectID,
				&role, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt)
	}
	if err != nil {
		return drive.NodeACLEntry{}, mapError(err, drive.CodeInternal, "could not set node ACL")
	}
	item.SubjectType, item.Role = drive.ACLSubjectType(subjectType), drive.ACLRole(role)
	if err := appendAuditTx(ctx, tx, command.TenantID, command.ActorPrincipalID, "node.acl.set", "node_acl", item.ID, command.Now, map[string]string{"node_id": command.NodeID, "role": string(command.Role), "subject_type": string(command.SubjectType), "subject_id": command.SubjectID}); err != nil {
		return drive.NodeACLEntry{}, err
	}
	return item, commit(tx, ctx)
}

func (r *Repository) DeleteNodeACL(ctx context.Context, command drive.DeleteNodeACLCommand) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not delete node ACL")
	}
	defer tx.Rollback(ctx)
	if err := lockTenant(ctx, tx, command.TenantID); err != nil {
		return err
	}
	if err := authorizeNodeTx(ctx, tx, command.TenantID, command.ActorPrincipalID, command.ActorRole, command.NodeID, drive.NodeCapabilityManageACL); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM node_acl WHERE tenant_id=$1 AND node_id=$2 AND id=$3`, command.TenantID, command.NodeID, command.EntryID)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not delete node ACL")
	}
	if tag.RowsAffected() == 0 {
		return drive.E(drive.CodeNotFound, "ACL entry was not found")
	}
	if err := appendAuditTx(ctx, tx, command.TenantID, command.ActorPrincipalID, "node.acl.deleted", "node_acl", command.EntryID, command.Now, map[string]string{"node_id": command.NodeID}); err != nil {
		return err
	}
	return commit(tx, ctx)
}

func (r *Repository) AuthorizeNode(ctx context.Context, identity drive.Identity, nodeID string, capability drive.NodeCapability) error {
	return authorizeNodeQuery(ctx, r.pool, identity.TenantID, identity.PrincipalID, "", nodeID, capability)
}

func (r *Repository) AppendAudit(ctx context.Context, event drive.AuditEvent) error {
	metadata, err := encodeAuditMetadata(event.Metadata)
	if err != nil {
		return err
	}
	if event.RequestID == "" {
		event.RequestID = drive.RequestIDFromContext(ctx)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO audit_event(id,tenant_id,actor_principal_id,action,target_type,target_id,request_id,metadata,occurred_at)
		VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,NULLIF($6,'')::uuid,$7,$8::jsonb,$9)`,
		event.ID, event.TenantID, event.ActorPrincipalID, event.Action, event.TargetType,
		event.TargetID, event.RequestID, metadata, event.OccurredAt)
	return mapError(err, drive.CodeInternal, "could not append audit event")
}

func (r *Repository) ListAudit(ctx context.Context, filter drive.AuditFilter) ([]drive.AuditEvent, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT sequence,id::text,tenant_id::text,COALESCE(actor_principal_id::text,''),
		       action,target_type,COALESCE(target_id::text,''),request_id,metadata,occurred_at
		FROM audit_event
		WHERE tenant_id=$1 AND sequence>$2
		  AND ($3::timestamptz IS NULL OR occurred_at >= $3)
		  AND ($4::timestamptz IS NULL OR occurred_at < $4)
		ORDER BY sequence LIMIT $5`, filter.TenantID, filter.AfterSequence,
		nullTime(filter.From), nullTime(filter.Until), limit)
	if err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not list audit events")
	}
	defer rows.Close()
	items := make([]drive.AuditEvent, 0, limit)
	for rows.Next() {
		var item drive.AuditEvent
		var metadata []byte
		if err := rows.Scan(&item.Sequence, &item.ID, &item.TenantID, &item.ActorPrincipalID,
			&item.Action, &item.TargetType, &item.TargetID, &item.RequestID, &metadata, &item.OccurredAt); err != nil {
			return nil, mapError(err, drive.CodeInternal, "could not read audit event")
		}
		if err := json.Unmarshal(metadata, &item.Metadata); err != nil {
			return nil, drive.E(drive.CodeInternal, "stored audit metadata is invalid", err)
		}
		if item.Metadata == nil {
			item.Metadata = map[string]string{}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, drive.CodeInternal, "could not list audit events")
	}
	return items, nil
}

func lockTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tenant WHERE id=$1 FOR UPDATE`, tenantID).Scan(&id); err != nil {
		return mapError(err, drive.CodeNotFound, "tenant was not found")
	}
	return nil
}

func authorizeTenantManager(ctx context.Context, tx pgx.Tx, tenantID, principalID string, declared drive.AccessRole) error {
	var role, status string
	if err := tx.QueryRow(ctx, `
		SELECT role,status FROM tenant_member
		WHERE tenant_id=$1 AND principal_id=$2 FOR UPDATE`, tenantID, principalID).Scan(&role, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return drive.E(drive.CodeForbidden, "only active owners and admins may manage tenant governance")
		}
		return mapError(err, drive.CodeInternal, "could not authorize tenant manager")
	}
	if drive.AccessRole(role) != declared || drive.MemberStatus(status) != drive.MemberStatusActive ||
		(declared != drive.RoleOwner && declared != drive.RoleAdmin) {
		return drive.E(drive.CodeForbidden, "only active owners and admins may manage tenant governance")
	}
	return nil
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func authorizeNodeQuery(ctx context.Context, q queryer, tenantID, principalID string, declared drive.AccessRole, nodeID string, capability drive.NodeCapability) error {
	if !drive.ValidNodeCapability(capability) {
		return drive.E(drive.CodeInvalidRequest, "node capability is invalid")
	}
	var role, status string
	if err := q.QueryRow(ctx, `SELECT role,status FROM tenant_member WHERE tenant_id=$1 AND principal_id=$2`, tenantID, principalID).Scan(&role, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return drive.E(drive.CodeForbidden, "node access denied")
		}
		return mapError(err, drive.CodeInternal, "could not authorize node")
	}
	if declared != "" && drive.AccessRole(role) != declared || drive.MemberStatus(status) != drive.MemberStatusActive {
		return drive.E(drive.CodeForbidden, "node access denied")
	}
	var nodeExists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM file_node WHERE tenant_id=$1 AND id=$2)`, tenantID, nodeID).Scan(&nodeExists); err != nil {
		return mapError(err, drive.CodeInternal, "could not authorize node")
	}
	if !nodeExists {
		return drive.E(drive.CodeNotFound, "node was not found")
	}
	required := capabilityLevel(capability)
	implicit := map[drive.AccessRole]int{drive.RoleOwner: 3, drive.RoleAdmin: 3, drive.RoleEditor: 2, drive.RoleViewer: 1}[drive.AccessRole(role)]
	if implicit >= required {
		return nil
	}
	var granted int
	err := q.QueryRow(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT id,parent_id FROM file_node WHERE tenant_id=$1 AND id=$2
			UNION ALL
			SELECT parent.id,parent.parent_id FROM file_node parent
			JOIN ancestors child ON child.parent_id=parent.id
			WHERE parent.tenant_id=$1
		)
		SELECT COALESCE(MAX(CASE acl.role WHEN 'reader' THEN 1 WHEN 'contributor' THEN 2 WHEN 'manager' THEN 3 ELSE 0 END),0)
		FROM ancestors a JOIN node_acl acl ON acl.tenant_id=$1 AND acl.node_id=a.id
		WHERE (acl.subject_type='principal' AND acl.principal_id=$3)
		   OR (acl.subject_type='group' AND EXISTS(
			SELECT 1 FROM tenant_group_member gm
			WHERE gm.tenant_id=$1 AND gm.group_id=acl.group_id AND gm.principal_id=$3))`,
		tenantID, nodeID, principalID).Scan(&granted)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not evaluate node ACL")
	}
	if granted < required {
		return drive.E(drive.CodeForbidden, "node access denied")
	}
	return nil
}

func authorizeNodeTx(ctx context.Context, tx pgx.Tx, tenantID, principalID string, declared drive.AccessRole, nodeID string, capability drive.NodeCapability) error {
	return authorizeNodeQuery(ctx, tx, tenantID, principalID, declared, nodeID, capability)
}

func capabilityLevel(capability drive.NodeCapability) int {
	return map[drive.NodeCapability]int{
		drive.NodeCapabilityRead: 1, drive.NodeCapabilityWrite: 2,
		drive.NodeCapabilityDelete: 3, drive.NodeCapabilityManageACL: 3,
	}[capability]
}

func appendAuditTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, action, targetType, targetID string, occurredAt time.Time, metadata map[string]string) error {
	id, err := drive.NewID()
	if err != nil {
		return drive.E(drive.CodeInternal, "could not generate audit event id", err)
	}
	encoded, err := encodeAuditMetadata(metadata)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_event(id,tenant_id,actor_principal_id,action,target_type,target_id,request_id,metadata,occurred_at)
		VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,NULLIF($6,'')::uuid,$7,$8::jsonb,$9)`,
		id, tenantID, actorID, action, targetType, targetID, drive.RequestIDFromContext(ctx), encoded, occurredAt)
	return mapError(err, drive.CodeInternal, "could not append audit event")
}

func validInvitationIdentity(issuer, subject string) bool {
	return issuer != "" && subject != "" && strings.TrimSpace(issuer) != "" && strings.TrimSpace(subject) != "" && len(issuer) <= 512 && len(subject) <= 512
}

func encodeAuditMetadata(metadata map[string]string) ([]byte, error) {
	if metadata == nil {
		metadata = map[string]string{}
	}
	for key, value := range metadata {
		if key == "" || len(key) > 128 || len(value) > 2048 {
			return nil, drive.E(drive.CodeInvalidRequest, "audit metadata is invalid")
		}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, drive.E(drive.CodeInvalidRequest, "audit metadata is invalid", err)
	}
	if len(encoded) > 4096 {
		return nil, drive.E(drive.CodeInvalidRequest, "audit metadata is too large")
	}
	return encoded, nil
}

func selectInvitationForUpdate(ctx context.Context, tx pgx.Tx, tokenHash string) (drive.TenantInvitation, error) {
	var item drive.TenantInvitation
	var role, status string
	err := tx.QueryRow(ctx, `
		SELECT id::text,tenant_id::text,issuer,subject,display_name,role,status,
		       COALESCE(accepted_principal_id::text,''),created_by::text,expires_at,
		       accepted_at,revoked_at,created_at,updated_at
		FROM tenant_invitation WHERE token_hash=$1 FOR UPDATE`, tokenHash).
		Scan(&item.ID, &item.TenantID, &item.Issuer, &item.Subject, &item.DisplayName,
			&role, &status, &item.AcceptedPrincipalID, &item.CreatedBy, &item.ExpiresAt,
			&item.AcceptedAt, &item.RevokedAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return drive.TenantInvitation{}, drive.E(drive.CodeNotFound, "invitation was not found")
	}
	if err != nil {
		return drive.TenantInvitation{}, mapError(err, drive.CodeInternal, "could not read invitation")
	}
	item.Role, item.Status = drive.AccessRole(role), drive.InvitationStatus(status)
	return item, nil
}

func selectMember(ctx context.Context, tx pgx.Tx, tenantID, principalID string) (drive.PrincipalRecord, error) {
	var member drive.PrincipalRecord
	var role, status string
	err := tx.QueryRow(ctx, `
		SELECT p.id::text,p.issuer,p.subject,p.display_name,t.display_name,tm.role,tm.status
		FROM tenant_member tm JOIN principal p ON p.id=tm.principal_id JOIN tenant t ON t.id=tm.tenant_id
		WHERE tm.tenant_id=$1 AND tm.principal_id=$2`, tenantID, principalID).
		Scan(&member.Identity.PrincipalID, &member.Issuer, &member.Subject, &member.DisplayName,
			&member.TenantDisplayName, &role, &status)
	if err != nil {
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not read tenant member")
	}
	member.Identity.TenantID, member.Role, member.Status = tenantID, drive.AccessRole(role), drive.MemberStatus(status)
	return member, nil
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
