package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) EnsureOIDCMember(ctx context.Context, seed drive.OIDCMemberSeed) (drive.PrincipalRecord, error) {
	if seed.TenantID == "" || seed.PrincipalID == "" || seed.Issuer == "" || seed.Subject == "" || !drive.ValidAccessRole(seed.Role) {
		return drive.PrincipalRecord{}, drive.E(drive.CodeInvalidRequest, "OIDC member seed is invalid")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not bootstrap OIDC member")
	}
	defer tx.Rollback(ctx)
	var tenantName string
	if err := tx.QueryRow(ctx, `
		SELECT display_name FROM tenant WHERE id=$1 FOR NO KEY UPDATE`, seed.TenantID).Scan(&tenantName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return drive.PrincipalRecord{}, drive.E(drive.CodeNotFound, "tenant was not found")
		}
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not read tenant for OIDC member")
	}
	var existingID string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM principal WHERE issuer=$1 AND subject=$2 FOR UPDATE`, seed.Issuer, seed.Subject).
		Scan(&existingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not read OIDC principal")
	}
	if err == nil && existingID != seed.PrincipalID {
		return drive.PrincipalRecord{}, drive.E(drive.CodeNameConflict, "OIDC identity is mapped to another principal")
	}
	var mappedIssuer, mappedSubject string
	err = tx.QueryRow(ctx, `
		SELECT issuer,subject FROM principal WHERE id=$1 FOR UPDATE`, seed.PrincipalID).
		Scan(&mappedIssuer, &mappedSubject)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not read principal mapping")
	}
	if err == nil && (mappedIssuer != seed.Issuer || mappedSubject != seed.Subject) {
		return drive.PrincipalRecord{}, drive.E(drive.CodeNameConflict, "principal id is mapped to another OIDC identity")
	}
	if errors.Is(err, pgx.ErrNoRows) && existingID == "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO principal(id,issuer,subject,display_name,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$5)`, seed.PrincipalID, seed.Issuer, seed.Subject, seed.DisplayName, seed.Now); err != nil {
			return drive.PrincipalRecord{}, mapError(err, drive.CodeNameConflict, "could not create OIDC principal")
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_member(tenant_id,principal_id,role,status,created_at,updated_at)
		VALUES($1,$2,$3,'active',$4,$4)
		ON CONFLICT(tenant_id,principal_id) DO UPDATE SET role=EXCLUDED.role,status='active',updated_at=EXCLUDED.updated_at`,
		seed.TenantID, seed.PrincipalID, seed.Role, seed.Now); err != nil {
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not bootstrap tenant member")
	}
	if err := commit(tx, ctx); err != nil {
		return drive.PrincipalRecord{}, err
	}
	return drive.PrincipalRecord{
		Identity: drive.Identity{TenantID: seed.TenantID, PrincipalID: seed.PrincipalID},
		Issuer:   seed.Issuer, Subject: seed.Subject, DisplayName: seed.DisplayName,
		TenantDisplayName: tenantName, Role: seed.Role, Status: drive.MemberStatusActive,
	}, nil
}

func (r *Repository) ResolveOIDCPrincipal(ctx context.Context, issuer, subject, tenantID string) (drive.PrincipalRecord, error) {
	var record drive.PrincipalRecord
	var role, status string
	err := r.pool.QueryRow(ctx, `
		SELECT p.id::text,p.issuer,p.subject,p.display_name,t.display_name,tm.role,tm.status
		FROM principal p
		JOIN tenant_member tm ON tm.principal_id=p.id AND tm.tenant_id=$3 AND tm.status='active'
		JOIN tenant t ON t.id=tm.tenant_id
		WHERE p.issuer=$1 AND p.subject=$2`, issuer, subject, tenantID).
		Scan(&record.Identity.PrincipalID, &record.Issuer, &record.Subject, &record.DisplayName,
			&record.TenantDisplayName, &role, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "identity is not a member of this tenant")
		}
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not resolve OIDC principal")
	}
	record.Identity.TenantID = tenantID
	record.Role = drive.AccessRole(role)
	record.Status = drive.MemberStatus(status)
	if record.Status != drive.MemberStatusActive {
		return drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "identity is not a member of this tenant")
	}
	return record, nil
}

func (r *Repository) SetOIDCMemberStatus(ctx context.Context, tenantID, principalID string, status drive.MemberStatus) error {
	if tenantID == "" || principalID == "" || !drive.ValidMemberStatus(status) {
		return drive.E(drive.CodeInvalidRequest, "OIDC member status is invalid")
	}
	commandTag, err := r.pool.Exec(ctx, `
		UPDATE tenant_member
		SET status=$3, updated_at=now()
		WHERE tenant_id=$1 AND principal_id=$2`, tenantID, principalID, status)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not update OIDC member status")
	}
	if commandTag.RowsAffected() == 0 {
		return drive.E(drive.CodeNotFound, "tenant member was not found")
	}
	return nil
}

func (r *Repository) ListMembers(ctx context.Context, tenantID string, after drive.CursorPosition, limit int) ([]drive.PrincipalRecord, bool, error) {
	if tenantID == "" || limit < 1 {
		return nil, false, drive.E(drive.CodeInvalidRequest, "member list parameters are invalid")
	}
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant WHERE id=$1)`, tenantID).Scan(&exists); err != nil {
		return nil, false, mapError(err, drive.CodeInternal, "could not check tenant")
	}
	if !exists {
		return nil, false, drive.E(drive.CodeNotFound, "tenant was not found")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT p.id::text,p.issuer,p.subject,p.display_name,t.display_name,tm.role,tm.status
		FROM tenant_member tm
		JOIN principal p ON p.id=tm.principal_id
		JOIN tenant t ON t.id=tm.tenant_id
		WHERE tm.tenant_id=$1 AND ($2='' OR p.id::text>$2)
		ORDER BY p.id::text
		LIMIT $3`, tenantID, after.Name, limit+1)
	if err != nil {
		return nil, false, mapError(err, drive.CodeInternal, "could not list tenant members")
	}
	defer rows.Close()
	items := make([]drive.PrincipalRecord, 0, limit)
	for rows.Next() {
		var record drive.PrincipalRecord
		var role, status string
		if err := rows.Scan(&record.Identity.PrincipalID, &record.Issuer, &record.Subject, &record.DisplayName,
			&record.TenantDisplayName, &role, &status); err != nil {
			return nil, false, mapError(err, drive.CodeInternal, "could not decode tenant member")
		}
		record.Identity.TenantID = tenantID
		record.Role, record.Status = drive.AccessRole(role), drive.MemberStatus(status)
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, mapError(err, drive.CodeInternal, "could not list tenant members")
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

func (r *Repository) UpdateMember(ctx context.Context, command drive.UpdateMemberCommand) (drive.PrincipalRecord, error) {
	if command.TenantID == "" || command.PrincipalID == "" || command.ActorPrincipalID == "" ||
		!drive.ValidAccessRole(command.ActorRole) || (command.Role == nil && command.Status == nil) ||
		command.Role != nil && !drive.ValidAccessRole(*command.Role) ||
		command.Status != nil && !drive.ValidMemberStatus(*command.Status) {
		return drive.PrincipalRecord{}, drive.E(drive.CodeInvalidRequest, "member update is invalid")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not update tenant member")
	}
	defer tx.Rollback(ctx)
	var tenantName string
	if err := tx.QueryRow(ctx, `SELECT display_name FROM tenant WHERE id=$1 FOR NO KEY UPDATE`, command.TenantID).Scan(&tenantName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return drive.PrincipalRecord{}, drive.E(drive.CodeNotFound, "tenant was not found")
		}
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not lock tenant")
	}
	var actorRole, actorStatus string
	if err := tx.QueryRow(ctx, `SELECT role,status FROM tenant_member WHERE tenant_id=$1 AND principal_id=$2 FOR UPDATE`, command.TenantID, command.ActorPrincipalID).Scan(&actorRole, &actorStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "only active owners and admins may manage members")
		}
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not lock actor membership")
	}
	if drive.AccessRole(actorRole) != command.ActorRole || drive.MemberStatus(actorStatus) != drive.MemberStatusActive ||
		(command.ActorRole != drive.RoleOwner && command.ActorRole != drive.RoleAdmin) {
		return drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "only active owners and admins may manage members")
	}
	var currentRole, currentStatus string
	if err := tx.QueryRow(ctx, `SELECT role,status FROM tenant_member WHERE tenant_id=$1 AND principal_id=$2 FOR UPDATE`, command.TenantID, command.PrincipalID).Scan(&currentRole, &currentStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return drive.PrincipalRecord{}, drive.E(drive.CodeNotFound, "tenant member was not found")
		}
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not lock target membership")
	}
	if command.ActorRole == drive.RoleAdmin && (drive.AccessRole(currentRole) == drive.RoleOwner || command.Role != nil && *command.Role == drive.RoleOwner) {
		return drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "admins cannot modify owners or grant the owner role")
	}
	newRole, newStatus := drive.AccessRole(currentRole), drive.MemberStatus(currentStatus)
	if command.Role != nil {
		newRole = *command.Role
	}
	if command.Status != nil {
		newStatus = *command.Status
	}
	if newRole != drive.RoleOwner || newStatus != drive.MemberStatusActive {
		var activeOwners int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM tenant_member WHERE tenant_id=$1 AND role='owner' AND status='active' AND principal_id<>$2`, command.TenantID, command.PrincipalID).Scan(&activeOwners); err != nil {
			return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not count active owners")
		}
		if drive.AccessRole(currentRole) == drive.RoleOwner && drive.MemberStatus(currentStatus) == drive.MemberStatusActive && activeOwners == 0 {
			return drive.PrincipalRecord{}, drive.E(drive.CodeInvalidState, "the last active owner cannot be removed")
		}
	}
	now := command.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if _, err := tx.Exec(ctx, `UPDATE tenant_member SET role=$3,status=$4,updated_at=$5 WHERE tenant_id=$1 AND principal_id=$2`, command.TenantID, command.PrincipalID, newRole, newStatus, now); err != nil {
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not update tenant member")
	}
	var record drive.PrincipalRecord
	var role, status string
	if err := tx.QueryRow(ctx, `
		SELECT p.id::text,p.issuer,p.subject,p.display_name,t.display_name,tm.role,tm.status
		FROM tenant_member tm JOIN principal p ON p.id=tm.principal_id JOIN tenant t ON t.id=tm.tenant_id
		WHERE tm.tenant_id=$1 AND tm.principal_id=$2`, command.TenantID, command.PrincipalID).
		Scan(&record.Identity.PrincipalID, &record.Issuer, &record.Subject, &record.DisplayName, &record.TenantDisplayName, &role, &status); err != nil {
		return drive.PrincipalRecord{}, mapError(err, drive.CodeInternal, "could not read updated tenant member")
	}
	if err := commit(tx, ctx); err != nil {
		return drive.PrincipalRecord{}, err
	}
	record.Identity.TenantID = command.TenantID
	record.Role, record.Status = drive.AccessRole(role), drive.MemberStatus(status)
	return record, nil
}
