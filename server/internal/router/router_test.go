package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"peerai-serv/internal/auth"
	"peerai-serv/internal/registry"
)

type testEnv struct {
	srv   *httptest.Server
	alice auth.User
	bob   auth.User
	eve   auth.User
	group auth.GroupView
}

func setup(t *testing.T, node http.HandlerFunc) *testEnv {
	t.Helper()
	fake := httptest.NewServer(node)
	t.Cleanup(fake.Close)
	reg := registry.New(time.Minute)
	store := auth.New()
	alice, _ := store.CreateUser("alice")
	bob, _ := store.CreateUser("bob")
	eve, _ := store.CreateUser("eve")
	g, _ := store.CreateGroup(alice, "lab")
	if _, err := store.AddMember(alice, g.GroupID, bob.ID); err != nil {
		t.Fatal(err)
	}
	reg.Upsert(registry.Node{
		NodeID:     "n1",
		OwnerID:    alice.ID,
		GroupIDs:   []string{g.GroupID},
		ListenAddr: strings.TrimPrefix(fake.URL, "http://"),
		Services:   []registry.Service{{Models: []string{"qwen"}, Healthy: true}},
	})
	mux := http.NewServeMux()
	New(reg, store, nil).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv, alice: alice, bob: bob, eve: eve, group: g}
}

func postKey(t *testing.T, url, body, key string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func getKey(t *testing.T, url, key string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
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
	req.Header.Set("Authorization", "Bearer "+env.bob.APIKey)
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
	if code, _ := postKey(t, env.srv.URL+"/v1/chat/completions", `{"model":"nope"}`, env.alice.APIKey); code != 404 {
		t.Fatalf("unknown model: status %d, want 404", code)
	}
	if code, _ := postKey(t, env.srv.URL+"/v1/chat/completions", `not json`, env.alice.APIKey); code != 400 {
		t.Fatalf("bad body: status %d, want 400", code)
	}
	if code, _ := postKey(t, env.srv.URL+"/v1/chat/completions", `{"model":"qwen"}`, ""); code != 401 {
		t.Fatalf("missing key: status %d, want 401", code)
	}
	if code, _ := postKey(t, env.srv.URL+"/v1/chat/completions", `{"model":"qwen"}`, env.eve.APIKey); code != 404 {
		t.Fatalf("outsider: status %d, want 404", code)
	}
}

func TestModels(t *testing.T) {
	env := setup(t, func(w http.ResponseWriter, r *http.Request) {})
	if code, body := getKey(t, env.srv.URL+"/v1/models", env.bob.APIKey); code != 200 || !strings.Contains(body, `"id":"qwen"`) {
		t.Fatalf("member models = %d %s", code, body)
	}
	if code, body := getKey(t, env.srv.URL+"/v1/models", env.eve.APIKey); code != 200 || strings.Contains(body, `"id":"qwen"`) {
		t.Fatalf("outsider models = %d %s", code, body)
	}
	if code, _ := getKey(t, env.srv.URL+"/v1/models", ""); code != 401 {
		t.Fatalf("missing key models: status %d, want 401", code)
	}
}
