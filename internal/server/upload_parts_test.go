package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/auth"

	"github.com/baicie/asteria-drive/internal/drive"
)

type listedPart struct {
	PartNumber    int    `json:"part_number"`
	ETag          string `json:"etag"`
	Size          int64  `json:"size"`
	ContentSHA256 string `json:"content_sha256"`
}

type uploadPartsPayload struct {
	UploadID string       `json:"upload_id"`
	Status   string       `json:"status"`
	PartSize int64        `json:"part_size"`
	Parts    []listedPart `json:"parts"`
	Complete bool         `json:"complete"`
}

func TestUploadPartsListingReflectsAcceptedStorageParts(t *testing.T) {
	api := newTestAPI(t)
	directory := createDirectory(t, api, "resume")
	upload := createUpload(t, api, directory.ID, "resume.bin", 12)

	// Storage knows the parts even though completion was never requested; that is the
	// reload-resume window ADR-0027 is about.
	session := readUploadSession(t, api, upload.ID)
	putUploadPart(t, api, session.StorageUploadID, 1, []byte("hello"))
	putUploadPart(t, api, session.StorageUploadID, 3, []byte("world"))

	recorder := api.request(t, http.MethodGet, "/api/v1/uploads/"+upload.ID+"/parts", testTokenA, nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list parts status=%d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"object_key", "storage_upload_id", session.ObjectKey, session.StorageUploadID} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("part listing leaked %q: %s", forbidden, body)
		}
	}
	payload := decodeData[uploadPartsPayload](t, recorder)
	if payload.UploadID != upload.ID || payload.Status != string(drive.UploadCreated) || !payload.Complete {
		t.Fatalf("unexpected listing header: %+v", payload)
	}
	if len(payload.Parts) != 2 || payload.Parts[0].PartNumber != 1 || payload.Parts[1].PartNumber != 3 {
		t.Fatalf("unexpected parts: %+v", payload.Parts)
	}
	if payload.Parts[0].ETag == "" || payload.Parts[0].Size != 5 {
		t.Fatalf("part 1 metadata = %+v", payload.Parts[0])
	}

	// Another tenant must not be able to enumerate parts of a foreign session.
	foreign := api.request(t, http.MethodGet, "/api/v1/uploads/"+upload.ID+"/parts", testTokenB, nil, nil)
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant listing status=%d, want 404: %s", foreign.Code, foreign.Body.String())
	}
}

func TestUploadPartsListingUsesFrozenCompletionParts(t *testing.T) {
	api := newTestAPI(t)
	directory := createDirectory(t, api, "frozen")
	parts, upload := completeSinglePartUpload(t, api, directory.ID, "frozen.bin", []byte("frozen payload"))

	payload := decodeData[uploadPartsPayload](t, api.request(t, http.MethodGet, "/api/v1/uploads/"+upload.ID+"/parts", testTokenA, nil, nil))
	if payload.Status != string(drive.UploadCommitted) {
		t.Fatalf("committed session listing status = %q", payload.Status)
	}
	if len(payload.Parts) != 1 || payload.Parts[0].ETag != parts[0].ETag || payload.Parts[0].PartNumber != 1 {
		t.Fatalf("frozen completion parts = %+v, want %+v", payload.Parts, parts)
	}
}

func TestUploadPartsListingRejectsInvalidLimit(t *testing.T) {
	api := newTestAPI(t)
	directory := createDirectory(t, api, "limit")
	upload := createUpload(t, api, directory.ID, "limit.bin", 1)

	for _, limit := range []string{"0", "-1", "10001", "many"} {
		recorder := api.request(t, http.MethodGet, "/api/v1/uploads/"+upload.ID+"/parts?limit="+limit, testTokenA, nil, nil)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("limit=%s status=%d, want 400: %s", limit, recorder.Code, recorder.Body.String())
		}
	}
}

func TestUploadPartsListingTruncationIsNotAuthoritative(t *testing.T) {
	api := newTestAPI(t)
	directory := createDirectory(t, api, "truncate")
	upload := createUpload(t, api, directory.ID, "truncate.bin", 30)
	session := readUploadSession(t, api, upload.ID)
	for partNumber := 1; partNumber <= 3; partNumber++ {
		putUploadPart(t, api, session.StorageUploadID, partNumber, []byte{byte(partNumber)})
	}
	payload := decodeData[uploadPartsPayload](t, api.request(t, http.MethodGet, "/api/v1/uploads/"+upload.ID+"/parts?limit=2", testTokenA, nil, nil))
	if payload.Complete {
		t.Fatalf("truncated listing must not claim completeness: %+v", payload)
	}
	if len(payload.Parts) != 2 {
		t.Fatalf("truncated listing returned %d parts, want 2", len(payload.Parts))
	}
}

func TestUploadPartsListingUsesStandardEnvelope(t *testing.T) {
	api := newTestAPI(t)
	directory := createDirectory(t, api, "envelope")
	upload := createUpload(t, api, directory.ID, "envelope.bin", 1)
	recorder := api.request(t, http.MethodGet, "/api/v1/uploads/"+upload.ID+"/parts", testTokenA, nil, nil)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if _, ok := raw["data"]; !ok {
		t.Fatalf("response is not the standard data envelope: %s", recorder.Body.String())
	}
	// An empty listing must serialize as [] so clients can iterate without a null check.
	if !strings.Contains(recorder.Body.String(), `"parts":[]`) {
		t.Fatalf("an empty listing must serialize parts as []: %s", recorder.Body.String())
	}
}

func createDirectory(t *testing.T, api testAPI, name string) nodeResponse {
	t.Helper()
	tenant := decodeData[tenantResponse](t, api.request(t, http.MethodGet, "/api/v1/tenant", testTokenA, nil, nil))
	return decodeData[nodeResponse](t, api.request(t, http.MethodPost, "/api/v1/directories", testTokenA, map[string]any{
		"parent_id": tenant.RootDirectoryID, "name": name,
	}, nil))
}

func createUpload(t *testing.T, api testAPI, parentID, name string, size int) uploadResponse {
	t.Helper()
	return decodeData[uploadResponse](t, api.request(t, http.MethodPost, "/api/v1/uploads", testTokenA, map[string]any{
		"parent_id": parentID, "name": name, "size": size, "mime_type": "application/octet-stream",
	}, nil))
}

func readUploadSession(t *testing.T, api testAPI, uploadID string) drive.UploadSession {
	t.Helper()
	session, err := api.service.Upload(context.Background(), drive.Identity{TenantID: testTenantA, PrincipalID: testPrincipalA}, uploadID)
	if err != nil {
		t.Fatalf("read upload session: %v", err)
	}
	return session
}

func putUploadPart(t *testing.T, api testAPI, storageUploadID string, partNumber int, payload []byte) drive.CompletedPart {
	t.Helper()
	stored, err := api.storage.PutPart(storageUploadID, partNumber, payload)
	if err != nil {
		t.Fatalf("store part %d: %v", partNumber, err)
	}
	return stored
}

func completeSinglePartUpload(t *testing.T, api testAPI, parentID, name string, payload []byte) ([]drive.CompletedPart, uploadResponse) {
	t.Helper()
	upload := createUpload(t, api, parentID, name, len(payload))
	session := readUploadSession(t, api, upload.ID)
	stored := putUploadPart(t, api, session.StorageUploadID, 1, payload)
	completed := []drive.CompletedPart{{PartNumber: 1, ETag: stored.ETag, Size: stored.Size}}
	recorder := api.request(t, http.MethodPost, "/api/v1/uploads/"+upload.ID+"/complete", testTokenA, map[string]any{
		"parts": completed,
	}, nil)
	if recorder.Code != http.StatusCreated && recorder.Code != http.StatusOK {
		t.Fatalf("complete upload status=%d: %s", recorder.Code, recorder.Body.String())
	}
	return completed, upload
}

// The listing is a GET: it must never transition the session or abort the multipart upload
// merely because the clock has passed the TTL. Maintenance owns that transition.
func TestUploadPartsListingIsReadOnlyForAnElapsedSession(t *testing.T) {
	api := newTestAPI(t)
	directory := createDirectory(t, api, "readonly-elapsed")
	upload := createUpload(t, api, directory.ID, "elapsed.bin", 4)
	session := readUploadSession(t, api, upload.ID)
	putUploadPart(t, api, session.StorageUploadID, 1, []byte("data"))

	// The listing carries the effective status. The control plane reads it through the
	// same clock the service uses, so the fixture moves that clock forward instead of
	// writing the row: writing it would be exactly the side effect under test.
	api.clock.now = api.clock.now.Add(2 * time.Hour)
	t.Cleanup(func() { api.clock.now = api.clock.now.Add(-2 * time.Hour) })

	payload := decodeData[uploadPartsPayload](t, api.request(t, http.MethodGet, "/api/v1/uploads/"+upload.ID+"/parts", testTokenA, nil, nil))
	if payload.Status != string(drive.UploadExpired) || len(payload.Parts) != 0 {
		t.Fatalf("elapsed listing = %+v, want expired with no parts", payload)
	}
	after := readUploadSession(t, api, upload.ID)
	if after.Status != drive.UploadCreated {
		t.Fatalf("GET persisted a status change: %s", after.Status)
	}
	if after.UpdatedAt.After(session.UpdatedAt) {
		t.Fatalf("GET touched the session row: updated_at %s -> %s", session.UpdatedAt, after.UpdatedAt)
	}
	if _, err := api.storage.ListParts(context.Background(), session.ObjectKey, session.StorageUploadID); err != nil {
		t.Fatalf("GET aborted the multipart upload: %v", err)
	}
}

// Signed URLs are short-lived bearer credentials; no cache may outlive the signature.
func TestSignedURLResponsesAreNotCacheable(t *testing.T) {
	api := newTestAPI(t)
	directory := createDirectory(t, api, "no-store")

	// Signing happens on a live session; a committed one cannot be signed any more.
	live := createUpload(t, api, directory.ID, "live.bin", 5)
	sign := api.request(t, http.MethodPost, "/api/v1/uploads/"+live.ID+"/parts/sign", testTokenA, map[string]any{"part_number": 1}, nil)
	if sign.Code != http.StatusOK {
		t.Fatalf("sign part status=%d: %s", sign.Code, sign.Body.String())
	}
	if got := sign.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("signing Cache-Control = %q, want no-store", got)
	}

	_, upload := completeSinglePartUpload(t, api, directory.ID, "cached.bin", []byte("payload"))
	committed := readUploadSession(t, api, upload.ID)
	if committed.CommittedNodeID == "" {
		t.Fatal("completed upload has no committed file id")
	}
	download := api.request(t, http.MethodPost, "/api/v1/files/"+committed.CommittedNodeID+"/download-authorizations", testTokenA, nil, nil)
	if download.Code != http.StatusCreated {
		t.Fatalf("download authorization status=%d: %s", download.Code, download.Body.String())
	}
	if got := download.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("download authorization Cache-Control = %q, want no-store", got)
	}
}

// A session is tenant-scoped, not principal-scoped: any member of the same tenant may read
// it, and a viewer may read the listing but never sign. This is the documented contract,
// so it is pinned here rather than left to prose.
func TestUploadPartsListingIsTenantSharedAndReadOnlyForViewers(t *testing.T) {
	base := newTestAPI(t)
	ctx := context.Background()
	issuer := "https://issuer.resume.test"
	now := time.Now().UTC()
	members := []struct {
		token       string
		principalID string
		role        drive.AccessRole
	}{
		{token: "resume-viewer-token", principalID: testPrincipalC, role: drive.RoleViewer},
		{token: "resume-editor-token", principalID: testPrincipalD, role: drive.RoleEditor},
	}
	verifier := make(testOIDCVerifier, len(members))
	for _, member := range members {
		if _, err := base.repository.EnsureOIDCMember(ctx, drive.OIDCMemberSeed{
			PrincipalID: member.principalID, TenantID: testTenantA, Issuer: issuer, Subject: member.token,
			DisplayName: member.token, Role: member.role, Now: now,
		}); err != nil {
			t.Fatal(err)
		}
		verifier[member.token] = auth.OIDCClaims{
			Issuer: issuer, Subject: member.token, Audience: []string{"asteria-api"}, ExpiresAt: now.Add(time.Hour),
		}
	}
	resolver := func(ctx context.Context, issuer, subject, tenantID string) (auth.Principal, error) {
		record, err := base.repository.ResolveOIDCPrincipal(ctx, issuer, subject, tenantID)
		if err != nil {
			return auth.Principal{}, err
		}
		return auth.Principal{Identity: record.Identity, TenantDisplayName: record.TenantDisplayName, Role: record.Role}, nil
	}
	authenticator, err := auth.NewOIDCWithVerifier(issuer, "asteria-api", verifier, resolver)
	if err != nil {
		t.Fatal(err)
	}
	httpServer, err := New(Options{Address: ":0", Service: base.service, Authenticator: authenticator, Logger: slog.Default(), ReadHeaderTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	api := base
	api.handler = httpServer.Handler()
	headers := map[string]string{auth.TenantHeader: testTenantA}

	// The upload is created by the editor member; the viewer then reads it.
	created := api.request(t, http.MethodPost, "/api/v1/directories", "resume-editor-token", map[string]any{
		"parent_id": base.tenantA.RootNodeID, "name": "shared",
	}, headers)
	if created.Code != http.StatusCreated {
		t.Fatalf("editor directory status=%d: %s", created.Code, created.Body.String())
	}
	directory := decodeData[nodeResponse](t, created)
	createdUpload := api.request(t, http.MethodPost, "/api/v1/uploads", "resume-editor-token", map[string]any{
		"parent_id": directory.ID, "name": "shared.bin", "size": 5, "mime_type": "application/octet-stream",
	}, headers)
	if createdUpload.Code != http.StatusCreated {
		t.Fatalf("editor upload status=%d: %s", createdUpload.Code, createdUpload.Body.String())
	}
	upload := decodeData[uploadResponse](t, createdUpload)
	identity := drive.Identity{TenantID: testTenantA, PrincipalID: testPrincipalD}
	session, err := base.service.Upload(ctx, identity, upload.ID)
	if err != nil {
		t.Fatalf("read upload session: %v", err)
	}
	putUploadPart(t, api, session.StorageUploadID, 1, []byte("hello"))

	// A viewer can read the part listing of a session created by another principal.
	viewerListing := api.request(t, http.MethodGet, "/api/v1/uploads/"+upload.ID+"/parts", "resume-viewer-token", nil, headers)
	if viewerListing.Code != http.StatusOK {
		t.Fatalf("viewer listing status=%d: %s", viewerListing.Code, viewerListing.Body.String())
	}
	// But signing is a write and stays forbidden.
	viewerSign := api.request(t, http.MethodPost, "/api/v1/uploads/"+upload.ID+"/parts/sign", "resume-viewer-token", map[string]any{"part_number": 1}, headers)
	if viewerSign.Code != http.StatusForbidden {
		t.Fatalf("viewer signing status=%d, want 403: %s", viewerSign.Code, viewerSign.Body.String())
	}
	// An editor of the same tenant may read the very same session.
	editorListing := api.request(t, http.MethodGet, "/api/v1/uploads/"+upload.ID+"/parts", "resume-editor-token", nil, headers)
	if editorListing.Code != http.StatusOK {
		t.Fatalf("editor listing status=%d: %s", editorListing.Code, editorListing.Body.String())
	}
}
