# Identity API

Package: `peer-ai-client/internal/identity`

This document describes public functions, signatures, and usage.
It does not describe implementation details.

## `identity.DefaultNodeIDPath`

```go
func DefaultNodeIDPath() (string, error)
```

Return the default path for the persistent node ID.

```go
path, err := identity.DefaultNodeIDPath()
if err != nil {
	return err
}
fmt.Println(path)
```

## `identity.LoadOrCreateNodeID`

```go
func LoadOrCreateNodeID(path string) (string, error)
```

Load a node ID from `path`.
Pass an empty path to use the default path.

```go
nodeID, err := identity.LoadOrCreateNodeID("")
if err != nil {
	return err
}
fmt.Println(nodeID)
```

## `identity.ResolveTailscaleIPv4`

```go
func ResolveTailscaleIPv4(ctx context.Context) (net.IP, error)
```

Return the local node's Tailscale IPv4 address.

```go
ip, err := identity.ResolveTailscaleIPv4(ctx)
if err != nil {
	return err
}
fmt.Println(ip.String())
```

## `identity.ResolveTailscaleIPv4WithRunner`

```go
func ResolveTailscaleIPv4WithRunner(
	ctx context.Context,
	runner CommandRunner,
) (net.IP, error)
```

Resolve the Tailscale IPv4 with a supplied command runner.
Use this function in tests or when the caller controls command execution.

```go
runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return []byte("100.64.0.5\n"), nil
}
ip, err := identity.ResolveTailscaleIPv4WithRunner(ctx, runner)
```

The runner signature is:

```go
type CommandRunner func(context.Context, string, ...string) ([]byte, error)
```

## `identity.IsTailscaleIPv4`

```go
func IsTailscaleIPv4(ip net.IP) bool
```

Report whether an IP belongs to the Tailscale IPv4 range.

```go
if !identity.IsTailscaleIPv4(ip) {
	return errors.New("IP is not a Tailscale IPv4 address")
}
```

## `identity.BuildListenAddr`

```go
func BuildListenAddr(tailscaleIP string, port int) (string, error)
```

Build the address that the node advertises to central.

```go
listenAddr, err := identity.BuildListenAddr(ip.String(), listenPort)
if err != nil {
	return err
}
fmt.Println(listenAddr)
```
