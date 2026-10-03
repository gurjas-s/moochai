package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testClient(server *httptest.Server) *http.Client {
	return server.Client()
}

func TestDiscoverBackendGenericReadsMaxModelLen(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen2.5-32b","max_model_len":32768},{"id":"embed"}]}`))
	}))
	defer backend.Close()

	b := Backend{Name: "main", Endpoint: backend.URL}
	services, err := DiscoverBackend(context.Background(), testClient(backend), b)
	if err != nil {
		t.Fatalf("DiscoverBackend() error = %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("services = %d, want 2", len(services))
	}
	if services[0].ID != "main-qwen2.5-32b" || len(services[0].Models) != 1 {
		t.Fatalf("service[0] = %+v", services[0])
	}
	got, ok := services[0].Meta["context_window"]
	if !ok || got != 32768 {
		t.Fatalf("context_window = %v, want 32768", services[0].Meta)
	}
	if services[0].APIBase != "/v1" || !services[0].Healthy || !services[0].SupportsStreaming {
		t.Fatalf("defaults = %+v", services[0])
	}
	if services[1].Meta != nil {
		t.Fatalf("meta = %v, want nil when backend hides context", services[1].Meta)
	}
}

func TestDiscoverBackendPrefersLiveNCtxOverTrain(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m","meta":{"n_ctx":8192,"n_ctx_train":40960}}]}`))
	}))
	defer backend.Close()

	b := Backend{Name: "main", Endpoint: backend.URL}
	services, err := DiscoverBackend(context.Background(), testClient(backend), b)
	if err != nil {
		t.Fatalf("DiscoverBackend() error = %v", err)
	}
	if services[0].Meta["context_window"] != 8192 {
		t.Fatalf("meta = %v, want live n_ctx 8192", services[0].Meta)
	}
}

func TestDiscoverBackendAllowlistFilters(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
	}))
	defer backend.Close()

	b := Backend{Name: "main", Endpoint: backend.URL, Expose: []string{"b"}}
	services, err := DiscoverBackend(context.Background(), testClient(backend), b)
	if err != nil {
		t.Fatalf("DiscoverBackend() error = %v", err)
	}
	if len(services) != 1 || services[0].Models[0] != "b" {
		t.Fatalf("services = %+v, want only b", services)
	}
}

func TestDiscoverBackendManualOverrideWins(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m","max_model_len":4096}]}`))
	}))
	defer backend.Close()

	ctx := 32768
	b := Backend{Name: "main", Endpoint: backend.URL,
		ModelMeta: map[string]ModelMeta{"m": {ContextWindow: &ctx}}}
	services, err := DiscoverBackend(context.Background(), testClient(backend), b)
	if err != nil {
		t.Fatalf("DiscoverBackend() error = %v", err)
	}
	if services[0].Meta["context_window"] != 32768 {
		t.Fatalf("meta = %v, want override 32768", services[0].Meta)
	}
}

func TestDiscoverBackendLlamaPropsWinsOverTrain(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen","meta":{"n_ctx_train":262144}}]}`))
	})
	mux.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":65536}}`))
	})
	backend := httptest.NewServer(mux)
	defer backend.Close()

	b := Backend{Name: "llama", Endpoint: backend.URL, Provider: ProviderLlamaCPP}
	services, err := DiscoverBackend(context.Background(), testClient(backend), b)
	if err != nil {
		t.Fatalf("DiscoverBackend() error = %v", err)
	}
	if services[0].Meta["context_window"] != 65536 {
		t.Fatalf("meta = %v, want live n_ctx 65536", services[0].Meta)
	}
}

func TestDiscoverBackendOllamaShow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen2.5"}]}`))
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"details":{"family":"qwen2"},"model_info":{"general.architecture":"qwen2","qwen2.context_length":131072}}`))
	})
	backend := httptest.NewServer(mux)
	defer backend.Close()

	b := Backend{Name: "ollama", Endpoint: backend.URL, Provider: ProviderOllama}
	services, err := DiscoverBackend(context.Background(), testClient(backend), b)
	if err != nil {
		t.Fatalf("DiscoverBackend() error = %v", err)
	}
	if services[0].Meta["context_window"] != 131072 {
		t.Fatalf("meta = %v, want 131072", services[0].Meta)
	}
}

func TestDiscoverBackendFailsWhenListFails(t *testing.T) {
	backend := httptest.NewServer(http.NotFoundHandler())
	defer backend.Close()

	b := Backend{Name: "main", Endpoint: backend.URL}
	if _, err := DiscoverBackend(context.Background(), testClient(backend), b); err == nil {
		t.Fatal("DiscoverBackend() error = nil, want error")
	}
}

func TestDiscoverAllSkipsFailedBackends(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer good.Close()
	bad := httptest.NewServer(http.NotFoundHandler())
	defer bad.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	services := DiscoverAll(ctx, good.Client(), []Backend{
		{Name: "good", Endpoint: good.URL},
		{Name: "bad", Endpoint: bad.URL},
	})
	if len(services) != 1 || services[0].ID != "good-m" {
		t.Fatalf("services = %+v, want one good-m", services)
	}
}

func TestBackendValidateRejectsBadFields(t *testing.T) {
	for _, backend := range []Backend{
		{},
		{Name: "b", Endpoint: "ftp://x"},
		{Name: "b", Endpoint: "http://127.0.0.1:8000", APIBase: "v1"},
		{Name: "b", Endpoint: "http://127.0.0.1:8000", Provider: "nope"},
		{Name: "b", Endpoint: "http://127.0.0.1:8000", Type: "nope"},
		{Name: "b", Endpoint: "http://127.0.0.1:8000", Expose: []string{""}},
	} {
		if err := backend.Validate(); err == nil {
			t.Fatalf("Validate() = nil for %+v, want error", backend)
		}
	}
}

func writeTempYAML(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.yaml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAcceptsBackendsOnly(t *testing.T) {
	data := `network:
  central_host: 100.64.0.10
  central_port: 8080
  listen_host: 0.0.0.0
  listen_port: 9100
node:
  id: node-1
backends:
  - name: main
    endpoint: http://127.0.0.1:8000
    provider: llamacpp
    expose: ["*"]
`
	path := writeTempYAML(t, data)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0].ResolvedAPIBase() != "/v1" {
		t.Fatalf("backends = %+v", cfg.Backends)
	}
}
