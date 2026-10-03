// Package registry keeps the in-memory table of nodes that central knows.
//
// Nodes add themselves with register and heartbeat calls. A node that
// does not send a call within the TTL is stale. Central does not route
// requests to stale nodes or to unhealthy services.
package registry

import (
	"sort"
	"sync"
	"time"
)

// DefaultTTL is three times the default node heartbeat interval (15s).
const DefaultTTL = 45 * time.Second

// Service is one advertised capability entry from a node.
// The JSON shape follows REQUIREMENTS.md section 3.
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

// Node is the register and heartbeat payload from a node.
// The JSON shape follows REQUIREMENTS.md section 4.
type Node struct {
	NodeID      string    `json:"node_id"`
	Name        string    `json:"name"`
	TailscaleIP string    `json:"tailscale_ip"`
	ListenAddr  string    `json:"listen_addr"`
	Version     string    `json:"version"`
	Services    []Service `json:"services"`
}

// Model is one model that a fresh node serves through a healthy service.
type Model struct {
	ID     string
	NodeID string
}

type entry struct {
	node     Node
	lastSeen time.Time
}

// Registry is safe for concurrent use.
type Registry struct {
	ttl time.Duration
	now func() time.Time

	mu    sync.RWMutex
	nodes map[string]entry
}

// New returns an empty Registry. A ttl of zero means DefaultTTL.
// A nil now means time.Now.
func New(ttl time.Duration, now func() time.Time) *Registry {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Registry{ttl: ttl, now: now, nodes: make(map[string]entry)}
}

// Upsert adds the node or replaces the node with the same NodeID.
func (r *Registry) Upsert(n Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[n.NodeID] = entry{node: n, lastSeen: r.now()}
}

// Lookup returns the fresh nodes that serve model through a healthy
// service. The result is sorted by NodeID so that the order is stable.
func (r *Registry) Lookup(model string) []Node {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.now()
	var out []Node
	for _, e := range r.nodes {
		if r.stale(e, now) {
			continue
		}
		if serves(e.node, model) {
			out = append(out, e.node)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// Models returns each model that a fresh node serves through a healthy
// service, sorted by model ID and then by NodeID.
func (r *Registry) Models() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.now()
	var out []Model
	for _, e := range r.nodes {
		if r.stale(e, now) {
			continue
		}
		seen := make(map[string]bool)
		for _, s := range e.node.Services {
			if !s.Healthy {
				continue
			}
			for _, m := range s.Models {
				if !seen[m] {
					seen[m] = true
					out = append(out, Model{ID: m, NodeID: e.node.NodeID})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

// Len returns the number of fresh nodes.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.now()
	n := 0
	for _, e := range r.nodes {
		if !r.stale(e, now) {
			n++
		}
	}
	return n
}

// Sweep removes stale nodes from memory.
func (r *Registry) Sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for id, e := range r.nodes {
		if r.stale(e, now) {
			delete(r.nodes, id)
		}
	}
}

func (r *Registry) stale(e entry, now time.Time) bool {
	return now.Sub(e.lastSeen) > r.ttl
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
