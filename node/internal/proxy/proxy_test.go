package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testNode = "test-node-1"

func postJSON(t *testing.T, h http.Handler, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func decodeErr(t *testing.T, body []byte) openAIError {
	t.Helper()
	var e openAIError
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body is not OpenAI-shaped JSON: %v (%s)", err, body)
	}
	return e
}

// fake backend that records what it received and replies canned JSON.
type recorded struct {
	path   string
	query  string
	body   []byte
	header http.Header
}

func echoBackend(status int, reply string, rec *recorded) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if rec != nil {
			rec.path, rec.query, rec.body, rec.header = r.URL.Path, r.URL.RawQuery, b, r.Header.Clone()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
}

func TestRouteByModel(t *testing.T) {
	var recA, recB recorded
	srvA := echoBackend(200, `{"id":"a"}`, &recA)
	defer srvA.Close()
	srvB := echoBackend(200, `{"id":"b"}`, &recB)
	defer srvB.Close()

	h := NewHandler([]Service{
		StaticService{ID: "svc-a", Endpoint: srvA.URL, APIBase: "/v1", Models: []string{"model-a"}, Healthy: true},
		StaticService{ID: "svc-b", Endpoint: srvB.URL, APIBase: "/v1", Models: []string{"model-b"}, Healthy: true},
	}, testNode)

	rr := postJSON(t, h, "/v1/chat/completions?foo=bar", `{"model":"model-b","messages":[]}`, nil)
	if rr.Code != 200 {
		t.Fatalf("got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"id":"b"`) {
		t.Fatalf("request went to wrong backend: %s", rr.Body.String())
	}
	// upstream path = endpoint + apiBase + remaining, no /v1/v1 doubling
	if recB.path != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want /v1/chat/completions", recB.path)
	}
	if recB.query != "foo=bar" {
		t.Fatalf("query not preserved: %q", recB.query)
	}
	// raw body unchanged
	if !strings.Contains(string(recB.body), `"model":"model-b"`) {
		t.Fatalf("body not passed through: %s", recB.body)
	}
	if len(recA.body) != 0 {
		t.Fatal("wrong backend received the request")
	}
}

func TestUnknownModel400(t *testing.T) {
	srv := echoBackend(200, `{}`, nil)
	defer srv.Close()
	h := NewHandler([]Service{
		StaticService{ID: "s", Endpoint: srv.URL, APIBase: "/v1", Models: []string{"known"}, Healthy: true},
	}, testNode)

	rr := postJSON(t, h, "/v1/chat/completions", `{"model":"nope"}`, nil)
	if rr.Code != 400 {
		t.Fatalf("got %d, want 400", rr.Code)
	}
	if rr.Header().Get(NodeHeader) != testNode {
		t.Fatalf("missing %s header", NodeHeader)
	}
	e := decodeErr(t, rr.Body.Bytes())
	if !strings.Contains(e.Error.Message, "nope") {
		t.Fatalf("error should name the model: %s", e.Error.Message)
	}
}

func TestMissingAndInvalidModel400(t *testing.T) {
	srv := echoBackend(200, `{}`, nil)
	defer srv.Close()
	h := NewHandler([]Service{
		StaticService{ID: "s", Endpoint: srv.URL, APIBase: "/v1", Models: []string{"m"}, Healthy: true},
	}, testNode)

	for name, body := range map[string]string{
		"missing model": `{"messages":[]}`,
		"empty body":    ``,
		"invalid json":  `{oops`,
	} {
		rr := postJSON(t, h, "/v1/chat/completions", body, nil)
		if rr.Code != 400 {
			t.Fatalf("%s: got %d, want 400", name, rr.Code)
		}
		if rr.Header().Get(NodeHeader) != testNode {
			t.Fatalf("%s: missing %s header", name, NodeHeader)
		}
		decodeErr(t, rr.Body.Bytes()) // must stay OpenAI-shaped
	}
}

func TestUnhealthy503(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(200)
	}))
	defer srv.Close()
	h := NewHandler([]Service{
		StaticService{ID: "sick", Endpoint: srv.URL, APIBase: "/v1", Models: []string{"m"}, Healthy: false},
	}, testNode)

	rr := postJSON(t, h, "/v1/chat/completions", `{"model":"m"}`, nil)
	if rr.Code != 503 {
		t.Fatalf("got %d, want 503", rr.Code)
	}
	if rr.Header().Get(NodeHeader) != testNode {
		t.Fatalf("missing %s header", NodeHeader)
	}
	if hit {
		t.Fatal("unhealthy backend must not be contacted")
	}
}

func TestHeaderPassthroughAndHopByHopStripped(t *testing.T) {
	var rec recorded
	srv := echoBackend(200, `{}`, &rec)
	defer srv.Close()
	h := NewHandler([]Service{
		StaticService{ID: "s", Endpoint: srv.URL, APIBase: "/v1", Models: []string{"m"}, Healthy: true},
	}, testNode)

	rr := postJSON(t, h, "/v1/completions", `{"model":"m"}`, map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer sk-test",
		"User-Agent":    "mooch-test/0.1",
		"Connection":    "keep-alive",
		"Upgrade":       "websocket",
		"Proxy-Foo":     "bar",
	})
	if rr.Code != 200 {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	for k, want := range map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer sk-test",
		"User-Agent":    "mooch-test/0.1",
		"Content-Type":  "application/json",
	} {
		if rec.header.Get(k) != want {
			t.Fatalf("header %s = %q, want %q", k, rec.header.Get(k), want)
		}
	}
	for _, k := range []string{"Connection", "Upgrade", "Proxy-Foo"} {
		if rec.header.Get(k) != "" {
			t.Fatalf("hop-by-hop header %s must be stripped, got %q", k, rec.header.Get(k))
		}
	}
}

func TestUpstreamErrorPassthrough(t *testing.T) {
	srv := echoBackend(429, `{"error":{"message":"rate limited","type":"rate_limit"}}`, nil)
	defer srv.Close()
	h := NewHandler([]Service{
		StaticService{ID: "s", Endpoint: srv.URL, APIBase: "/v1", Models: []string{"m"}, Healthy: true},
	}, testNode)

	rr := postJSON(t, h, "/v1/chat/completions", `{"model":"m"}`, nil)
	if rr.Code != 429 {
		t.Fatalf("got %d, want upstream 429", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "rate limited") {
		t.Fatalf("upstream error body not preserved: %s", rr.Body.String())
	}
}

func TestStreamingSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: {\"chunk\":%d}\n\n", i)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()
	h := NewHandler([]Service{
		StaticService{ID: "s", Endpoint: srv.URL, APIBase: "/v1", Models: []string{"m"}, Healthy: true},
	}, testNode)

	rr := postJSON(t, h, "/v1/chat/completions", `{"model":"m","stream":true}`, map[string]string{"Accept": "text/event-stream"})
	if rr.Code != 200 {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	for i := 0; i < 3; i++ {
		if want := fmt.Sprintf(`"chunk":%d`, i); !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("missing SSE chunk %d in: %s", i, rr.Body.String())
		}
	}
}

func TestUpstreamDown502(t *testing.T) {
	// Bind then close to get a guaranteed-dead port.
	dead := httptest.NewServer(nil)
	url := dead.URL
	dead.Close()

	h := NewHandler([]Service{
		StaticService{ID: "gone", Endpoint: url, APIBase: "/v1", Models: []string{"m"}, Healthy: true},
	}, testNode)
	rr := postJSON(t, h, "/v1/chat/completions", `{"model":"m"}`, nil)
	if rr.Code != 502 {
		t.Fatalf("got %d, want 502", rr.Code)
	}
	if rr.Header().Get(NodeHeader) != testNode {
		t.Fatalf("missing %s header", NodeHeader)
	}
	decodeErr(t, rr.Body.Bytes())
}

func TestDynamicProvider(t *testing.T) {
	srv := echoBackend(200, `{"ok":true}`, nil)
	defer srv.Close()
	models := []string{"old"}
	h := NewHandlerFunc(func() []Service {
		return []Service{StaticService{ID: "s", Endpoint: srv.URL, APIBase: "/v1", Models: models, Healthy: true}}
	}, testNode)

	if rr := postJSON(t, h, "/v1/chat/completions", `{"model":"new"}`, nil); rr.Code != 400 {
		t.Fatalf("before reload: got %d, want 400", rr.Code)
	}
	models = []string{"new"} // simulate SIGHUP reload
	if rr := postJSON(t, h, "/v1/chat/completions", `{"model":"new"}`, nil); rr.Code != 200 {
		t.Fatalf("after reload: got %d, want 200", rr.Code)
	}
}

func TestUpstreamPathJoin(t *testing.T) {
	cases := []struct{ endpointPath, apiBase, incoming, want string }{
		{"", "/v1", "/v1/chat/completions", "/v1/chat/completions"},
		{"", "/v1/", "/v1/chat/completions", "/v1/chat/completions"},
		{"/base", "/v1", "/v1/chat/completions", "/base/v1/chat/completions"},
		{"", "/", "/v1/chat/completions", "/v1/chat/completions"},
		{"", "/v1", "/other/path", "/v1/other/path"},
	}
	for _, c := range cases {
		if got := upstreamPath(c.endpointPath, c.apiBase, c.incoming); got != c.want {
			t.Fatalf("upstreamPath(%q,%q,%q) = %q, want %q",
				c.endpointPath, c.apiBase, c.incoming, got, c.want)
		}
	}
}

func slogBuffer() (*bytes.Buffer, *slog.Logger) {
	var buf bytes.Buffer
	return &buf, slog.New(slog.NewJSONHandler(&buf, nil))
}

func lastLog(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.TrimSpace(buf.String())
	if lines == "" {
		t.Fatal("no log output")
	}
	// Take the last JSON line (forward + reject may log once per request).
	last := lines
	if idx := strings.LastIndex(lines, "\n"); idx >= 0 {
		last = lines[idx+1:]
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(last), &entry); err != nil {
		t.Fatalf("log is not JSON: %v (%q)", err, last)
	}
	return entry
}

func TestProxyForwardLogHasModelFields(t *testing.T) {
	srv := echoBackend(200, `{"ok":true}`, nil)
	defer srv.Close()
	buf, logger := slogBuffer()
	h := NewHandler([]Service{
		StaticService{ID: "s", Endpoint: srv.URL, APIBase: "/v1", Models: []string{"m"}, Healthy: true},
	}, testNode).(*Handler).WithLogger(logger)

	rr := postJSON(t, h, "/v1/chat/completions", `{"model":"m"}`, nil)
	if rr.Code != 200 {
		t.Fatalf("got %d, want 200", rr.Code)
	}
	entry := lastLog(t, buf)
	for _, key := range []string{"method", "path", "model", "node_id", "component"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("log missing key %q: %v", key, entry)
		}
	}
	if entry["model"] != "m" {
		t.Errorf("model = %v, want m", entry["model"])
	}
	if entry["component"] != "proxy" {
		t.Errorf("component = %v, want proxy", entry["component"])
	}
	if entry["node_id"] != testNode {
		t.Errorf("node_id = %v, want %s", entry["node_id"], testNode)
	}
}

func TestProxyUnknownModelLogHasModel(t *testing.T) {
	srv := echoBackend(200, `{}`, nil)
	defer srv.Close()
	buf, logger := slogBuffer()
	h := NewHandler([]Service{
		StaticService{ID: "s", Endpoint: srv.URL, APIBase: "/v1", Models: []string{"known"}, Healthy: true},
	}, testNode).(*Handler).WithLogger(logger)

	rr := postJSON(t, h, "/v1/chat/completions", `{"model":"nope"}`, nil)
	if rr.Code != 400 {
		t.Fatalf("got %d, want 400", rr.Code)
	}
	entry := lastLog(t, buf)
	if entry["model"] != "nope" {
		t.Errorf("model = %v, want nope", entry["model"])
	}
	if entry["component"] != "proxy" {
		t.Errorf("component = %v, want proxy", entry["component"])
	}
}

func TestProxyResponseLogHasPreviews(t *testing.T) {
	for _, tc := range []struct {
		name, reply, answer string
		status              int
	}{
		{"json", `{"choices":[{"message":{"content":"hello  there"}}]}`, "hello there", 200},
		{"sse", "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\ndata: [DONE]\n\n", "hello", 200},
		{"error", `{"error":{"message":"boom"}}`, "error: boom", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := echoBackend(tc.status, tc.reply, nil)
			defer srv.Close()
			buf, logger := slogBuffer()
			h := NewHandler([]Service{
				StaticService{ID: "s", Endpoint: srv.URL, APIBase: "/v1", Models: []string{"m"}, Healthy: true},
			}, testNode).(*Handler).WithLogger(logger)

			postJSON(t, h, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, nil)
			if !strings.Contains(buf.String(), `"prompt":"hi"`) {
				t.Errorf("forward log misses the prompt: %s", buf.String())
			}
			entry := lastLog(t, buf)
			if entry["msg"] != "proxy response" || entry["answer"] != tc.answer || entry["status"] != float64(tc.status) {
				t.Errorf("response log = %v, want answer %q status %d", entry, tc.answer, tc.status)
			}
		})
	}
}
