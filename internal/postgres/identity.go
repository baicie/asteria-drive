package postgres

import (
	"context"
	"errors"

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
