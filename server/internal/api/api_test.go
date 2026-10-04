package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"peerai-serv/internal/registry"
)

func TestUpsert(t *testing.T) {
	reg := registry.New(time.Minute)
	mux := http.NewServeMux()
	New(reg, nil).Register(mux)

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
			req := httptest.NewRequest(http.MethodPost, "/api/nodes/heartbeat", strings.NewReader(tt.body))
			req.RemoteAddr = tt.remote
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
	if reg.Len() != 2 {
		t.Fatalf("registry has %d nodes, want 2", reg.Len())
	}
}
