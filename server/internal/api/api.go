// Package api serves the control plane routes that nodes call.
// It also serves users and groups for private sharing.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"peerai-serv/internal/auth"
	"peerai-serv/internal/openaierr"
	"peerai-serv/internal/registry"
)

const maxBody = 1 << 20

// Handler serves register, heartbeat, node list, users, groups, and health.
type Handler struct {
	reg   *registry.Registry
	auth  *auth.Store
	log   *slog.Logger
	start time.Time
}

// New returns a Handler. A nil log means slog.Default.
func New(reg *registry.Registry, authStore *auth.Store, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	if authStore == nil {
		authStore = auth.New()
	}
	return &Handler{reg: reg, auth: authStore, log: log.With("component", "api"), start: time.Now()}
}

// Register adds the routes to mux.
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

func (h *Handler) needUser(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	u, ok := h.auth.Authenticate(r)
	if !ok {
		openaierr.Write(w, http.StatusUnauthorized, openaierr.TypeAuth, "missing or invalid API key")
		return auth.User{}, false
	}
	return u, true
}

func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, "invalid JSON body: "+err.Error())
		return
	}
	u, err := h.auth.CreateUser(req.Name)
	if err != nil {
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, err.Error())
		return
	}
	h.log.Info("user created", "user_id", u.ID, "name", u.Name)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]string{"user_id": u.ID, "api_key": u.APIKey, "name": u.Name})
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := h.needUser(w, r)
	if !ok {
		return
	}
	groups := h.auth.ListGroups(u.ID)
	callerGroups := h.auth.GroupIDsFor(u.ID)
	nodes := h.reg.Nodes(u.ID, callerGroups)
	writeJSON(w, map[string]any{
		"user_id":          u.ID,
		"name":             u.Name,
		"groups":           groups,
		"accessible_nodes": len(nodes),
	})
}

func (h *Handler) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	u, ok := h.needUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, "invalid JSON body: "+err.Error())
		return
	}
	g, err := h.auth.CreateGroup(u, req.Name)
	if err != nil {
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, err.Error())
		return
	}
	h.log.Info("group created", "group_id", g.GroupID, "owner_id", u.ID)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, g)
}

func (h *Handler) handleListGroups(w http.ResponseWriter, r *http.Request) {
	u, ok := h.needUser(w, r)
	if !ok {
		return
	}
	writeJSON(w, h.auth.ListGroups(u.ID))
}

func (h *Handler) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	u, ok := h.needUser(w, r)
	if !ok {
		return
	}
	g, found := h.auth.GetGroup(u.ID, r.PathValue("id"))
	if !found {
		openaierr.Write(w, http.StatusNotFound, openaierr.TypeNotFound, "group not found")
		return
	}
	writeJSON(w, g)
}

func (h *Handler) handleAddMember(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.needUser(w, r)
	if !ok {
		return
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, "invalid JSON body: "+err.Error())
		return
	}
	g, err := h.auth.AddMember(caller, r.PathValue("id"), req.UserID)
	if err != nil {
		status := http.StatusBadRequest
		errType := openaierr.TypeInvalidRequest
		switch err.Error() {
		case "group not found":
			status, errType = http.StatusNotFound, openaierr.TypeNotFound
		case "only the group owner can add members":
			status, errType = http.StatusForbidden, openaierr.TypeForbidden
		}
		openaierr.Write(w, status, errType, err.Error())
		return
	}
	writeJSON(w, g)
}

func (h *Handler) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.needUser(w, r)
	if !ok {
		return
	}
	err := h.auth.RemoveMember(caller, r.PathValue("id"), r.PathValue("user_id"))
	if err != nil {
		status := http.StatusBadRequest
		errType := openaierr.TypeInvalidRequest
		switch err.Error() {
		case "group not found", "user is not a member":
			status, errType = http.StatusNotFound, openaierr.TypeNotFound
		case "only the group owner can remove other members":
			status, errType = http.StatusForbidden, openaierr.TypeForbidden
		}
		openaierr.Write(w, status, errType, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleUpsert accepts register and heartbeat. Both store the full payload,
// so a heartbeat after a central restart registers the node again.
// The Bearer key sets the owner. The payload groups must all exist
// and hold the caller as a member.
func (h *Handler) handleUpsert(w http.ResponseWriter, r *http.Request) {
	u, ok := h.needUser(w, r)
	if !ok {
		return
	}
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
	for _, g := range n.GroupIDs {
		if !h.auth.IsMember(g, u.ID) {
			openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest, "caller is not a member of group "+g)
			return
		}
	}
	n.OwnerID = u.ID
	if h.reg.Upsert(n) {
		h.log.Info("node joined", "node_id", n.NodeID, "owner_id", u.ID, "listen_addr", n.ListenAddr)
	} else {
		h.log.Debug("node heartbeat", "node_id", n.NodeID, "owner_id", u.ID)
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
	u, ok := h.needUser(w, r)
	if !ok {
		return
	}
	writeJSON(w, h.reg.Nodes(u.ID, h.auth.GroupIDsFor(u.ID)))
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
