# Configuration API

Package: `peer-ai-client/internal/config`

This document describes public functions, signatures, and usage.
It does not describe implementation details.

## `config.Load`

```go
func Load(path string) (Config, string, error)
```

Load and validate a YAML configuration file.
Pass an empty path to search the default paths.
Pass a non-empty path to select one file.

```go
cfg, path, err := config.Load("")
if err != nil {
	return err
}
fmt.Println("loaded", path, cfg.Network.CentralHost)
```

## `config.ResolvePath`

```go
func ResolvePath(path string) (string, error)
```

Return the selected configuration path.
An explicit path has priority.
An empty path checks `~/.config/peerai/node.yaml` and `./peerai-node.yaml`.

```go
path, err := config.ResolvePath("node.yaml")
if err != nil {
	return err
}
fmt.Println(path)
```

## `config.Config.Validate`

```go
func (c Config) Validate() error
```

Validate network fields and every configured service.
`config.Load` calls this function before it returns.

```go
if err := cfg.Validate(); err != nil {
	return fmt.Errorf("invalid configuration: %w", err)
}
```

## `config.Service.Validate`

```go
func (s Service) Validate() error
```

Validate one service before adding it to a configuration.

```go
service := config.Service{
	ID:       "local-llm",
	Name:     "Local LLM",
	Type:     config.ServiceTypeLLM,
	Provider: config.ProviderOllama,
	Endpoint: "http://127.0.0.1:11434",
	APIBase:  "/v1",
	Models:   []string{"qwen2.5"},
}
if err := service.Validate(); err != nil {
	return err
}
```

## Configuration types

```go
type Config struct {
	Network  Network
	Node     Node
	Services []Service
}

type Network struct {
	CentralHost       string
	CentralPort       int
	ListenHost        string
	ListenPort        int
	HeartbeatInterval time.Duration
}

type Node struct {
	Name string
	ID   string
}

type Service struct {
	ID                string
	Name              string
	Type              ServiceType
	Provider          Provider
	Endpoint          string
	APIBase           string
	Models            []string
	SupportsStreaming bool
	Healthy           bool
	Meta              map[string]any
}
```

Use `DefaultHeartbeatInterval` for the default interval:

```go
const DefaultHeartbeatInterval = 15 * time.Second
```

Use `ServiceType...` constants for `Service.Type`.
Use `Provider...` constants for `Service.Provider`.
