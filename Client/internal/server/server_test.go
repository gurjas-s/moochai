package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testOptions() Options {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return Options{
		ListenHost:  "127.0.0.1",
		ListenPort:  9100,
		NodeID:      "test-node",
		TailscaleIP: "100.64.0.5",
		Version:     "v0.1.0-dev",
		StartTime:   start,
		Now:         func() time.Time { return start.Add(90 * time.Second) },
	}
}

func TestHealthz(t *testing.T) {
	s := New(testOptions())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(res.Body)
	var got struct {
		NodeID      string `json:"node_id"`
		TailscaleIP string `json:"tailscale_ip"`
		Uptime      string `json:"uptime"`
		Version     string `json:"version"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("healthz body is not JSON: %v (%s)", err, body)
	}
	if got.NodeID != "test-node" {
		t.Errorf("node_id = %q, want test-node", got.NodeID)
	}
	if got.TailscaleIP != "100.64.0.5" {
		t.Errorf("tailscale_ip = %q, want 100.64.0.5", got.TailscaleIP)
	}
	if got.Version != "v0.1.0-dev" {
		t.Errorf("version = %q, want v0.1.0-dev", got.Version)
	}
	if got.Uptime != "1m30s" {
		t.Errorf("uptime = %q, want 1m30s", got.Uptime)
	}
}

func TestHealthzMethodNotAllowed(t *testing.T) {
	s := New(testOptions())
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status = %d, want 405", rec.Code)
	}
}

func TestOpenAIRoutesRegistered_Stub501(t *testing.T) {
	s := New(testOptions())
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/v1/chat/completions", `{"model":"x"}`},
		{http.MethodPost, "/v1/completions", `{"model":"x"}`},
		{http.MethodPost, "/v1/embeddings", `{"model":"x"}`},
		{http.MethodPost, "/v1/audio/transcriptions", `{"model":"x"}`},
		{http.MethodPost, "/v1/images/generations", `{"model":"x"}`},
		{http.MethodGet, "/v1/models", ``},
	}
	for _, tc := range cases {
		var rdr io.Reader
		if tc.body != "" {
			rdr = strings.NewReader(tc.body)
		}
		req := httptest.NewRequest(tc.method, tc.path, rdr)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		wantStatus := http.StatusNotImplemented
		if tc.path == "/v1/models" {
			wantStatus = http.StatusOK
		}
		if rec.Code != wantStatus {
			t.Errorf("%s %s status = %d, want %d", tc.method, tc.path, rec.Code, wantStatus)
		}
		if tc.path != "/v1/models" {
			if got := rec.Header().Get("X-PeerAI-Node"); got != "test-node" {
				t.Errorf("%s %s X-PeerAI-Node = %q, want test-node", tc.method, tc.path, got)
			}
		}
	}
}

func TestProxyHandlerMockIsInvoked(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody string
	mock := ProxyHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusTeapot)
	})
	opts := testOptions()
	opts.Proxy = mock
	s := New(opts)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("mock proxy status = %d, want 418", rec.Code)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/chat/completions" {
		t.Errorf("mock saw %s %s, want POST /v1/chat/completions", gotMethod, gotPath)
	}
	if gotBody != `{"model":"m"}` {
		t.Errorf("mock saw body %q", gotBody)
	}
}

func TestWrongMethodIs405(t *testing.T) {
	s := New(testOptions())
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /v1/chat/completions status = %d, want 405", rec.Code)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	s := New(testOptions())
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nope status = %d, want 404", rec.Code)
	}
}

func TestAddr(t *testing.T) {
	s := New(testOptions())
	if got := s.Addr(); got != "127.0.0.1:9100" {
		t.Errorf("Addr() = %q, want 127.0.0.1:9100", got)
	}
	// IPv6 host must be bracketed via JoinHostPort.
	opts := testOptions()
	opts.ListenHost = "::1"
	s6 := New(opts)
	if got := s6.Addr(); got != "[::1]:9100" {
		t.Errorf("Addr() v6 = %q, want [::1]:9100", got)
	}
}

func TestCustomRouteExtensible(t *testing.T) {
	opts := testOptions()
	opts.Routes = append(append([]Route{}, DefaultOpenAIRoutes...),
		Route{Method: http.MethodPost, Path: "/v1/custom-thing"})
	s := New(opts)
	req := httptest.NewRequest(http.MethodPost, "/v1/custom-thing", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("custom route status = %d, want 501 stub", rec.Code)
	}
}

func TestLiveBindAndHealthz(t *testing.T) {
	opts := testOptions()
	opts.ListenHost = "127.0.0.1"
	opts.ListenPort = 0 // OS-assigned; only validates Handler over real HTTP
	s := New(opts)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz over live server: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("live /healthz status = %d, want 200", res.StatusCode)
	}
}

func TestRequestLoggingHasRequiredFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	opts := testOptions()
	opts.Logger = logger
	s := New(opts)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var entry map[string]any
	dec := json.NewDecoder(&buf)
	if err := dec.Decode(&entry); err != nil {
		t.Fatalf("log is not JSON: %v (%q)", err, buf.String())
	}
	for _, key := range []string{"method", "path", "status", "node_id", "component"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("log missing key %q: %v", key, entry)
		}
	}
	if entry["method"] != http.MethodGet {
		t.Errorf("method = %v, want GET", entry["method"])
	}
	if entry["path"] != "/healthz" {
		t.Errorf("path = %v, want /healthz", entry["path"])
	}
	if entry["node_id"] != "test-node" {
		t.Errorf("node_id = %v, want test-node", entry["node_id"])
	}
	if entry["component"] != "server" {
		t.Errorf("component = %v, want server", entry["component"])
	}
}

func TestServeAndShutdownClean(t *testing.T) {
	var buf bytes.Buffer
	opts := testOptions()
	opts.ListenHost = "127.0.0.1"
	opts.ListenPort = 0
	opts.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	s := New(opts)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.Serve(ln)
	}()

	// Wait for the listener to accept. Poll healthz with mock addr.
	url := "http://" + ln.Addr().String() + "/healthz"
	var res *http.Response
	for i := 0; i < 50; i++ {
		res, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			t.Fatalf("Serve returned %v, want nil or ErrServerClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop within 5s")
	}
}

func TestShutdownWithoutServeIsNil(t *testing.T) {
	s := New(testOptions())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown without Serve = %v, want nil", err)
	}
}
