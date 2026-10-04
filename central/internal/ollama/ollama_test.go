package ollama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mooch-central/internal/feed"
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
			!strings.Contains(string(body), `"content":"earlier"`) ||
			!strings.Contains(string(body), `"context":[1,2,3]`) ||
			!strings.Contains(string(body), `"tools":[{"type":"function"`) {
			t.Errorf("upstream request = %s %s", r.URL.Path, body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "hello"},
			}},
			"usage": map[string]any{"prompt_tokens": 42, "completion_tokens": 7},
			"context": []int{4, 5, 6},
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
		strings.NewReader(`{"model":"qwen","messages":[{"role":"user","content":"earlier"},{"role":"user","content":"hi"}],"context":[1,2,3],"stream":false,"options":{"num_predict":12},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `"content":"hello"`) ||
		!strings.Contains(string(body), `"prompt_eval_count":42`) ||
		!strings.Contains(string(body), `"eval_count":7`) ||
		!strings.Contains(string(body), `"context":[4,5,6]`) {
		t.Fatalf("chat = %d %s", resp.StatusCode, body)
	}
}

func TestChatStreamRecordsRequest(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"include_usage":true`) {
			t.Errorf("upstream request must ask for usage: %s", body)
		}

		func TestChatStreamConvertsToolCallArguments(t *testing.T) {
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"write_file\",\"arguments\":\"{\\\"path\\\":\\\"README.txt\\\"}\"}}]}}]}\n\n"+
					"data: [DONE]\n\n")
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

			body := postJSON(t, srv.URL+"/api/chat", `{"model":"qwen","stream":true,"messages":[{"role":"user","content":"write"}]}`)
			if !strings.Contains(body, `"arguments":{"path":"README.txt"}`) {
				t.Fatalf("stream tool arguments were not converted: %s", body)
			}
		}

		func TestChatPrintsFeed(t *testing.T) {
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)
			}))
			t.Cleanup(node.Close)
			reg := registry.New(time.Minute)
			reg.Upsert(registry.Node{
				NodeID: "n1", Name: "gpu-box", ListenAddr: strings.TrimPrefix(node.URL, "http://"),
				Services: []registry.Service{{Models: []string{"qwen"}, Healthy: true}},
			})
			var output bytes.Buffer
			f := feed.New(&output)
			f.Details = true
			mux := http.NewServeMux()
			New(reg, f).Register(mux)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			resp, err := http.Post(srv.URL+"/api/chat", "application/json",
				strings.NewReader(`{"model":"qwen","messages":[{"role":"user","content":"hello"}]}`))
			if err != nil {
				t.Fatal(err)
			}

			func TestChatContextRestoresPreviousMessages(t *testing.T) {
				var requests [][]map[string]any
				node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Messages []map[string]any `json:"messages"`
					}

					func TestChatContextRestoresSessionWhenClientOmitsContext(t *testing.T) {
						var requests [][]map[string]any
						node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							var body struct {
								Messages []map[string]any `json:"messages"`
							}
							_ = json.NewDecoder(r.Body).Decode(&body)
							requests = append(requests, body.Messages)
							_ = json.NewEncoder(w).Encode(map[string]any{
								"choices": []any{map[string]any{
									"message": map[string]any{"role": "assistant", "content": "answer"},
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

						postJSON(t, srv.URL+"/api/chat", `{"model":"qwen","messages":[{"role":"user","content":"first"}]}`)
						postJSON(t, srv.URL+"/api/chat", `{"model":"qwen","messages":[{"role":"user","content":"second"}]}`)
						if len(requests) != 2 || len(requests[1]) != 3 ||
							requests[1][0]["content"] != "first" ||
							requests[1][1]["content"] != "answer" ||
							requests[1][2]["content"] != "second" {
							t.Fatalf("second backend request = %+v", requests)
						}
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					requests = append(requests, body.Messages)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"choices": []any{map[string]any{
							"message": map[string]any{"role": "assistant", "content": "answer"},
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

				first := postJSON(t, srv.URL+"/api/chat",
					`{"model":"qwen","messages":[{"role":"user","content":"first"}]}`)
				var firstResponse struct {
					Context []int `json:"context"`
				}
				if err := json.Unmarshal([]byte(first), &firstResponse); err != nil {
					t.Fatal(err)
				}
				if len(firstResponse.Context) == 0 {
					t.Fatalf("first response has no context: %s", first)
				}
				postJSON(t, srv.URL+"/api/chat",
					fmt.Sprintf(`{"model":"qwen","context":[%d],"messages":[{"role":"user","content":"second"}]}`, firstResponse.Context[0]))
				if len(requests) != 2 || len(requests[1]) != 3 ||
					requests[1][0]["content"] != "first" ||
					requests[1][1]["content"] != "answer" ||
					requests[1][2]["content"] != "second" {
					t.Fatalf("second backend request = %+v", requests)
				}
			}

			func TestChatConvertsToolCallArgumentsForOllama(t *testing.T) {
				node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"choices": []any{map[string]any{
							"message": map[string]any{
								"role": "assistant",
								"tool_calls": []any{map[string]any{
									"id": "call-1", "type": "function",
									"function": map[string]any{
										"name": "write_file",
										"arguments": `{"path":"/etc/hosts","content":"test"}`,
									},
								}},
							},
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

				body := postJSON(t, srv.URL+"/api/chat", `{"model":"qwen","messages":[{"role":"user","content":"write a file"}]}`)
				if !strings.Contains(body, `"arguments":{"path":"/etc/hosts","content":"test"}`) {
					t.Fatalf("tool arguments were not converted: %s", body)
				}
			}

			func postJSON(t *testing.T, url, body string) string {
				t.Helper()
				resp, err := http.Post(url, "application/json", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("POST %s returned %d: %s", url, resp.StatusCode, data)
				}
				return string(data)
			}
			resp.Body.Close()
			got := output.String()
			for _, want := range []string{
				"MODEL    [ 127.0.0.1 → gpu-box ] qwen",
				"REQUEST  [ 127.0.0.1 → gpu-box ] POST",
				`"hello"`,
				"RESPONSE [ gpu-box → 127.0.0.1 ] 200",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("feed misses %q:\n%s", want, got)
				}
			}
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"+
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2},\"context\":[8,9]}\n\ndata: [DONE]\n\n")
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
	if !strings.Contains(string(body), `"content":"hel"`) ||
		!strings.Contains(string(body), `"done":true`) ||
		!strings.Contains(string(body), `"prompt_eval_count":4`) ||
		!strings.Contains(string(body), `"eval_count":2`) ||
		!strings.Contains(string(body), `"context":[8,9]`) {
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
