package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/auth"
	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/baicie/asteria-drive/internal/memory"
)

const (
	testTokenA     = "tenant-a-token-000000000000000000000000"
	testTokenB     = "tenant-b-token-000000000000000000000000"
	testTenantA    = "11111111-1111-4111-8111-111111111111"
	testTenantB    = "22222222-2222-4222-8222-222222222222"
	testPrincipalA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testPrincipalB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	testPrincipalC = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	testPrincipalD = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
)

type testOIDCVerifier map[string]auth.OIDCClaims

func (v testOIDCVerifier) Verify(_ context.Context, token string) (auth.OIDCClaims, error) {
	claims, ok := v[token]
	if !ok {
		return auth.OIDCClaims{}, drive.E(drive.CodeUnauthenticated, "unknown test token")
	}
	return claims, nil
}

type testAPI struct {
	handler    http.Handler
	server     *Server
	service    *drive.Service
	storage    *memory.Storage
	repository *memory.Repository
	tenantA    drive.Tenant
	tenantB    drive.Tenant
}

func newTestAPI(t *testing.T) testAPI {
	return newTestAPIWithLogger(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func newTestAPIWithLogger(t *testing.T, logger *slog.Logger) testAPI {
	t.Helper()
	repository := memory.NewRepository()
	storage := memory.NewStorage("test-bucket")
	cursor, err := drive.NewCursorCodec([]byte("test-cursor-key-that-is-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := drive.NewService(drive.ServiceOptions{
		Repository: repository, Storage: storage, Cursor: cursor,
		MaxFileSize: 1 << 30, PartSize: 8 << 20, UploadTTL: time.Hour,
		UploadSignTTL: time.Minute, DownloadSignTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	tenantA, err := service.EnsureTenant(context.Background(), testTenantA, "Tenant A")
	if err != nil {
		t.Fatal(err)
	}
	tenantB, err := service.EnsureTenant(context.Background(), testTenantB, "Tenant B")
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewTrusted(map[string]auth.Principal{
		testTokenA: {Identity: drive.Identity{TenantID: testTenantA, PrincipalID: testPrincipalA}, TenantDisplayName: "Tenant A"},
		testTokenB: {Identity: drive.Identity{TenantID: testTenantB, PrincipalID: testPrincipalB}, TenantDisplayName: "Tenant B"},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer, err := New(Options{
		Address: ":0", Service: service, Authenticator: authenticator,
		Logger: logger, ReadHeaderTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return testAPI{
		handler: httpServer.Handler(), server: httpServer, service: service,
		storage: storage, repository: repository, tenantA: tenantA, tenantB: tenantB,
	}
}

func TestHealthStaysUpWhenDependenciesFail(t *testing.T) {
	api := newTestAPI(t)
	api.repository.SetReadyError(errors.New("postgres unavailable"))
	health := api.request(t, http.MethodGet, "/healthz", "", nil, nil)
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthz should stay up: status=%d body=%s", health.Code, health.Body.String())
	}
	ready := api.request(t, http.MethodGet, "/readyz", "", nil, nil)
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), `"status":"not_ready"`) {
		t.Fatalf("readyz should fail when metadata is down: status=%d body=%s", ready.Code, ready.Body.String())
	}
	api.repository.SetReadyError(nil)
	api.storage.SetReadyError(errors.New("object storage unavailable"))
	ready = api.request(t, http.MethodGet, "/readyz", "", nil, nil)
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz should fail when object storage is down: status=%d body=%s", ready.Code, ready.Body.String())
	}
}

func TestGracefulShutdownStopsListener(t *testing.T) {
	api := newTestAPI(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api.server.httpServer.Addr = listener.Addr().String()
	done := make(chan error, 1)
	go func() {
		done <- api.server.httpServer.Serve(listener)
	}()
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("health before shutdown: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health before shutdown status=%d", response.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := api.server.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve after shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop after shutdown")
	}
	if _, err := client.Get("http://" + listener.Addr().String() + "/healthz"); err == nil {
		t.Fatal("listener still accepted requests after shutdown")
	}
}

func TestGracefulShutdownDrainsInFlightRequest(t *testing.T) {
	api := newTestAPI(t)
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseHandler)
		}
	}()
	api.server.httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(handlerStarted)
		<-releaseHandler
		w.WriteHeader(http.StatusNoContent)
	})

	connectionActive := make(chan struct{}, 1)
	api.server.httpServer.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateActive {
			select {
			case connectionActive <- struct{}{}:
			default:
			}
		}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- api.server.httpServer.Serve(listener)
	}()

	type responseResult struct {
		response *http.Response
		err      error
	}
	responseDone := make(chan responseResult, 1)
	client := &http.Client{Timeout: 5 * time.Second}
	go func() {
		response, err := client.Get("http://" + listener.Addr().String() + "/in-flight")
		responseDone <- responseResult{response: response, err: err}
	}()

	select {
	case <-connectionActive:
	case <-time.After(2 * time.Second):
		t.Fatal("request connection did not become active")
	}
	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("request handler did not start")
	}

	shutdownDone := make(chan error, 1)
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	go func() {
		shutdownDone <- api.server.Shutdown(shutdownContext)
	}()

	select {
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve after shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not stop the listener")
	}
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before the in-flight request completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseHandler)
	released = true
	select {
	case result := <-responseDone:
		if result.err != nil {
			t.Fatalf("in-flight request: %v", result.err)
		}
		defer result.response.Body.Close()
		if result.response.StatusCode != http.StatusNoContent {
			t.Fatalf("in-flight request status=%d, want %d", result.response.StatusCode, http.StatusNoContent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request did not complete after the handler was released")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not complete after the in-flight request finished")
	}
}

func TestControlPlaneLatencySmokeBaseline(t *testing.T) {
	api := newTestAPI(t)
	tenant := decodeData[tenantResponse](t, api.request(t, http.MethodGet, "/api/v1/tenant", testTokenA, nil, nil))
	directory := decodeData[nodeResponse](t, api.request(t, http.MethodPost, "/api/v1/directories", testTokenA, map[string]any{
		"parent_id": tenant.RootDirectoryID, "name": "latency-smoke",
	}, nil))
	upload := decodeData[uploadResponse](t, api.request(t, http.MethodPost, "/api/v1/uploads", testTokenA, map[string]any{
		"parent_id": directory.ID, "name": "latency.bin", "size": 1, "mime_type": "application/octet-stream",
	}, nil))

	measure := func(name string, fn func()) time.Duration {
		t.Helper()
		const samples = 50
		latencies := make([]time.Duration, 0, samples)
		for range samples {
			started := time.Now()
			fn()
			latencies = append(latencies, time.Since(started))
		}
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p95 := latencies[(samples*95)/100]
		t.Logf("%s p50=%s p95=%s max=%s", name, latencies[samples/2], p95, latencies[samples-1])
		if p95 > 200*time.Millisecond {
			t.Fatalf("%s p95=%s exceeds local smoke ceiling 200ms", name, p95)
		}
		return p95
	}
	measure("healthz", func() {
		if api.request(t, http.MethodGet, "/healthz", "", nil, nil).Code != http.StatusOK {
			t.Fatal("healthz failed")
		}
	})
	measure("get_tenant", func() {
		if api.request(t, http.MethodGet, "/api/v1/tenant", testTokenA, nil, nil).Code != http.StatusOK {
			t.Fatal("tenant failed")
		}
	})
	measure("list_children", func() {
		if api.request(t, http.MethodGet, "/api/v1/directories/"+directory.ID+"/children", testTokenA, nil, nil).Code != http.StatusOK {
			t.Fatal("list failed")
		}
	})
	measure("sign_part", func() {
		if api.request(t, http.MethodPost, "/api/v1/uploads/"+upload.ID+"/parts/sign", testTokenA, map[string]any{
			"part_number": 1,
		}, nil).Code != http.StatusOK {
			t.Fatal("sign failed")
		}
	})
}

func TestStructuredRequestLogIsCorrelatedAndRedacted(t *testing.T) {
	var output bytes.Buffer
	api := newTestAPIWithLogger(t, slog.New(slog.NewJSONHandler(&output, nil)))
	recorder := api.request(t, http.MethodGet, "/api/v1/tenant", testTokenA, nil, map[string]string{"X-Request-ID": "request-log-test"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("get tenant: got %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(output.String(), testTokenA) || strings.Contains(strings.ToLower(output.String()), "authorization") {
		t.Fatalf("request log leaked authentication material: %s", output.String())
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode request log: %v; log=%s", err, output.String())
	}
	for key, expected := range map[string]any{
		"msg": "http request completed", "request_id": "request-log-test",
		"method": http.MethodGet, "route": "GET /api/v1/tenant",
		"tenant_id": testTenantA, "principal_id": testPrincipalA,
	} {
		if entry[key] != expected {
			t.Errorf("log field %s=%v, want %v; log=%s", key, entry[key], expected, output.String())
		}
	}
	if entry["status"] != float64(http.StatusOK) {
		t.Errorf("log status=%v, want 200", entry["status"])
	}
}

func TestDependencyErrorLogAndResponseDoNotLeakCause(t *testing.T) {
	const secretCause = "https://storage.invalid/object?X-Amz-Signature=do-not-log"
	var output bytes.Buffer
	a := &api{logger: slog.New(slog.NewJSONHandler(&output, nil))}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/uploads/id/complete", nil)
	request.Pattern = "POST /api/v1/uploads/{id}/complete"
	request = request.WithContext(context.WithValue(request.Context(), requestIDKey{}, "request-error-log"))
	recorder := httptest.NewRecorder()
	a.writeError(recorder, request, drive.Retryable(drive.CodeDependencyUnavailable, "storage operation failed", errors.New(secretCause)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("dependency error status=%d, want 503", recorder.Code)
	}
	if strings.Contains(output.String(), secretCause) || strings.Contains(recorder.Body.String(), secretCause) {
		t.Fatalf("dependency error leaked its cause: log=%s response=%s", output.String(), recorder.Body.String())
	}
	if !strings.Contains(output.String(), `"code":"dependency_unavailable"`) || !strings.Contains(output.String(), `"retryable":true`) {
		t.Fatalf("dependency error log lost stable classification: %s", output.String())
	}
}

func TestUnknownAPIRouteUsesErrorEnvelope(t *testing.T) {
	api := newTestAPI(t)
	recorder := api.request(t, http.MethodGet, "/api/v1/does-not-exist", testTokenA, nil, nil)
	if recorder.Code != http.StatusNotFound || recorder.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("unknown API route: got %d %q: %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"not_found"`) || !strings.Contains(recorder.Body.String(), `"request_id":`) {
		t.Fatalf("unknown route did not use the error envelope: %s", recorder.Body.String())
	}
}

func (api testAPI) request(t *testing.T, method, path, token string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	api.handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeData[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	return envelope.Data
}

func TestHealthAndReadiness(t *testing.T) {
	api := newTestAPI(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		recorder := api.request(t, http.MethodGet, path, "", nil, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", path, recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Fatalf("%s: unexpected content type %q", path, got)
		}
		if recorder.Header().Get("X-Request-ID") == "" {
			t.Fatalf("%s: request id is missing", path)
		}
	}
}

func TestProtectedRouteRequiresBearerToken(t *testing.T) {
	api := newTestAPI(t)
	recorder := api.request(t, http.MethodGet, "/api/v1/directories/"+api.tenantA.RootNodeID, "", nil, nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"unauthenticated"`) {
		t.Fatalf("unexpected error: %s", recorder.Body.String())
	}
}

func TestTenantEndpointDiscoversRootDirectory(t *testing.T) {
	api := newTestAPI(t)
	recorder := api.request(t, http.MethodGet, "/api/v1/tenant", testTokenA, nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("get tenant: got %d: %s", recorder.Code, recorder.Body.String())
	}
	tenant := decodeData[tenantResponse](t, recorder)
	if tenant.ID != testTenantA || tenant.DisplayName != "Tenant A" || tenant.RootDirectoryID != api.tenantA.RootNodeID {
		t.Fatalf("unexpected tenant bootstrap response: %+v", tenant)
	}
}

func TestMalformedPathIdentifierReturnsBadRequest(t *testing.T) {
	api := newTestAPI(t)
	response := api.request(t, http.MethodGet, "/api/v1/directories/not-a-uuid", testTokenA, nil, nil)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("malformed identifier: got %d: %s", response.Code, response.Body.String())
	}
}

func TestDirectoryPaginationIsStableAndCursorIsTamperProof(t *testing.T) {
	api := newTestAPI(t)
	names := []string{"Zulu", "alpha", "Echo", "bravo", "Delta"}
	for _, name := range names {
		response := api.request(t, http.MethodPost, "/api/v1/directories", testTokenA, map[string]any{
			"parent_id": api.tenantA.RootNodeID, "name": name,
		}, nil)
		if response.Code != http.StatusCreated {
			t.Fatalf("create %q: got %d: %s", name, response.Code, response.Body.String())
		}
	}
	var listed []string
	cursor := ""
	for {
		path := "/api/v1/directories/" + api.tenantA.RootNodeID + "/children?limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		response := api.request(t, http.MethodGet, path, testTokenA, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("list page: got %d: %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data []nodeResponse `json:"data"`
			Page struct {
				NextCursor *string `json:"next_cursor"`
			} `json:"page"`
		}
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		for _, node := range envelope.Data {
			listed = append(listed, strings.ToLower(node.Name))
		}
		if envelope.Page.NextCursor == nil {
			break
		}
		cursor = *envelope.Page.NextCursor
	}
	want := make([]string, len(names))
	for i, name := range names {
		want[i] = strings.ToLower(name)
	}
	sort.Strings(want)
	if strings.Join(listed, ",") != strings.Join(want, ",") {
		t.Fatalf("listed names=%v, want %v", listed, want)
	}
	tampered := api.request(t, http.MethodGet, "/api/v1/directories/"+api.tenantA.RootNodeID+"/children?cursor="+url.QueryEscape(cursor+"x"), testTokenA, nil, nil)
	if tampered.Code != http.StatusBadRequest || !strings.Contains(tampered.Body.String(), `"code":"invalid_cursor"`) {
		t.Fatalf("tampered cursor: got %d: %s", tampered.Code, tampered.Body.String())
	}
}

func TestDriveVerticalSliceAndTenantIsolation(t *testing.T) {
	api := newTestAPI(t)
	bootstrap := api.request(t, http.MethodGet, "/api/v1/tenant", testTokenA, nil, nil)
	if bootstrap.Code != http.StatusOK {
		t.Fatalf("discover tenant root: got %d: %s", bootstrap.Code, bootstrap.Body.String())
	}
	tenant := decodeData[tenantResponse](t, bootstrap)

	createDirectory := api.request(t, http.MethodPost, "/api/v1/directories", testTokenA, map[string]any{
		"parent_id": tenant.RootDirectoryID, "name": "Projects",
	}, nil)
	if createDirectory.Code != http.StatusCreated {
		t.Fatalf("create directory: got %d: %s", createDirectory.Code, createDirectory.Body.String())
	}
	directory := decodeData[nodeResponse](t, createDirectory)

	createUpload := api.request(t, http.MethodPost, "/api/v1/uploads", testTokenA, map[string]any{
		"parent_id": directory.ID, "name": "report.bin", "size": 12, "mime_type": "application/octet-stream",
	}, nil)
	if createUpload.Code != http.StatusCreated {
		t.Fatalf("create upload: got %d: %s", createUpload.Code, createUpload.Body.String())
	}
	upload := decodeData[uploadResponse](t, createUpload)

	sign := api.request(t, http.MethodPost, "/api/v1/uploads/"+upload.ID+"/parts/sign", testTokenA, map[string]any{"part_number": 1}, nil)
	if sign.Code != http.StatusOK {
		t.Fatalf("sign upload part: got %d: %s", sign.Code, sign.Body.String())
	}

	session, err := api.service.Upload(context.Background(), drive.Identity{TenantID: testTenantA, PrincipalID: testPrincipalA}, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	part, err := api.storage.PutPart(session.StorageUploadID, 1, []byte("hello world!"))
	if err != nil {
		t.Fatal(err)
	}
	complete := api.request(t, http.MethodPost, "/api/v1/uploads/"+upload.ID+"/complete", testTokenA, map[string]any{"parts": []drive.CompletedPart{part}}, nil)
	if complete.Code != http.StatusCreated {
		t.Fatalf("complete upload: got %d: %s", complete.Code, complete.Body.String())
	}
	var completed struct {
		Upload uploadResponse `json:"upload"`
		File   nodeResponse   `json:"file"`
	}
	completed = decodeData[struct {
		Upload uploadResponse `json:"upload"`
		File   nodeResponse   `json:"file"`
	}](t, complete)

	retry := api.request(t, http.MethodPost, "/api/v1/uploads/"+upload.ID+"/complete", testTokenA, map[string]any{"parts": []drive.CompletedPart{part}}, nil)
	if retry.Code != http.StatusOK {
		t.Fatalf("retry complete: got %d: %s", retry.Code, retry.Body.String())
	}
	retried := decodeData[struct {
		Upload uploadResponse `json:"upload"`
		File   nodeResponse   `json:"file"`
	}](t, retry)
	if retried.File.ID != completed.File.ID {
		t.Fatalf("completion was not idempotent: %s != %s", retried.File.ID, completed.File.ID)
	}

	list := api.request(t, http.MethodGet, "/api/v1/directories/"+directory.ID+"/children", testTokenA, nil, nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), completed.File.ID) {
		t.Fatalf("list directory: got %d: %s", list.Code, list.Body.String())
	}

	download := api.request(t, http.MethodPost, "/api/v1/files/"+completed.File.ID+"/download-authorizations", testTokenA, nil, nil)
	if download.Code != http.StatusCreated || !strings.Contains(download.Body.String(), `"method":"GET"`) {
		t.Fatalf("download authorization: got %d: %s", download.Code, download.Body.String())
	}

	crossTenant := api.request(t, http.MethodGet, "/api/v1/files/"+completed.File.ID, testTokenB, nil, nil)
	if crossTenant.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant read should be 404, got %d: %s", crossTenant.Code, crossTenant.Body.String())
	}

	recycle := api.request(t, http.MethodDelete, "/api/v1/nodes/"+completed.File.ID, testTokenA, nil, map[string]string{"If-Match": `"1"`})
	if recycle.Code != http.StatusNoContent {
		t.Fatalf("recycle file: got %d: %s", recycle.Code, recycle.Body.String())
	}
	blockedDownload := api.request(t, http.MethodPost, "/api/v1/files/"+completed.File.ID+"/download-authorizations", testTokenA, nil, nil)
	if blockedDownload.Code != http.StatusNotFound {
		t.Fatalf("recycled file should not be downloadable, got %d: %s", blockedDownload.Code, blockedDownload.Body.String())
	}

	recycleList := api.request(t, http.MethodGet, "/api/v1/recycle-bin", testTokenA, nil, nil)
	if recycleList.Code != http.StatusOK || !strings.Contains(recycleList.Body.String(), completed.File.ID) {
		t.Fatalf("list recycle bin: got %d: %s", recycleList.Code, recycleList.Body.String())
	}
	restore := api.request(t, http.MethodPost, "/api/v1/recycle-bin/"+completed.File.ID+"/restore", testTokenA, nil, map[string]string{"If-Match": `"2"`})
	if restore.Code != http.StatusOK {
		t.Fatalf("restore file: got %d: %s", restore.Code, restore.Body.String())
	}
	restoredDownload := api.request(t, http.MethodPost, "/api/v1/files/"+completed.File.ID+"/download-authorizations", testTokenA, nil, nil)
	if restoredDownload.Code != http.StatusCreated {
		t.Fatalf("restored file should be downloadable, got %d: %s", restoredDownload.Code, restoredDownload.Body.String())
	}
	recycleAgain := api.request(t, http.MethodDelete, "/api/v1/nodes/"+completed.File.ID, testTokenA, nil, map[string]string{"If-Match": `"3"`})
	if recycleAgain.Code != http.StatusNoContent {
		t.Fatalf("recycle restored file: got %d: %s", recycleAgain.Code, recycleAgain.Body.String())
	}
	purge := api.request(t, http.MethodDelete, "/api/v1/recycle-bin/"+completed.File.ID, testTokenA, nil, map[string]string{"If-Match": `"4"`})
	if purge.Code != http.StatusNoContent {
		t.Fatalf("purge file: got %d: %s", purge.Code, purge.Body.String())
	}
	if _, _, exists := api.storage.Object(session.ObjectKey); exists {
		t.Fatal("purged file object still exists")
	}
}

func TestStrictJSONAndTenantHeaderCannotSwitchContext(t *testing.T) {
	api := newTestAPI(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/directories", strings.NewReader(`{"parent_id":"`+api.tenantA.RootNodeID+`","name":"x","tenant_id":"`+testTenantB+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testTokenA)
	request.Header.Set("X-Tenant-ID", testTenantB)
	recorder := httptest.NewRecorder()
	api.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown tenant field should be rejected, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRequestBodyLimitsAndMediaType(t *testing.T) {
	api := newTestAPI(t)
	tenant := decodeData[tenantResponse](t, api.request(t, http.MethodGet, "/api/v1/tenant", testTokenA, nil, nil))

	plain := httptest.NewRequest(http.MethodPost, "/api/v1/directories", strings.NewReader(`{"parent_id":"`+tenant.RootDirectoryID+`","name":"x"}`))
	plain.Header.Set("Content-Type", "text/plain")
	plain.Header.Set("Authorization", "Bearer "+testTokenA)
	plainRecorder := httptest.NewRecorder()
	api.handler.ServeHTTP(plainRecorder, plain)
	if plainRecorder.Code != http.StatusUnsupportedMediaType || !strings.Contains(plainRecorder.Body.String(), `"code":"unsupported_media_type"`) {
		t.Fatalf("plain content type: status=%d body=%s", plainRecorder.Code, plainRecorder.Body.String())
	}

	oversized := strings.NewReader(`{"parent_id":"` + tenant.RootDirectoryID + `","name":"` + strings.Repeat("n", maxJSONBody) + `"}`)
	large := httptest.NewRequest(http.MethodPost, "/api/v1/directories", oversized)
	large.Header.Set("Content-Type", "application/json")
	large.Header.Set("Authorization", "Bearer "+testTokenA)
	largeRecorder := httptest.NewRecorder()
	api.handler.ServeHTTP(largeRecorder, large)
	if largeRecorder.Code != http.StatusRequestEntityTooLarge || !strings.Contains(largeRecorder.Body.String(), `"code":"request_too_large"`) {
		t.Fatalf("oversized JSON: status=%d body=%s", largeRecorder.Code, largeRecorder.Body.String())
	}
	children := api.request(t, http.MethodGet, "/api/v1/directories/"+tenant.RootDirectoryID+"/children", testTokenA, nil, nil)
	if children.Code != http.StatusOK || strings.Contains(children.Body.String(), `"name":"`+strings.Repeat("n", 32)) {
		t.Fatalf("oversized request must not partially create nodes: %s", children.Body.String())
	}
}

func TestOIDCAuthorizationEnforcesTenantSelectorMembershipAndRBAC(t *testing.T) {
	base := newTestAPI(t)
	ctx := context.Background()
	issuer := "https://issuer.example.test"
	now := time.Now().UTC()
	seeds := []struct {
		token       string
		principalID string
		role        drive.AccessRole
	}{
		{token: "viewer-token", principalID: testPrincipalA, role: drive.RoleViewer},
		{token: "editor-token", principalID: testPrincipalB, role: drive.RoleEditor},
		{token: "admin-token", principalID: testPrincipalC, role: drive.RoleAdmin},
		{token: "suspended-token", principalID: testPrincipalD, role: drive.RoleViewer},
	}
	verifier := make(testOIDCVerifier, len(seeds))
	for _, seed := range seeds {
		if _, err := base.repository.EnsureOIDCMember(ctx, drive.OIDCMemberSeed{
			PrincipalID: seed.principalID, TenantID: testTenantA, Issuer: issuer, Subject: seed.token,
			DisplayName: seed.token, Role: seed.role, Now: now,
		}); err != nil {
			t.Fatal(err)
		}
		verifier[seed.token] = auth.OIDCClaims{
			Issuer: issuer, Subject: seed.token, Audience: []string{"asteria-api"}, ExpiresAt: now.Add(time.Hour),
		}
	}
	if err := base.repository.SetOIDCMemberStatus(ctx, testTenantA, testPrincipalD, drive.MemberStatusSuspended); err != nil {
		t.Fatal(err)
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
	base.handler = httpServer.Handler()
	unauthenticated := base.request(t, http.MethodGet, "/api/v1/directories/"+base.tenantA.RootNodeID, "", nil, map[string]string{auth.TenantHeader: testTenantA})
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("OIDC request without bearer token should be 401, got %d: %s", unauthenticated.Code, unauthenticated.Body.String())
	}
	missingTenant := base.request(t, http.MethodGet, "/api/v1/tenant", "viewer-token", nil, nil)
	if missingTenant.Code != http.StatusBadRequest {
		t.Fatalf("OIDC request without tenant selector should be 400, got %d: %s", missingTenant.Code, missingTenant.Body.String())
	}
	invalidTenant := base.request(t, http.MethodGet, "/api/v1/tenant", "viewer-token", nil, map[string]string{auth.TenantHeader: "not-a-uuid"})
	if invalidTenant.Code != http.StatusBadRequest {
		t.Fatalf("OIDC request with invalid tenant selector should be 400, got %d: %s", invalidTenant.Code, invalidTenant.Body.String())
	}
	viewerRead := base.request(t, http.MethodGet, "/api/v1/directories/"+base.tenantA.RootNodeID, "viewer-token", nil, map[string]string{auth.TenantHeader: testTenantA})
	if viewerRead.Code != http.StatusOK {
		t.Fatalf("viewer file metadata read should succeed, got %d: %s", viewerRead.Code, viewerRead.Body.String())
	}
	viewerWrite := base.request(t, http.MethodPost, "/api/v1/directories", "viewer-token", map[string]any{"parent_id": base.tenantA.RootNodeID, "name": "viewer"}, map[string]string{auth.TenantHeader: testTenantA})
	if viewerWrite.Code != http.StatusForbidden {
		t.Fatalf("viewer write should be forbidden, got %d: %s", viewerWrite.Code, viewerWrite.Body.String())
	}
	editorWrite := base.request(t, http.MethodPost, "/api/v1/directories", "editor-token", map[string]any{"parent_id": base.tenantA.RootNodeID, "name": "editor"}, map[string]string{auth.TenantHeader: testTenantA})
	if editorWrite.Code != http.StatusCreated {
		t.Fatalf("editor write should succeed, got %d: %s", editorWrite.Code, editorWrite.Body.String())
	}
	editorDirectory := decodeData[nodeResponse](t, editorWrite)
	editorDelete := base.request(t, http.MethodDelete, "/api/v1/nodes/"+editorDirectory.ID, "editor-token", nil, map[string]string{auth.TenantHeader: testTenantA, "If-Match": `"1"`})
	if editorDelete.Code != http.StatusForbidden {
		t.Fatalf("editor delete should be forbidden, got %d: %s", editorDelete.Code, editorDelete.Body.String())
	}
	adminDelete := base.request(t, http.MethodDelete, "/api/v1/nodes/"+editorDirectory.ID, "admin-token", nil, map[string]string{auth.TenantHeader: testTenantA, "If-Match": `"1"`})
	if adminDelete.Code != http.StatusNoContent {
		t.Fatalf("admin delete should succeed, got %d: %s", adminDelete.Code, adminDelete.Body.String())
	}
	suspended := base.request(t, http.MethodGet, "/api/v1/tenant", "suspended-token", nil, map[string]string{auth.TenantHeader: testTenantA})
	if suspended.Code != http.StatusForbidden {
		t.Fatalf("suspended member should be forbidden, got %d: %s", suspended.Code, suspended.Body.String())
	}
	wrongTenant := base.request(t, http.MethodGet, "/api/v1/tenant", "viewer-token", nil, map[string]string{auth.TenantHeader: testTenantB})
	if wrongTenant.Code != http.StatusForbidden {
		t.Fatalf("member of another tenant should be forbidden, got %d: %s", wrongTenant.Code, wrongTenant.Body.String())
	}
}
