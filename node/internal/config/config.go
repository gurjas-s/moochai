// Package config loads and validates node configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultHeartbeatInterval = 15 * time.Second

// Defaults for network fields that the config file can leave out.
const (
	DefaultCentralPort = 8080
	DefaultListenHost  = "0.0.0.0"
	DefaultListenPort  = 9100
)

type Config struct {
	Network  Network   `yaml:"network"`
	Node     Node      `yaml:"node"`
	Services []Service `yaml:"services"`
	// Backends is the simplified config form. Each entry names one local
	// backend. The node discovers models from the backend and builds the
	// broadcast Service objects. The broadcast shape stays unchanged.
	Backends []Backend `yaml:"backends"`
}

type Network struct {
	CentralHost       string        `yaml:"central_host"`
	CentralPort       int           `yaml:"central_port"`
	ListenHost        string        `yaml:"listen_host"`
	ListenPort        int           `yaml:"listen_port"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
}

func (n *Network) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		CentralHost       string `yaml:"central_host"`
		CentralPort       int    `yaml:"central_port"`
		ListenHost        string `yaml:"listen_host"`
		ListenPort        int    `yaml:"listen_port"`
		HeartbeatInterval string `yaml:"heartbeat_interval"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	n.CentralHost = raw.CentralHost
	n.CentralPort = raw.CentralPort
	n.ListenHost = raw.ListenHost
	n.ListenPort = raw.ListenPort
	n.HeartbeatInterval = DefaultHeartbeatInterval
	if strings.TrimSpace(raw.HeartbeatInterval) != "" {
		interval, err := time.ParseDuration(raw.HeartbeatInterval)
		if err != nil {
			return fmt.Errorf("parse network.heartbeat_interval: %w", err)
		}
		n.HeartbeatInterval = interval
	}
	return nil
}

type Node struct {
	Name string `yaml:"name"`
	ID   string `yaml:"id"`
}

type ServiceType string

const (
	ServiceTypeLLM       ServiceType = "llm"
	ServiceTypeEmbedding ServiceType = "embedding"
	ServiceTypeWhisper   ServiceType = "whisper"
	ServiceTypeVision    ServiceType = "vision"
	ServiceTypeImage     ServiceType = "image"
	ServiceTypeBrowser   ServiceType = "browser"
	ServiceTypeCodeExec  ServiceType = "code_exec"
	ServiceTypeVM        ServiceType = "vm"
	ServiceTypeCustom    ServiceType = "custom"
)

type Provider string

const (
	ProviderVLLM       Provider = "vllm"
	ProviderOllama     Provider = "ollama"
	ProviderLlamaCPP   Provider = "llamacpp"
	ProviderOpenAILike Provider = "openai-like"
	ProviderWhisper    Provider = "whisper"
	ProviderVision     Provider = "vision"
	ProviderImage      Provider = "image"
	ProviderBrowser    Provider = "browser"
	ProviderCodeExec   Provider = "code_exec"
	ProviderVM         Provider = "vm"
	ProviderCustom     Provider = "custom"
)

type Service struct {
	ID                string         `yaml:"id" json:"id"`
	Name              string         `yaml:"name" json:"name"`
	Type              ServiceType    `yaml:"type" json:"type"`
	Provider          Provider       `yaml:"provider" json:"provider"`
	Endpoint          string         `yaml:"endpoint" json:"endpoint"`
	APIBase           string         `yaml:"api_base" json:"api_base"`
	Models            []string       `yaml:"models" json:"models"`
	SupportsStreaming bool           `yaml:"supports_streaming" json:"supports_streaming"`
	Healthy           bool           `yaml:"healthy" json:"healthy"`
	Meta              map[string]any `yaml:"meta,omitempty" json:"meta,omitempty"`
}

var validServiceTypes = map[ServiceType]bool{
	ServiceTypeLLM: true, ServiceTypeEmbedding: true, ServiceTypeWhisper: true,
	ServiceTypeVision: true, ServiceTypeImage: true, ServiceTypeBrowser: true,
	ServiceTypeCodeExec: true, ServiceTypeVM: true, ServiceTypeCustom: true,
}

var validProviders = map[Provider]bool{
	ProviderVLLM: true, ProviderOllama: true, ProviderLlamaCPP: true,
	ProviderOpenAILike: true, ProviderWhisper: true, ProviderVision: true,
	ProviderImage: true, ProviderBrowser: true, ProviderCodeExec: true,
	ProviderVM: true, ProviderCustom: true,
}

// DefaultAPIBase is used when a backend omits api_base.
const DefaultAPIBase = "/v1"

// Backend names one local model service. The node fills models and meta
// with discovery. The user only chooses the endpoint and the expose list.
type Backend struct {
	Name string `yaml:"name"`
	// Endpoint is the backend HTTP URL (e.g. http://127.0.0.1:8000).
	Endpoint string `yaml:"endpoint"`
	// APIBase defaults to /v1 when empty.
	APIBase string `yaml:"api_base"`
	// Provider enables extras. Empty means generic OpenAI (models list
	// only). Set ollama for /api/show or llamacpp for /props.
	Provider Provider `yaml:"provider"`
	// Type defaults to llm when empty.
	Type ServiceType `yaml:"type"`
	// Expose lists model IDs to advertise. Empty or ["*"] means all
	// discovered models. Explicit names filter the list down.
	Expose []string `yaml:"expose"`
	// SupportsStreaming defaults to true. Set false to disable it.
	SupportsStreaming *bool `yaml:"supports_streaming"`
	// ModelMeta holds manual overrides per model ID. Config wins over
	// discovery. Use it when the backend hides context_window.
	ModelMeta map[string]ModelMeta `yaml:"model_meta"`
}

// ModelMeta overrides discovery for one model.
type ModelMeta struct {
	ContextWindow *int `yaml:"context_window"`
	MaxTokens     *int `yaml:"max_tokens"`
	Capabilities  []string `yaml:"capabilities"`
}

// ResolvedAPIBase returns APIBase or the default.
func (b Backend) ResolvedAPIBase() string {
	if b.APIBase == "" {
		return DefaultAPIBase
	}
	return b.APIBase
}

// ResolvedType returns Type or the default.
func (b Backend) ResolvedType() ServiceType {
	if b.Type == "" {
		return ServiceTypeLLM
	}
	return b.Type
}

// ResolvedProvider returns Provider or the generic default.
func (b Backend) ResolvedProvider() Provider {
	if b.Provider == "" {
		return ProviderOpenAILike
	}
	return b.Provider
}

// ResolvedStreaming returns SupportsStreaming or true when unset.
func (b Backend) ResolvedStreaming() bool {
	if b.SupportsStreaming == nil {
		return true
	}
	return *b.SupportsStreaming
}

// Validate checks that a backend entry can be discovered.
func (b Backend) Validate() error {
	if strings.TrimSpace(b.Name) == "" {
		return errors.New("name is required")
	}
	endpoint, err := url.Parse(b.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return errors.New("endpoint must be a valid HTTP URL")
	}
	if endpoint.User != nil {
		return errors.New("endpoint must not contain user information")
	}
	if b.APIBase != "" && !strings.HasPrefix(b.APIBase, "/") {
		return errors.New("api_base must start with /")
	}
	if b.Provider != "" && !validProviders[b.Provider] {
		return fmt.Errorf("provider %q is invalid", b.Provider)
	}
	if b.Type != "" && !validServiceTypes[b.Type] {
		return fmt.Errorf("type %q is invalid", b.Type)
	}
	for _, name := range b.Expose {
		if strings.TrimSpace(name) == "" {
			return errors.New("expose must not contain empty values")
		}
	}
	for id, meta := range b.ModelMeta {
		if strings.TrimSpace(id) == "" {
			return errors.New("model_meta must not contain empty model IDs")
		}
		if meta.ContextWindow != nil && *meta.ContextWindow <= 0 {
			return fmt.Errorf("model_meta %q context_window must be positive", id)
		}
		if meta.MaxTokens != nil && *meta.MaxTokens <= 0 {
			return fmt.Errorf("model_meta %q max_tokens must be positive", id)
		}
	}
	return nil
}

// Load discovers or loads a configuration file. An explicit path is required
// when path is not empty; otherwise default paths are checked in order.
// Load fills missing network ports and the listen host with defaults.
func Load(path string) (Config, string, error) {
	resolved, err := ResolvePath(path)
	if err != nil {
		return Config{}, "", err
	}
	data, err := os.ReadFile(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, "", fmt.Errorf("config file %q does not exist. Check the --config path", resolved)
	}
	if err != nil {
		return Config{}, "", fmt.Errorf("read config %q: %w", resolved, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, "", fmt.Errorf("parse config %q: %w", resolved, err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, "", fmt.Errorf("config %q is not valid:\n  %s", resolved, strings.ReplaceAll(err.Error(), "\n", "\n  "))
	}
	return cfg, resolved, nil
}

func (c *Config) applyDefaults() {
	if c.Network.CentralPort == 0 {
		c.Network.CentralPort = DefaultCentralPort
	}
	if strings.TrimSpace(c.Network.ListenHost) == "" {
		c.Network.ListenHost = DefaultListenHost
	}
	if c.Network.ListenPort == 0 {
		c.Network.ListenPort = DefaultListenPort
	}
	if c.Network.HeartbeatInterval == 0 {
		c.Network.HeartbeatInterval = DefaultHeartbeatInterval
	}
}

// DefaultPaths returns the config paths that the node checks when --config is not given.
func DefaultPaths() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home directory: %w", err)
	}
	paths := []string{filepath.Join(home, ".config", "mooch", "node.yaml")}
	// macOS keeps the user config directory in ~/Library/Application Support.
	if dir, err := os.UserConfigDir(); err == nil && dir != filepath.Join(home, ".config") {
		paths = append(paths, filepath.Join(dir, "mooch", "node.yaml"))
	}
	// The join script installs the config here.
	return append(paths, filepath.Join(home, ".mooch", "mooch-node.yaml"), "mooch-node.yaml"), nil
}

// ResolvePath returns an explicit path or the first existing default path.
func ResolvePath(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return path, nil
	}
	candidates, err := DefaultPaths()
	if err != nil {
		return "", err
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("check config %q: %w", candidate, err)
		}
	}
	return "", fmt.Errorf(`no config file found. The node checked these paths:
  %s
Do one of these steps:
  - Run the join command that central shows: curl -fsSL http://<central>:8080/join.sh | sh
  - Copy mooch-node.yaml.example to mooch-node.yaml and set network.central_host.
  - Give the path of a config file: mooch-node --config <path>`, strings.Join(candidates, "\n  "))
}

// Validate returns all problems of the config, one for each line.
func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.Network.CentralHost) == "" {
		errs = append(errs, errors.New("network.central_host is missing. Set it to the Tailscale IPv4 of central, for example 100.64.0.10"))
	}
	if c.Network.CentralPort < 1 || c.Network.CentralPort > 65535 {
		errs = append(errs, errors.New("network.central_port must be between 1 and 65535"))
	}
	if strings.TrimSpace(c.Network.ListenHost) == "" {
		errs = append(errs, errors.New("network.listen_host is missing"))
	}
	if c.Network.ListenPort < 1 || c.Network.ListenPort > 65535 {
		errs = append(errs, errors.New("network.listen_port must be between 1 and 65535"))
	}
	if c.Network.HeartbeatInterval <= 0 {
		errs = append(errs, errors.New("network.heartbeat_interval must be positive"))
	}
	seen := make(map[string]bool, len(c.Services))
	for i, service := range c.Services {
		if err := service.validate(i, seen); err != nil {
			errs = append(errs, err)
		}
	}
	seenBackends := make(map[string]bool, len(c.Backends))
	for i, backend := range c.Backends {
		if seenBackends[backend.Name] {
			errs = append(errs, fmt.Errorf("backends[%d].name %q is duplicated", i, backend.Name))
		}
		if err := backend.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("backends[%d]: %w", i, err))
		}
		seenBackends[backend.Name] = true
	}
	return errors.Join(errs...)
}

func (s Service) validate(index int, seen map[string]bool) error {
	prefix := fmt.Sprintf("services[%d]", index)
	if seen[s.ID] {
		return fmt.Errorf("%s.id %q is duplicated", prefix, s.ID)
	}
	if err := s.Validate(); err != nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	seen[s.ID] = true
	return nil
}

// Validate checks that a service can be advertised to central.
func (s Service) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return errors.New("id is required")
	}
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("name is required")
	}
	if !validServiceTypes[s.Type] {
		return fmt.Errorf("type %q is invalid", s.Type)
	}
	if !validProviders[s.Provider] {
		return fmt.Errorf("provider %q is invalid", s.Provider)
	}
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return errors.New("endpoint must be a valid HTTP URL")
	}
	if endpoint.User != nil {
		return errors.New("endpoint must not contain user information")
	}
	if strings.TrimSpace(s.APIBase) == "" {
		return errors.New("api_base is required")
	}
	if len(s.Models) == 0 {
		return errors.New("models must not be empty")
	}
	for _, model := range s.Models {
		if strings.TrimSpace(model) == "" {
			return errors.New("models must not contain empty values")
		}
	}
	return nil
}
