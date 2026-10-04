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

	"peerai-serv/internal/auth"
	"peerai-serv/internal/registry"
	"peerai-serv/internal/respond"
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
	auth  *auth.Store
	log   *slog.Logger
	proxy *httputil.ReverseProxy
}

func New(reg *registry.Registry, authStore *auth.Store) *Handler {
	h := &Handler{reg: reg, auth: authStore, log: slog.With("component", "router")}
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
			h.log.Warn("node unreachable", "host", r.URL.Host, "error", err)
			respond.Error(w, http.StatusBadGateway, "node at "+r.URL.Host+" is unreachable")
		},
	}
	return h
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/models", h.handleModels)
	for _, p := range forwardPaths {
		mux.HandleFunc("POST "+p, h.handleForward)
	}
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	u, ok := h.auth.RequireUser(w, r)
	if !ok {
		return
	}
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	data := []model{}
	for _, m := range h.reg.Models(u.ID, h.auth.GroupIDsFor(u.ID)) {
		data = append(data, model{ID: m.ID, Object: "model", OwnedBy: m.NodeID})
	}
	respond.JSON(w, map[string]any{"object": "list", "data": data})
}

func (h *Handler) handleForward(w http.ResponseWriter, r *http.Request) {
	u, ok := h.auth.RequireUser(w, r)
	if !ok {
		return
	}
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
	node, found := h.reg.Lookup(req.Model, u.ID, h.auth.GroupIDsFor(u.ID))
	if !found {
		respond.Error(w, http.StatusNotFound, fmt.Sprintf("no live node serves model %q", req.Model))
		return
	}
	h.log.Info("forward", "method", r.Method, "path", r.URL.Path, "model", req.Model, "node_id", node.NodeID, "user_id", u.ID)

	r.URL = &url.URL{Scheme: "http", Host: node.ListenAddr, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
	r.Host = node.ListenAddr
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	h.proxy.ServeHTTP(w, r)
}
