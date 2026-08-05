package observability

import (
	"context"
	"net/http"
	"time"
)

type Server struct {
	server *http.Server
}

func NewServer(address string, handler http.Handler) *Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", handler)
	return &Server{server: &http.Server{
		Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 * 1024,
	}}
}

func (s *Server) ListenAndServe() error { return s.server.ListenAndServe() }

func (s *Server) Shutdown(ctx context.Context) error { return s.server.Shutdown(ctx) }
