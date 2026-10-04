// Package api serves the control-plane routes: users, groups, and node register/heartbeat.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"peerai-serv/internal/auth"
	"peerai-serv/internal/registry"
	"peerai-serv/internal/respond"
)

const maxControlBody = 1 << 20

type Handler struct {
	reg   *registry.Registry
	auth  *auth.Store
	log   *slog.Logger
	start time.Time
}

func New(reg *registry.Registry, authStore *auth.Store) *Handler {
	return &Handler{reg: reg, auth: authStore, log: slog.With("component", "api"), start: time.Now()}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/users", h.handleCreateUser)
	mux.HandleFunc("GET /api/me", h.handleMe)
	mux.HandleFunc("POST /api/groups", h.handleCreateGroup)
	mux.HandleFunc("GET /api/groups", h.handleListGroups)
	mux.HandleFunc("GET /api/groups/{id}", h.handleGetGroup)
	mux.HandleFunc("POST /api/groups/{id}/members", h.handleAddMember)
	mux.HandleFunc("DELETE /api/groups/{id}/members/{user_id}", h.handleRemoveMember)
	mux.HandleFunc("POST /api/nodes/register", h.handleUpsert)
	mux.HandleFunc("POST /api/nodes/heartbeat", h.handleUpsert)
	mux.HandleFunc("GET /api/nodes", h.handleNodes)
	mux.HandleFunc("GET /healthz", h.handleHealthz)
}

func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := h.auth.CreateUser(req.Name)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	h.log.Info("user created", "user_id", u.ID, "name", u.Name)
	w.WriteHeader(http.StatusCreated)
	respond.JSON(w, map[string]string{"user_id": u.ID, "api_key": u.APIKey, "name": u.Name})
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := h.auth.RequireUser(w, r)
	if !ok {
		return
	}
	nodes := h.reg.Nodes(u.ID, h.auth.GroupIDsFor(u.ID))
	respond.JSON(w, map[string]any{
		"user_id":          u.ID,
		"name":             u.Name,
		"groups":           h.auth.ListGroups(u.ID),
		"accessible_nodes": len(nodes),
	})
}

func (h *Handler) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	u, ok := h.auth.RequireUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	g, err := h.auth.CreateGroup(u, req.Name)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	h.log.Info("group created", "group_id", g.GroupID, "owner_id", u.ID)
	w.WriteHeader(http.StatusCreated)
	respond.JSON(w, g)
}

func (h *Handler) handleListGroups(w http.ResponseWriter, r *http.Request) {
	u, ok := h.auth.RequireUser(w, r)
	if !ok {
		return
	}
	respond.JSON(w, h.auth.ListGroups(u.ID))
}

func (h *Handler) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	u, ok := h.auth.RequireUser(w, r)
	if !ok {
		return
	}
	g, found := h.auth.GetGroup(u.ID, r.PathValue("id"))
	if !found {
		respond.Error(w, http.StatusNotFound, "group not found")
		return
	}
	respond.JSON(w, g)
}

func (h *Handler) handleAddMember(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.auth.RequireUser(w, r)
	if !ok {
		return
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	g, err := h.auth.AddMember(caller, r.PathValue("id"), req.UserID)
	if err != nil {
		writeGroupError(w, err)
		return
	}
	respond.JSON(w, g)
}

func (h *Handler) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.auth.RequireUser(w, r)
	if !ok {
		return
	}
	if err := h.auth.RemoveMember(caller, r.PathValue("id"), r.PathValue("user_id")); err != nil {
		writeGroupError(w, err)
		return
	}
	respond.JSON(w, map[string]string{"status": "ok"})
}

// Register and heartbeat store the same payload, so a heartbeat after a central restart registers again.
func (h *Handler) handleUpsert(w http.ResponseWriter, r *http.Request) {
	u, ok := h.auth.RequireUser(w, r)
	if !ok {
		return
	}
	var n registry.Node
	if !decodeJSON(w, r, &n) {
		return
	}
	if err := validateNode(n, r.RemoteAddr); err != nil {
		h.log.Warn("node rejected", "node_id", n.NodeID, "remote", r.RemoteAddr, "error", err)
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, g := range n.GroupIDs {
		if !h.auth.IsMember(g, u.ID) {
			respond.Error(w, http.StatusBadRequest, "caller is not a member of group "+g)
			return
		}
	}
	n.OwnerID = u.ID
	if h.reg.Upsert(n) {
		h.log.Info("node joined", "node_id", n.NodeID, "owner_id", u.ID, "listen_addr", n.ListenAddr)
	} else {
		h.log.Debug("node heartbeat", "node_id", n.NodeID, "owner_id", u.ID)
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
	u, ok := h.auth.RequireUser(w, r)
	if !ok {
		return
	}
	respond.JSON(w, h.reg.Nodes(u.ID, h.auth.GroupIDsFor(u.ID)))
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

func writeGroupError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, auth.ErrGroupNotFound), errors.Is(err, auth.ErrNotMember):
		status = http.StatusNotFound
	case errors.Is(err, auth.ErrOwnerOnlyAdd), errors.Is(err, auth.ErrOwnerOnlyRemove):
		status = http.StatusForbidden
	}
	respond.Error(w, status, err.Error())
}
