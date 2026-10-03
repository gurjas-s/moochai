package registry

import (
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time      { return c.t }
func (c *fakeClock) Add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *fakeClock               { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }
func node(id string, svcs ...Service) Node {
	return Node{NodeID: id, ListenAddr: id + ":9100", Services: svcs}
}
func svc(healthy bool, models ...string) Service {
	return Service{ID: "svc", Models: models, Healthy: healthy}
}

func TestUpsertReplacesNode(t *testing.T) {
	r := New(0, newClock().Now)
	r.Upsert(node("a", svc(true, "m1")))
	r.Upsert(node("a", svc(true, "m2")))

	if got := r.Lookup("m1"); len(got) != 0 {
		t.Fatalf("Lookup(m1) = %v, want none after replace", got)
	}
	if got := r.Lookup("m2"); len(got) != 1 || got[0].NodeID != "a" {
		t.Fatalf("Lookup(m2) = %v, want node a", got)
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1", r.Len())
	}
}

func TestLookupSkipsUnhealthyService(t *testing.T) {
	r := New(0, newClock().Now)
	r.Upsert(node("a", svc(false, "m")))
	r.Upsert(node("b", svc(true, "m")))

	got := r.Lookup("m")
	if len(got) != 1 || got[0].NodeID != "b" {
		t.Fatalf("Lookup = %v, want only node b", got)
	}
}

func TestStaleNodeIsSkippedAndSwept(t *testing.T) {
	c := newClock()
	r := New(10*time.Second, c.Now)
	r.Upsert(node("a", svc(true, "m")))

	c.Add(10 * time.Second)
	if len(r.Lookup("m")) != 1 {
		t.Fatal("node at exactly TTL must still be fresh")
	}

	c.Add(time.Second)
	if got := r.Lookup("m"); len(got) != 0 {
		t.Fatalf("Lookup = %v, want none for stale node", got)
	}
	if r.Len() != 0 {
		t.Fatalf("Len = %d, want 0", r.Len())
	}
	if len(r.Models()) != 0 {
		t.Fatal("Models must skip stale nodes")
	}

	r.Sweep()
	r.mu.RLock()
	n := len(r.nodes)
	r.mu.RUnlock()
	if n != 0 {
		t.Fatalf("after Sweep %d nodes remain, want 0", n)
	}
}

func TestHeartbeatRefreshesNode(t *testing.T) {
	c := newClock()
	r := New(10*time.Second, c.Now)
	r.Upsert(node("a", svc(true, "m")))
	c.Add(8 * time.Second)
	r.Upsert(node("a", svc(true, "m")))
	c.Add(8 * time.Second)

	if len(r.Lookup("m")) != 1 {
		t.Fatal("refreshed node must be fresh")
	}
}

func TestModelsSortedAndDeduplicated(t *testing.T) {
	r := New(0, newClock().Now)
	r.Upsert(node("b", svc(true, "m2", "m1"), svc(true, "m1")))
	r.Upsert(node("a", svc(true, "m1"), svc(false, "m3")))

	want := []Model{{"m1", "a"}, {"m1", "b"}, {"m2", "b"}}
	got := r.Models()
	if len(got) != len(want) {
		t.Fatalf("Models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Models = %v, want %v", got, want)
		}
	}
}
