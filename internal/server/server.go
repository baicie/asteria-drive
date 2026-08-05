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
	Metrics           HTTPMetrics
}

type HTTPMetrics interface {
	HTTPRequestStarted()
	HTTPRequestFinished(method, route string, status int, duration time.Duration)
}

type Server struct {
	httpServer *http.Server
	handler    http.Handler
}

type apiHandler func(*api, http.ResponseWriter, *http.Request)

type apiRoute struct {
	pattern      string
	permission   drive.Permission
	aclElevation bool
	handler      apiHandler
}

var publicAPIRoutes = []apiRoute{
	{pattern: "GET /healthz", handler: (*api).health},
	{pattern: "GET /readyz", handler: (*api).ready},
}

var externalAPIRoutes = []apiRoute{
	{pattern: "POST /api/v1/invitations/accept", handler: (*api).acceptInvitation},
}

var protectedAPIRoutes = []apiRoute{
	{pattern: "GET /api/v1/tenant", permission: drive.PermissionTenantRead, handler: (*api).getTenant},
	{pattern: "GET /api/v1/tenant/members", permission: drive.PermissionMembersRead, handler: (*api).listMembers},
	{pattern: "PATCH /api/v1/tenant/members/{principal_id}", permission: drive.PermissionMembersManage, handler: (*api).updateMember},
	{pattern: "DELETE /api/v1/tenant/members/{principal_id}", permission: drive.PermissionMembersManage, handler: (*api).deleteMember},
	{pattern: "POST /api/v1/tenant/invitations", permission: drive.PermissionMembersManage, handler: (*api).createInvitation},
	{pattern: "GET /api/v1/tenant/invitations", permission: drive.PermissionMembersManage, handler: (*api).listInvitations},
	{pattern: "POST /api/v1/tenant/invitations/{invitation_id}/revoke", permission: drive.PermissionMembersManage, handler: (*api).revokeInvitation},
	{pattern: "POST /api/v1/tenant/groups", permission: drive.PermissionGroupsManage, handler: (*api).createGroup},
	{pattern: "GET /api/v1/tenant/groups", permission: drive.PermissionGroupsManage, handler: (*api).listGroups},
	{pattern: "PATCH /api/v1/tenant/groups/{group_id}", permission: drive.PermissionGroupsManage, handler: (*api).updateGroup},
	{pattern: "DELETE /api/v1/tenant/groups/{group_id}", permission: drive.PermissionGroupsManage, handler: (*api).deleteGroup},
	{pattern: "GET /api/v1/tenant/groups/{group_id}/members", permission: drive.PermissionGroupsManage, handler: (*api).listGroupMembers},
	{pattern: "PUT /api/v1/tenant/groups/{group_id}/members/{principal_id}", permission: drive.PermissionGroupsManage, handler: (*api).addGroupMember},
	{pattern: "DELETE /api/v1/tenant/groups/{group_id}/members/{principal_id}", permission: drive.PermissionGroupsManage, handler: (*api).removeGroupMember},
	{pattern: "GET /api/v1/tenant/audit-events", permission: drive.PermissionAuditRead, handler: (*api).listAuditEvents},
	{pattern: "GET /api/v1/tenant/audit-events/export", permission: drive.PermissionAuditRead, handler: (*api).exportAuditEvents},
	{pattern: "POST /api/v1/directories", permission: drive.PermissionFilesWrite, aclElevation: true, handler: (*api).createDirectory},
	{pattern: "GET /api/v1/directories/{id}", permission: drive.PermissionFilesRead, handler: (*api).getDirectory},
	{pattern: "GET /api/v1/directories/{id}/children", permission: drive.PermissionFilesRead, handler: (*api).listChildren},
	{pattern: "GET /api/v1/files/{id}", permission: drive.PermissionFilesRead, handler: (*api).getFile},
	{pattern: "POST /api/v1/files/{id}/download-authorizations", permission: drive.PermissionFilesRead, handler: (*api).createDownloadAuthorization},
	{pattern: "PATCH /api/v1/nodes/{id}", permission: drive.PermissionFilesWrite, aclElevation: true, handler: (*api).updateNode},
	{pattern: "DELETE /api/v1/nodes/{id}", permission: drive.PermissionFilesDelete, aclElevation: true, handler: (*api).recycleNode},
	{pattern: "GET /api/v1/nodes/{id}/acl", permission: drive.PermissionFilesRead, handler: (*api).listNodeACL},
	{pattern: "PUT /api/v1/nodes/{id}/acl", permission: drive.PermissionFilesRead, handler: (*api).setNodeACL},
	{pattern: "DELETE /api/v1/nodes/{id}/acl/{entry_id}", permission: drive.PermissionFilesRead, handler: (*api).deleteNodeACL},
	{pattern: "POST /api/v1/uploads", permission: drive.PermissionFilesWrite, aclElevation: true, handler: (*api).createUpload},
	{pattern: "GET /api/v1/uploads/{id}", permission: drive.PermissionFilesRead, handler: (*api).getUpload},
	{pattern: "POST /api/v1/uploads/{id}/parts/sign", permission: drive.PermissionFilesWrite, aclElevation: true, handler: (*api).signUploadPart},
	{pattern: "POST /api/v1/uploads/{id}/complete", permission: drive.PermissionFilesWrite, aclElevation: true, handler: (*api).completeUpload},
	{pattern: "DELETE /api/v1/uploads/{id}", permission: drive.PermissionFilesWrite, aclElevation: true, handler: (*api).abortUpload},
	{pattern: "GET /api/v1/recycle-bin", permission: drive.PermissionFilesRead, handler: (*api).listRecycle},
	{pattern: "POST /api/v1/recycle-bin/{id}/restore", permission: drive.PermissionFilesWrite, aclElevation: true, handler: (*api).restoreNode},
	{pattern: "DELETE /api/v1/recycle-bin/{id}", permission: drive.PermissionFilesDelete, aclElevation: true, handler: (*api).purgeNode},
}

func New(options Options) (*Server, error) {
	if options.Service == nil || options.Authenticator == nil {
		return nil, drive.E(drive.CodeInvalidRequest, "service and authenticator are required")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	api := &api{service: options.Service, logger: options.Logger, metrics: options.Metrics}
	mux := http.NewServeMux()
	for _, route := range publicAPIRoutes {
		handler := route.handler
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			handler(api, w, r)
		})
	}
	for _, route := range externalAPIRoutes {
		handler := route.handler
		if external, ok := options.Authenticator.(auth.ExternalAuthenticator); ok {
			mux.Handle(route.pattern, external.ExternalMiddleware(api.writeError)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler(api, w, r)
			})))
		} else {
			mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
				api.writeError(w, r, drive.E(drive.CodeUnauthenticated, "external identity authentication is unavailable"))
			})
		}
	}

	protected := func(permission drive.Permission, aclElevation bool, next http.Handler) http.Handler {
		return options.Authenticator.Middleware(api.writeError)(api.captureIdentity(api.authorize(permission, aclElevation)(next)))
	}
	for _, route := range protectedAPIRoutes {
		handler := route.handler
		mux.Handle(route.pattern, protected(route.permission, route.aclElevation, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler(api, w, r)
		})))
	}
	mux.Handle("/api/", protected("", false, http.HandlerFunc(api.notFound)))

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
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(drive.ContextWithRequestID(ctx, id)))
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
		if a.metrics != nil {
			a.metrics.HTTPRequestStarted()
		}
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
		if a.metrics != nil {
			a.metrics.HTTPRequestFinished(r.Method, r.Pattern, recorder.status, time.Since(started))
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

func (a *api) authorize(permission drive.Permission, aclElevation bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := auth.FromContext(r.Context())
			if !ok {
				a.writeError(w, r, drive.E(drive.CodeUnauthenticated, "a valid bearer token is required"))
				return
			}
			if permission != "" && !principal.HasPermission(permission) && !aclElevation {
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
