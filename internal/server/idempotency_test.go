package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdempotencyKeyReplaysDirectoryAndUploadCreation(t *testing.T) {
	api := newTestAPI(t)
	directoryBody := map[string]any{
		"parent_id": api.tenantA.RootNodeID,
		"name":      "Idempotent Projects",
	}
	directoryHeaders := map[string]string{idempotencyKeyHeader: "directory-request-1"}

	firstDirectory := api.request(t, http.MethodPost, "/api/v1/directories", testTokenA, directoryBody, directoryHeaders)
	if firstDirectory.Code != http.StatusCreated || firstDirectory.Header().Get(idempotencyReplayedHeader) != "false" {
		t.Fatalf("first directory creation: got %d replayed=%q: %s", firstDirectory.Code, firstDirectory.Header().Get(idempotencyReplayedHeader), firstDirectory.Body.String())
	}
	createdDirectory := decodeData[nodeResponse](t, firstDirectory)

	replayedDirectory := api.request(t, http.MethodPost, "/api/v1/directories", testTokenA, directoryBody, directoryHeaders)
	if replayedDirectory.Code != http.StatusCreated || replayedDirectory.Header().Get(idempotencyReplayedHeader) != "true" {
		t.Fatalf("replayed directory creation: got %d replayed=%q: %s", replayedDirectory.Code, replayedDirectory.Header().Get(idempotencyReplayedHeader), replayedDirectory.Body.String())
	}
	if replayed := decodeData[nodeResponse](t, replayedDirectory); replayed.ID != createdDirectory.ID {
		t.Fatalf("directory replay returned id %q, want %q", replayed.ID, createdDirectory.ID)
	}

	changedDirectory := api.request(t, http.MethodPost, "/api/v1/directories", testTokenA, map[string]any{
		"parent_id": api.tenantA.RootNodeID,
		"name":      "Different Projects",
	}, directoryHeaders)
	if changedDirectory.Code != http.StatusConflict || !strings.Contains(changedDirectory.Body.String(), `"code":"idempotency_conflict"`) {
		t.Fatalf("changed directory request: got %d: %s", changedDirectory.Code, changedDirectory.Body.String())
	}

	uploadBody := map[string]any{
		"parent_id": createdDirectory.ID,
		"name":      "idempotent.bin",
		"size":      12,
		"mime_type": "application/octet-stream",
	}
	uploadHeaders := map[string]string{idempotencyKeyHeader: "upload-request-1"}
	firstUpload := api.request(t, http.MethodPost, "/api/v1/uploads", testTokenA, uploadBody, uploadHeaders)
	if firstUpload.Code != http.StatusCreated || firstUpload.Header().Get(idempotencyReplayedHeader) != "false" {
		t.Fatalf("first upload creation: got %d replayed=%q: %s", firstUpload.Code, firstUpload.Header().Get(idempotencyReplayedHeader), firstUpload.Body.String())
	}
	createdUpload := decodeData[uploadResponse](t, firstUpload)

	replayedUpload := api.request(t, http.MethodPost, "/api/v1/uploads", testTokenA, uploadBody, uploadHeaders)
	if replayedUpload.Code != http.StatusCreated || replayedUpload.Header().Get(idempotencyReplayedHeader) != "true" {
		t.Fatalf("replayed upload creation: got %d replayed=%q: %s", replayedUpload.Code, replayedUpload.Header().Get(idempotencyReplayedHeader), replayedUpload.Body.String())
	}
	if replayed := decodeData[uploadResponse](t, replayedUpload); replayed.ID != createdUpload.ID {
		t.Fatalf("upload replay returned id %q, want %q", replayed.ID, createdUpload.ID)
	}

	changedUpload := api.request(t, http.MethodPost, "/api/v1/uploads", testTokenA, map[string]any{
		"parent_id": createdDirectory.ID,
		"name":      "idempotent.bin",
		"size":      13,
		"mime_type": "application/octet-stream",
	}, uploadHeaders)
	if changedUpload.Code != http.StatusConflict || !strings.Contains(changedUpload.Body.String(), `"code":"idempotency_conflict"`) {
		t.Fatalf("changed upload request: got %d: %s", changedUpload.Code, changedUpload.Body.String())
	}
}

func TestIdempotencyKeyRejectsInvalidHeaders(t *testing.T) {
	api := newTestAPI(t)
	body := map[string]any{
		"parent_id": api.tenantA.RootNodeID,
		"name":      "Invalid Key",
	}
	for _, test := range []struct {
		name string
		key  string
	}{
		{name: "empty", key: ""},
		{name: "contains space", key: "not visible"},
		{name: "overlong", key: strings.Repeat("k", 256)},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := api.request(t, http.MethodPost, "/api/v1/directories", testTokenA, body, map[string]string{idempotencyKeyHeader: test.key})
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
				t.Fatalf("got %d: %s", response.Code, response.Body.String())
			}
		})
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/directories", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testTokenA)
	request.Header.Add(idempotencyKeyHeader, "duplicate-1")
	request.Header.Add(idempotencyKeyHeader, "duplicate-2")
	recorder := httptest.NewRecorder()
	api.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("duplicate header: got %d: %s", recorder.Code, recorder.Body.String())
	}
}
