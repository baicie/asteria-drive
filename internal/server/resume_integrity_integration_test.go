package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/baicie/asteria-drive/internal/postgres"
	"github.com/baicie/asteria-drive/internal/s3store"
)

// TestLiveReSignKeepsStoredPartIdentity is the R3 regression for the R2 defect that a
// later signature silently replaced the identity of a part already in object storage.
//
// Sequence, on real PostgreSQL and real SeaweedFS:
//
//  1. sign part 1 with file A's digest and PUT A;
//  2. sign part 1 again with file B's digest, but never PUT B (interrupted attempt);
//  3. read the part listing: it must not claim that storage holds B;
//  4. resume with file B exactly as the client does -- skip part 1 only when the listing
//     proves B's bytes are stored, otherwise re-send it -- then complete;
//  5. download the object and compare every byte with file B.
//
// On the R2 implementation step 3 reports B's digest for A's stored part and step 5
// returns A's first part spliced onto B's second part. This test fails there.
func TestLiveReSignKeepsStoredPartIdentity(t *testing.T) {
	baseDatabaseURL := os.Getenv(liveDatabaseEnv)
	endpoint := os.Getenv(liveS3Endpoint)
	if baseDatabaseURL == "" || endpoint == "" {
		t.Skipf("set %s and %s to run the re-sign integrity test", liveDatabaseEnv, liveS3Endpoint)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	databaseURL := liveIsolatedDatabaseURL(t, baseDatabaseURL)
	if err := postgres.Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("migrate re-sign schema: %v", err)
	}
	// EnableChecksumHeaders is the mode CI / web-live and the R2 evidence used. It is what
	// makes the server record a signature at all; without it the R2 path never engages and
	// the client re-uploads everything (the safe degradation, covered separately).
	storage, err := s3store.New(ctx, s3store.Options{
		Endpoint: endpoint, Region: liveEnvOrDefault("ASTERIA_TEST_S3_REGION", "us-east-1"),
		Bucket:       liveEnvOrDefault("ASTERIA_TEST_S3_BUCKET", "asteria-http-e2e"),
		AccessKey:    liveRequiredEnv(t, "ASTERIA_TEST_S3_ACCESS_KEY"),
		SecretKey:    liveRequiredEnv(t, "ASTERIA_TEST_S3_SECRET_KEY"),
		UsePathStyle: true, AutoCreateBucket: true, EnableChecksumHeaders: true,
	})
	if err != nil {
		t.Fatalf("create re-sign S3 provider: %v", err)
	}
	server := startLiveHTTPServer(t, ctx, databaseURL, storage)
	tenant := liveDecodeData[tenantResponse](t, liveExpectStatus(t,
		server.request(ctx, http.MethodGet, "/api/v1/tenant", liveTokenA, nil, nil), http.StatusOK))

	// Files A and B agree on name, size and lastModified and differ in every byte.
	partSize := 5 << 20
	size := partSize + 137
	payloadA := bytes.Repeat([]byte{0xA1}, size)
	payloadB := bytes.Repeat([]byte{0xB2}, size)
	chunkA := payloadA[:partSize]
	chunkB := payloadB[:partSize]
	digestA := sha256Digest(chunkA)
	digestB := sha256Digest(chunkB)
	if digestA == digestB {
		t.Fatal("the two candidate parts must differ")
	}

	created := liveDecodeData[uploadResponse](t, liveExpectStatus(t,
		server.request(ctx, http.MethodPost, "/api/v1/uploads", liveTokenA, map[string]any{
			"parent_id": tenant.RootDirectoryID, "name": "re-sign.bin", "size": size,
			"mime_type": "application/octet-stream",
		}, nil), http.StatusCreated))
	objectKey, storageUploadID := liveUploadStorageIdentity(t, ctx, databaseURL, created.ID)
	objectCompleted := false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if !objectCompleted {
			_ = storage.AbortMultipart(cleanupCtx, objectKey, storageUploadID)
		}
		_ = storage.DeleteObject(cleanupCtx, objectKey)
	})

	// 1. Sign and store A's first part. This is a completed PUT: storage now holds A.
	etagA := liveSignAndPutPart(t, ctx, server, created.ID, 1, digestA, chunkA)
	// 2. Re-sign the same part with B's digest and never send the PUT. This is the
	// interrupted attempt the defect treated as proof of stored content.
	liveSignOnly(t, ctx, server, created.ID, 1, digestB)

	// 3. The listing describes the part that is actually stored. It must not carry B's
	// digest, because storage never saw B's bytes.
	listing := liveDecodeData[liveUploadPartsResponse](t, liveExpectStatus(t,
		server.request(ctx, http.MethodGet, "/api/v1/uploads/"+created.ID+"/parts", liveTokenA, nil, nil), http.StatusOK))
	if len(listing.Parts) != 1 || listing.Parts[0].PartNumber != 1 {
		t.Fatalf("listing after re-sign = %+v, want exactly part 1", listing.Parts)
	}
	if listing.Parts[0].StorageSHA256 == digestB {
		t.Fatalf("part listing claims the stored part holds file B's bytes after only B's signature was issued: storage_sha256=%q stored etag=%q",
			listing.Parts[0].StorageSHA256, listing.Parts[0].ETag)
	}
	// SeaweedFS 3.85 reports no per-part checksum, so the conservative answer is expected:
	// the part is not reusable and the resume re-sends it.
	if listing.Parts[0].Reusable {
		t.Fatalf("SeaweedFS reports no per-part checksum, so the part cannot be proven reusable: %+v", listing.Parts[0])
	}

	// 4. Resume with file B using the documented client rule: skip a listed part only when
	// the server marks it reusable AND its storage digest equals the selected file's part
	// digest. Anything else is re-sent.
	parts := make([]drive.CompletedPart, 0, 2)
	if listed := listing.Parts[0]; listed.Reusable && listed.StorageSHA256 == digestB {
		parts = append(parts, drive.CompletedPart{PartNumber: 1, ETag: listed.ETag, Size: listed.Size})
	} else {
		etag := liveSignAndPutPart(t, ctx, server, created.ID, 1, digestB, chunkB)
		parts = append(parts, drive.CompletedPart{PartNumber: 1, ETag: etag, Size: int64(len(chunkB))})
	}
	etag2 := liveSignAndPutPart(t, ctx, server, created.ID, 2, sha256Digest(payloadB[partSize:]), payloadB[partSize:])
	parts = append(parts, drive.CompletedPart{PartNumber: 2, ETag: etag2, Size: int64(size - partSize)})
	liveRequireStatus(t, server.request(ctx, http.MethodPost, "/api/v1/uploads/"+created.ID+"/complete", liveTokenA,
		map[string]any{"parts": parts}, nil), http.StatusCreated)
	objectCompleted = true

	// 5. Every byte must be file B. A skipped part from file A is a silent corruption.
	committed := liveDecodePage[nodeResponse](t, liveExpectStatus(t,
		server.request(ctx, http.MethodGet, "/api/v1/directories/"+tenant.RootDirectoryID+"/children", liveTokenA, nil, nil), http.StatusOK))
	var fileID string
	for _, node := range committed {
		if node.Name == "re-sign.bin" {
			fileID = node.ID
		}
	}
	if fileID == "" {
		t.Fatalf("committed file missing from directory listing: %+v", committed)
	}
	download := liveAuthorizeDownload(t, ctx, server, fileID)
	object := liveDownloadBytes(t, ctx, download)
	if len(object) != size {
		t.Fatalf("committed object is %d bytes, want %d", len(object), size)
	}
	if !bytes.Equal(object, payloadB) {
		sameAsA := bytes.Equal(object, payloadA)
		t.Fatalf("committed object is not the re-selected file B byte for byte (equals A entirely: %v, etagA=%q)", sameAsA, etagA)
	}
}

// liveSignAndPutPart signs one part with the digest of body and stores it.
func liveSignAndPutPart(t *testing.T, ctx context.Context, server *liveServer, uploadID string, partNumber int, digest string, body []byte) string {
	t.Helper()
	signed := liveSignOnly(t, ctx, server, uploadID, partNumber, digest)
	return livePutSignedPart(t, ctx, signed, body)
}

// liveSignOnly requests a signature without sending any part.
func liveSignOnly(t *testing.T, ctx context.Context, server *liveServer, uploadID string, partNumber int, digest string) liveSignedPartResponse {
	t.Helper()
	return liveDecodeData[liveSignedPartResponse](t, liveExpectStatus(t,
		server.request(ctx, http.MethodPost, "/api/v1/uploads/"+uploadID+"/parts/sign", liveTokenA, map[string]any{
			"part_number": partNumber,
			"checksum":    drive.Checksum{Algorithm: "sha256", Value: digest},
		}, nil), http.StatusOK))
}

func liveDownloadBytes(t *testing.T, ctx context.Context, signed liveDownloadResponse) []byte {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, signed.Method, signed.URL, nil)
	if err != nil {
		t.Fatalf("create download request: %v", err)
	}
	response, err := (&http.Client{Timeout: 45 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("download committed object: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read committed object: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("download returned %s: %.200s", response.Status, body)
	}
	return body
}

func sha256Digest(body []byte) string {
	sum := sha256.Sum256(body)
	return base64.StdEncoding.EncodeToString(sum[:])
}
