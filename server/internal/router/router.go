// Package router forwards OpenAI requests to a node that serves the requested model.
package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"mooch-serv/internal/feed"
	"mooch-serv/internal/registry"
	"mooch-serv/internal/respond"
)

const maxRequestBody = 32 << 20

var forwardPaths = []string{
	"/v1/chat/completions",
	"/v1/completions",
	"/v1/embeddings",
	"/v1/images/generations",
}

type Handler struct {
	reg   *registry.Registry
	log   *slog.Logger
	feed  *feed.Feed
	proxy *httputil.ReverseProxy
}

// New returns a router. A nil feed prints no events.
func New(reg *registry.Registry, f *feed.Feed) *Handler {
	h := &Handler{reg: reg, log: slog.With("component", "router"), feed: f}
	h.proxy = &httputil.ReverseProxy{
		// handleForward sets the target URL. FlushInterval -1 sends each stream chunk at once.
		Director:      func(*http.Request) {},
		FlushInterval: -1,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			ResponseHeaderTimeout: 5 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if h.feed == nil {
				h.log.Warn("node unreachable", "host", r.URL.Host, "error", err)
			}
			h.feed.Unreachable(r.URL.Host)
			respond.Error(w, http.StatusBadGateway, "node at "+r.URL.Host+" is unreachable")
		},
	}
	return h
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/models", h.handleModels)
	mux.HandleFunc("GET /health", h.handleHealth)
	for _, p := range forwardPaths {
		mux.HandleFunc("POST "+p, h.handleForward)
	}
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID          string `json:"id"`
		Object      string `json:"object"`
		OwnedBy     string `json:"owned_by"`
		MaxModelLen *int   `json:"max_model_len,omitempty"`
	}
	data := []model{}
	for _, m := range h.reg.Models() {
		entry := model{ID: m.ID, Object: "model", OwnedBy: "vllm"}
		if m.MaxModelLen > 0 {
			n := m.MaxModelLen
			entry.MaxModelLen = &n
		}
		data = append(data, entry)
	}
	respond.JSON(w, map[string]any{"object": "list", "data": data})
}

// handleHealth answers the vLLM health check with 200 OK.
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) handleForward(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		respond.Error(w, http.StatusRequestEntityTooLarge, "request body is too large")
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		respond.Error(w, http.StatusBadRequest, `request body must be JSON with a "model" field`)
		return
	}
	node, found := h.reg.Lookup(req.Model)
	if !found {
		respond.Error(w, http.StatusNotFound, fmt.Sprintf("no live node serves model %q", req.Model))
		return
	}
	h.log.Info("forward", "method", r.Method, "path", r.URL.Path, "model", req.Model, "node_id", node.NodeID)
	from, to := h.requester(r), feed.Name(node)
	h.feed.Route(from, req.Model, to)
	h.feed.Request(from, to, r.Method, r.URL.Path, feed.Preview(body))
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	w = rec
	defer func() {
		h.feed.Response(to, from, rec.status, time.Since(start), feed.ResponsePreview(rec.body.Bytes()))
	}()

	r.URL = &url.URL{Scheme: "http", Host: node.ListenAddr, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
	r.Host = node.ListenAddr
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	h.proxy.ServeHTTP(w, r)
}

// requester names the caller: the node at the remote IP, else the remote IP.
func (h *Handler) requester(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if n, ok := h.reg.NodeByIP(host); ok {
		return feed.Name(n)
	}
	return host
}

// maxPreviewBody limits the response bytes that the feed keeps for its preview.
const maxPreviewBody = 64 << 10

// statusRecorder keeps the response status and the start of the body. Unwrap lets the proxy flush stream chunks.
type statusRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if room := maxPreviewBody - s.body.Len(); room > 0 {
		s.body.Write(b[:min(len(b), room)])
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
