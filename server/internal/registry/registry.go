// Package registry keeps the live nodes and finds a node for a model.
package registry

import (
	"slices"
	"sort"
	"sync"
	"time"
)

type Service struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Type              string         `json:"type"`
	Provider          string         `json:"provider"`
	Endpoint          string         `json:"endpoint"`
	APIBase           string         `json:"api_base"`
	Models            []string       `json:"models"`
	SupportsStreaming bool           `json:"supports_streaming"`
	Healthy           bool           `json:"healthy"`
	Meta              map[string]any `json:"meta,omitempty"`
}

type Node struct {
	NodeID      string    `json:"node_id"`
	Name        string    `json:"name"`
	TailscaleIP string    `json:"tailscale_ip"`
	ListenAddr  string    `json:"listen_addr"`
	Version     string    `json:"version"`
	Services    []Service `json:"services"`
	OwnerID     string    `json:"owner_id,omitempty"`
	GroupIDs    []string  `json:"groups,omitempty"`
	LastSeen    time.Time `json:"last_seen"`
}

type Model struct {
	ID     string
	NodeID string
}

type Registry struct {
	mu         sync.Mutex
	ttl        time.Duration
	now        func() time.Time
	nodes      map[string]Node
	roundRobin int
}

// New returns an empty registry where nodes expire ttl after their last heartbeat.
func New(ttl time.Duration) *Registry {
	return &Registry{ttl: ttl, now: time.Now, nodes: map[string]Node{}}
}

// Upsert stores n with a fresh LastSeen and reports whether the node is new.
func (r *Registry) Upsert(n Node) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, known := r.nodes[n.NodeID]
	n.LastSeen = r.now()
	r.nodes[n.NodeID] = n
	return !known
}

// Lookup picks an accessible node that serves model, rotating between matches.
func (r *Registry) Lookup(model, callerID string, callerGroups map[string]bool) (Node, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var matches []Node
	for _, n := range r.accessibleLocked(callerID, callerGroups) {
		if slices.Contains(n.healthyModels(), model) {
			matches = append(matches, n)
		}
	}
	if len(matches) == 0 {
		return Node{}, false
	}
	r.roundRobin++
	return matches[r.roundRobin%len(matches)], true
}

// Models lists the models the caller can use, sorted by ID.
func (r *Registry) Models(callerID string, callerGroups map[string]bool) []Model {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Model
	seen := map[string]bool{}
	for _, n := range r.accessibleLocked(callerID, callerGroups) {
		for _, m := range n.healthyModels() {
			if !seen[m] {
				seen[m] = true
				out = append(out, Model{ID: m, NodeID: n.NodeID})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Nodes lists the live nodes the caller can access, sorted by ID.
func (r *Registry) Nodes(callerID string, callerGroups map[string]bool) []Node {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.accessibleLocked(callerID, callerGroups)
}

// Sweep deletes expired nodes and returns their IDs.
func (r *Registry) Sweep() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var removed []string
	for id, n := range r.nodes {
		if r.expired(n) {
			delete(r.nodes, id)
			removed = append(removed, id)
		}
	}
	return removed
}

// Len returns the number of stored nodes, including expired ones.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.nodes)
}

// accessibleLocked returns live nodes the caller can access, sorted by ID; caller holds mu.
func (r *Registry) accessibleLocked(callerID string, callerGroups map[string]bool) []Node {
	out := make([]Node, 0, len(r.nodes))
	for _, n := range r.nodes {
		if !r.expired(n) && canAccess(n, callerID, callerGroups) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// expired reports whether n missed heartbeats for longer than ttl.
func (r *Registry) expired(n Node) bool { return r.now().Sub(n.LastSeen) > r.ttl }

// canAccess reports whether the caller owns n or shares one of its groups.
func canAccess(n Node, callerID string, callerGroups map[string]bool) bool {
	if n.OwnerID != "" && n.OwnerID == callerID {
		return true
	}
	for _, g := range n.GroupIDs {
		if callerGroups[g] {
			return true
		}
	}
	return false
}

// healthyModels returns the models of the healthy services of n.
func (n Node) healthyModels() []string {
	var models []string
	for _, s := range n.Services {
		if s.Healthy {
			models = append(models, s.Models...)
		}
	}
	return models
}
