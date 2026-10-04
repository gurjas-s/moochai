// Package api serves the control-plane routes: node register/heartbeat and node list.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"mooch-serv/internal/feed"
	"mooch-serv/internal/registry"
	"mooch-serv/internal/respond"
)

const maxControlBody = 1 << 20

type Handler struct {
	reg   *registry.Registry
	log   *slog.Logger
	feed  *feed.Feed
	start time.Time
}

// New returns the control-plane handler. A nil feed prints no events.
func New(reg *registry.Registry, f *feed.Feed) *Handler {
	return &Handler{reg: reg, log: slog.With("component", "api"), feed: f, start: time.Now()}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/nodes/register", h.handleUpsert)
	mux.HandleFunc("POST /api/nodes/heartbeat", h.handleUpsert)
	mux.HandleFunc("GET /api/nodes", h.handleNodes)
	mux.HandleFunc("GET /healthz", h.handleHealthz)
}

// Register and heartbeat store the same payload, so a heartbeat after a central restart registers again.
func (h *Handler) handleUpsert(w http.ResponseWriter, r *http.Request) {
	var n registry.Node
	if !decodeJSON(w, r, &n) {
		return
	}
	if err := validateNode(n, r.RemoteAddr); err != nil {
		h.log.Warn("node rejected", "node_id", n.NodeID, "remote", r.RemoteAddr, "error", err)
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.reg.Upsert(n) {
		h.log.Info("node joined", "node_id", n.NodeID, "listen_addr", n.ListenAddr)
		h.feed.Joined(n)
	} else {
		h.log.Debug("node heartbeat", "node_id", n.NodeID)
	}
	respond.JSON(w, map[string]string{"status": "ok"})
}

// The node must call from its own Tailscale IP or loopback, so it cannot send traffic to another host.
func validateNode(n registry.Node, remoteAddr string) error {
	if n.NodeID == "" {
		return errors.New("node_id is required")
	}
	host, _, err := net.SplitHostPort(n.ListenAddr)
	if err != nil {
		return errors.New("listen_addr must be host:port")
	}
	if host != n.TailscaleIP {
		return errors.New("listen_addr host must equal tailscale_ip")
	}
	remoteHost, _, _ := net.SplitHostPort(remoteAddr)
	if ip := net.ParseIP(remoteHost); remoteHost != n.TailscaleIP && (ip == nil || !ip.IsLoopback()) {
		return errors.New("request must come from tailscale_ip")
	}
	return nil
}

func (h *Handler) handleNodes(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, h.reg.Nodes())
}

func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, map[string]any{
		"status": "ok",
		"nodes":  h.reg.Len(),
		"uptime": time.Since(h.start).Round(time.Second).String(),
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlBody)).Decode(v); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
