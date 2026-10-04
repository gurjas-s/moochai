package router

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"peerai-serv/internal/feed"
	"peerai-serv/internal/registry"
)

type testEnv struct {
	srv *httptest.Server
}

func setup(t *testing.T, node http.HandlerFunc) *testEnv {
	t.Helper()
	return setupFeed(t, node, nil)
}

func setupFeed(t *testing.T, node http.HandlerFunc, f *feed.Feed) *testEnv {
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
	New(reg, f).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}
}

func post(t *testing.T, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestForwardStreamsBody(t *testing.T) {
	env := setup(t, func(w http.ResponseWriter, r *http.Request) {
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
	req, err := http.NewRequest(http.MethodPost, env.srv.URL+"/v1/chat/completions",
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
	env := setup(t, func(w http.ResponseWriter, r *http.Request) {})
	if code, _ := post(t, env.srv.URL+"/v1/chat/completions", `{"model":"nope"}`); code != 404 {
		t.Fatalf("unknown model: status %d, want 404", code)
	}
	if code, _ := post(t, env.srv.URL+"/v1/chat/completions", `not json`); code != 400 {
		t.Fatalf("bad body: status %d, want 400", code)
	}
}

func TestModels(t *testing.T) {
	env := setup(t, func(w http.ResponseWriter, r *http.Request) {})
	code, body := get(t, env.srv.URL+"/v1/models")
	if code != 200 || !strings.Contains(body, `"id":"qwen"`) {
		t.Fatalf("models = %d %s", code, body)
	}
	if !strings.Contains(body, `"owned_by":"vllm"`) {
		t.Fatalf("models must use vllm owner for discovery: %s", body)
	}
}

func TestHealth(t *testing.T) {
	env := setup(t, func(w http.ResponseWriter, r *http.Request) {})
	if code, _ := get(t, env.srv.URL+"/health"); code != 200 {
		t.Fatalf("health = %d, want 200", code)
	}
}

func TestModelsExposeMaxModelLen(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(fake.Close)
	reg := registry.New(time.Minute)
	reg.Upsert(registry.Node{
		NodeID:     "n1",
		ListenAddr: strings.TrimPrefix(fake.URL, "http://"),
		Services: []registry.Service{{
			Models:  []string{"qwen"},
			Healthy: true,
			Meta:    map[string]any{"context_window": 8192},
		}},
	})
	mux := http.NewServeMux()
	New(reg, nil).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if code, body := get(t, srv.URL+"/v1/models"); code != 200 || !strings.Contains(body, `"max_model_len":8192`) {
		t.Fatalf("models = %d %s, want max_model_len 8192", code, body)
	}
}

// syncBuffer lets the test read feed lines that the handler writes after the response.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestForwardPrintsFeed(t *testing.T) {
	var out syncBuffer
	env := setupFeed(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, `{"choices":[{"message":{"content":"general kenobi"}}]}`)
	}, feed.New(&out))
	post(t, env.srv.URL+"/v1/chat/completions", `{"model":"qwen","messages":[{"role":"user","content":"hello there"}]}`)
	// The feed prints RESP after the client has the body.
	for i := 0; i < 100 && !strings.Contains(out.String(), "RESP"); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	got := out.String()
	for _, want := range []string{
		"ROUTE #1 127.0.0.1 asks for qwen → n1 · served by: n1",
		`REQ   #1 POST /v1/chat/completions "hello there"`,
		"RESP  #1 418 in ",
		`"general kenobi"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("feed misses %q:\n%s", want, got)
		}
	}
}
