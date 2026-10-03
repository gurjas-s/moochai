package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validYAML = `network:
  central_host: 100.64.0.10
  central_port: 8080
  listen_host: 0.0.0.0
  listen_port: 9100
node:
  id: node-1
services:
  - id: local-llm
    name: Local LLM
    type: llm
    provider: ollama
    endpoint: http://127.0.0.1:11434
    api_base: /v1
    models: [qwen]
    supports_streaming: true
`

func TestLoadParsesCentralAndDefaultsHeartbeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.yaml")
	if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, resolved, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if resolved != path || cfg.Network.CentralHost != "100.64.0.10" ||
		cfg.Network.CentralPort != 8080 {
		t.Fatalf("config network = %+v, path = %q", cfg.Network, resolved)
	}
	if cfg.Network.HeartbeatInterval != DefaultHeartbeatInterval {
		t.Fatalf("heartbeat = %s, want %s", cfg.Network.HeartbeatInterval, DefaultHeartbeatInterval)
	}
}

func TestLoadParsesHeartbeatInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.yaml")
	data := strings.Replace(validYAML, "  listen_port: 9100\n", "  listen_port: 9100\n  heartbeat_interval: 2m\n", 1)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Network.HeartbeatInterval != 2*time.Minute {
		t.Fatalf("heartbeat = %s, want 2m0s", cfg.Network.HeartbeatInterval)
	}
}

func TestValidateRejectsMissingCentralHostAndEmptyModels(t *testing.T) {
	cfg := Config{
		Network: Network{CentralPort: 8080, ListenHost: "0.0.0.0", ListenPort: 9100, HeartbeatInterval: time.Second},
		Node:    Node{ID: "node-1"},
		Services: []Service{{ID: "svc", Name: "service", Type: "llm", Provider: "ollama",
			Endpoint: "http://127.0.0.1:1", APIBase: "/v1"}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "central_host") {
		t.Fatalf("Validate() error = %v, want central_host error", err)
	}
	cfg.Network.CentralHost = "100.64.0.10"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "models") {
		t.Fatalf("Validate() error = %v, want models error", err)
	}
}

func TestResolvePathExplicitPath(t *testing.T) {
	if got, err := ResolvePath("custom.yaml"); err != nil || got != "custom.yaml" {
		t.Fatalf("ResolvePath() = %q, %v", got, err)
	}
}

func TestServiceValidateAcceptsAllSupportedTypesAndProviders(t *testing.T) {
	for _, serviceType := range []ServiceType{
		ServiceTypeLLM, ServiceTypeEmbedding, ServiceTypeWhisper, ServiceTypeVision,
		ServiceTypeImage, ServiceTypeBrowser, ServiceTypeCodeExec, ServiceTypeVM,
		ServiceTypeCustom,
	} {
		for _, provider := range []Provider{
			ProviderVLLM, ProviderOllama, ProviderLlamaCPP, ProviderOpenAILike,
			ProviderWhisper, ProviderVision, ProviderImage, ProviderBrowser,
			ProviderCodeExec, ProviderVM, ProviderCustom,
		} {
			service := Service{
				ID: "service", Name: "Service", Type: serviceType, Provider: provider,
				Endpoint: "http://127.0.0.1:8000", APIBase: "/v1",
				Models: []string{"model"},
			}
			if err := service.Validate(); err != nil {
				t.Fatalf("Service.Validate() for type %q and provider %q: %v", serviceType, provider, err)
			}
		}
	}
}

func TestServiceValidateRejectsInvalidFields(t *testing.T) {
	service := Service{
		ID: "service", Name: "Service", Type: ServiceType("invalid"),
		Provider: ProviderOllama, Endpoint: "http://127.0.0.1:8000",
		APIBase: "/v1", Models: []string{"model"},
	}
	if err := service.Validate(); err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("Service.Validate() error = %v, want type error", err)
	}
}
