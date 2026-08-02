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
	Authenticator     *auth.Trusted
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

func New(options Options) (*Server, error) {
	if options.Service == nil || options.Authenticator == nil {
		return nil, drive.E(drive.CodeInvalidRequest, "service and authenticator are required")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	api := &api{service: options.Service, logger: options.Logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	mux.HandleFunc("GET /readyz", api.ready)

	protected := func(next http.Handler) http.Handler {
		return options.Authenticator.Middleware(api.writeError)(api.captureIdentity(next))
	}
	mux.Handle("GET /api/v1/tenant", protected(http.HandlerFunc(api.getTenant)))
	mux.Handle("POST /api/v1/directories", protected(http.HandlerFunc(api.createDirectory)))
	mux.Handle("GET /api/v1/directories/{id}", protected(http.HandlerFunc(api.getDirectory)))
	mux.Handle("GET /api/v1/directories/{id}/children", protected(http.HandlerFunc(api.listChildren)))
	mux.Handle("GET /api/v1/files/{id}", protected(http.HandlerFunc(api.getFile)))
	mux.Handle("POST /api/v1/files/{id}/download-authorizations", protected(http.HandlerFunc(api.createDownloadAuthorization)))
	mux.Handle("PATCH /api/v1/nodes/{id}", protected(http.HandlerFunc(api.updateNode)))
	mux.Handle("DELETE /api/v1/nodes/{id}", protected(http.HandlerFunc(api.recycleNode)))
	mux.Handle("POST /api/v1/uploads", protected(http.HandlerFunc(api.createUpload)))
	mux.Handle("GET /api/v1/uploads/{id}", protected(http.HandlerFunc(api.getUpload)))
	mux.Handle("POST /api/v1/uploads/{id}/parts/sign", protected(http.HandlerFunc(api.signUploadPart)))
	mux.Handle("POST /api/v1/uploads/{id}/complete", protected(http.HandlerFunc(api.completeUpload)))
	mux.Handle("DELETE /api/v1/uploads/{id}", protected(http.HandlerFunc(api.abortUpload)))
	mux.Handle("GET /api/v1/recycle-bin", protected(http.HandlerFunc(api.listRecycle)))
	mux.Handle("POST /api/v1/recycle-bin/{id}/restore", protected(http.HandlerFunc(api.restoreNode)))
	mux.Handle("DELETE /api/v1/recycle-bin/{id}", protected(http.HandlerFunc(api.purgeNode)))
	mux.Handle("/api/", protected(http.HandlerFunc(api.notFound)))

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
