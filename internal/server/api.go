package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/baicie/asteria-drive/internal/auth"
	"github.com/baicie/asteria-drive/internal/drive"
)

const maxJSONBody = 1 << 20

type api struct {
	service *drive.Service
	logger  *slog.Logger
}

type tenantResponse struct {
	ID              string    `json:"id"`
	DisplayName     string    `json:"display_name"`
	RootDirectoryID string    `json:"root_directory_id"`
	CreatedAt       time.Time `json:"created_at"`
}

type nodeResponse struct {
	ID        string         `json:"id"`
	ParentID  string         `json:"parent_id,omitempty"`
	Kind      drive.NodeKind `json:"kind"`
	Name      string         `json:"name"`
	Size      int64          `json:"size,omitempty"`
	MimeType  string         `json:"mime_type,omitempty"`
	Revision  int64          `json:"revision"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type uploadResponse struct {
	ID              string             `json:"id"`
	ParentID        string             `json:"parent_id"`
	Name            string             `json:"name"`
	ExpectedSize    int64              `json:"expected_size"`
	MimeType        string             `json:"mime_type"`
	Status          drive.UploadStatus `json:"status"`
	PartSize        int64              `json:"part_size"`
	ExpiresAt       time.Time          `json:"expires_at"`
	CommittedFileID *string            `json:"committed_file_id"`
}

func toNodeResponse(node drive.Node) nodeResponse {
	return nodeResponse{
		ID: node.ID, ParentID: node.ParentID, Kind: node.Kind, Name: node.DisplayName,
		Size: node.Size, MimeType: node.MimeType, Revision: node.Revision,
		CreatedAt: node.CreatedAt, UpdatedAt: node.UpdatedAt,
	}
}

func toUploadResponse(upload drive.UploadSession) uploadResponse {
	response := uploadResponse{
		ID: upload.ID, ParentID: upload.ParentID, Name: upload.DisplayName, ExpectedSize: upload.ExpectedSize,
		MimeType: upload.MimeType, Status: upload.Status, PartSize: upload.PartSize, ExpiresAt: upload.ExpiresAt,
	}
	if upload.CommittedNodeID != "" {
		response.CommittedFileID = &upload.CommittedNodeID
	}
	return response
}

func (a *api) health(w http.ResponseWriter, _ *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]string{"service": "asteria-server", "status": "ok"})
}

func (a *api) ready(w http.ResponseWriter, r *http.Request) {
	if err := a.service.Ready(r.Context()); err != nil {
		a.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"service": "asteria-server", "status": "not_ready"})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]string{"service": "asteria-server", "status": "ready"})
}

func (a *api) notFound(w http.ResponseWriter, r *http.Request) {
	a.writeError(w, r, drive.E(drive.CodeNotFound, "API route was not found"))
}

func (a *api) getTenant(w http.ResponseWriter, r *http.Request) {
	tenant, err := a.service.Tenant(r.Context(), identity(r).TenantID)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeData(w, http.StatusOK, tenantResponse{
		ID: tenant.ID, DisplayName: tenant.DisplayName,
		RootDirectoryID: tenant.RootNodeID, CreatedAt: tenant.CreatedAt,
	})
}

func (a *api) createDirectory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(w, r, &input, maxJSONBody, false); err != nil {
		a.writeError(w, r, err)
		return
	}
	node, err := a.service.CreateDirectory(r.Context(), identity(r), input.ParentID, input.Name)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/directories/"+node.ID)
	setETag(w, node.Revision)
	a.writeData(w, http.StatusCreated, toNodeResponse(node))
}

func (a *api) getDirectory(w http.ResponseWriter, r *http.Request) {
	node, err := a.service.Node(r.Context(), identity(r), r.PathValue("id"), drive.NodeDirectory)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	setETag(w, node.Revision)
	a.writeData(w, http.StatusOK, toNodeResponse(node))
}

func (a *api) getFile(w http.ResponseWriter, r *http.Request) {
	node, err := a.service.Node(r.Context(), identity(r), r.PathValue("id"), drive.NodeFile)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	setETag(w, node.Revision)
	a.writeData(w, http.StatusOK, toNodeResponse(node))
}

func (a *api) listChildren(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	page, err := a.service.ListChildren(r.Context(), identity(r), r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	items := make([]nodeResponse, len(page.Items))
	for i := range page.Items {
		items[i] = toNodeResponse(page.Items[i])
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"data": items, "page": map[string]any{"next_cursor": nullableString(page.NextCursor)}})
}

func (a *api) updateNode(w http.ResponseWriter, r *http.Request) {
	revision, err := parseETag(r.Header.Get("If-Match"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	var input struct {
		Name     *string `json:"name"`
		ParentID *string `json:"parent_id"`
	}
	if err := decodeJSON(w, r, &input, maxJSONBody, false); err != nil {
		a.writeError(w, r, err)
		return
	}
	node, err := a.service.UpdateNode(r.Context(), identity(r), r.PathValue("id"), input.Name, input.ParentID, revision)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	setETag(w, node.Revision)
	a.writeData(w, http.StatusOK, toNodeResponse(node))
}

func (a *api) createUpload(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ParentID string         `json:"parent_id"`
		Name     string         `json:"name"`
		Size     int64          `json:"size"`
		MimeType string         `json:"mime_type"`
		Checksum drive.Checksum `json:"checksum"`
	}
	if err := decodeJSON(w, r, &input, maxJSONBody, false); err != nil {
		a.writeError(w, r, err)
		return
	}
	upload, err := a.service.CreateUpload(r.Context(), identity(r), drive.CreateUploadInput{
		ParentID: input.ParentID, Name: input.Name, Size: input.Size, MimeType: input.MimeType, Checksum: input.Checksum,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/uploads/"+upload.ID)
	a.writeData(w, http.StatusCreated, toUploadResponse(upload))
}

func (a *api) getUpload(w http.ResponseWriter, r *http.Request) {
	upload, err := a.service.Upload(r.Context(), identity(r), r.PathValue("id"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeData(w, http.StatusOK, toUploadResponse(upload))
}

func (a *api) signUploadPart(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PartNumber int            `json:"part_number"`
		Checksum   drive.Checksum `json:"checksum"`
	}
	if err := decodeJSON(w, r, &input, maxJSONBody, false); err != nil {
		a.writeError(w, r, err)
		return
	}
	signed, err := a.service.SignUploadPart(r.Context(), identity(r), r.PathValue("id"), input.PartNumber, input.Checksum)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeData(w, http.StatusOK, map[string]any{
		"part_number": signed.PartNumber, "method": signed.Method, "url": signed.URL,
		"required_headers": signed.RequiredHeaders, "expires_at": signed.ExpiresAt,
	})
}

func (a *api) completeUpload(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Parts []drive.CompletedPart `json:"parts"`
	}
	if err := decodeJSON(w, r, &input, maxJSONBody, false); err != nil {
		a.writeError(w, r, err)
		return
	}
	result, err := a.service.CompleteUpload(r.Context(), identity(r), r.PathValue("id"), input.Parts)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if result.First {
		status = http.StatusCreated
	}
	a.writeData(w, status, map[string]any{"upload": toUploadResponse(result.Upload), "file": toNodeResponse(result.Node)})
}

func (a *api) abortUpload(w http.ResponseWriter, r *http.Request) {
	if err := a.service.AbortUpload(r.Context(), identity(r), r.PathValue("id")); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) createDownloadAuthorization(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil && r.ContentLength != 0 {
		var empty struct{}
		if err := decodeJSON(w, r, &empty, maxJSONBody, true); err != nil {
			a.writeError(w, r, err)
			return
		}
	}
	signed, err := a.service.Download(r.Context(), identity(r), r.PathValue("id"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeData(w, http.StatusCreated, map[string]any{"method": signed.Method, "url": signed.URL, "expires_at": signed.ExpiresAt})
}

func (a *api) recycleNode(w http.ResponseWriter, r *http.Request) {
	revision, err := parseETag(r.Header.Get("If-Match"))
	if err == nil {
		err = a.service.Recycle(r.Context(), identity(r), r.PathValue("id"), revision)
	}
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) listRecycle(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	page, err := a.service.ListRecycle(r.Context(), identity(r), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	items := make([]map[string]any, len(page.Items))
	for i, entry := range page.Items {
		items[i] = map[string]any{"node": toNodeResponse(entry.Node), "original_parent_id": entry.OriginalParentID, "deleted_at": entry.DeletedAt}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"data": items, "page": map[string]any{"next_cursor": nullableString(page.NextCursor)}})
}

func (a *api) restoreNode(w http.ResponseWriter, r *http.Request) {
	revision, err := parseETag(r.Header.Get("If-Match"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if r.Body != nil && r.ContentLength != 0 {
		var empty struct{}
		if err := decodeJSON(w, r, &empty, maxJSONBody, true); err != nil {
			a.writeError(w, r, err)
			return
		}
	}
	node, err := a.service.Restore(r.Context(), identity(r), r.PathValue("id"), revision)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	setETag(w, node.Revision)
	a.writeData(w, http.StatusOK, toNodeResponse(node))
}

func (a *api) purgeNode(w http.ResponseWriter, r *http.Request) {
	revision, err := parseETag(r.Header.Get("If-Match"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.service.Purge(r.Context(), identity(r), r.PathValue("id"), revision); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func identity(r *http.Request) drive.Identity {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		return drive.Identity{}
	}
	return principal.Identity
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any, max int64, allowEmpty bool) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return drive.E(drive.CodeUnsupportedMediaType, "content type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, max)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return drive.E(drive.CodeRequestTooLarge, "request body exceeds the allowed size")
		}
		if allowEmpty && errors.Is(err, io.EOF) {
			return nil
		}
		return drive.E(drive.CodeInvalidRequest, "request body must be a valid JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return drive.E(drive.CodeInvalidRequest, "request body must contain one JSON object")
	}
	return nil
}

func parseLimit(value string) (int, error) {
	if value == "" {
		return 50, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 200 {
		return 0, drive.E(drive.CodeInvalidRequest, "limit must be between 1 and 200")
	}
	return limit, nil
}

func parseETag(value string) (int64, error) {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, drive.E(drive.CodeInvalidRequest, "If-Match must contain a quoted revision")
	}
	revision, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	if err != nil || revision <= 0 {
		return 0, drive.E(drive.CodeInvalidRequest, "If-Match must contain a quoted revision")
	}
	return revision, nil
}

func setETag(w http.ResponseWriter, revision int64) {
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", revision))
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (a *api) writeData(w http.ResponseWriter, status int, data any) {
	a.writeJSON(w, status, map[string]any{"data": data})
}

func (a *api) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (a *api) writeError(w http.ResponseWriter, r *http.Request, err error) {
	code := drive.CodeOf(err)
	if fields, ok := r.Context().Value(requestLogKey{}).(*requestLogFields); ok {
		fields.errorCode = string(code)
	}
	status := statusFor(code)
	message := externalMessage(code)
	var domainErr *drive.Error
	if errors.As(err, &domainErr) && (code == drive.CodeInvalidRequest || code == drive.CodeInvalidCursor || code == drive.CodeRevisionMismatch) {
		message = domainErr.Message
	}
	if status >= 500 {
		retryable := false
		if errors.As(err, &domainErr) {
			retryable = domainErr.Retryable
		}
		a.logger.Error("request failed", "request_id", requestID(r.Context()), "method", r.Method, "route", r.Pattern, "code", code, "retryable", retryable)
	}
	a.writeJSON(w, status, map[string]any{"error": map[string]string{
		"code": string(code), "message": message, "request_id": requestID(r.Context()),
	}})
}

func statusFor(code drive.ErrorCode) int {
	switch code {
	case drive.CodeInvalidRequest, drive.CodeInvalidCursor:
		return http.StatusBadRequest
	case drive.CodeUnauthenticated:
		return http.StatusUnauthorized
	case drive.CodeNotFound:
		return http.StatusNotFound
	case drive.CodeNameConflict, drive.CodeInvalidState, drive.CodeIdempotencyConflict, drive.CodeRestoreConflict:
		return http.StatusConflict
	case drive.CodeRevisionMismatch:
		return http.StatusPreconditionFailed
	case drive.CodeRequestTooLarge:
		return http.StatusRequestEntityTooLarge
	case drive.CodeUnsupportedMediaType:
		return http.StatusUnsupportedMediaType
	case drive.CodeDependencyUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func externalMessage(code drive.ErrorCode) string {
	switch code {
	case drive.CodeUnauthenticated:
		return "a valid bearer token is required"
	case drive.CodeNotFound:
		return "resource was not found"
	case drive.CodeNameConflict:
		return "an active item with this name already exists"
	case drive.CodeInvalidState:
		return "resource is not in a valid state for this operation"
	case drive.CodeIdempotencyConflict:
		return "request differs from the first completed operation"
	case drive.CodeRestoreConflict:
		return "the original location is unavailable"
	case drive.CodeRequestTooLarge:
		return "request exceeds the allowed size"
	case drive.CodeUnsupportedMediaType:
		return "content type must be application/json"
	case drive.CodeDependencyUnavailable:
		return "a required dependency is unavailable"
	case drive.CodeInvalidCursor:
		return "cursor is invalid"
	case drive.CodeRevisionMismatch:
		return "resource revision does not match"
	case drive.CodeInvalidRequest:
		return "request is invalid"
	default:
		return "internal server error"
	}
}
