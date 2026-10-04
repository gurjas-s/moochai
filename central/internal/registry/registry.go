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
	LastSeen    time.Time `json:"last_seen"`
}

type Model struct {
	ID          string
	NodeID      string
	MaxModelLen int
}

type Registry struct {
	nodesLock   sync.Mutex
	nodeTTL     time.Duration
	clock       func() time.Time
	nodes       map[string]Node
	lookupCount int
	load        func(Node) float64
}

// New returns an empty registry where nodes expire nodeTTL after their last heartbeat.
func New(nodeTTL time.Duration) *Registry {
	return &Registry{nodeTTL: nodeTTL, clock: time.Now, nodes: map[string]Node{}}
}

// Upsert stores node with a fresh LastSeen and reports whether the node is new.
func (reg *Registry) Upsert(node Node) bool {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	_, known := reg.nodes[node.NodeID]
	node.LastSeen = reg.clock()
	reg.nodes[node.NodeID] = node
	return !known
}

// SetLoad makes Lookup prefer the match with the lowest load. load must be fast, because Lookup calls it under the lock.
func (reg *Registry) SetLoad(load func(Node) float64) {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	reg.load = load
}

// Lookup picks a live node that serves model. With SetLoad, it keeps only the matches with the lowest load.
// It rotates between the remaining matches.
func (reg *Registry) Lookup(model string) (Node, bool) {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	matches := reg.servingLocked(model)
	if len(matches) == 0 {
		return Node{}, false
	}
	if reg.load != nil && len(matches) > 1 {
		matches = reg.leastLoaded(matches)
	}
	reg.lookupCount++
	return matches[reg.lookupCount%len(matches)], true
}

// NodeByIP returns the live node with the Tailscale IP ip.
func (reg *Registry) NodeByIP(ip string) (Node, bool) {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	for _, node := range reg.nodes {
		if node.TailscaleIP == ip && !reg.expired(node) {
			return node, true
		}
	}
	return Node{}, false
}

// Models lists the models of the live nodes, sorted by ID.
func (reg *Registry) Models() []Model {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	var result []Model
	seen := map[string]bool{}
	for _, node := range reg.liveLocked() {
		for _, modelID := range node.healthyModels() {
			if !seen[modelID] {
				seen[modelID] = true
				result = append(result, Model{ID: modelID, NodeID: node.NodeID, MaxModelLen: node.modelContextWindow(modelID)})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Nodes lists the live nodes, sorted by ID.
func (reg *Registry) Nodes() []Node {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	return reg.liveLocked()
}

// Sweep deletes expired nodes and returns them.
func (reg *Registry) Sweep() []Node {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	var removed []Node
	for id, node := range reg.nodes {
		if reg.expired(node) {
			delete(reg.nodes, id)
			removed = append(removed, node)
		}
	}
	return removed
}

// Len returns the number of stored nodes, including expired ones.
func (reg *Registry) Len() int {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	return len(reg.nodes)
}

// liveLocked returns the live nodes, sorted by ID; caller holds nodesLock.
func (reg *Registry) liveLocked() []Node {
	result := make([]Node, 0, len(reg.nodes))
	for _, node := range reg.nodes {
		if !reg.expired(node) {
			result = append(result, node)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].NodeID < result[j].NodeID })
	return result
}

// servingLocked returns live nodes that serve model; caller holds nodesLock.
func (reg *Registry) servingLocked(model string) []Node {
	var matches []Node
	for _, node := range reg.liveLocked() {
		if slices.Contains(node.healthyModels(), model) {
			matches = append(matches, node)
		}
	}
	return matches
}

// leastLoaded returns the matches that have the lowest load.
func (reg *Registry) leastLoaded(matches []Node) []Node {
	var best []Node
	low := 0.0
	for _, node := range matches {
		switch load := reg.load(node); {
		case best == nil || load < low:
			best, low = []Node{node}, load
		case load == low:
			best = append(best, node)
		}
	}
	return best
}

// expired reports whether node missed heartbeats for longer than nodeTTL.
func (reg *Registry) expired(node Node) bool { return reg.clock().Sub(node.LastSeen) > reg.nodeTTL }

// healthyModels returns the models of the healthy services of node.
func (node Node) healthyModels() []string {
	var models []string
	for _, service := range node.Services {
		if service.Healthy {
			models = append(models, service.Models...)
		}
	}
	return models
}

// modelContextWindow returns the context window of model on node, or zero.
func (node Node) modelContextWindow(modelID string) int {
	for _, service := range node.Services {
		if !service.Healthy {
			continue
		}
		for _, id := range service.Models {
			if id == modelID {
				return contextWindow(service.Meta)
			}
		}
	}
	return 0
}

// contextWindow reads context_window from meta, or returns zero.
func contextWindow(meta map[string]any) int {
	if meta == nil {
		return 0
	}
	switch v := meta["context_window"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	default:
		return 0
	}
}
