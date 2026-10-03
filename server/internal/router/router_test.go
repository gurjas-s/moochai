package router

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"peerai-serv/internal/registry"
)

// setup starts a fake node and a central router. The registry holds one
// node that serves model "m" at the fake node address.
func setup(t *testing.T, node http.HandlerFunc) (*httptest.Server, *registry.Registry) {
	t.Helper()
	nodeSrv := httptest.NewServer(node)
	t.Cleanup(nodeSrv.Close)

	reg := registry.New(0, nil)
	reg.Upsert(registry.Node{
		NodeID:     "node-a",
		ListenAddr: strings.TrimPrefix(nodeSrv.URL, "http://"),
		Services:   []registry.Service{{ID: "s", Models: []string{"m"}, Healthy: true}},
	})

	mux := http.NewServeMux()
	New(reg, nil, nil).Register(mux)
	central := httptest.NewServer(mux)
	t.Cleanup(central.Close)
	return central, reg
}

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func errType(t *testing.T, resp *http.Response) string {
	t.Helper()
	var env struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	return env.Error.Type
}

func TestForwardSendsUnchangedBodyAndPath(t *testing.T) {
	const body = `{"model":"m",  "messages":[{"role":"user","content":"hi"}]}`
	var gotPath, gotQuery, gotBody, gotAuth string
	central, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotQuery, gotBody, gotAuth = r.URL.Path, r.URL.RawQuery, string(b), r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion"}`)
	})

	req, _ := http.NewRequest(http.MethodPost, central.URL+"/v1/chat/completions?a=1", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK || string(out) != `{"id":"x","object":"chat.completion"}` {
		t.Fatalf("response = %d %s", resp.StatusCode, out)
	}
	if gotPath != "/v1/chat/completions" || gotQuery != "a=1" {
		t.Fatalf("node got path %q query %q", gotPath, gotQuery)
	}
	if gotBody != body {
		t.Fatalf("node got body %q, want %q", gotBody, body)
	}
	if gotAuth != "Bearer k" {
		t.Fatalf("node got Authorization %q", gotAuth)
	}
}

func TestForwardCompletionsRoute(t *testing.T) {
	var gotPath string
	central, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { gotPath = r.URL.Path })
	resp := post(t, central.URL+"/v1/completions", `{"model":"m"}`)
	if resp.StatusCode != http.StatusOK || gotPath != "/v1/completions" {
		t.Fatalf("status %d, node path %q", resp.StatusCode, gotPath)
	}
}

func TestForwardStreamsSSE(t *testing.T) {
	release := make(chan struct{})
	central, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release // hold the stream open until the caller sees chunk one
		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	resp := post(t, central.URL+"/v1/chat/completions", `{"model":"m","stream":true}`)
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	br := bufio.NewReader(resp.Body)
	lineCh := make(chan string, 1)
	go func() {
		line, _ := br.ReadString('\n')
		lineCh <- line
	}()
	select {
	case line := <-lineCh:
		if line != "data: one\n" {
			t.Fatalf("first line = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first chunk did not arrive before stream end: central buffers the stream")
	}
	close(release)
	rest, _ := io.ReadAll(br)
	if string(rest) != "\ndata: [DONE]\n\n" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestForwardPassesNodeError(t *testing.T) {
	central, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-PeerAI-Node", "node-a")
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":{"type":"service_unavailable"}}`)
	})
	resp := post(t, central.URL+"/v1/chat/completions", `{"model":"m"}`)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("X-PeerAI-Node") != "node-a" {
		t.Fatalf("status %d, header %q", resp.StatusCode, resp.Header.Get("X-PeerAI-Node"))
	}
	if got := errType(t, resp); got != "service_unavailable" {
		t.Fatalf("type = %q", got)
	}
}

func TestForwardRejectsBadRequests(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		typ        string
	}{
		{"invalid json", `{`, 400, "invalid_request_error"},
		{"empty body", ``, 400, "invalid_request_error"},
		{"no model", `{"messages":[]}`, 400, "invalid_request_error"},
		{"unknown model", `{"model":"nope"}`, 404, "model_not_found"},
	}
	called := false
	central, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := post(t, central.URL+"/v1/chat/completions", tc.body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
			if got := errType(t, resp); got != tc.typ {
				t.Fatalf("type = %q, want %q", got, tc.typ)
			}
		})
	}
	if called {
		t.Fatal("node must not receive rejected requests")
	}
}

func TestForwardStaleNodeIsNotFound(t *testing.T) {
	now := time.Now()
	reg := registry.New(time.Second, func() time.Time { return now })
	reg.Upsert(registry.Node{NodeID: "a", ListenAddr: "127.0.0.1:1",
		Services: []registry.Service{{ID: "s", Models: []string{"m"}, Healthy: true}}})
	now = now.Add(2 * time.Second)

	mux := http.NewServeMux()
	New(reg, nil, nil).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestForwardNodeDownIsBadGateway(t *testing.T) {
	central, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {})
	// Replace the node with an address where nothing listens.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadAddr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close()

	reg := registry.New(0, nil)
	reg.Upsert(registry.Node{NodeID: "a", ListenAddr: deadAddr,
		Services: []registry.Service{{ID: "s", Models: []string{"m"}, Healthy: true}}})
	mux := http.NewServeMux()
	New(reg, nil, nil).Register(mux)
	central.Config.Handler = mux

	resp := post(t, central.URL+"/v1/chat/completions", `{"model":"m"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if got := errType(t, resp); got != "bad_gateway" {
		t.Fatalf("type = %q", got)
	}
}

func TestModels(t *testing.T) {
	central, reg := setup(t, func(w http.ResponseWriter, r *http.Request) {})
	reg.Upsert(registry.Node{NodeID: "node-b", ListenAddr: "x:1",
		Services: []registry.Service{{ID: "s", Models: []string{"a-model"}, Healthy: true}}})

	resp, err := http.Get(central.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got modelList
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := modelList{Object: "list", Data: []modelObject{
		{ID: "a-model", Object: "model", OwnedBy: "node-b"},
		{ID: "m", Object: "model", OwnedBy: "node-a"},
	}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("models = %+v, want %+v", got, want)
	}
}

func TestModelsEmptyIsList(t *testing.T) {
	mux := http.NewServeMux()
	New(registry.New(0, nil), nil, nil).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if strings.TrimSpace(rec.Body.String()) != `{"object":"list","data":[]}` {
		t.Fatalf("body = %s", rec.Body)
	}
}
