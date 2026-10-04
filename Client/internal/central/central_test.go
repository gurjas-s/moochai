package central

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"peer-ai-client/internal/config"
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

func TestPostSendsBearerKey(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, httpClient: server.Client()}
	client.WithAPIKey("peerai_test")
	if err := client.Register(context.Background(), Payload{}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer peerai_test" {
		t.Fatalf("Authorization = %q, want Bearer key", gotAuth)
	}
}

func TestSetCredentialsUpdatesTarget(t *testing.T) {
	var gotAuth, gotPath string
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = "first"
		w.WriteHeader(http.StatusOK)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = "second"
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(second.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{baseURL: first.URL, httpClient: first.Client()}
	client.SetCredentials(host, port, "peerai_new")
	if err := client.Register(context.Background(), Payload{}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "second" || gotAuth != "Bearer peerai_new" {
		t.Fatalf("request went to %q with auth %q", gotPath, gotAuth)
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
