package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/baicie/asteria-drive/internal/drive"
)

type listedPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
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
