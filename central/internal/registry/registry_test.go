package registry

import (
	"testing"
	"time"
)

func node(id, model string, healthy bool) Node {
	return Node{NodeID: id, Services: []Service{{ID: "s", Models: []string{model}, Healthy: healthy}}}
}

func TestLookupSkipsUnhealthyAndStale(t *testing.T) {
	now := time.Unix(0, 0)
	r := New(time.Minute)
	r.clock = func() time.Time { return now }
	r.Upsert(node("a", "qwen", false))
	if _, ok := r.Lookup("qwen"); ok {
		t.Fatal("unhealthy service must not match")
	}
	r.Upsert(node("b", "qwen", true))
	if n, ok := r.Lookup("qwen"); !ok || n.NodeID != "b" {
		t.Fatalf("got %q %v, want b", n.NodeID, ok)
	}
	now = now.Add(2 * time.Minute)
	if _, ok := r.Lookup("qwen"); ok {
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
	first, _ := r.Lookup("qwen")
	second, _ := r.Lookup("qwen")
	if first.NodeID == second.NodeID {
		t.Fatal("lookup must rotate between nodes")
	}
	if m := r.Models(); len(m) != 1 || m[0].ID != "qwen" {
		t.Fatalf("models = %v", m)
	}
}

func TestLookupPrefersLowestLoad(t *testing.T) {
	r := New(time.Minute)
	r.Upsert(node("a", "qwen", true))
	r.Upsert(node("b", "qwen", true))
	r.Upsert(node("c", "qwen", true))
	load := map[string]float64{"a": 500, "b": 0, "c": 0}
	r.SetLoad(func(n Node) float64 { return load[n.NodeID] })
	seen := map[string]bool{}
	for range 4 {
		n, _ := r.Lookup("qwen")
		seen[n.NodeID] = true
	}
	if seen["a"] || !seen["b"] || !seen["c"] {
		t.Fatalf("lookup used %v, want only b and c in rotation", seen)
	}
	load["b"], load["c"] = 900, 900
	if n, _ := r.Lookup("qwen"); n.NodeID != "a" {
		t.Fatalf("got %q, want a with the lowest load", n.NodeID)
	}
}
