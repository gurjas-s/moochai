package registry

import (
	"testing"
	"time"
)

func node(id, model string, healthy bool) Node {
	return Node{NodeID: id, OwnerID: "u_owner", Services: []Service{{ID: "s", Models: []string{model}, Healthy: healthy}}}
}

func TestLookupSkipsUnhealthyAndStale(t *testing.T) {
	now := time.Unix(0, 0)
	r := New(time.Minute)
	r.now = func() time.Time { return now }
	r.Upsert(node("a", "qwen", false))
	if _, ok := r.Lookup("qwen", "u_owner", map[string]bool{}); ok {
		t.Fatal("unhealthy service must not match")
	}
	r.Upsert(node("b", "qwen", true))
	if n, ok := r.Lookup("qwen", "u_owner", map[string]bool{}); !ok || n.NodeID != "b" {
		t.Fatalf("got %q %v, want b", n.NodeID, ok)
	}
	now = now.Add(2 * time.Minute)
	if _, ok := r.Lookup("qwen", "u_owner", map[string]bool{}); ok {
		t.Fatal("stale node must not match")
	}
	if gone := r.Sweep(); len(gone) != 2 || r.Len() != 0 {
		t.Fatalf("sweep removed %v, len %d", gone, r.Len())
	}
}

func TestLookupRotatesAndModelsDedupe(t *testing.T) {
	r := New(time.Minute)
	r.Upsert(node("a", "qwen", true))
	r.Upsert(node("b", "qwen", true))
	first, _ := r.Lookup("qwen", "u_owner", map[string]bool{})
	second, _ := r.Lookup("qwen", "u_owner", map[string]bool{})
	if first.NodeID == second.NodeID {
		t.Fatal("lookup must rotate between nodes")
	}
	if m := r.Models("u_owner", map[string]bool{}); len(m) != 1 || m[0].ID != "qwen" {
		t.Fatalf("models = %v", m)
	}
}

func TestPrivateAccess(t *testing.T) {
	r := New(time.Minute)
	r.Upsert(Node{
		NodeID:   "n1",
		OwnerID:  "u_alice",
		GroupIDs: []string{"g_lab"},
		Services: []Service{{ID: "s", Models: []string{"qwen"}, Healthy: true}},
	})
	// Owner sees the node.
	if _, ok := r.Lookup("qwen", "u_alice", map[string]bool{}); !ok {
		t.Fatal("owner must see own node")
	}
	// Group member sees the node.
	if _, ok := r.Lookup("qwen", "u_bob", map[string]bool{"g_lab": true}); !ok {
		t.Fatal("group member must see shared node")
	}
	// Outsider sees nothing.
	if _, ok := r.Lookup("qwen", "u_eve", map[string]bool{}); ok {
		t.Fatal("outsider must not see private node")
	}
	if m := r.Models("u_eve", map[string]bool{}); len(m) != 0 {
		t.Fatalf("outsider models = %v, want none", m)
	}
	if n := r.Nodes("u_bob", map[string]bool{"g_lab": true}); len(n) != 1 {
		t.Fatalf("member nodes = %v, want 1", n)
	}
	if n := r.Nodes("u_eve", map[string]bool{}); len(n) != 0 {
		t.Fatalf("outsider nodes = %v, want none", n)
	}
}
