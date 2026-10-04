package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mooch-central/internal/registry"
)

func newMux() (*http.ServeMux, *registry.Registry) {
	reg := registry.New(time.Minute)
	mux := http.NewServeMux()
	New(reg, nil).Register(mux)
	return mux, reg
}

func doReq(mux *http.ServeMux, method, path, body, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestUpsert(t *testing.T) {
	mux, reg := newMux()

	tests := []struct {
		name, remote, body string
		want               int
	}{
		{"ok", "100.64.0.5:5000", `{"node_id":"n1","tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100"}`, 200},
		{"loopback ok", "127.0.0.1:5000", `{"node_id":"n2","tailscale_ip":"100.64.0.6","listen_addr":"100.64.0.6:9100"}`, 200},
		{"spoofed ip", "100.64.0.9:5000", `{"node_id":"n3","tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100"}`, 400},
		{"addr mismatch", "100.64.0.5:5000", `{"node_id":"n4","tailscale_ip":"100.64.0.5","listen_addr":"10.0.0.1:9100"}`, 400},
		{"no id", "100.64.0.5:5000", `{"tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100"}`, 400},
		{"bad json", "100.64.0.5:5000", `{`, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doReq(mux, "POST", "/api/nodes/heartbeat", tt.body, tt.remote)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
	if reg.Len() != 2 {
		t.Fatalf("registry has %d nodes, want 2", reg.Len())
	}
	if rec := doReq(mux, "GET", "/api/nodes", "", "127.0.0.1:5000"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "n1") {
		t.Fatalf("nodes = %d %s", rec.Code, rec.Body)
	}
}

func TestUpsertCallsHeartbeat(t *testing.T) {
	reg := registry.New(time.Minute)
	h := New(reg, nil)
	var beats []string
	h.Heartbeat = func(n registry.Node) { beats = append(beats, n.NodeID) }
	mux := http.NewServeMux()
	h.Register(mux)
	doReq(mux, "POST", "/api/nodes/register", `{"node_id":"n1","tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100"}`, "100.64.0.5:5000")
	doReq(mux, "POST", "/api/nodes/heartbeat", `{"node_id":"n1","tailscale_ip":"100.64.0.9","listen_addr":"100.64.0.5:9100"}`, "100.64.0.5:5000")
	if len(beats) != 1 || beats[0] != "n1" {
		t.Fatalf("heartbeats = %v, want only the accepted one", beats)
	}
}
