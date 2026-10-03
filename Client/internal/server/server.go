// Package server implements the data-plane HTTP listener.
//
// It binds listen_host:listen_port, serves GET /healthz, and registers the
// OpenAI-compatible routes. Actual forwarding to local backends is owned by
// #6 (internal/proxy). This package adds the #7 data-plane scope: slog
// request logs and graceful shutdown on SIGINT/SIGTERM. Health probing
// stays with the control plane.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// ProxyHandler handles an OpenAI route after the listener has accepted it.
// #6 (internal/proxy) will provide the real implementation (model routing +
// ReverseProxy). Until then Server uses stubProxy which returns 501.
type ProxyHandler interface {
	ServeProxy(w http.ResponseWriter, r *http.Request)
}

// ProxyHandlerFunc adapts a function to ProxyHandler.
type ProxyHandlerFunc func(w http.ResponseWriter, r *http.Request)

func (f ProxyHandlerFunc) ServeProxy(w http.ResponseWriter, r *http.Request) {
	f(w, r)
}

// Route describes one OpenAI-compatible endpoint.
type Route struct {
	Method string
	Path   string
}

// DefaultOpenAIRoutes is the route set required by REQUIREMENTS.md:
// the two mandatory routes plus the extras. Custom deployments can extend
// this via Options.Routes (append more entries) without touching Server.
var DefaultOpenAIRoutes = []Route{
	{Method: http.MethodPost, Path: "/v1/chat/completions"},
	{Method: http.MethodPost, Path: "/v1/completions"},
	{Method: http.MethodPost, Path: "/v1/embeddings"},
	{Method: http.MethodPost, Path: "/v1/audio/transcriptions"},
	{Method: http.MethodPost, Path: "/v1/images/generations"},
	{Method: http.MethodGet, Path: "/v1/models"},
}

// Options configures the listener. It is intentionally self-contained and
// does not depend on Person 1's config/identity packages.
type Options struct {
	ListenHost string
	ListenPort int

	NodeID      string
	TailscaleIP string
	Version     string

	// Proxy handles OpenAI routes. Nil means the 501 stub.
	Proxy ProxyHandler

	// Routes overrides the OpenAI route set. Nil/empty uses DefaultOpenAIRoutes.
	Routes []Route

	// StartTime is when the node started (for /healthz uptime).
	// Zero means time.Now() at New().
	StartTime time.Time

	// Now reports current time (for /healthz uptime). Nil means time.Now.
	// Exposed for deterministic tests.
	Now func() time.Time

	// Logger receives structured request logs. Nil means slog.Default().
	Logger *slog.Logger
}

// Server is the data-plane HTTP listener.
type Server struct {
	opts   Options
	start  time.Time
	now    func() time.Time
	logger *slog.Logger
	mux    *http.ServeMux
	srv    *http.Server
}

// New builds a Server and registers all routes on an internal ServeMux.
func New(opts Options) *Server {
	start := opts.StartTime
	if start.IsZero() {
		start = time.Now()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		opts:   opts,
		start:  start,
		now:    now,
		logger: logger,
		mux:    http.NewServeMux(),
	}
	s.register()
	return s
}

// Addr returns "host:port" for binding (net.JoinHostPort handles IPv6).
func (s *Server) Addr() string {
	return net.JoinHostPort(s.opts.ListenHost, strconv.Itoa(s.opts.ListenPort))
}

// Handler exposes the underlying mux for tests and for httptest servers.
// The handler includes the slog request log middleware.
func (s *Server) Handler() http.Handler {
	return s.loggingMiddleware(s.mux)
}

// ListenAndServe binds Addr and serves. It blocks until error or Shutdown.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.Addr())
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve accepts connections on ln. It blocks until error or Shutdown.
// Use Serve with an ephemeral port in tests.
func (s *Server) Serve(ln net.Listener) error {
	s.srv = &http.Server{
		Handler: s.Handler(),
	}
	return s.srv.Serve(ln)
}

// Shutdown gracefully stops a server started with ListenAndServe or Serve.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

// DefaultShutdownTimeout bounds graceful shutdown. Active connections
// close within this time.
const DefaultShutdownTimeout = 5 * time.Second

// ListenAndServeWithGracefulShutdown serves Addr and stops cleanly on
// SIGINT or SIGTERM. It shares ctx with the caller, so the control plane
// can stop the heartbeat loop on the same signal. It exits within the
// timeout.
func (s *Server) ListenAndServeWithGracefulShutdown(ctx context.Context, shutdownTimeout time.Duration) error {
	if shutdownTimeout <= 0 {
		shutdownTimeout = DefaultShutdownTimeout
	}
	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s.srv = &http.Server{
		Addr:    s.Addr(),
		Handler: s.Handler(),
	}
	s.logger.Info("listener start",
		"component", "server",
		"node_id", s.opts.NodeID,
		"addr", s.Addr(),
		"version", s.opts.Version,
	)

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.srv.ListenAndServe()
	}()

	select {
	case <-sigCtx.Done():
		s.logger.Info("shutdown signal",
			"component", "server",
			"node_id", s.opts.NodeID,
		)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
		err := <-errCh
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) register() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)

	routes := s.opts.Routes
	if len(routes) == 0 {
		routes = DefaultOpenAIRoutes
	}
	for _, rt := range routes {
		rt := rt
		s.mux.HandleFunc(rt.Method+" "+rt.Path, func(w http.ResponseWriter, r *http.Request) {
			s.handleProxy(w, r)
		})
	}
}

// statusRecorder captures the status code for logs.
// It forwards Flush to support SSE streams.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
		r.ResponseWriter.WriteHeader(status)
	}
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// loggingMiddleware logs each request with slog. It records method,
// path, status, duration, node_id, and component.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.logger.Info("request",
			"component", "server",
			"node_id", s.opts.NodeID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", s.now().Sub(start).String(),
		)
	})
}

// healthResponse is the GET /healthz payload.
type healthResponse struct {
	NodeID      string `json:"node_id"`
	TailscaleIP string `json:"tailscale_ip"`
	Uptime      string `json:"uptime"`
	Version     string `json:"version"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	uptime := s.now().Sub(s.start).Truncate(time.Second).String()
	writeJSON(w, http.StatusOK, healthResponse{
		NodeID:      s.opts.NodeID,
		TailscaleIP: s.opts.TailscaleIP,
		Uptime:      uptime,
		Version:     s.opts.Version,
	})
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	proxy := s.opts.Proxy
	if proxy == nil {
		proxy = ProxyHandlerFunc(stubProxy)
	}
	// Attach node identity to error responses (used by stub now,
	// real proxy in #6 should do the same).
	if s.opts.NodeID != "" {
		w.Header().Set("X-PeerAI-Node", s.opts.NodeID)
	}
	proxy.ServeProxy(w, r)
}

// stubProxy is the placeholder until #6 implements forwarding.
func stubProxy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"error": map[string]any{
			"message": "proxy not implemented (forwarding lands in #6)",
			"type":    "proxy_not_implemented",
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
