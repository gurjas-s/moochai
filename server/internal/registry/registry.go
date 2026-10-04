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
	nodesLock   sync.Mutex
	nodeTTL     time.Duration
	clock       func() time.Time
	nodes       map[string]Node
	lookupCount int
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

// Lookup picks an accessible node that serves model, rotating between matches.
func (reg *Registry) Lookup(model, callerID string, callerGroups map[string]bool) (Node, bool) {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	var matches []Node
	for _, node := range reg.accessibleLocked(callerID, callerGroups) {
		if slices.Contains(node.healthyModels(), model) {
			matches = append(matches, node)
		}
	}
	if len(matches) == 0 {
		return Node{}, false
	}
	reg.lookupCount++
	return matches[reg.lookupCount%len(matches)], true
}

// Models lists the models the caller can use, sorted by ID.
func (reg *Registry) Models(callerID string, callerGroups map[string]bool) []Model {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	var result []Model
	seen := map[string]bool{}
	for _, node := range reg.accessibleLocked(callerID, callerGroups) {
		for _, modelID := range node.healthyModels() {
			if !seen[modelID] {
				seen[modelID] = true
				result = append(result, Model{ID: modelID, NodeID: node.NodeID})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Nodes lists the live nodes the caller can access, sorted by ID.
func (reg *Registry) Nodes(callerID string, callerGroups map[string]bool) []Node {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	return reg.accessibleLocked(callerID, callerGroups)
}

// Sweep deletes expired nodes and returns their IDs.
func (reg *Registry) Sweep() []string {
	reg.nodesLock.Lock()
	defer reg.nodesLock.Unlock()
	var removed []string
	for id, node := range reg.nodes {
		if reg.expired(node) {
			delete(reg.nodes, id)
			removed = append(removed, id)
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

// accessibleLocked returns live nodes the caller can access, sorted by ID; caller holds nodesLock.
func (reg *Registry) accessibleLocked(callerID string, callerGroups map[string]bool) []Node {
	result := make([]Node, 0, len(reg.nodes))
	for _, node := range reg.nodes {
		if !reg.expired(node) && canAccess(node, callerID, callerGroups) {
			result = append(result, node)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].NodeID < result[j].NodeID })
	return result
}

// expired reports whether node missed heartbeats for longer than nodeTTL.
func (reg *Registry) expired(node Node) bool { return reg.clock().Sub(node.LastSeen) > reg.nodeTTL }

// canAccess reports whether the caller owns node or shares one of its groups.
func canAccess(node Node, callerID string, callerGroups map[string]bool) bool {
	if node.OwnerID != "" && node.OwnerID == callerID {
		return true
	}
	for _, groupID := range node.GroupIDs {
		if callerGroups[groupID] {
			return true
		}
	}
	return false
}

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
