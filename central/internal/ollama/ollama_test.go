package ollama

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mooch-central/internal/registry"
	"mooch-central/internal/stats"
)

func TestTagsAndShow(t *testing.T) {
	reg := registry.New(time.Minute)
	reg.Upsert(registry.Node{
		NodeID: "n1",
		Services: []registry.Service{{Models: []string{"qwen"}, Healthy: true,
			Meta: map[string]any{"context_window": 8192, "capabilities": []string{"completion", "tools"}}}},
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
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `"mooch.context_length":8192`) ||
		!strings.Contains(string(body), `"capabilities":["completion","tools"]`) {
		t.Fatalf("show = %d %s", resp.StatusCode, body)
	}
}

func TestChatTranslatesNonStreamingResponse(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" ||
			!strings.Contains(string(body), `"max_tokens":12`) ||
			!strings.Contains(string(body), `"tools":[{"type":"function"`) {
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
		strings.NewReader(`{"model":"qwen","messages":[{"role":"user","content":"hi"}],"stream":false,"options":{"num_predict":12},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"content":"hello"`) {
		t.Fatalf("chat = %d %s", resp.StatusCode, body)
	}
}

func TestChatStreamRecordsRequest(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"include_usage":true`) {
			t.Errorf("upstream request must ask for usage: %s", body)
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"+
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(node.Close)
	reg := registry.New(time.Minute)
	reg.Upsert(registry.Node{
		NodeID: "n1", ListenAddr: strings.TrimPrefix(node.URL, "http://"),
		Services: []registry.Service{{Models: []string{"qwen"}, Healthy: true}},
	})
	h := New(reg)
	got := make(chan stats.Request, 1)
	h.Record = func(r stats.Request) { got <- r }
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/chat", "application/json", strings.NewReader(`{"model":"qwen","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"content":"hel"`) || !strings.Contains(string(body), `"done":true`) {
		t.Fatalf("stream = %s", body)
	}
	select {
	case r := <-got:
		if r.Node != "n1" || r.Model != "qwen" || r.Status != http.StatusOK || r.Path != "/api/chat" ||
			r.CompletionTokens == nil || *r.CompletionTokens != 2 {
			t.Fatalf("row = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no row was recorded")
	}
}
