package memory

import (
	"context"
	"strings"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

func (r *Repository) ClaimIdempotency(_ context.Context, request drive.IdempotencyRequest) (drive.IdempotencyRecord, error) {
	if err := drive.ValidateIdempotencyRequest(request); err != nil {
		return drive.IdempotencyRecord{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	key := idempotencyKey(request)
	record, exists := r.idempotency[key]
	if !exists || !request.Now.Before(record.ExpiresAt) {
		record = drive.IdempotencyRecord{
			Identity: request.Identity, Scope: request.Scope, KeyHash: request.KeyHash,
			RequestDigest: request.RequestDigest, State: drive.IdempotencyPending,
			ClaimToken: request.ClaimToken, LockedUntil: request.LockedUntil, ExpiresAt: request.ExpiresAt,
			CreatedAt: request.Now, UpdatedAt: request.Now,
		}
		r.idempotency[key] = record
		return record, nil
	}
	if record.RequestDigest != request.RequestDigest {
		return drive.IdempotencyRecord{}, drive.E(drive.CodeIdempotencyConflict, "Idempotency-Key was already used for a different request")
	}
	if record.State == drive.IdempotencyCompleted || record.ClaimToken == request.ClaimToken {
		return record, nil
	}
	if request.Now.Before(record.LockedUntil) {
		return drive.IdempotencyRecord{}, drive.Retryable(drive.CodeDependencyUnavailable, "matching idempotent request is still in progress", nil)
	}
	record.ClaimToken = request.ClaimToken
	record.LockedUntil = request.LockedUntil
	record.ExpiresAt = request.ExpiresAt
	record.UpdatedAt = request.Now
	r.idempotency[key] = record
	return record, nil
}

func (r *Repository) ReleaseIdempotency(_ context.Context, request drive.IdempotencyRequest) error {
	if err := drive.ValidateIdempotencyRequest(request); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := idempotencyKey(request)
	record, exists := r.idempotency[key]
	if exists && record.State == drive.IdempotencyPending && record.RequestDigest == request.RequestDigest && record.ClaimToken == request.ClaimToken {
		delete(r.idempotency, key)
	}
	return nil
}

func (r *Repository) completeIdempotency(request *drive.IdempotencyRequest, resourceID string, now time.Time) error {
	if request == nil {
		return nil
	}
	if err := drive.ValidateIdempotencyRequest(*request); err != nil {
		return err
	}
	if !drive.ValidID(resourceID) {
		return drive.E(drive.CodeInvalidRequest, "idempotency resource identifier is invalid")
	}
	key := idempotencyKey(*request)
	record, exists := r.idempotency[key]
	if !exists || record.State != drive.IdempotencyPending || record.RequestDigest != request.RequestDigest || record.ClaimToken != request.ClaimToken {
		return drive.E(drive.CodeInvalidState, "idempotency claim is no longer owned by this request")
	}
	record.State = drive.IdempotencyCompleted
	record.ClaimToken = ""
	record.ResourceID = resourceID
	record.LockedUntil = time.Time{}
	record.UpdatedAt = now
	r.idempotency[key] = record
	return nil
}

func idempotencyKey(request drive.IdempotencyRequest) string {
	return strings.Join([]string{
		request.Identity.TenantID, request.Identity.PrincipalID, string(request.Scope), request.KeyHash,
	}, "\x00")
}
