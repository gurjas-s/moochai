// Package router serves the OpenAI-compatible API for tools.
// It routes each request to a node that serves the requested model.
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

	"peerai-serv/internal/openaierr"
	"peerai-serv/internal/registry"
)

const maxBody = 32 << 20

var routes = []string{
	"/v1/chat/completions",
	"/v1/completions",
	"/v1/embeddings",
	"/v1/images/generations",
}

// Handler forwards OpenAI requests to nodes.
type Handler struct {
	reg   *registry.Registry
	log   *slog.Logger
	proxy *httputil.ReverseProxy
}

// New returns a Handler. A nil log means slog.Default.
func New(reg *registry.Registry, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	h := &Handler{reg: reg, log: log.With("component", "router")}
	h.proxy = &httputil.ReverseProxy{
		// handleForward sets the target URL. FlushInterval -1 sends each
		// stream chunk at once. No total deadline cuts an active stream.
		Rewrite:       func(*httputil.ProxyRequest) {},
		FlushInterval: -1,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			ResponseHeaderTimeout: 5 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h.log.Warn("node unreachable", "host", r.URL.Host, "error", err)
			openaierr.Write(w, http.StatusBadGateway, openaierr.TypeUnavailable,
				"node at "+r.URL.Host+" is unreachable")
		},
	}
	return h
}

// Register adds the routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/models", h.handleModels)
	for _, p := range routes {
		mux.HandleFunc("POST "+p, h.handleForward)
	}
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	data := []model{}
	for _, m := range h.reg.Models() {
		data = append(data, model{ID: m.ID, Object: "model", OwnedBy: m.NodeID})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (h *Handler) handleForward(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		openaierr.Write(w, http.StatusRequestEntityTooLarge, openaierr.TypeInvalidRequest, "request body is too large")
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest,
			`request body must be JSON with a "model" field`)
		return
	}
	node, ok := h.reg.Lookup(req.Model)
	if !ok {
		openaierr.Write(w, http.StatusNotFound, openaierr.TypeNotFound,
			fmt.Sprintf("no live node serves model %q", req.Model))
		return
	}
	h.log.Info("forward", "method", r.Method, "path", r.URL.Path, "model", req.Model, "node_id", node.NodeID)

	r.URL = &url.URL{Scheme: "http", Host: node.ListenAddr, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
	r.Host = node.ListenAddr
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	h.proxy.ServeHTTP(w, r)
}
