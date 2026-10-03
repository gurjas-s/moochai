# Central API

Package: `peer-ai-client/internal/central`

This document describes public functions, signatures, and usage.
It does not describe implementation details.

## `central.New`

```go
func New(host string, port int) (*Client, error)
```

Create a client for the central server.

```go
centralClient, err := central.New(
	cfg.Network.CentralHost,
	cfg.Network.CentralPort,
)
if err != nil {
	return err
}
```

## `central.Client.Register`

```go
func (c *Client) Register(ctx context.Context, payload Payload) error
```

Send the node registration payload to central.

```go
if err := centralClient.Register(ctx, payload); err != nil {
	return err
}
```

## `central.Client.Heartbeat`

```go
func (c *Client) Heartbeat(ctx context.Context, payload Payload) error
```

Send the current node state to central.

```go
if err := centralClient.Heartbeat(ctx, payload); err != nil {
	return err
}
```

## `central.Client.Run`

```go
func (c *Client) Run(
	ctx context.Context,
	payload func() Payload,
	interval time.Duration,
) error
```

Register the node, then send heartbeats until the context stops.
The payload function can return current service health values.

```go
err := centralClient.Run(ctx, func() central.Payload {
	return central.Payload{
		NodeID:      nodeID,
		Name:        cfg.Node.Name,
		TailscaleIP: tailscaleIP,
		ListenAddr:  listenAddr,
		Version:     central.Version,
		Services:    cfg.Services,
	}
}, cfg.Network.HeartbeatInterval)
if err != nil && !errors.Is(err, context.Canceled) {
	return err
}
```

## Central types

```go
type Identity struct {
	NodeID      string
	Name        string
	TailscaleIP string
	ListenAddr  string
}

type Payload struct {
	NodeID      string
	Name        string
	TailscaleIP string
	ListenAddr  string
	Version     string
	Services    []config.Service
}

type RetryPolicy struct {
	Initial time.Duration
	Max     time.Duration
}
```

Use `central.Version` for the node version:

```go
const Version = "v0.1.0-dev"
```
