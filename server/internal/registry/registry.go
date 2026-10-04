// Package registry keeps the nodes that send register and heartbeat to central.
package registry

import (
	"sort"
	"sync"
	"time"
)

// Service is one advertised capability entry of a node.
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

// Node is the register and heartbeat payload, plus the last time central saw it.
// OwnerID comes from the Bearer key. GroupIDs share the node in private mode.
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

// Model is one model that a live node serves.
type Model struct {
	ID     string
	NodeID string
}

// Registry is safe for concurrent use.
type Registry struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	nodes map[string]Node
	next  int // round-robin counter for Lookup
}

// New returns a Registry. A node expires ttl after its last heartbeat.
func New(ttl time.Duration) *Registry {
	return &Registry{ttl: ttl, now: time.Now, nodes: map[string]Node{}}
}

// Upsert adds or replaces n and sets LastSeen.
// It returns true when the node is new.
func (r *Registry) Upsert(n Node) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.nodes[n.NodeID]
	n.LastSeen = r.now()
	r.nodes[n.NodeID] = n
	return !ok
}

// Lookup returns a live node with a healthy service for model
// that the caller can access. Calls rotate between matching nodes.
func (r *Registry) Lookup(model, callerID string, callerGroups map[string]bool) (Node, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var match []Node
	for _, n := range r.sorted() {
		if serves(n, model) && canSee(n, callerID, callerGroups) {
			match = append(match, n)
		}
	}
	if len(match) == 0 {
		return Node{}, false
	}
	r.next++
	return match[r.next%len(match)], true
}

// Models returns each accessible model that a live node serves, sorted by ID.
func (r *Registry) Models(callerID string, callerGroups map[string]bool) []Model {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Model
	seen := map[string]bool{}
	for _, n := range r.sorted() {
		if !canSee(n, callerID, callerGroups) {
			continue
		}
		for _, s := range n.Services {
			for _, m := range s.Models {
				if s.Healthy && !seen[m] {
					seen[m] = true
					out = append(out, Model{ID: m, NodeID: n.NodeID})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Nodes returns the live nodes the caller can access, sorted by ID.
func (r *Registry) Nodes(callerID string, callerGroups map[string]bool) []Node {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Node, 0)
	for _, n := range r.sorted() {
		if canSee(n, callerID, callerGroups) {
			out = append(out, n)
		}
	}
	return out
}

// Sweep removes expired nodes and returns their IDs.
func (r *Registry) Sweep() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var gone []string
	for id, n := range r.nodes {
		if r.stale(n) {
			delete(r.nodes, id)
			gone = append(gone, id)
		}
	}
	return gone
}

// Len returns the number of stored nodes.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.nodes)
}

// sorted returns live nodes in a stable order. The caller holds mu.
func (r *Registry) sorted() []Node {
	out := make([]Node, 0, len(r.nodes))
	for _, n := range r.nodes {
		if !r.stale(n) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func (r *Registry) stale(n Node) bool { return r.now().Sub(n.LastSeen) > r.ttl }

// canSee reports private access. The owner sees the node.
// A group member sees the node when the node shares that group.
// The caller holds mu.
func canSee(n Node, callerID string, callerGroups map[string]bool) bool {
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

func serves(n Node, model string) bool {
	for _, s := range n.Services {
		if !s.Healthy {
			continue
		}
		for _, m := range s.Models {
			if m == model {
				return true
			}
		}
	}
	return false
}
