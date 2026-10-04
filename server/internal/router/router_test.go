package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"peerai-serv/internal/registry"
)

func setup(t *testing.T, node http.HandlerFunc) *httptest.Server {
	t.Helper()
	fake := httptest.NewServer(node)
	t.Cleanup(fake.Close)
	reg := registry.New(time.Minute)
	reg.Upsert(registry.Node{
		NodeID:     "n1",
		ListenAddr: strings.TrimPrefix(fake.URL, "http://"),
		Services:   []registry.Service{{Models: []string{"qwen"}, Healthy: true}},
	})
	mux := http.NewServeMux()
	New(reg, nil).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestForwardStreamsBody(t *testing.T) {
	srv := setup(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" || !strings.Contains(string(got), `"qwen"`) {
			t.Errorf("node got %s %s", r.URL.Path, got)
		}
		if r.Header.Get("X-Test-Auth") != "forwarded-value" {
			t.Errorf("node test header = %q, want forwarded header", r.Header.Get("X-Test-Auth"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: one\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		io.WriteString(w, "data: [DONE]\n\n")
	})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"qwen","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test-Auth", "forwarded-value")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	code, body := resp.StatusCode, string(bodyBytes)
	if code != 200 || body != "data: one\n\ndata: [DONE]\n\n" {
		t.Fatalf("status %d body %q", code, body)
	}
}

func TestForwardErrors(t *testing.T) {
	srv := setup(t, func(w http.ResponseWriter, r *http.Request) {})
	if code, _ := post(t, srv.URL+"/v1/chat/completions", `{"model":"nope"}`); code != 404 {
		t.Fatalf("unknown model: status %d, want 404", code)
	}
	if code, _ := post(t, srv.URL+"/v1/chat/completions", `not json`); code != 400 {
		t.Fatalf("bad body: status %d, want 400", code)
	}
}

func TestModels(t *testing.T) {
	srv := setup(t, func(w http.ResponseWriter, r *http.Request) {})
	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"id":"qwen"`) {
		t.Fatalf("models = %s", b)
	}
}
