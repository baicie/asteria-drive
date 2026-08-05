package recovery

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

type BlobRecord struct {
	ID             string
	TenantID       string
	Bucket         string
	ObjectKey      string
	Size           int64
	Checksum       drive.Checksum
	ChecksumStatus drive.ChecksumStatus
}

type BlobCursor struct {
	TenantID string
	BlobID   string
}

type Catalog interface {
	SchemaVersion(context.Context) (int64, error)
	ListAvailableBlobs(context.Context, BlobCursor, int) ([]BlobRecord, error)
	Close()
}

type Options struct {
	Catalog     Catalog
	Storage     drive.StorageProvider
	BatchSize   int
	Concurrency int
	MaxFindings int
	Now         func() time.Time
}

type Finding struct {
	Code     string `json:"code"`
	TenantID string `json:"tenant_id"`
	BlobID   string `json:"blob_id"`
}

type Report struct {
	StartedAt     time.Time      `json:"started_at"`
	FinishedAt    time.Time      `json:"finished_at"`
	SchemaVersion int64          `json:"schema_version"`
	Checked       int64          `json:"checked"`
	Healthy       int64          `json:"healthy"`
	Counts        map[string]int `json:"finding_counts"`
	Findings      []Finding      `json:"findings"`
	Truncated     bool           `json:"findings_truncated"`
	Verified      bool           `json:"verified"`
}

type Verifier struct {
	catalog     Catalog
	storage     drive.StorageProvider
	batchSize   int
	concurrency int
	maxFindings int
	now         func() time.Time
}

func NewVerifier(options Options) (*Verifier, error) {
	if options.Catalog == nil || options.Storage == nil {
		return nil, drive.E(drive.CodeInvalidRequest, "catalog and storage are required")
	}
	if options.BatchSize == 0 {
		options.BatchSize = 500
	}
	if options.Concurrency == 0 {
		options.Concurrency = 16
	}
	if options.MaxFindings == 0 {
		options.MaxFindings = 100
	}
	if options.BatchSize < 1 || options.BatchSize > 10000 || options.Concurrency < 1 || options.Concurrency > 256 || options.MaxFindings < 1 || options.MaxFindings > 10000 {
		return nil, drive.E(drive.CodeInvalidRequest, "verification limits are invalid")
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Verifier{
		catalog: options.Catalog, storage: options.Storage, batchSize: options.BatchSize,
		concurrency: options.Concurrency, maxFindings: options.MaxFindings, now: options.Now,
	}, nil
}

func (v *Verifier) Verify(ctx context.Context) (Report, error) {
	report := Report{StartedAt: v.now(), Counts: make(map[string]int)}
	version, err := v.catalog.SchemaVersion(ctx)
	if err != nil {
		return Report{}, err
	}
	report.SchemaVersion = version
	cursor := BlobCursor{}
	for {
		items, err := v.catalog.ListAvailableBlobs(ctx, cursor, v.batchSize)
		if err != nil {
			return Report{}, err
		}
		if len(items) == 0 {
			break
		}
		findings, err := v.verifyBatch(ctx, items)
		if err != nil {
			return Report{}, err
		}
		report.Checked += int64(len(items))
		report.Healthy += int64(len(items) - len(findings))
		for _, finding := range findings {
			report.Counts[finding.Code]++
			if len(report.Findings) < v.maxFindings {
				report.Findings = append(report.Findings, finding)
			} else {
				report.Truncated = true
			}
		}
		last := items[len(items)-1]
		cursor = BlobCursor{TenantID: last.TenantID, BlobID: last.ID}
		if len(items) < v.batchSize {
			break
		}
	}
	report.FinishedAt = v.now()
	report.Verified = len(report.Counts) == 0
	return report, nil
}

func (v *Verifier) verifyBatch(ctx context.Context, items []BlobRecord) ([]Finding, error) {
	type result struct {
		finding *Finding
		err     error
	}
	jobs := make(chan BlobRecord)
	results := make(chan result, len(items))
	var workers sync.WaitGroup
	workerCount := v.concurrency
	if workerCount > len(items) {
		workerCount = len(items)
	}
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				finding, err := v.verifyBlob(ctx, item)
				results <- result{finding: finding, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, item := range items {
			select {
			case jobs <- item:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	findings := make([]Finding, 0)
	for item := range results {
		if item.err != nil {
			return nil, item.err
		}
		if item.finding != nil {
			findings = append(findings, *item.finding)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].TenantID < findings[j].TenantID ||
			(findings[i].TenantID == findings[j].TenantID && findings[i].BlobID < findings[j].BlobID)
	})
	return findings, nil
}

func (v *Verifier) verifyBlob(ctx context.Context, item BlobRecord) (*Finding, error) {
	makeFinding := func(code string) *Finding {
		return &Finding{Code: code, TenantID: item.TenantID, BlobID: item.ID}
	}
	if item.Bucket != v.storage.Bucket() {
		return makeFinding("bucket_mismatch"), nil
	}
	object, err := v.storage.StatObject(ctx, item.ObjectKey)
	if err != nil {
		switch drive.CodeOf(err) {
		case drive.CodeNotFound:
			return makeFinding("object_missing"), nil
		case drive.CodeDependencyUnavailable:
			return makeFinding("storage_unavailable"), nil
		default:
			return makeFinding("storage_error"), nil
		}
	}
	if object.Size != item.Size {
		return makeFinding("size_mismatch"), nil
	}
	if item.ChecksumStatus == drive.ChecksumVerified && item.Checksum.Algorithm != "" {
		if object.ChecksumStatus != drive.ChecksumVerified || object.Checksum != item.Checksum {
			return makeFinding("checksum_mismatch"), nil
		}
	}
	if item.ChecksumStatus == drive.ChecksumDeclared && object.ChecksumStatus == drive.ChecksumVerified && object.Checksum != item.Checksum {
		return makeFinding("checksum_mismatch"), nil
	}
	return nil, nil
}
