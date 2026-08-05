package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5"
)

const idempotencyColumns = `
tenant_id::text, principal_id::text, scope, key_hash, request_digest, state,
COALESCE(claim_token::text,''), COALESCE(resource_id::text,''), locked_until,
expires_at, created_at, updated_at`

func (r *Repository) ClaimIdempotency(ctx context.Context, request drive.IdempotencyRequest) (drive.IdempotencyRecord, error) {
	if err := drive.ValidateIdempotencyRequest(request); err != nil {
		return drive.IdempotencyRecord{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return drive.IdempotencyRecord{}, mapError(err, drive.CodeInternal, "could not claim idempotent request")
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO idempotency_record(
			tenant_id,principal_id,scope,key_hash,request_digest,state,claim_token,
			resource_id,locked_until,expires_at,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,'pending',$6,NULL,$7,$8,$9,$9)
		ON CONFLICT(tenant_id,principal_id,scope,key_hash) DO NOTHING`,
		request.Identity.TenantID, request.Identity.PrincipalID, request.Scope,
		request.KeyHash, request.RequestDigest, request.ClaimToken,
		request.LockedUntil, request.ExpiresAt, request.Now); err != nil {
		return drive.IdempotencyRecord{}, mapError(err, drive.CodeInternal, "could not create idempotency claim")
	}
	record, err := scanIdempotency(tx.QueryRow(ctx, `
		SELECT `+idempotencyColumns+` FROM idempotency_record
		WHERE tenant_id=$1 AND principal_id=$2 AND scope=$3 AND key_hash=$4
		FOR UPDATE`, request.Identity.TenantID, request.Identity.PrincipalID, request.Scope, request.KeyHash))
	if err != nil {
		return drive.IdempotencyRecord{}, mapError(err, drive.CodeInternal, "could not read idempotency claim")
	}
	if !request.Now.Before(record.ExpiresAt) {
		record, err = scanIdempotency(tx.QueryRow(ctx, `
			UPDATE idempotency_record SET
				request_digest=$5,state='pending',claim_token=$6,resource_id=NULL,
				locked_until=$7,expires_at=$8,created_at=$9,updated_at=$9
			WHERE tenant_id=$1 AND principal_id=$2 AND scope=$3 AND key_hash=$4
			RETURNING `+idempotencyColumns,
			request.Identity.TenantID, request.Identity.PrincipalID, request.Scope, request.KeyHash,
			request.RequestDigest, request.ClaimToken, request.LockedUntil, request.ExpiresAt, request.Now))
		if err != nil {
			return drive.IdempotencyRecord{}, mapError(err, drive.CodeInternal, "could not replace expired idempotency claim")
		}
	} else {
		if record.RequestDigest != request.RequestDigest {
			return drive.IdempotencyRecord{}, drive.E(drive.CodeIdempotencyConflict, "Idempotency-Key was already used for a different request")
		}
		if record.State == drive.IdempotencyPending && record.ClaimToken != request.ClaimToken {
			if request.Now.Before(record.LockedUntil) {
				return drive.IdempotencyRecord{}, drive.Retryable(drive.CodeDependencyUnavailable, "matching idempotent request is still in progress", nil)
			}
			record, err = scanIdempotency(tx.QueryRow(ctx, `
				UPDATE idempotency_record SET claim_token=$5,locked_until=$6,expires_at=$7,updated_at=$8
				WHERE tenant_id=$1 AND principal_id=$2 AND scope=$3 AND key_hash=$4
				RETURNING `+idempotencyColumns,
				request.Identity.TenantID, request.Identity.PrincipalID, request.Scope, request.KeyHash,
				request.ClaimToken, request.LockedUntil, request.ExpiresAt, request.Now))
			if err != nil {
				return drive.IdempotencyRecord{}, mapError(err, drive.CodeInternal, "could not take over idempotency claim")
			}
		}
	}
	if err := commit(tx, ctx); err != nil {
		return drive.IdempotencyRecord{}, err
	}
	return record, nil
}

func (r *Repository) ReleaseIdempotency(ctx context.Context, request drive.IdempotencyRequest) error {
	if err := drive.ValidateIdempotencyRequest(request); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM idempotency_record
		WHERE tenant_id=$1 AND principal_id=$2 AND scope=$3 AND key_hash=$4
		  AND request_digest=$5 AND state='pending' AND claim_token=$6`,
		request.Identity.TenantID, request.Identity.PrincipalID, request.Scope,
		request.KeyHash, request.RequestDigest, request.ClaimToken)
	return mapError(err, drive.CodeInternal, "could not release idempotency claim")
}

func completeIdempotency(ctx context.Context, tx pgx.Tx, request *drive.IdempotencyRequest, resourceID string, now time.Time) error {
	if request == nil {
		return nil
	}
	if err := drive.ValidateIdempotencyRequest(*request); err != nil {
		return err
	}
	if !drive.ValidID(resourceID) {
		return drive.E(drive.CodeInvalidRequest, "idempotency resource identifier is invalid")
	}
	tag, err := tx.Exec(ctx, `
		UPDATE idempotency_record SET
			state='completed',claim_token=NULL,resource_id=$7,locked_until=NULL,updated_at=$8
		WHERE tenant_id=$1 AND principal_id=$2 AND scope=$3 AND key_hash=$4
		  AND request_digest=$5 AND state='pending' AND claim_token=$6`,
		request.Identity.TenantID, request.Identity.PrincipalID, request.Scope,
		request.KeyHash, request.RequestDigest, request.ClaimToken, resourceID, now)
	if err != nil {
		return mapError(err, drive.CodeInternal, "could not complete idempotency claim")
	}
	if tag.RowsAffected() != 1 {
		return drive.E(drive.CodeInvalidState, "idempotency claim is no longer owned by this request")
	}
	return nil
}

func scanIdempotency(row scanner) (drive.IdempotencyRecord, error) {
	var record drive.IdempotencyRecord
	var scope, state string
	var lockedUntil sql.NullTime
	err := row.Scan(
		&record.Identity.TenantID, &record.Identity.PrincipalID, &scope,
		&record.KeyHash, &record.RequestDigest, &state, &record.ClaimToken,
		&record.ResourceID, &lockedUntil, &record.ExpiresAt, &record.CreatedAt, &record.UpdatedAt,
	)
	record.Scope = drive.IdempotencyScope(scope)
	record.State = drive.IdempotencyState(state)
	if lockedUntil.Valid {
		record.LockedUntil = lockedUntil.Time
	}
	return record, err
}
