package central

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mooch-node/internal/config"
)

func TestRegisterAndHeartbeatSendExactPayload(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s %s content-type=%q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		var got map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		for _, key := range []string{"node_id", "name", "tailscale_ip", "listen_addr", "version", "services"} {
			if _, ok := got[key]; !ok {
				t.Errorf("payload does not contain %q", key)
			}
		}
		if len(got) != 6 {
			t.Errorf("payload keys = %d, want 6", len(got))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, httpClient: server.Client()}
	payload := Payload{
		NodeID: "node-1", Name: "Node", TailscaleIP: "100.64.0.5",
		ListenAddr: "100.64.0.5:9100", Version: Version,
		Services: []config.Service{{ID: "svc", Models: []string{"model"}}},
	}
	if err := client.Register(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if err := client.Heartbeat(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/api/nodes/register" || paths[1] != "/api/nodes/heartbeat" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestRegisterRequiresOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusBadRequest)
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, httpClient: server.Client()}
	if err := client.Register(context.Background(), Payload{}); err == nil {
		t.Fatal("Register() error = nil, want error")
	}
}

func TestRunRetriesAndStops(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &Client{
		baseURL: server.URL, httpClient: server.Client(),
		retry: RetryPolicy{Initial: time.Millisecond, Max: time.Millisecond},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(3 * time.Millisecond)
		cancel()
	}()
	err := client.Run(ctx, func() Payload { return Payload{} }, time.Hour)
	if err != context.Canceled {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d, want retry", attempts)
	}
}

func TestRunFiresHooks(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &Client{
		baseURL: server.URL, httpClient: server.Client(),
		retry: RetryPolicy{Initial: time.Millisecond, Max: time.Millisecond},
	}
	registered, heartbeats, failures := 0, 0, 0
	client.WithHooks(Hooks{
		OnRegistered: func() { registered++ },
		OnHeartbeat:  func() { heartbeats++ },
		OnError:      func(err error) { failures++ },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if err := client.Run(ctx, func() Payload { return Payload{} }, time.Millisecond); err != context.Canceled {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}
	if registered != 1 {
		t.Errorf("registered = %d, want 1", registered)
	}
	if failures < 1 {
		t.Errorf("failures = %d, want at least 1", failures)
	}
	if heartbeats < 1 {
		t.Errorf("heartbeats = %d, want at least 1", heartbeats)
	}
}
