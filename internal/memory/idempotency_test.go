package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

func TestRepositoryIdempotencyClaimContract(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: memoryIdempotencyID(1), PrincipalID: memoryIdempotencyID(2)}

	t.Run("new claim", func(t *testing.T) {
		repository := NewRepository()
		request := memoryIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "a", "b", memoryIdempotencyID(10), now)

		record, err := repository.ClaimIdempotency(ctx, request)
		assertNoError(t, err)
		assertPendingMemoryIdempotencyRecord(t, record, request)
	})

	t.Run("active matching request is blocked", func(t *testing.T) {
		repository := NewRepository()
		request := memoryIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "c", "d", memoryIdempotencyID(11), now)
		_, err := repository.ClaimIdempotency(ctx, request)
		assertNoError(t, err)

		contender := memoryIdempotencyRetry(request, memoryIdempotencyID(12), now.Add(10*time.Second))
		_, err = repository.ClaimIdempotency(ctx, contender)
		assertErrorCode(t, err, drive.CodeDependencyUnavailable)
		var domainErr *drive.Error
		if !errors.As(err, &domainErr) || !domainErr.Retryable {
			t.Fatalf("active claim error was not retryable: %v", err)
		}
	})

	t.Run("different request digest conflicts", func(t *testing.T) {
		repository := NewRepository()
		request := memoryIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "e", "1", memoryIdempotencyID(13), now)
		_, err := repository.ClaimIdempotency(ctx, request)
		assertNoError(t, err)

		conflict := memoryIdempotencyRetry(request, memoryIdempotencyID(14), now.Add(10*time.Second))
		conflict.RequestDigest = strings.Repeat("2", 64)
		_, err = repository.ClaimIdempotency(ctx, conflict)
		assertErrorCode(t, err, drive.CodeIdempotencyConflict)
	})

	t.Run("expired lease can be taken over", func(t *testing.T) {
		repository := NewRepository()
		request := memoryIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "3", "4", memoryIdempotencyID(15), now)
		_, err := repository.ClaimIdempotency(ctx, request)
		assertNoError(t, err)

		takeover := memoryIdempotencyRetry(request, memoryIdempotencyID(16), request.LockedUntil)
		record, err := repository.ClaimIdempotency(ctx, takeover)
		assertNoError(t, err)
		assertPendingMemoryIdempotencyRecord(t, record, takeover)
		if !record.CreatedAt.Equal(request.Now) || !record.UpdatedAt.Equal(takeover.Now) {
			t.Fatalf("lease takeover timestamps changed unexpectedly: %+v", record)
		}
	})

	t.Run("release requires the current owner token", func(t *testing.T) {
		repository := NewRepository()
		owner := memoryIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "5", "6", memoryIdempotencyID(17), now)
		_, err := repository.ClaimIdempotency(ctx, owner)
		assertNoError(t, err)

		other := memoryIdempotencyRetry(owner, memoryIdempotencyID(18), now.Add(10*time.Second))
		assertNoError(t, repository.ReleaseIdempotency(ctx, other))
		_, err = repository.ClaimIdempotency(ctx, other)
		assertErrorCode(t, err, drive.CodeDependencyUnavailable)

		assertNoError(t, repository.ReleaseIdempotency(ctx, owner))
		record, err := repository.ClaimIdempotency(ctx, other)
		assertNoError(t, err)
		assertPendingMemoryIdempotencyRecord(t, record, other)
	})
}

func TestRepositoryIdempotencyCreationContract(t *testing.T) {
	t.Run("directory completion and replay are atomic", func(t *testing.T) {
		repository := NewRepository()
		ctx := context.Background()
		now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
		identity := drive.Identity{TenantID: memoryIdempotencyID(20), PrincipalID: memoryIdempotencyID(21)}
		tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
			TenantID: identity.TenantID, DisplayName: "Idempotency", RootNodeID: memoryIdempotencyID(22), Now: now,
		})
		assertNoError(t, err)

		request := memoryIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "7", "8", memoryIdempotencyID(23), now.Add(time.Minute))
		_, err = repository.ClaimIdempotency(ctx, request)
		assertNoError(t, err)
		nodeID := memoryIdempotencyID(24)
		created, err := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
			Identity: identity, ID: nodeID, ParentID: tenant.RootNodeID,
			DisplayName: "Reports", NormalizedName: "reports", Now: now.Add(2 * time.Minute), Idempotency: &request,
		})
		assertNoError(t, err)
		if created.ID != nodeID {
			t.Fatalf("created directory id = %q, want %q", created.ID, nodeID)
		}

		replay := memoryIdempotencyRetry(request, memoryIdempotencyID(25), now.Add(3*time.Minute))
		record, err := repository.ClaimIdempotency(ctx, replay)
		assertNoError(t, err)
		assertCompletedMemoryIdempotencyRecord(t, record, request, nodeID, now.Add(2*time.Minute))
		persisted, err := repository.Node(ctx, identity, record.ResourceID, false)
		assertNoError(t, err)
		if persisted.ID != created.ID {
			t.Fatalf("replay resolved directory %q, want %q", persisted.ID, created.ID)
		}

		unowned := memoryIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "9", "a", memoryIdempotencyID(26), now.Add(4*time.Minute))
		rolledBackID := memoryIdempotencyID(27)
		_, err = repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
			Identity: identity, ID: rolledBackID, ParentID: tenant.RootNodeID,
			DisplayName: "Unowned", NormalizedName: "unowned", Now: now.Add(5 * time.Minute), Idempotency: &unowned,
		})
		assertErrorCode(t, err, drive.CodeInvalidState)
		_, err = repository.Node(ctx, identity, rolledBackID, false)
		assertErrorCode(t, err, drive.CodeNotFound)
	})

	t.Run("upload completion and replay are atomic", func(t *testing.T) {
		repository := NewRepository()
		ctx := context.Background()
		now := time.Date(2026, 8, 5, 11, 0, 0, 0, time.UTC)
		identity := drive.Identity{TenantID: memoryIdempotencyID(30), PrincipalID: memoryIdempotencyID(31)}
		tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
			TenantID: identity.TenantID, DisplayName: "Idempotency", RootNodeID: memoryIdempotencyID(32), Now: now,
		})
		assertNoError(t, err)

		request := memoryIdempotencyRequest(identity, drive.IdempotencyCreateUpload, "b", "c", memoryIdempotencyID(33), now.Add(time.Minute))
		_, err = repository.ClaimIdempotency(ctx, request)
		assertNoError(t, err)
		session := memoryUpload(memoryIdempotencyID(34), identity, tenant.RootNodeID, "report.bin", now.Add(2*time.Minute))
		created, err := repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: session, Idempotency: &request})
		assertNoError(t, err)
		if created.ID != session.ID {
			t.Fatalf("created upload id = %q, want %q", created.ID, session.ID)
		}

		replay := memoryIdempotencyRetry(request, memoryIdempotencyID(35), now.Add(3*time.Minute))
		record, err := repository.ClaimIdempotency(ctx, replay)
		assertNoError(t, err)
		assertCompletedMemoryIdempotencyRecord(t, record, request, session.ID, session.CreatedAt)
		persisted, err := repository.Upload(ctx, identity, record.ResourceID)
		assertNoError(t, err)
		if persisted.ID != created.ID {
			t.Fatalf("replay resolved upload %q, want %q", persisted.ID, created.ID)
		}

		unowned := memoryIdempotencyRequest(identity, drive.IdempotencyCreateUpload, "d", "e", memoryIdempotencyID(36), now.Add(4*time.Minute))
		rolledBack := memoryUpload(memoryIdempotencyID(37), identity, tenant.RootNodeID, "unowned.bin", now.Add(5*time.Minute))
		_, err = repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: rolledBack, Idempotency: &unowned})
		assertErrorCode(t, err, drive.CodeInvalidState)
		_, err = repository.Upload(ctx, identity, rolledBack.ID)
		assertErrorCode(t, err, drive.CodeNotFound)
	})
}

func memoryIdempotencyRequest(identity drive.Identity, scope drive.IdempotencyScope, keyDigit, digestDigit, token string, now time.Time) drive.IdempotencyRequest {
	return drive.IdempotencyRequest{
		Identity: identity, Scope: scope, KeyHash: strings.Repeat(keyDigit, 64), RequestDigest: strings.Repeat(digestDigit, 64),
		ClaimToken: token, Now: now, LockedUntil: now.Add(time.Minute), ExpiresAt: now.Add(24 * time.Hour),
	}
}

func memoryIdempotencyRetry(request drive.IdempotencyRequest, token string, now time.Time) drive.IdempotencyRequest {
	request.ClaimToken = token
	request.Now = now
	request.LockedUntil = now.Add(time.Minute)
	request.ExpiresAt = now.Add(24 * time.Hour)
	return request
}

func assertPendingMemoryIdempotencyRecord(t *testing.T, record drive.IdempotencyRecord, request drive.IdempotencyRequest) {
	t.Helper()
	if record.Identity != request.Identity || record.Scope != request.Scope || record.KeyHash != request.KeyHash ||
		record.RequestDigest != request.RequestDigest || record.State != drive.IdempotencyPending ||
		record.ClaimToken != request.ClaimToken || record.ResourceID != "" ||
		!record.LockedUntil.Equal(request.LockedUntil) || !record.ExpiresAt.Equal(request.ExpiresAt) {
		t.Fatalf("unexpected pending idempotency record: %+v", record)
	}
}

func assertCompletedMemoryIdempotencyRecord(t *testing.T, record drive.IdempotencyRecord, request drive.IdempotencyRequest, resourceID string, updatedAt time.Time) {
	t.Helper()
	if record.Identity != request.Identity || record.Scope != request.Scope || record.KeyHash != request.KeyHash ||
		record.RequestDigest != request.RequestDigest || record.State != drive.IdempotencyCompleted ||
		record.ClaimToken != "" || record.ResourceID != resourceID || !record.LockedUntil.IsZero() ||
		!record.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("unexpected completed idempotency record: %+v", record)
	}
}

func memoryIdempotencyID(number int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", number)
}
