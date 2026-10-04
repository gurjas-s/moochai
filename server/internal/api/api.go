// Package api serves the control plane routes that nodes call.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"peerai-serv/internal/openaierr"
	"peerai-serv/internal/registry"
)

const maxBody = 1 << 20

// Handler serves register, heartbeat, node list, and health routes.
type Handler struct {
	reg   *registry.Registry
	log   *slog.Logger
	start time.Time
}

// New returns a Handler. A nil log means slog.Default.
func New(reg *registry.Registry, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{reg: reg, log: log.With("component", "api"), start: time.Now()}
}

// Register adds the routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/nodes/register", h.handleUpsert)
	mux.HandleFunc("POST /api/nodes/heartbeat", h.handleUpsert)
	mux.HandleFunc("GET /api/nodes", h.handleNodes)
	mux.HandleFunc("GET /healthz", h.handleHealthz)
}

// handleUpsert accepts register and heartbeat. Both store the full payload,
// so a heartbeat after a central restart registers the node again.
func (h *Handler) handleUpsert(w http.ResponseWriter, r *http.Request) {
	var n registry.Node
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&n); err != nil {
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := validate(n, r.RemoteAddr); err != nil {
		h.log.Warn("node rejected", "node_id", n.NodeID, "remote", r.RemoteAddr, "error", err)
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, err.Error())
		return
	}
	if h.reg.Upsert(n) {
		h.log.Info("node joined", "node_id", n.NodeID, "name", n.Name, "listen_addr", n.ListenAddr)
	} else {
		h.log.Debug("node heartbeat", "node_id", n.NodeID)
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// validate checks the payload. The node must send from its own Tailscale IP
// (or from loopback), so a node cannot route traffic to another host.
func validate(n registry.Node, remote string) error {
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
	rhost, _, _ := net.SplitHostPort(remote)
	if ip := net.ParseIP(rhost); rhost != n.TailscaleIP && (ip == nil || !ip.IsLoopback()) {
		return errors.New("request must come from tailscale_ip")
	}
	return nil
}

func (h *Handler) handleNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.reg.Nodes())
}

func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ok",
		"nodes":  h.reg.Len(),
		"uptime": time.Since(h.start).Round(time.Second).String(),
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
