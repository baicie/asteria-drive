package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/baicie/asteria-drive/internal/auth"
	"github.com/baicie/asteria-drive/internal/drive"
)

type Options struct {
	Address           string
	Service           *drive.Service
	Authenticator     auth.Authenticator
	Logger            *slog.Logger
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

type Server struct {
	httpServer *http.Server
	handler    http.Handler
}

type apiHandler func(*api, http.ResponseWriter, *http.Request)

type apiRoute struct {
	pattern    string
	permission drive.Permission
	handler    apiHandler
}

var publicAPIRoutes = []apiRoute{
	{pattern: "GET /healthz", handler: (*api).health},
	{pattern: "GET /readyz", handler: (*api).ready},
}

var protectedAPIRoutes = []apiRoute{
	{pattern: "GET /api/v1/tenant", permission: drive.PermissionTenantRead, handler: (*api).getTenant},
	{pattern: "GET /api/v1/tenant/members", permission: drive.PermissionMembersRead, handler: (*api).listMembers},
	{pattern: "PATCH /api/v1/tenant/members/{principal_id}", permission: drive.PermissionMembersManage, handler: (*api).updateMember},
	{pattern: "POST /api/v1/directories", permission: drive.PermissionFilesWrite, handler: (*api).createDirectory},
	{pattern: "GET /api/v1/directories/{id}", permission: drive.PermissionFilesRead, handler: (*api).getDirectory},
	{pattern: "GET /api/v1/directories/{id}/children", permission: drive.PermissionFilesRead, handler: (*api).listChildren},
	{pattern: "GET /api/v1/files/{id}", permission: drive.PermissionFilesRead, handler: (*api).getFile},
	{pattern: "POST /api/v1/files/{id}/download-authorizations", permission: drive.PermissionFilesRead, handler: (*api).createDownloadAuthorization},
	{pattern: "PATCH /api/v1/nodes/{id}", permission: drive.PermissionFilesWrite, handler: (*api).updateNode},
	{pattern: "DELETE /api/v1/nodes/{id}", permission: drive.PermissionFilesDelete, handler: (*api).recycleNode},
	{pattern: "POST /api/v1/uploads", permission: drive.PermissionFilesWrite, handler: (*api).createUpload},
	{pattern: "GET /api/v1/uploads/{id}", permission: drive.PermissionFilesRead, handler: (*api).getUpload},
	{pattern: "POST /api/v1/uploads/{id}/parts/sign", permission: drive.PermissionFilesWrite, handler: (*api).signUploadPart},
	{pattern: "POST /api/v1/uploads/{id}/complete", permission: drive.PermissionFilesWrite, handler: (*api).completeUpload},
	{pattern: "DELETE /api/v1/uploads/{id}", permission: drive.PermissionFilesWrite, handler: (*api).abortUpload},
	{pattern: "GET /api/v1/recycle-bin", permission: drive.PermissionFilesRead, handler: (*api).listRecycle},
	{pattern: "POST /api/v1/recycle-bin/{id}/restore", permission: drive.PermissionFilesWrite, handler: (*api).restoreNode},
	{pattern: "DELETE /api/v1/recycle-bin/{id}", permission: drive.PermissionFilesDelete, handler: (*api).purgeNode},
}

func New(options Options) (*Server, error) {
	if options.Service == nil || options.Authenticator == nil {
		return nil, drive.E(drive.CodeInvalidRequest, "service and authenticator are required")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	api := &api{service: options.Service, logger: options.Logger}
	mux := http.NewServeMux()
	for _, route := range publicAPIRoutes {
		handler := route.handler
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			handler(api, w, r)
		})
	}

	protected := func(permission drive.Permission, next http.Handler) http.Handler {
		return options.Authenticator.Middleware(api.writeError)(api.captureIdentity(api.authorize(permission)(next)))
	}
	for _, route := range protectedAPIRoutes {
		handler := route.handler
		mux.Handle(route.pattern, protected(route.permission, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler(api, w, r)
		})))
	}
	mux.Handle("/api/", protected("", http.HandlerFunc(api.notFound)))

	handler := api.requestID(api.logRequest(api.recover(mux)))
	server := &Server{handler: handler}
	server.httpServer = &http.Server{
		Addr: options.Address, Handler: handler, ReadHeaderTimeout: options.ReadHeaderTimeout,
		ReadTimeout: options.ReadTimeout, WriteTimeout: options.WriteTimeout, IdleTimeout: options.IdleTimeout,
		MaxHeaderBytes: 1 << 20,
	}
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) ListenAndServe() error { return s.httpServer.ListenAndServe() }

func (s *Server) Shutdown(ctx context.Context) error { return s.httpServer.Shutdown(ctx) }

type requestIDKey struct{}
type requestLogKey struct{}

type requestLogFields struct {
	tenantID    string
	principalID string
	errorCode   string
}

func requestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

func (a *api) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			generated, err := drive.NewID()
			if err != nil {
				id = "req_unavailable"
			} else {
				id = "req_" + generated
			}
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

type responseStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *responseStatusRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseStatusRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *responseStatusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (a *api) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		fields := &requestLogFields{}
		r = r.WithContext(context.WithValue(r.Context(), requestLogKey{}, fields))
		recorder := &responseStatusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		if recorder.status == 0 {
			recorder.status = http.StatusOK
		}
		attributes := []any{
			"request_id", requestID(r.Context()), "method", r.Method,
			"route", r.Pattern, "status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
		}
		if fields.tenantID != "" {
			attributes = append(attributes, "tenant_id", fields.tenantID, "principal_id", fields.principalID)
		}
		if fields.errorCode != "" {
			attributes = append(attributes, "error_code", fields.errorCode)
		}
		a.logger.Info("http request completed", attributes...)
	})
}

func (a *api) captureIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principal, ok := auth.FromContext(r.Context()); ok {
			if fields, ok := r.Context().Value(requestLogKey{}).(*requestLogFields); ok {
				fields.tenantID = principal.Identity.TenantID
				fields.principalID = principal.Identity.PrincipalID
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *api) authorize(permission drive.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := auth.FromContext(r.Context())
			if !ok {
				a.writeError(w, r, drive.E(drive.CodeUnauthenticated, "a valid bearer token is required"))
				return
			}
			if permission != "" && !principal.HasPermission(permission) {
				a.writeError(w, r, drive.E(drive.CodeForbidden, "the authenticated principal lacks this permission"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func validRequestID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func (a *api) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("http handler panic", "request_id", requestID(r.Context()), "method", r.Method, "route", r.Pattern, "stack", string(debug.Stack()))
				a.writeError(w, r, drive.E(drive.CodeInternal, "internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func isServerClosed(err error) bool { return errors.Is(err, http.ErrServerClosed) }
