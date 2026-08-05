package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

func TestRepositoryIdempotencyClaimContract(t *testing.T) {
	repository := integrationRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(2001), PrincipalID: testID(2002)}
	_, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Idempotency", RootNodeID: testID(2003), Now: now,
	})
	assertNoError(t, err)

	t.Run("new claim", func(t *testing.T) {
		request := postgresIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "a", "b", testID(2010), now)
		record, claimErr := repository.ClaimIdempotency(ctx, request)
		assertNoError(t, claimErr)
		assertPendingPostgresIdempotencyRecord(t, record, request)
	})

	t.Run("active matching request is blocked", func(t *testing.T) {
		request := postgresIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "c", "d", testID(2011), now)
		_, claimErr := repository.ClaimIdempotency(ctx, request)
		assertNoError(t, claimErr)

		contender := postgresIdempotencyRetry(request, testID(2012), now.Add(10*time.Second))
		_, claimErr = repository.ClaimIdempotency(ctx, contender)
		assertCode(t, claimErr, drive.CodeDependencyUnavailable)
		var domainErr *drive.Error
		if !errors.As(claimErr, &domainErr) || !domainErr.Retryable {
			t.Fatalf("active claim error was not retryable: %v", claimErr)
		}
	})

	t.Run("different request digest conflicts", func(t *testing.T) {
		request := postgresIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "e", "1", testID(2013), now)
		_, claimErr := repository.ClaimIdempotency(ctx, request)
		assertNoError(t, claimErr)

		conflict := postgresIdempotencyRetry(request, testID(2014), now.Add(10*time.Second))
		conflict.RequestDigest = strings.Repeat("2", 64)
		_, claimErr = repository.ClaimIdempotency(ctx, conflict)
		assertCode(t, claimErr, drive.CodeIdempotencyConflict)
	})

	t.Run("expired lease can be taken over", func(t *testing.T) {
		request := postgresIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "3", "4", testID(2015), now)
		_, claimErr := repository.ClaimIdempotency(ctx, request)
		assertNoError(t, claimErr)

		takeover := postgresIdempotencyRetry(request, testID(2016), request.LockedUntil)
		record, claimErr := repository.ClaimIdempotency(ctx, takeover)
		assertNoError(t, claimErr)
		assertPendingPostgresIdempotencyRecord(t, record, takeover)
		if !record.CreatedAt.Equal(request.Now) || !record.UpdatedAt.Equal(takeover.Now) {
			t.Fatalf("lease takeover timestamps changed unexpectedly: %+v", record)
		}
	})

	t.Run("release requires the current owner token", func(t *testing.T) {
		owner := postgresIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "5", "6", testID(2017), now)
		_, claimErr := repository.ClaimIdempotency(ctx, owner)
		assertNoError(t, claimErr)

		other := postgresIdempotencyRetry(owner, testID(2018), now.Add(10*time.Second))
		assertNoError(t, repository.ReleaseIdempotency(ctx, other))
		_, claimErr = repository.ClaimIdempotency(ctx, other)
		assertCode(t, claimErr, drive.CodeDependencyUnavailable)

		assertNoError(t, repository.ReleaseIdempotency(ctx, owner))
		record, claimErr := repository.ClaimIdempotency(ctx, other)
		assertNoError(t, claimErr)
		assertPendingPostgresIdempotencyRecord(t, record, other)
	})
}

func TestRepositoryIdempotencyCreationContract(t *testing.T) {
	repository := integrationRepository(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: testID(2101), PrincipalID: testID(2102)}
	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Idempotency", RootNodeID: testID(2103), Now: now,
	})
	assertNoError(t, err)

	t.Run("directory completion and replay are atomic", func(t *testing.T) {
		request := postgresIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "7", "8", testID(2110), now.Add(time.Minute))
		_, claimErr := repository.ClaimIdempotency(ctx, request)
		assertNoError(t, claimErr)
		nodeID := testID(2111)
		created, createErr := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
			Identity: identity, ID: nodeID, ParentID: tenant.RootNodeID,
			DisplayName: "Reports", NormalizedName: "reports", Now: now.Add(2 * time.Minute), Idempotency: &request,
		})
		assertNoError(t, createErr)
		if created.ID != nodeID {
			t.Fatalf("created directory id = %q, want %q", created.ID, nodeID)
		}

		replay := postgresIdempotencyRetry(request, testID(2112), now.Add(3*time.Minute))
		record, claimErr := repository.ClaimIdempotency(ctx, replay)
		assertNoError(t, claimErr)
		assertCompletedPostgresIdempotencyRecord(t, record, request, nodeID, now.Add(2*time.Minute))
		persisted, nodeErr := repository.Node(ctx, identity, record.ResourceID, false)
		assertNoError(t, nodeErr)
		if persisted.ID != created.ID {
			t.Fatalf("replay resolved directory %q, want %q", persisted.ID, created.ID)
		}

		unowned := postgresIdempotencyRequest(identity, drive.IdempotencyCreateDirectory, "9", "a", testID(2113), now.Add(4*time.Minute))
		rolledBackID := testID(2114)
		_, createErr = repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
			Identity: identity, ID: rolledBackID, ParentID: tenant.RootNodeID,
			DisplayName: "Unowned", NormalizedName: "unowned", Now: now.Add(5 * time.Minute), Idempotency: &unowned,
		})
		assertCode(t, createErr, drive.CodeInvalidState)
		_, nodeErr = repository.Node(ctx, identity, rolledBackID, false)
		assertCode(t, nodeErr, drive.CodeNotFound)
	})

	t.Run("upload completion and replay are atomic", func(t *testing.T) {
		request := postgresIdempotencyRequest(identity, drive.IdempotencyCreateUpload, "b", "c", testID(2120), now.Add(6*time.Minute))
		_, claimErr := repository.ClaimIdempotency(ctx, request)
		assertNoError(t, claimErr)
		session := newUpload(testID(2121), identity, tenant.RootNodeID, "report.bin", "report.bin", now.Add(7*time.Minute))
		created, createErr := repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: session, Idempotency: &request})
		assertNoError(t, createErr)
		if created.ID != session.ID {
			t.Fatalf("created upload id = %q, want %q", created.ID, session.ID)
		}

		replay := postgresIdempotencyRetry(request, testID(2122), now.Add(8*time.Minute))
		record, claimErr := repository.ClaimIdempotency(ctx, replay)
		assertNoError(t, claimErr)
		assertCompletedPostgresIdempotencyRecord(t, record, request, session.ID, session.CreatedAt)
		persisted, uploadErr := repository.Upload(ctx, identity, record.ResourceID)
		assertNoError(t, uploadErr)
		if persisted.ID != created.ID {
			t.Fatalf("replay resolved upload %q, want %q", persisted.ID, created.ID)
		}

		unowned := postgresIdempotencyRequest(identity, drive.IdempotencyCreateUpload, "d", "e", testID(2123), now.Add(9*time.Minute))
		rolledBack := newUpload(testID(2124), identity, tenant.RootNodeID, "unowned.bin", "unowned.bin", now.Add(10*time.Minute))
		_, createErr = repository.CreateUpload(ctx, drive.CreateUploadCommand{Session: rolledBack, Idempotency: &unowned})
		assertCode(t, createErr, drive.CodeInvalidState)
		_, uploadErr = repository.Upload(ctx, identity, rolledBack.ID)
		assertCode(t, uploadErr, drive.CodeNotFound)
	})
}

func postgresIdempotencyRequest(identity drive.Identity, scope drive.IdempotencyScope, keyDigit, digestDigit, token string, now time.Time) drive.IdempotencyRequest {
	return drive.IdempotencyRequest{
		Identity: identity, Scope: scope, KeyHash: strings.Repeat(keyDigit, 64), RequestDigest: strings.Repeat(digestDigit, 64),
		ClaimToken: token, Now: now, LockedUntil: now.Add(time.Minute), ExpiresAt: now.Add(24 * time.Hour),
	}
}

func postgresIdempotencyRetry(request drive.IdempotencyRequest, token string, now time.Time) drive.IdempotencyRequest {
	request.ClaimToken = token
	request.Now = now
	request.LockedUntil = now.Add(time.Minute)
	request.ExpiresAt = now.Add(24 * time.Hour)
	return request
}

func assertPendingPostgresIdempotencyRecord(t *testing.T, record drive.IdempotencyRecord, request drive.IdempotencyRequest) {
	t.Helper()
	if record.Identity != request.Identity || record.Scope != request.Scope || record.KeyHash != request.KeyHash ||
		record.RequestDigest != request.RequestDigest || record.State != drive.IdempotencyPending ||
		record.ClaimToken != request.ClaimToken || record.ResourceID != "" ||
		!record.LockedUntil.Equal(request.LockedUntil) || !record.ExpiresAt.Equal(request.ExpiresAt) {
		t.Fatalf("unexpected pending idempotency record: %+v", record)
	}
}

func assertCompletedPostgresIdempotencyRecord(t *testing.T, record drive.IdempotencyRecord, request drive.IdempotencyRequest, resourceID string, updatedAt time.Time) {
	t.Helper()
	if record.Identity != request.Identity || record.Scope != request.Scope || record.KeyHash != request.KeyHash ||
		record.RequestDigest != request.RequestDigest || record.State != drive.IdempotencyCompleted ||
		record.ClaimToken != "" || record.ResourceID != resourceID || !record.LockedUntil.IsZero() ||
		!record.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("unexpected completed idempotency record: %+v", record)
	}
}
