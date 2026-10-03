// Package server implements the data-plane HTTP listener.
//
// It binds listen_host:listen_port, serves GET /healthz, and registers the
// OpenAI-compatible routes. Actual forwarding to local backends is owned by
// #6 (internal/proxy); until then OpenAI routes delegate to a ProxyHandler
// which defaults to a 501 stub. Graceful shutdown / signal handling and
// request logging land in #7; this package exposes Shutdown so #7 can use it.
package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
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
}

// Server is the data-plane HTTP listener.
type Server struct {
	opts  Options
	start time.Time
	now   func() time.Time
	mux   *http.ServeMux
	srv   *http.Server
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
	s := &Server{
		opts:  opts,
		start: start,
		now:   now,
		mux:   http.NewServeMux(),
	}
	s.register()
	return s
}

// Addr returns "host:port" for binding (net.JoinHostPort handles IPv6).
func (s *Server) Addr() string {
	return net.JoinHostPort(s.opts.ListenHost, strconv.Itoa(s.opts.ListenPort))
}

// Handler exposes the underlying mux for tests and for httptest servers.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// ListenAndServe binds Addr and serves. It blocks until error or Shutdown.
func (s *Server) ListenAndServe() error {
	s.srv = &http.Server{
		Addr:    s.Addr(),
		Handler: s.mux,
	}
	return s.srv.ListenAndServe()
}

// Shutdown gracefully stops a server started with ListenAndServe.
// Signal wiring (SIGINT/SIGTERM) is owned by #7; this is just the primitive.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
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
