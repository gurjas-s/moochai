package ollama

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mooch-serv/internal/registry"
)

func TestTagsAndShow(t *testing.T) {
	reg := registry.New(time.Minute)
	reg.Upsert(registry.Node{
		NodeID: "n1",
		Services: []registry.Service{{Models: []string{"qwen"}, Healthy: true,
			Meta: map[string]any{"context_window": 8192}}},
	})
	mux := http.NewServeMux()
	New(reg).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/tags")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"name":"qwen"`) {
		t.Fatalf("tags = %d %s", resp.StatusCode, body)
	}

	resp, err = http.Post(srv.URL+"/api/show", "application/json", strings.NewReader(`{"model":"qwen"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"mooch.context_length":8192`) {
		t.Fatalf("show = %d %s", resp.StatusCode, body)
	}
}

func TestChatTranslatesNonStreamingResponse(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" || !strings.Contains(string(body), `"max_tokens":12`) {
			t.Errorf("upstream request = %s %s", r.URL.Path, body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "hello"},
			}},
		})
	}))
	t.Cleanup(node.Close)

	reg := registry.New(time.Minute)
	reg.Upsert(registry.Node{
		NodeID: "n1", ListenAddr: strings.TrimPrefix(node.URL, "http://"),
		Services: []registry.Service{{Models: []string{"qwen"}, Healthy: true}},
	})
	mux := http.NewServeMux()
	New(reg).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/chat", "application/json",
		strings.NewReader(`{"model":"qwen","messages":[{"role":"user","content":"hi"}],"stream":false,"options":{"num_predict":12}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"content":"hello"`) {
		t.Fatalf("chat = %d %s", resp.StatusCode, body)
	}
}
