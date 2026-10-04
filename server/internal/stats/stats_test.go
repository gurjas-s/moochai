package stats

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"mooch-serv/internal/registry"
)

func TestUsage(t *testing.T) {
	tests := []struct {
		name, body       string
		prompt, complete int
		found            bool
	}{
		{"json", `{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":30}}`, 12, 30, true},
		{"sse", "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":7}}\n\ndata: [DONE]\n", 5, 7, true},
		{"none", `{"choices":[{"message":{"content":"hi"}}]}`, 0, 0, false},
		{"garbage", `not json`, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, c := Usage([]byte(tt.body))
			if (p != nil) != tt.found {
				t.Fatalf("found = %v, want %v", p != nil, tt.found)
			}
			if tt.found && (*p != tt.prompt || *c != tt.complete) {
				t.Fatalf("usage = %d/%d, want %d/%d", *p, *c, tt.prompt, tt.complete)
			}
		})
	}
}

type flushes struct {
	mu    sync.Mutex
	reqs  int
	beats int
	calls int
}

func (f *flushes) flush(_ context.Context, reqs []Request, beats []beat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs += len(reqs)
	f.beats += len(beats)
	f.calls++
	return nil
}

func TestStoreBatchesAndCloseFlushes(t *testing.T) {
	f := &flushes{}
	s := newStore(f.flush)
	for range batchSize {
		s.Record(Request{Node: "a", Duration: 10 * time.Millisecond})
	}
	s.Heartbeat(registry.Node{NodeID: "a"})
	s.Close()
	s.Record(Request{Node: "a"}) // after Close: dropped, and no panic
	if f.reqs != batchSize || f.beats != 1 {
		t.Fatalf("flushed %d requests and %d heartbeats, want %d and 1", f.reqs, f.beats, batchSize)
	}
	if f.calls != 2 {
		t.Fatalf("flush calls = %d, want 2: one full batch, one at Close", f.calls)
	}
	if got := s.Load(registry.Node{NodeID: "a"}); got != float64(batchSize*10) {
		t.Fatalf("load = %v, want %d", got, batchSize*10)
	}
}

func TestStoreDropsWhenQueueIsFull(t *testing.T) {
	block := make(chan struct{})
	s := newStore(func(context.Context, []Request, []beat) error { <-block; return nil })
	done := make(chan struct{})
	go func() {
		for range 3 * queueSize {
			s.Record(Request{Node: "a"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked on a full queue")
	}
	if s.dropped.Load() == 0 {
		t.Fatal("no rows were dropped")
	}
	close(block)
	s.Close()
}

func TestNilStore(t *testing.T) {
	var s *Store
	s.Record(Request{})
	s.Heartbeat(registry.Node{})
	s.Close()
	if s.Load(registry.Node{}) != 0 {
		t.Fatal("nil store must report zero load")
	}
	mux := http.NewServeMux()
	s.Register(mux)
	for path, want := range map[string]int{
		"/api/analytics/summary": http.StatusServiceUnavailable,
		"/leaderboard.json":      http.StatusServiceUnavailable,
		"/analytics":             http.StatusOK,
		"/analytics/chart.js":    http.StatusOK,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Fatalf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}

func TestTailKeepsTheEndOfALongStream(t *testing.T) {
	var tail Tail
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", 500) + "\"}}]}\n\n")
	for range 1000 { // about 530 KB, much more than tailSize
		_, _ = tail.Write(chunk)
	}
	_, _ = tail.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":1200}}\n\ndata: [DONE]\n\n"))
	if n := len(tail.Bytes()); n != tailSize {
		t.Fatalf("tail keeps %d bytes, want %d", n, tailSize)
	}
	if p, c := Usage(tail.Bytes()); p == nil || *p != 9 || *c != 1200 {
		t.Fatal("usage at the end of a long stream was not found")
	}
}

func TestParseWindow(t *testing.T) {
	for q, ok := range map[string]bool{"": true, "1h": true, "30d": true, "2h": false, "1h;DROP TABLE requests": false} {
		r := httptest.NewRequest("GET", "/?window="+url.QueryEscape(q), nil)
		if _, _, got := parseWindow(r); got != ok {
			t.Fatalf("parseWindow(%q) = %v, want %v", q, got, ok)
		}
	}
}

func TestLeaderboardSkipsTakersAndSortsByServed(t *testing.T) {
	sum := Summary{Nodes: []NodeUsage{
		{Name: "laptop", Used: 40},
		{Name: "a", Served: 10, UptimeMin: 135},
		{Name: "b", Served: 25},
	}}
	entries := Leaderboard(sum)["entries"].([]LeaderboardEntry)
	if len(entries) != 2 || entries[0].Alias != "b" || entries[1].Uptime != "2h15m0s" {
		t.Fatalf("entries = %+v", entries)
	}
}
