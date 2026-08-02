package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/auth"
	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/baicie/asteria-drive/internal/postgres"
	"github.com/baicie/asteria-drive/internal/s3store"
	"github.com/jackc/pgx/v5"
)

const (
	liveDatabaseEnv = "ASTERIA_TEST_DATABASE_URL"
	liveS3Endpoint  = "ASTERIA_TEST_S3_ENDPOINT"

	liveTokenA     = "asteria-live-tenant-a-token-00000000000000000000"
	liveTokenB     = "asteria-live-tenant-b-token-00000000000000000000"
	liveTenantA    = "70000000-0000-4000-8000-000000000001"
	liveTenantB    = "70000000-0000-4000-8000-000000000002"
	livePrincipalA = "70000000-0000-4000-8000-000000000003"
	livePrincipalB = "70000000-0000-4000-8000-000000000004"
)

func TestLiveHTTPPostgresSeaweedFSEndToEnd(t *testing.T) {
	baseDatabaseURL := os.Getenv(liveDatabaseEnv)
	endpoint := os.Getenv(liveS3Endpoint)
	if baseDatabaseURL == "" || endpoint == "" {
		t.Skipf("set %s and %s to run the live HTTP end-to-end test", liveDatabaseEnv, liveS3Endpoint)
	}
	accessKey := liveRequiredEnv(t, "ASTERIA_TEST_S3_ACCESS_KEY")
	secretKey := liveRequiredEnv(t, "ASTERIA_TEST_S3_SECRET_KEY")
	region := liveEnvOrDefault("ASTERIA_TEST_S3_REGION", "us-east-1")
	bucket := liveEnvOrDefault("ASTERIA_TEST_S3_BUCKET", "asteria-http-e2e")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	databaseURL := liveIsolatedDatabaseURL(t, baseDatabaseURL)
	if err := postgres.Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("migrate live HTTP schema: %v", err)
	}
	storage, err := s3store.New(ctx, s3store.Options{
		Endpoint: endpoint, Region: region, Bucket: bucket,
		AccessKey: accessKey, SecretKey: secretKey, UsePathStyle: true, AutoCreateBucket: true,
	})
	if err != nil {
		t.Fatalf("create live S3 provider: %v", err)
	}

	first := startLiveHTTPServer(t, ctx, databaseURL, storage)
	liveRequireStatus(t, first.request(ctx, http.MethodGet, "/readyz", "", nil, nil), http.StatusOK)
	tenantAResponse := first.request(ctx, http.MethodGet, "/api/v1/tenant", liveTokenA, nil, nil)
	liveRequireStatus(t, tenantAResponse, http.StatusOK)
	tenantA := liveDecodeData[tenantResponse](t, tenantAResponse)
	tenantBResponse := first.request(ctx, http.MethodGet, "/api/v1/tenant", liveTokenB, nil, nil)
	liveRequireStatus(t, tenantBResponse, http.StatusOK)
	tenantB := liveDecodeData[tenantResponse](t, tenantBResponse)
	if tenantA.ID != liveTenantA || tenantA.RootDirectoryID == "" || tenantB.ID != liveTenantB || tenantB.RootDirectoryID == "" {
		t.Fatalf("tenant discovery returned unexpected roots: tenantA=%+v tenantB=%+v", tenantA, tenantB)
	}
	if tenantA.RootDirectoryID == tenantB.RootDirectoryID {
		t.Fatal("isolated tenants returned the same root directory")
	}

	directoryResponse := first.request(ctx, http.MethodPost, "/api/v1/directories", liveTokenA, map[string]any{
		"parent_id": tenantA.RootDirectoryID,
		"name":      "Live E2E",
	}, nil)
	liveRequireStatus(t, directoryResponse, http.StatusCreated)
	directory := liveDecodeData[nodeResponse](t, directoryResponse)
	if directory.Kind != drive.NodeDirectory || directory.ParentID != tenantA.RootDirectoryID {
		t.Fatalf("unexpected created directory: %+v", directory)
	}
	liveRequireStatus(t, first.request(ctx, http.MethodGet, "/api/v1/directories/"+directory.ID, liveTokenA, nil, nil), http.StatusOK)
	liveRequireStatus(t, first.request(ctx, http.MethodGet, "/api/v1/directories/"+directory.ID, liveTokenB, nil, nil), http.StatusNotFound)
	liveRequireStatus(t, first.request(ctx, http.MethodGet, "/api/v1/directories/"+directory.ID+"/children", liveTokenB, nil, nil), http.StatusNotFound)
	liveRequireStatus(t, first.request(ctx, http.MethodPatch, "/api/v1/nodes/"+directory.ID, liveTokenB,
		map[string]any{"name": "foreign rename"}, map[string]string{"If-Match": `"1"`}), http.StatusNotFound)
	liveRequireStatus(t, first.request(ctx, http.MethodDelete, "/api/v1/nodes/"+directory.ID, liveTokenB,
		nil, map[string]string{"If-Match": `"1"`}), http.StatusNotFound)
	liveRequireStatus(t, first.request(ctx, http.MethodPost, "/api/v1/directories", liveTokenB, map[string]any{
		"parent_id": directory.ID, "name": "foreign child",
	}, nil), http.StatusNotFound)

	firstPart := bytes.Repeat([]byte("0123456789abcdef"), (5<<20)/16)
	secondPart := []byte("asteria-live-http-final-part")
	wantObject := append(append([]byte(nil), firstPart...), secondPart...)
	objectDigest := sha256.Sum256(wantObject)
	declaredChecksum := drive.Checksum{Algorithm: "sha256", Value: base64.StdEncoding.EncodeToString(objectDigest[:])}
	createUploadResponse := first.request(ctx, http.MethodPost, "/api/v1/uploads", liveTokenA, map[string]any{
		"parent_id": directory.ID,
		"name":      "live e2e.bin",
		"size":      len(wantObject),
		"mime_type": "application/octet-stream",
		"checksum":  declaredChecksum,
	}, nil)
	liveRequireStatus(t, createUploadResponse, http.StatusCreated)
	upload := liveDecodeData[uploadResponse](t, createUploadResponse)
	if upload.Status != drive.UploadCreated || upload.PartSize != 5<<20 {
		t.Fatalf("unexpected upload session: %+v", upload)
	}

	objectKey, storageUploadID := liveUploadStorageIdentity(t, ctx, databaseURL, upload.ID)
	objectCompleted := false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if !objectCompleted {
			if err := storage.AbortMultipart(cleanupCtx, objectKey, storageUploadID); err != nil {
				t.Errorf("abort live multipart upload during cleanup: %v", err)
			}
		}
		if err := storage.DeleteObject(cleanupCtx, objectKey); err != nil {
			t.Errorf("delete live object during cleanup: %v", err)
		}
	})

	liveRequireStatus(t, first.request(ctx, http.MethodGet, "/api/v1/uploads/"+upload.ID, liveTokenB, nil, nil), http.StatusNotFound)
	liveRequireStatus(t, first.request(ctx, http.MethodPost, "/api/v1/uploads/"+upload.ID+"/parts/sign", liveTokenB,
		map[string]any{"part_number": 1}, nil), http.StatusNotFound)
	liveRequireStatus(t, first.request(ctx, http.MethodDelete, "/api/v1/uploads/"+upload.ID, liveTokenB, nil, nil), http.StatusNotFound)
	parts := make([]drive.CompletedPart, 0, 2)
	for index, body := range [][]byte{firstPart, secondPart} {
		partNumber := index + 1
		partDigest := sha256.Sum256(body)
		partChecksum := drive.Checksum{Algorithm: "sha256", Value: base64.StdEncoding.EncodeToString(partDigest[:])}
		signResponse := first.request(ctx, http.MethodPost, "/api/v1/uploads/"+upload.ID+"/parts/sign", liveTokenA, map[string]any{
			"part_number": partNumber,
			"checksum":    partChecksum,
		}, nil)
		liveRequireStatus(t, signResponse, http.StatusOK)
		signed := liveDecodeData[liveSignedPartResponse](t, signResponse)
		etag := livePutSignedPart(t, ctx, signed, body)
		parts = append(parts, drive.CompletedPart{PartNumber: partNumber, ETag: etag, Checksum: partChecksum, Size: int64(len(body))})
	}
	liveRequireStatus(t, first.request(ctx, http.MethodPost, "/api/v1/uploads/"+upload.ID+"/complete", liveTokenB,
		map[string]any{"parts": parts}, nil), http.StatusNotFound)

	completeResponse := first.request(ctx, http.MethodPost, "/api/v1/uploads/"+upload.ID+"/complete", liveTokenA, map[string]any{
		"parts": parts,
	}, nil)
	liveRequireStatus(t, completeResponse, http.StatusCreated)
	completed := liveDecodeData[liveCompleteResponse](t, completeResponse)
	objectCompleted = true
	if completed.Upload.Status != drive.UploadCommitted || completed.File.Kind != drive.NodeFile || completed.File.Size != int64(len(wantObject)) {
		t.Fatalf("unexpected completed upload: %+v", completed)
	}
	_, committedBlob, err := first.repository.DownloadBlob(ctx, drive.Identity{TenantID: liveTenantA, PrincipalID: livePrincipalA}, completed.File.ID)
	if err != nil {
		t.Fatalf("read committed blob checksum: %v", err)
	}
	if committedBlob.Checksum != declaredChecksum || (committedBlob.ChecksumStatus != drive.ChecksumDeclared && committedBlob.ChecksumStatus != drive.ChecksumVerified) {
		t.Fatalf("committed checksum=%+v status=%s, want declared or verified %+v", committedBlob.Checksum, committedBlob.ChecksumStatus, declaredChecksum)
	}

	childrenResponse := first.request(ctx, http.MethodGet, "/api/v1/directories/"+directory.ID+"/children", liveTokenA, nil, nil)
	liveRequireStatus(t, childrenResponse, http.StatusOK)
	children := liveDecodePage[nodeResponse](t, childrenResponse)
	if len(children) != 1 || children[0].ID != completed.File.ID {
		t.Fatalf("directory listing did not contain committed file: %+v", children)
	}
	fileResponse := first.request(ctx, http.MethodGet, "/api/v1/files/"+completed.File.ID, liveTokenA, nil, nil)
	liveRequireStatus(t, fileResponse, http.StatusOK)
	if file := liveDecodeData[nodeResponse](t, fileResponse); file.Name != "live e2e.bin" || file.Revision != 1 {
		t.Fatalf("unexpected committed file metadata: %+v", file)
	}
	liveRequireStatus(t, first.request(ctx, http.MethodGet, "/api/v1/files/"+completed.File.ID, liveTokenB, nil, nil), http.StatusNotFound)
	liveRequireStatus(t, first.request(ctx, http.MethodPost, "/api/v1/files/"+completed.File.ID+"/download-authorizations", liveTokenB, nil, nil), http.StatusNotFound)
	liveRequireStatus(t, first.request(ctx, http.MethodPatch, "/api/v1/nodes/"+completed.File.ID, liveTokenB,
		map[string]any{"name": "foreign.bin"}, map[string]string{"If-Match": `"1"`}), http.StatusNotFound)
	liveRequireStatus(t, first.request(ctx, http.MethodDelete, "/api/v1/nodes/"+completed.File.ID, liveTokenB,
		nil, map[string]string{"If-Match": `"1"`}), http.StatusNotFound)

	download := liveAuthorizeDownload(t, ctx, first, completed.File.ID)
	liveVerifyDownload(t, ctx, download, wantObject)

	first.Close()
	second := startLiveHTTPServer(t, ctx, databaseURL, storage)
	liveRequireStatus(t, second.request(ctx, http.MethodGet, "/readyz", "", nil, nil), http.StatusOK)
	restartedTenant := liveDecodeData[tenantResponse](t, liveExpectStatus(t,
		second.request(ctx, http.MethodGet, "/api/v1/tenant", liveTokenA, nil, nil), http.StatusOK))
	if restartedTenant.RootDirectoryID != tenantA.RootDirectoryID {
		t.Fatalf("tenant root changed after restart: got %q, want %q", restartedTenant.RootDirectoryID, tenantA.RootDirectoryID)
	}
	restartedFileResponse := second.request(ctx, http.MethodGet, "/api/v1/files/"+completed.File.ID, liveTokenA, nil, nil)
	liveRequireStatus(t, restartedFileResponse, http.StatusOK)
	if restartedFile := liveDecodeData[nodeResponse](t, restartedFileResponse); restartedFile.Name != completed.File.Name || restartedFile.Size != completed.File.Size {
		t.Fatalf("committed metadata did not survive restart: got %+v, want %+v", restartedFile, completed.File)
	}
	restartedChildren := liveDecodePage[nodeResponse](t, liveExpectStatus(t,
		second.request(ctx, http.MethodGet, "/api/v1/directories/"+directory.ID+"/children", liveTokenA, nil, nil), http.StatusOK))
	if len(restartedChildren) != 1 || restartedChildren[0].ID != completed.File.ID {
		t.Fatalf("persisted file missing from listing after restart: %+v", restartedChildren)
	}
	liveVerifyDownload(t, ctx, liveAuthorizeDownload(t, ctx, second, completed.File.ID), wantObject)

	recycleResponse := second.request(ctx, http.MethodDelete, "/api/v1/nodes/"+completed.File.ID, liveTokenA, nil, map[string]string{"If-Match": `"1"`})
	liveRequireStatus(t, recycleResponse, http.StatusNoContent)
	liveRequireStatus(t, second.request(ctx, http.MethodGet, "/api/v1/files/"+completed.File.ID, liveTokenA, nil, nil), http.StatusNotFound)
	recycleListResponse := second.request(ctx, http.MethodGet, "/api/v1/recycle-bin", liveTokenA, nil, nil)
	liveRequireStatus(t, recycleListResponse, http.StatusOK)
	recycleEntries := liveDecodeRecyclePage(t, recycleListResponse)
	if len(recycleEntries) != 1 || recycleEntries[0].Node.ID != completed.File.ID || recycleEntries[0].Node.Revision != 2 {
		t.Fatalf("unexpected recycle-bin contents: %+v", recycleEntries)
	}
	if foreignEntries := liveDecodeRecyclePage(t, liveExpectStatus(t,
		second.request(ctx, http.MethodGet, "/api/v1/recycle-bin", liveTokenB, nil, nil), http.StatusOK)); len(foreignEntries) != 0 {
		t.Fatalf("tenant B saw tenant A recycle entries: %+v", foreignEntries)
	}
	liveRequireStatus(t, second.request(ctx, http.MethodPost, "/api/v1/recycle-bin/"+completed.File.ID+"/restore", liveTokenB,
		nil, map[string]string{"If-Match": `"2"`}), http.StatusNotFound)
	conflictResponse := second.request(ctx, http.MethodPost, "/api/v1/directories", liveTokenA, map[string]any{
		"parent_id": directory.ID, "name": completed.File.Name,
	}, nil)
	liveRequireStatus(t, conflictResponse, http.StatusCreated)
	conflict := liveDecodeData[nodeResponse](t, conflictResponse)
	restoreConflict := second.request(ctx, http.MethodPost, "/api/v1/recycle-bin/"+completed.File.ID+"/restore", liveTokenA,
		nil, map[string]string{"If-Match": `"2"`})
	liveRequireStatus(t, restoreConflict, http.StatusConflict)
	if !bytes.Contains(restoreConflict.body, []byte(`"code":"restore_conflict"`)) {
		t.Fatalf("restore conflict returned the wrong error: %s", restoreConflict.body)
	}
	renameConflictResponse := second.request(ctx, http.MethodPatch, "/api/v1/nodes/"+conflict.ID, liveTokenA,
		map[string]any{"name": "released conflict"}, map[string]string{"If-Match": `"1"`})
	liveRequireStatus(t, renameConflictResponse, http.StatusOK)

	restoreResponse := second.request(ctx, http.MethodPost, "/api/v1/recycle-bin/"+completed.File.ID+"/restore", liveTokenA, nil, map[string]string{"If-Match": `"2"`})
	liveRequireStatus(t, restoreResponse, http.StatusOK)
	restored := liveDecodeData[nodeResponse](t, restoreResponse)
	if restored.Revision != 3 {
		t.Fatalf("restored revision = %d, want 3", restored.Revision)
	}
	liveRequireStatus(t, second.request(ctx, http.MethodPost, "/api/v1/files/"+completed.File.ID+"/download-authorizations", liveTokenA, nil, nil), http.StatusCreated)
	liveRequireStatus(t, second.request(ctx, http.MethodDelete, "/api/v1/nodes/"+completed.File.ID, liveTokenA, nil, map[string]string{"If-Match": `"3"`}), http.StatusNoContent)
	liveRequireStatus(t, second.request(ctx, http.MethodDelete, "/api/v1/recycle-bin/"+completed.File.ID, liveTokenB, nil, map[string]string{"If-Match": `"4"`}), http.StatusNotFound)
	liveRequireStatus(t, second.request(ctx, http.MethodDelete, "/api/v1/recycle-bin/"+completed.File.ID, liveTokenA, nil, map[string]string{"If-Match": `"4"`}), http.StatusNoContent)

	liveWaitForObjectMissing(t, ctx, storage, objectKey)
	liveRequireStatus(t, second.request(ctx, http.MethodGet, "/api/v1/files/"+completed.File.ID, liveTokenA, nil, nil), http.StatusNotFound)
	if entries := liveDecodeRecyclePage(t, liveExpectStatus(t,
		second.request(ctx, http.MethodGet, "/api/v1/recycle-bin", liveTokenA, nil, nil), http.StatusOK)); len(entries) != 0 {
		t.Fatalf("purged entry remained visible in recycle bin: %+v", entries)
	}
}

func TestLiveHTTPConcurrentUploadSessionBaseline(t *testing.T) {
	baseDatabaseURL := os.Getenv(liveDatabaseEnv)
	endpoint := os.Getenv(liveS3Endpoint)
	if baseDatabaseURL == "" || endpoint == "" {
		t.Skipf("set %s and %s to run the live HTTP concurrency baseline", liveDatabaseEnv, liveS3Endpoint)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	databaseURL := liveIsolatedDatabaseURL(t, baseDatabaseURL)
	if err := postgres.Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("migrate concurrency schema: %v", err)
	}
	storage, err := s3store.New(ctx, s3store.Options{
		Endpoint: endpoint, Region: liveEnvOrDefault("ASTERIA_TEST_S3_REGION", "us-east-1"),
		Bucket:       liveEnvOrDefault("ASTERIA_TEST_S3_BUCKET", "asteria-http-e2e"),
		AccessKey:    liveRequiredEnv(t, "ASTERIA_TEST_S3_ACCESS_KEY"),
		SecretKey:    liveRequiredEnv(t, "ASTERIA_TEST_S3_SECRET_KEY"),
		UsePathStyle: true, AutoCreateBucket: true,
	})
	if err != nil {
		t.Fatalf("create concurrency S3 provider: %v", err)
	}
	server := startLiveHTTPServer(t, ctx, databaseURL, storage)
	tenant := liveDecodeData[tenantResponse](t, liveExpectStatus(t,
		server.request(ctx, http.MethodGet, "/api/v1/tenant", liveTokenA, nil, nil), http.StatusOK))
	directory := liveDecodeData[nodeResponse](t, liveExpectStatus(t,
		server.request(ctx, http.MethodPost, "/api/v1/directories", liveTokenA, map[string]any{
			"parent_id": tenant.RootDirectoryID, "name": "Concurrent sessions",
		}, nil), http.StatusCreated))

	const sessionCount = 100
	type createResult struct {
		index   int
		latency time.Duration
		status  int
		body    []byte
		err     error
	}
	results := make(chan createResult, sessionCount)
	var wait sync.WaitGroup
	for index := range sessionCount {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			started := time.Now()
			response, err := liveConcurrentJSONRequest(ctx, server, http.MethodPost, "/api/v1/uploads", liveTokenA, map[string]any{
				"parent_id": directory.ID, "name": fmt.Sprintf("session-%03d.bin", index),
				"size": 1, "mime_type": "application/octet-stream",
			})
			results <- createResult{index: index, latency: time.Since(started), status: response.status, body: response.body, err: err}
		}(index)
	}
	wait.Wait()
	close(results)
	uploads := make([]uploadResponse, sessionCount)
	latencies := make([]time.Duration, 0, sessionCount)
	seen := make(map[string]struct{}, sessionCount)
	for result := range results {
		if result.err != nil || result.status != http.StatusCreated {
			t.Fatalf("create concurrent upload %d: status=%d err=%v body=%s", result.index, result.status, result.err, result.body)
		}
		var envelope struct {
			Data uploadResponse `json:"data"`
		}
		if err := json.Unmarshal(result.body, &envelope); err != nil {
			t.Fatalf("decode concurrent upload %d: %v", result.index, err)
		}
		if _, duplicate := seen[envelope.Data.ID]; duplicate {
			t.Fatalf("duplicate upload ID %q", envelope.Data.ID)
		}
		seen[envelope.Data.ID] = struct{}{}
		uploads[result.index] = envelope.Data
		latencies = append(latencies, result.latency)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		for _, upload := range uploads {
			if upload.ID != "" {
				_ = server.request(cleanupCtx, http.MethodDelete, "/api/v1/uploads/"+upload.ID, liveTokenA, nil, nil)
			}
		}
	})

	type operationResult struct {
		index  int
		status int
		body   []byte
		err    error
	}
	operations := make(chan operationResult, sessionCount)
	for index, upload := range uploads {
		wait.Add(1)
		go func(index int, uploadID string) {
			defer wait.Done()
			response, err := liveConcurrentJSONRequest(ctx, server, http.MethodPost, "/api/v1/uploads/"+uploadID+"/parts/sign", liveTokenA, map[string]any{"part_number": 1})
			if err == nil && response.status == http.StatusOK {
				response, err = liveConcurrentJSONRequest(ctx, server, http.MethodGet, "/api/v1/uploads/"+uploadID, liveTokenA, nil)
			}
			operations <- operationResult{index: index, status: response.status, body: response.body, err: err}
		}(index, upload.ID)
	}
	wait.Wait()
	close(operations)
	for result := range operations {
		if result.err != nil || result.status != http.StatusOK {
			t.Fatalf("sign/query concurrent upload %d: status=%d err=%v body=%s", result.index, result.status, result.err, result.body)
		}
	}

	children := liveDecodePage[nodeResponse](t, liveExpectStatus(t,
		server.request(ctx, http.MethodGet, "/api/v1/directories/"+directory.ID+"/children", liveTokenA, nil, nil), http.StatusOK))
	if len(children) != 0 {
		t.Fatalf("uncommitted sessions became visible in namespace: %+v", children)
	}
	liveRequireStatus(t, server.request(ctx, http.MethodGet, "/api/v1/uploads/"+uploads[0].ID, liveTokenB, nil, nil), http.StatusNotFound)
	for _, upload := range uploads {
		liveRequireStatus(t, server.request(ctx, http.MethodDelete, "/api/v1/uploads/"+upload.ID, liveTokenA, nil, nil), http.StatusNoContent)
		liveRequireStatus(t, server.request(ctx, http.MethodDelete, "/api/v1/uploads/"+upload.ID, liveTokenA, nil, nil), http.StatusNoContent)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("100-session create baseline: p50=%s p95=%s p99=%s max=%s",
		latencies[49], latencies[94], latencies[98], latencies[99])
}

type liveServer struct {
	t          *testing.T
	baseURL    string
	client     *http.Client
	httpServer *httptest.Server
	repository *postgres.Repository
	closeOnce  sync.Once
}

func startLiveHTTPServer(t *testing.T, ctx context.Context, databaseURL string, storage *s3store.Provider) *liveServer {
	t.Helper()
	repository, err := postgres.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open live PostgreSQL repository: %v", err)
	}
	cursor, err := drive.NewCursorCodec([]byte("asteria-live-http-e2e-cursor-key-000000000000"))
	if err != nil {
		repository.Close()
		t.Fatalf("create live cursor codec: %v", err)
	}
	service, err := drive.NewService(drive.ServiceOptions{
		Repository: repository, Storage: storage, Cursor: cursor,
		MaxFileSize: 1 << 30, PartSize: 5 << 20, UploadTTL: time.Hour,
		UploadSignTTL: 5 * time.Minute, DownloadSignTTL: 5 * time.Minute,
	})
	if err != nil {
		repository.Close()
		t.Fatalf("create live drive service: %v", err)
	}
	principals := map[string]auth.Principal{
		liveTokenA: {Identity: drive.Identity{TenantID: liveTenantA, PrincipalID: livePrincipalA}, TenantDisplayName: "Live Tenant A"},
		liveTokenB: {Identity: drive.Identity{TenantID: liveTenantB, PrincipalID: livePrincipalB}, TenantDisplayName: "Live Tenant B"},
	}
	for _, principal := range principals {
		if _, err := service.EnsureTenant(ctx, principal.Identity.TenantID, principal.TenantDisplayName); err != nil {
			repository.Close()
			t.Fatalf("ensure live tenant %s: %v", principal.Identity.TenantID, err)
		}
	}
	authenticator, err := auth.NewTrusted(principals)
	if err != nil {
		repository.Close()
		t.Fatalf("create live trusted authenticator: %v", err)
	}
	httpService, err := New(Options{
		Address: ":0", Service: service, Authenticator: authenticator,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ReadHeaderTimeout: time.Second,
	})
	if err != nil {
		repository.Close()
		t.Fatalf("create live HTTP server: %v", err)
	}
	testServer := httptest.NewServer(httpService.Handler())
	live := &liveServer{t: t, baseURL: testServer.URL, client: testServer.Client(), httpServer: testServer, repository: repository}
	t.Cleanup(live.Close)
	return live
}

func (s *liveServer) Close() {
	s.closeOnce.Do(func() {
		s.httpServer.Close()
		s.repository.Close()
	})
}

type liveHTTPResponse struct {
	status int
	header http.Header
	body   []byte
}

func (s *liveServer) request(ctx context.Context, method, path, token string, body any, headers map[string]string) liveHTTPResponse {
	s.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			s.t.Fatalf("encode live request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, reader)
	if err != nil {
		s.t.Fatalf("create live request: %v", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := s.client.Do(request)
	if err != nil {
		s.t.Fatalf("execute live request: %v", err)
	}
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		s.t.Fatalf("read live response: %v", readErr)
	}
	if closeErr != nil {
		s.t.Fatalf("close live response: %v", closeErr)
	}
	return liveHTTPResponse{status: response.StatusCode, header: response.Header.Clone(), body: responseBody}
}

func liveConcurrentJSONRequest(ctx context.Context, server *liveServer, method, path, token string, body any) (liveHTTPResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return liveHTTPResponse{}, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, server.baseURL+path, reader)
	if err != nil {
		return liveHTTPResponse{}, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := server.client.Do(request)
	if err != nil {
		return liveHTTPResponse{}, fmt.Errorf("execute request: %w", err)
	}
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		return liveHTTPResponse{}, fmt.Errorf("read response: %w", readErr)
	}
	if closeErr != nil {
		return liveHTTPResponse{}, fmt.Errorf("close response: %w", closeErr)
	}
	return liveHTTPResponse{status: response.StatusCode, header: response.Header.Clone(), body: responseBody}, nil
}

type liveSignedPartResponse struct {
	PartNumber      int               `json:"part_number"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	RequiredHeaders map[string]string `json:"required_headers"`
}

type liveCompleteResponse struct {
	Upload uploadResponse `json:"upload"`
	File   nodeResponse   `json:"file"`
}

type liveDownloadResponse struct {
	Method string `json:"method"`
	URL    string `json:"url"`
}

type liveRecycleEntry struct {
	Node             nodeResponse `json:"node"`
	OriginalParentID string       `json:"original_parent_id"`
}

func liveDecodeData[T any](t *testing.T, response liveHTTPResponse) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(response.body, &envelope); err != nil {
		t.Fatalf("decode response data: %v; body=%s", err, response.body)
	}
	return envelope.Data
}

func liveDecodePage[T any](t *testing.T, response liveHTTPResponse) []T {
	t.Helper()
	var envelope struct {
		Data []T `json:"data"`
	}
	if err := json.Unmarshal(response.body, &envelope); err != nil {
		t.Fatalf("decode response page: %v; body=%s", err, response.body)
	}
	return envelope.Data
}

func liveDecodeRecyclePage(t *testing.T, response liveHTTPResponse) []liveRecycleEntry {
	t.Helper()
	return liveDecodePage[liveRecycleEntry](t, response)
}

func liveRequireStatus(t *testing.T, response liveHTTPResponse, want int) {
	t.Helper()
	if response.status != want {
		t.Fatalf("HTTP status = %d, want %d; body=%s", response.status, want, response.body)
	}
}

func liveExpectStatus(t *testing.T, response liveHTTPResponse, want int) liveHTTPResponse {
	t.Helper()
	liveRequireStatus(t, response, want)
	return response
}

func livePutSignedPart(t *testing.T, ctx context.Context, signed liveSignedPartResponse, body []byte) string {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, signed.Method, signed.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create signed part request: %v", err)
	}
	for name, value := range signed.RequiredHeaders {
		if http.CanonicalHeaderKey(name) == "Host" {
			request.Host = value
			continue
		}
		request.Header.Set(name, value)
	}
	response, err := (&http.Client{Timeout: 45 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("upload signed part %d: %v", signed.PartNumber, err)
	}
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read signed part %d response: read=%v close=%v", signed.PartNumber, readErr, closeErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		t.Fatalf("signed part %d returned %s: %s", signed.PartNumber, response.Status, responseBody)
	}
	etag := response.Header.Get("ETag")
	if etag == "" {
		t.Fatalf("signed part %d returned no ETag", signed.PartNumber)
	}
	return etag
}

func liveAuthorizeDownload(t *testing.T, ctx context.Context, server *liveServer, fileID string) liveDownloadResponse {
	t.Helper()
	response := server.request(ctx, http.MethodPost, "/api/v1/files/"+fileID+"/download-authorizations", liveTokenA, nil, nil)
	liveRequireStatus(t, response, http.StatusCreated)
	return liveDecodeData[liveDownloadResponse](t, response)
}

func liveVerifyDownload(t *testing.T, ctx context.Context, signed liveDownloadResponse, want []byte) {
	t.Helper()
	client := &http.Client{Timeout: 45 * time.Second}
	fullRequest, err := http.NewRequestWithContext(ctx, signed.Method, signed.URL, nil)
	if err != nil {
		t.Fatalf("create signed full download request: %v", err)
	}
	fullResponse, err := client.Do(fullRequest)
	if err != nil {
		t.Fatalf("execute signed full download: %v", err)
	}
	fullBody, readErr := io.ReadAll(fullResponse.Body)
	closeErr := fullResponse.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read signed full download: read=%v close=%v", readErr, closeErr)
	}
	if fullResponse.StatusCode != http.StatusOK || !bytes.Equal(fullBody, want) {
		t.Fatalf("signed full download returned status=%s bytes=%d, want status=200 bytes=%d", fullResponse.Status, len(fullBody), len(want))
	}
	if disposition := fullResponse.Header.Get("Content-Disposition"); disposition != `attachment; filename="live e2e.bin"` {
		t.Fatalf("signed full download Content-Disposition = %q", disposition)
	}

	const rangeStart, rangeEnd = 31, 127
	rangeRequest, err := http.NewRequestWithContext(ctx, signed.Method, signed.URL, nil)
	if err != nil {
		t.Fatalf("create signed range download request: %v", err)
	}
	rangeRequest.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rangeStart, rangeEnd))
	rangeResponse, err := client.Do(rangeRequest)
	if err != nil {
		t.Fatalf("execute signed range download: %v", err)
	}
	rangeBody, readErr := io.ReadAll(rangeResponse.Body)
	closeErr = rangeResponse.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read signed range download: read=%v close=%v", readErr, closeErr)
	}
	wantRange := want[rangeStart : rangeEnd+1]
	if rangeResponse.StatusCode != http.StatusPartialContent || !bytes.Equal(rangeBody, wantRange) {
		t.Fatalf("signed range download returned status=%s bytes=%d, want status=206 bytes=%d", rangeResponse.Status, len(rangeBody), len(wantRange))
	}
	wantContentRange := fmt.Sprintf("bytes %d-%d/%d", rangeStart, rangeEnd, len(want))
	if got := rangeResponse.Header.Get("Content-Range"); got != wantContentRange {
		t.Fatalf("signed range Content-Range = %q, want %q", got, wantContentRange)
	}
}

func liveUploadStorageIdentity(t *testing.T, ctx context.Context, databaseURL, uploadID string) (string, string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to inspect live upload storage identity: %v", err)
	}
	defer conn.Close(ctx)
	var objectKey, storageUploadID string
	if err := conn.QueryRow(ctx, `SELECT object_key,storage_upload_id FROM upload_session WHERE id=$1`, uploadID).Scan(&objectKey, &storageUploadID); err != nil {
		t.Fatalf("read live upload storage identity: %v", err)
	}
	return objectKey, storageUploadID
}

func liveWaitForObjectMissing(t *testing.T, ctx context.Context, storage *s3store.Provider, objectKey string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := storage.StatObject(ctx, objectKey)
		if drive.CodeOf(err) == drive.CodeNotFound {
			return
		}
		if err != nil {
			t.Fatalf("stat purged object: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("purged object %q remained in storage", objectKey)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func liveIsolatedDatabaseURL(t *testing.T, base string) string {
	t.Helper()
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("%s must be a PostgreSQL URL", liveDatabaseEnv)
	}
	admin, err := pgx.Connect(context.Background(), base)
	if err != nil {
		t.Fatalf("connect to live integration database: %v", err)
	}
	schema := fmt.Sprintf("asteria_http_e2e_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(context.Background(), `CREATE SCHEMA `+identifier); err != nil {
		_ = admin.Close(context.Background())
		t.Fatalf("create live integration schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, `DROP SCHEMA `+identifier+` CASCADE`); err != nil {
			t.Errorf("drop live integration schema: %v", err)
		}
		if err := admin.Close(cleanupCtx); err != nil {
			t.Errorf("close live integration database: %v", err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func liveRequiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required when %s is set", name, liveS3Endpoint)
	}
	return value
}

func liveEnvOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
