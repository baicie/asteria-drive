// Package maintenance runs bounded, leased recovery work shared by API instances.
package maintenance

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

type Metrics interface {
	ObserveMaintenance(task, result string, duration time.Duration)
	SetMaintenanceBacklog(task string, count int)
}

type Options struct {
	Repository       drive.Repository
	Storage          drive.StorageProvider
	Metrics          Metrics
	Interval         time.Duration
	LeaseDuration    time.Duration
	StaleAfter       time.Duration
	RecycleRetention time.Duration
	BatchSize        int
	Now              func() time.Time
}

type Loop struct {
	repository       drive.Repository
	storage          drive.StorageProvider
	metrics          Metrics
	interval         time.Duration
	leaseDuration    time.Duration
	staleAfter       time.Duration
	recycleRetention time.Duration
	batchSize        int
	now              func() time.Time
	owner            string
	wg               sync.WaitGroup
}

func New(options Options) (*Loop, error) {
	if options.Repository == nil || options.Storage == nil {
		return nil, drive.E(drive.CodeInvalidRequest, "maintenance repository and storage are required")
	}
	if options.Interval <= 0 {
		options.Interval = time.Minute
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = 2 * time.Minute
	}
	if options.StaleAfter <= 0 {
		options.StaleAfter = 15 * time.Minute
	}
	if options.RecycleRetention <= 0 {
		options.RecycleRetention = 30 * 24 * time.Hour
	}
	if options.BatchSize <= 0 {
		options.BatchSize = 50
	}
	if options.BatchSize > 1000 {
		return nil, drive.E(drive.CodeInvalidRequest, "maintenance batch size exceeds 1000")
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	owner, err := drive.NewID()
	if err != nil {
		return nil, drive.E(drive.CodeInternal, "could not generate maintenance owner", err)
	}
	return &Loop{repository: options.Repository, storage: options.Storage, metrics: options.Metrics,
		interval: options.Interval, leaseDuration: options.LeaseDuration, staleAfter: options.StaleAfter,
		recycleRetention: options.RecycleRetention, batchSize: options.BatchSize, now: options.Now, owner: owner}, nil
}

func (l *Loop) Start(ctx context.Context) {
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for {
			l.RunOnce(ctx)
			delay := l.interval + time.Duration(rand.Int64N(max(int64(l.interval/10), 1)))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

func (l *Loop) Wait() { l.wg.Wait() }

func (l *Loop) RunOnce(ctx context.Context) {
	l.runUploads(ctx)
	l.runRecycle(ctx)
	l.runIdempotency(ctx)
}

func (l *Loop) runUploads(ctx context.Context) {
	started, now := time.Now(), l.now()
	claims, err := l.repository.ClaimUploadsForMaintenance(ctx, l.owner, now, now.Add(-l.staleAfter), now.Add(l.leaseDuration), l.batchSize)
	l.observe("uploads", err, started)
	if err != nil {
		return
	}
	l.backlog("uploads", len(claims))
	for _, claim := range claims {
		started := time.Now()
		err := l.processUpload(ctx, claim)
		l.observe("upload", err, started)
	}
}

func (l *Loop) processUpload(ctx context.Context, claim drive.UploadMaintenanceClaim) error {
	upload, now := claim.Upload, l.now()
	if claim.CleanupPending || upload.Status.Terminal() {
		err := l.cleanupUpload(ctx, upload)
		return l.finishUpload(claim, upload.Status, err == nil, err, now)
	}
	if upload.Status == drive.UploadCreated || upload.Status == drive.UploadUploading {
		err := l.cleanupUpload(ctx, upload)
		return l.finishUpload(claim, drive.UploadExpired, err == nil, err, now)
	}
	object, err := l.storage.StatObject(ctx, upload.ObjectKey)
	if err != nil {
		if drive.CodeOf(err) == drive.CodeNotFound {
			cleanupErr := l.cleanupUpload(ctx, upload)
			return l.finishUpload(claim, drive.UploadFailed, cleanupErr == nil, cleanupErr, now)
		}
		return l.finishUpload(claim, upload.Status, true, err, now)
	}
	if object.Size != upload.ExpectedSize {
		err = l.storage.DeleteObject(ctx, upload.ObjectKey)
		return l.finishUpload(claim, drive.UploadFailed, err == nil, err, now)
	}
	identity := drive.Identity{TenantID: upload.TenantID, PrincipalID: upload.PrincipalID}
	if upload.Status == drive.UploadCompleting {
		if _, err = l.repository.MarkObjectCompleted(ctx, identity, upload.ID, object, now); err != nil {
			return l.finishUpload(claim, upload.Status, true, err, now)
		}
		upload.Status = drive.UploadObjectCompleted
	}
	blobID, err := drive.NewID()
	if err != nil {
		return l.finishUpload(claim, upload.Status, true, err, now)
	}
	nodeID, err := drive.NewID()
	if err != nil {
		return l.finishUpload(claim, upload.Status, true, err, now)
	}
	versionID, err := drive.NewID()
	if err != nil {
		return l.finishUpload(claim, upload.Status, true, err, now)
	}
	result, _, err := l.repository.CommitUpload(ctx, drive.CommitUploadCommand{
		Identity: identity, SessionID: upload.ID, Digest: upload.CompletionDigest, Now: now,
		Blob:    drive.Blob{ID: blobID, TenantID: upload.TenantID, Bucket: object.Bucket, ObjectKey: object.ObjectKey, Size: object.Size, MimeType: upload.MimeType, Checksum: object.Checksum, ChecksumStatus: object.ChecksumStatus, Status: drive.BlobAvailable, ReferenceCount: 1, CreatedAt: now},
		Version: drive.FileVersion{ID: versionID, TenantID: upload.TenantID, NodeID: nodeID, BlobID: blobID, Size: object.Size, MimeType: upload.MimeType, Checksum: object.Checksum, CreatedBy: upload.PrincipalID, CreatedAt: now},
		Node:    drive.Node{ID: nodeID, TenantID: upload.TenantID, ParentID: upload.ParentID, Kind: drive.NodeFile, DisplayName: upload.DisplayName, NormalizedName: upload.NormalizedName, CurrentVersionID: versionID, Size: object.Size, MimeType: upload.MimeType, Status: drive.NodeActive, Revision: 1, CreatedAt: now, UpdatedAt: now},
	})
	if err != nil {
		if drive.CodeOf(err) == drive.CodeNameConflict || drive.CodeOf(err) == drive.CodeNotFound {
			cleanupErr := l.storage.DeleteObject(ctx, upload.ObjectKey)
			return l.finishUpload(claim, drive.UploadFailed, cleanupErr == nil, cleanupErr, now)
		}
		return l.finishUpload(claim, upload.Status, true, err, now)
	}
	return l.finishUpload(claim, result.Upload.Status, true, nil, now)
}

func (l *Loop) cleanupUpload(ctx context.Context, upload drive.UploadSession) error {
	var err error
	if upload.Status == drive.UploadFailed && failedUploadHasObject(upload.FailureCode) {
		err = l.storage.DeleteObject(ctx, upload.ObjectKey)
	} else {
		err = l.storage.AbortMultipart(ctx, upload.ObjectKey, upload.StorageUploadID)
	}
	if drive.CodeOf(err) == drive.CodeNotFound {
		return nil
	}
	return err
}

func (l *Loop) finishUpload(claim drive.UploadMaintenanceClaim, status drive.UploadStatus, complete bool, workErr error, now time.Time) error {
	retryAt, code := time.Time{}, ""
	if workErr != nil {
		retryAt, code = now.Add(backoff(claim.Attempts)), maintenanceErrorCode(workErr)
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := l.repository.FinishUploadMaintenance(finishCtx, claim.Owner, claim.Upload.ID, status, complete, retryAt, code, now)
	if err != nil {
		return err
	}
	return workErr
}

func (l *Loop) runRecycle(ctx context.Context) {
	started, now := time.Now(), l.now()
	claims, err := l.repository.ClaimRecycleForMaintenance(ctx, l.owner, now.Add(-l.recycleRetention), now, now.Add(l.leaseDuration), l.batchSize)
	l.observe("recycle", err, started)
	if err != nil {
		return
	}
	l.backlog("recycle", len(claims))
	for _, claim := range claims {
		started := time.Now()
		err := l.processRecycle(ctx, claim)
		l.observe("recycle_item", err, started)
	}
}

func (l *Loop) processRecycle(ctx context.Context, claim drive.RecycleMaintenanceClaim) error {
	now := l.now()
	plan, err := l.repository.PreparePurge(ctx, claim.Identity, claim.RootID, claim.Revision, now)
	if err == nil {
		for _, blob := range plan.Blobs {
			if err = l.storage.DeleteObject(ctx, blob.ObjectKey); err != nil && drive.CodeOf(err) != drive.CodeNotFound {
				break
			}
		}
	}
	if err == nil {
		err = l.repository.FinishPurge(ctx, claim.Identity, plan, now)
	}
	if err == nil {
		return nil
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	releaseErr := l.repository.ReleaseRecycleMaintenance(releaseCtx, claim.Owner, claim.RootID, now.Add(backoff(0)), maintenanceErrorCode(err), now)
	if releaseErr != nil {
		return releaseErr
	}
	return err
}

func (l *Loop) runIdempotency(ctx context.Context) {
	started := time.Now()
	count, err := l.repository.DeleteExpiredIdempotency(ctx, l.now(), l.batchSize)
	l.observe("idempotency", err, started)
	if err == nil {
		l.backlog("idempotency", count)
	}
}

func (l *Loop) observe(task string, err error, started time.Time) {
	if l.metrics == nil {
		return
	}
	result := "success"
	if err != nil && !errors.Is(err, context.Canceled) {
		result = "error"
	}
	l.metrics.ObserveMaintenance(task, result, time.Since(started))
}
func (l *Loop) backlog(task string, count int) {
	if l.metrics != nil {
		l.metrics.SetMaintenanceBacklog(task, count)
	}
}

func backoff(attempt int) time.Duration {
	if attempt > 8 {
		attempt = 8
	}
	delay := time.Second * time.Duration(1<<attempt)
	return delay + time.Duration(rand.Int64N(max(int64(delay/4), 1)))
}
func maintenanceErrorCode(err error) string {
	switch drive.CodeOf(err) {
	case drive.CodeDependencyUnavailable:
		return "dependency_unavailable"
	case drive.CodeNotFound:
		return "not_found"
	case drive.CodeInvalidRequest, drive.CodeInvalidState, drive.CodeNameConflict:
		return "rejected"
	default:
		return "internal"
	}
}
func failedUploadHasObject(code string) bool {
	return code == drive.UploadFailureSizeMismatch || code == drive.UploadFailureChecksumMismatch || code == drive.UploadFailureParentUnavailable || code == drive.UploadFailureNameConflict
}
func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
