// Package api serves the central control plane.
//
// Nodes send register and heartbeat calls with the payload from
// REQUIREMENTS.md section 4. Central stores the payload in the registry.
// Both calls have the same behavior, so a node that sends a heartbeat
// after a central restart is added again.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"peerai-serv/internal/openaierr"
	"peerai-serv/internal/registry"
)

// MaxBodyBytes is the largest register or heartbeat body that central accepts.
const MaxBodyBytes = 1 << 20

// Handler serves the control plane routes.
type Handler struct {
	reg     *registry.Registry
	version string
	log     *slog.Logger
}

// New returns a Handler. A nil log means slog.Default.
func New(reg *registry.Registry, version string, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{reg: reg, version: version, log: log.With("component", "api")}
}

// Register adds the control plane routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/nodes/register", h.handleUpsert("register"))
	mux.HandleFunc("POST /api/nodes/heartbeat", h.handleUpsert("heartbeat"))
	mux.HandleFunc("GET /healthz", h.handleHealthz)
}

func (h *Handler) handleUpsert(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var n registry.Node
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes)).Decode(&n); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				openaierr.Write(w, http.StatusRequestEntityTooLarge, openaierr.TypeTooLarge,
					fmt.Sprintf("request body is larger than %d bytes", MaxBodyBytes))
				return
			}
			openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, "invalid JSON body: "+err.Error())
			return
		}
		if err := validate(n); err != nil {
			openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, err.Error())
			return
		}
		h.reg.Upsert(n)
		h.log.Debug("node "+kind, "node_id", n.NodeID, "listen_addr", n.ListenAddr, "services", len(n.Services))
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func validate(n registry.Node) error {
	if n.NodeID == "" {
		return errors.New(`missing "node_id"`)
	}
	if n.ListenAddr == "" {
		return errors.New(`missing "listen_addr"`)
	}
	for i, s := range n.Services {
		if s.ID == "" {
			return fmt.Errorf(`services[%d]: missing "id"`, i)
		}
		if len(s.Models) == 0 {
			return fmt.Errorf(`services[%d] (%q): "models" is empty`, i, s.ID)
		}
	}
	return nil
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Nodes   int    `json:"nodes"`
}

func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: h.version, Nodes: h.reg.Len()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
