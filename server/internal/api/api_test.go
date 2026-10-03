package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"peerai-serv/internal/registry"
)

const validBody = `{
  "node_id": "alice-rtx",
  "name": "Alice RTX 4090",
  "tailscale_ip": "100.64.0.5",
  "listen_addr": "100.64.0.5:9100",
  "version": "0.1.0",
  "services": [{
    "id": "qwen-vllm", "name": "Qwen", "type": "llm", "provider": "vllm",
    "endpoint": "http://127.0.0.1:8000", "api_base": "/v1",
    "models": ["qwen2.5-32b-instruct"], "supports_streaming": true, "healthy": true,
    "meta": {"context_window": 32768}
  }],
  "extra_field": "ignored"
}`

func newMux(reg *registry.Registry) *http.ServeMux {
	mux := http.NewServeMux()
	New(reg, "v0.1.0-test", nil).Register(mux)
	return mux
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestRegisterAndHeartbeatStoreNode(t *testing.T) {
	for _, path := range []string{"/api/nodes/register", "/api/nodes/heartbeat"} {
		t.Run(path, func(t *testing.T) {
			reg := registry.New(0, nil)
			rec := do(t, newMux(reg), http.MethodPost, path, validBody)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
				t.Fatalf("body = %s, want status ok", rec.Body)
			}
			got := reg.Lookup("qwen2.5-32b-instruct")
			if len(got) != 1 || got[0].ListenAddr != "100.64.0.5:9100" {
				t.Fatalf("Lookup = %+v, want alice-rtx", got)
			}
		})
	}
}

func TestRegisterRejectsInvalidPayload(t *testing.T) {
	cases := map[string]string{
		"bad json":         `{`,
		"empty body":       ``,
		"no node_id":       `{"listen_addr":"x:1"}`,
		"no listen_addr":   `{"node_id":"a"}`,
		"service no id":    `{"node_id":"a","listen_addr":"x:1","services":[{"models":["m"]}]}`,
		"service no model": `{"node_id":"a","listen_addr":"x:1","services":[{"id":"s"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			reg := registry.New(0, nil)
			rec := do(t, newMux(reg), http.MethodPost, "/api/nodes/register", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			var env struct {
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error.Type != "invalid_request_error" {
				t.Fatalf("body = %s, want invalid_request_error envelope", rec.Body)
			}
			if reg.Len() != 0 {
				t.Fatal("invalid payload must not add a node")
			}
		})
	}
}

func TestRegisterRejectsLargeBody(t *testing.T) {
	body := `{"node_id":"` + strings.Repeat("a", MaxBodyBytes) + `"}`
	rec := do(t, newMux(registry.New(0, nil)), http.MethodPost, "/api/nodes/register", body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestRegisterRejectsGet(t *testing.T) {
	rec := do(t, newMux(registry.New(0, nil)), http.MethodGet, "/api/nodes/register", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	reg := registry.New(0, nil)
	mux := newMux(reg)
	do(t, mux, http.MethodPost, "/api/nodes/register", validBody)

	rec := do(t, mux, http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := healthResponse{Status: "ok", Version: "v0.1.0-test", Nodes: 1}
	if got != want {
		t.Fatalf("healthz = %+v, want %+v", got, want)
	}
}
