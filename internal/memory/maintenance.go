package memory

import (
	"context"
	"sort"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

type maintenanceLease struct {
	owner          string
	until          time.Time
	notBefore      time.Time
	attempts       int
	errorCode      string
	cleanupPending bool
}

func (r *Repository) ClaimUploadsForMaintenance(_ context.Context, owner string, now, staleBefore, leaseUntil time.Time, limit int) ([]drive.UploadMaintenanceClaim, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0)
	for id, upload := range r.uploads {
		lease := r.uploadMaintenance[id]
		if (!lease.until.IsZero() && lease.until.After(now)) || (!lease.notBefore.IsZero() && lease.notBefore.After(now)) {
			continue
		}
		due := (upload.Status == drive.UploadCreated || upload.Status == drive.UploadUploading) && !upload.ExpiresAt.After(now)
		stale := (upload.Status == drive.UploadCompleting || upload.Status == drive.UploadObjectCompleted) && !upload.UpdatedAt.After(staleBefore)
		if due || stale || lease.cleanupPending || lease.errorCode != "" {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return r.uploads[ids[i]].ExpiresAt.Before(r.uploads[ids[j]].ExpiresAt) })
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	claims := make([]drive.UploadMaintenanceClaim, 0, len(ids))
	for _, id := range ids {
		lease := r.uploadMaintenance[id]
		lease.owner, lease.until = owner, leaseUntil
		r.uploadMaintenance[id] = lease
		claims = append(claims, drive.UploadMaintenanceClaim{Upload: r.uploads[id], Owner: owner, CleanupPending: lease.cleanupPending, Attempts: lease.attempts})
	}
	return claims, nil
}

func (r *Repository) FinishUploadMaintenance(_ context.Context, owner, uploadID string, status drive.UploadStatus, cleanupComplete bool, retryAt time.Time, errorCode string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lease, ok := r.uploadMaintenance[uploadID]
	if !ok || lease.owner != owner {
		return drive.E(drive.CodeInvalidState, "upload maintenance claim is no longer owned")
	}
	upload, ok := r.uploads[uploadID]
	if !ok {
		return drive.E(drive.CodeInvalidState, "upload cannot be maintained")
	}
	upload.Status, upload.UpdatedAt, upload.Revision = status, now, upload.Revision+1
	r.uploads[uploadID] = upload
	lease.owner, lease.until, lease.notBefore, lease.errorCode = "", time.Time{}, retryAt, errorCode
	lease.cleanupPending = !cleanupComplete
	if errorCode != "" {
		lease.attempts++
	}
	if cleanupComplete && errorCode == "" {
		lease.errorCode, lease.notBefore = "", time.Time{}
	}
	r.uploadMaintenance[uploadID] = lease
	return nil
}

func (r *Repository) ClaimRecycleForMaintenance(_ context.Context, owner string, cutoff, now, leaseUntil time.Time, limit int) ([]drive.RecycleMaintenanceClaim, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0)
	for id, node := range r.nodes {
		if node.TrashedRootID != id || (node.Status != drive.NodeTrashed && node.Status != drive.NodePurging) || node.DeletedAt == nil || node.DeletedAt.After(cutoff) {
			continue
		}
		lease := r.recycleMaintenance[id]
		if (!lease.until.IsZero() && lease.until.After(now)) || (!lease.notBefore.IsZero() && lease.notBefore.After(now)) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	claims := make([]drive.RecycleMaintenanceClaim, 0, len(ids))
	for _, id := range ids {
		node, lease := r.nodes[id], r.recycleMaintenance[id]
		node.Status, node.UpdatedAt = drive.NodePurging, now
		r.nodes[id] = node
		lease.owner, lease.until = owner, leaseUntil
		r.recycleMaintenance[id] = lease
		claims = append(claims, drive.RecycleMaintenanceClaim{Identity: drive.Identity{TenantID: node.TenantID}, RootID: id, Revision: node.Revision, Owner: owner})
	}
	return claims, nil
}

func (r *Repository) ReleaseRecycleMaintenance(_ context.Context, owner, rootID string, retryAt time.Time, errorCode string, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lease, ok := r.recycleMaintenance[rootID]
	if !ok || lease.owner != owner {
		return drive.E(drive.CodeInvalidState, "recycle maintenance claim is no longer owned")
	}
	lease.owner, lease.until, lease.notBefore, lease.errorCode = "", time.Time{}, retryAt, errorCode
	lease.attempts++
	r.recycleMaintenance[rootID] = lease
	return nil
}

func (r *Repository) DeleteExpiredIdempotency(_ context.Context, now time.Time, limit int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0)
	for key, record := range r.idempotency {
		if !record.ExpiresAt.After(now) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	for _, key := range keys {
		delete(r.idempotency, key)
	}
	return len(keys), nil
}
