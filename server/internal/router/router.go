// Package router serves the central data plane.
//
// A caller sends an OpenAI-compatible request to central. The router reads
// the "model" field, finds a fresh node that serves the model, and forwards
// the unchanged request to that node. The path to the node is the same as
// the incoming path. The node then forwards the request to its backend.
package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"peerai-serv/internal/openaierr"
	"peerai-serv/internal/registry"
)

// MaxBodyBytes is the largest request body that central forwards.
const MaxBodyBytes = 16 << 20

// The transport limits connect and response-header time. There is no total
// deadline, so central does not stop an active SSE stream.
const (
	dialTimeout           = 5 * time.Second
	responseHeaderTimeout = 60 * time.Second
	idleConnTimeout       = 90 * time.Second
)

// DialFunc opens a connection to a node. Production uses tsnet.Server.Dial.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Handler serves the data plane routes.
type Handler struct {
	reg   *registry.Registry
	log   *slog.Logger
	proxy *httputil.ReverseProxy
}

type targetKey struct{}

// New returns a Handler. A nil dial means a plain net.Dialer.
// A nil log means slog.Default.
func New(reg *registry.Registry, dial DialFunc, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	if dial == nil {
		dial = (&net.Dialer{Timeout: dialTimeout}).DialContext
	}
	h := &Handler{reg: reg, log: log.With("component", "router")}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, dialTimeout)
			defer cancel()
			return dial(ctx, network, addr)
		},
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       idleConnTimeout,
		MaxIdleConnsPerHost:   8,
	}
	h.proxy = &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1, // send each SSE chunk at once
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = pr.In.Context().Value(targetKey{}).(string)
			pr.Out.Host = pr.Out.URL.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			addr, _ := r.Context().Value(targetKey{}).(string)
			h.log.Warn("node unreachable", "listen_addr", addr, "path", r.URL.Path, "err", err)
			openaierr.Write(w, http.StatusBadGateway, openaierr.TypeBadGateway,
				fmt.Sprintf("node at %s is unreachable: %v", addr, err))
		},
	}
	return h
}

// Register adds the data plane routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", h.handleForward)
	mux.HandleFunc("POST /v1/completions", h.handleForward)
	mux.HandleFunc("GET /v1/models", h.handleModels)
}

func (h *Handler) handleForward(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			openaierr.Write(w, http.StatusRequestEntityTooLarge, openaierr.TypeTooLarge,
				fmt.Sprintf("request body is larger than %d bytes", MaxBodyBytes))
			return
		}
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, "cannot read request body: "+err.Error())
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Model == "" {
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, `missing "model" in request body`)
		return
	}

	nodes := h.reg.Lookup(req.Model)
	if len(nodes) == 0 {
		openaierr.Write(w, http.StatusNotFound, openaierr.TypeModelNotFound,
			fmt.Sprintf("model %q is not available on any node", req.Model))
		return
	}
	n := nodes[0]

	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r = r.WithContext(context.WithValue(r.Context(), targetKey{}, n.ListenAddr))

	h.log.Info("forward", "method", r.Method, "path", r.URL.Path, "model", req.Model, "node_id", n.NodeID)
	h.proxy.ServeHTTP(w, r)
	h.log.Debug("forward done", "path", r.URL.Path, "node_id", n.NodeID, "duration", time.Since(start))
}

type modelList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	out := modelList{Object: "list", Data: []modelObject{}}
	for _, m := range h.reg.Models() {
		out.Data = append(out.Data, modelObject{ID: m.ID, Object: "model", OwnedBy: m.NodeID})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
