# PeerAI Central

Central tracks the live nodes on the tailnet and forwards OpenAI requests to a node that serves the requested model. All data is in memory.

## How a request flows

```text
tool ── POST /v1/chat/completions ──▶ central
         {"model": "qwen2.5", ...}      │ 1. read "model" from the body
                                        │ 2. registry.Lookup: live node, healthy
                                        │    service with that model
                                        │    (round-robin if many)
                                        ▼
                               node listen_addr ──▶ local backend (Ollama, vLLM, ...)
                                        │
tool ◀──────── response (streams flow chunk by chunk) ┘
```

Central does not change the request body. Central sends it to
`http://<listen_addr><same path>`.

## Node lifecycle

1. The node sends `POST /api/nodes/register` with its full payload: ID,
   Tailscale IP, `listen_addr`, and services with models and health.
2. The node sends `POST /api/nodes/heartbeat` with the same payload. Central
   treats both calls the same (upsert). Thus a heartbeat after a central restart
   registers the node again.
3. If no heartbeat comes for `-node-ttl` (default 45s), central stops routing to
   the node. A sweep removes expired nodes every `ttl/3`.

Central rejects a payload unless both conditions are true:

- The host of `listen_addr` equals `tailscale_ip`.
- The request comes from `tailscale_ip` or from loopback.

Thus a node cannot send central traffic to a different host.

## Access model

Central has no authentication. Any caller that can reach central can use all
live nodes and models. Central relies on the tailnet to limit who can connect.

## Join flow

1. `./build-nodes.sh` builds `peerai-node-<os>-<arch>` binaries into `dist/`.
2. A person on the tailnet opens `GET /join`. The page shows one command.
3. `curl -fsSL <central>/join.sh | sh` downloads the binary and a default
   config from central, and then starts the node. The config points to the
   central address that the person used.

## Packages

| Package | Job |
|---------|-----|
| `cmd/peerai-central` | Flags, Tailscale listen address, wiring, expiry loop, graceful shutdown |
| `internal/api` | Control-plane routes: node register/heartbeat/list, `/healthz` |
| `internal/router` | OpenAI routes: `/v1/models` and forwarding by `model` |
| `internal/registry` | In-memory node list: upsert, expiry, model lookup |
| `internal/join` | Join page, install script, default node config, node binaries |
| `internal/app` | Embedded test UI (`page.html`) at `/app` |
| `internal/feed` | Event lines: join, leave, route, request, response |
| `internal/tui` | Terminal dashboard: header, node list, event console |
| `internal/respond` | JSON responses and the OpenAI error envelope |

## Run

```sh
./build-nodes.sh                      # build node binaries into dist/ (for /join)
go run ./cmd/peerai-central           # listen on the Tailscale IPv4, port 8080
go run ./cmd/peerai-central -addr 127.0.0.1:8080   # local use only
```

| Flag | Default | Use |
|------|---------|-----|
| `-addr` | `:8080` | Listen address. An empty host means the Tailscale IPv4. |
| `-bin` | `dist` | Directory with node binaries for `/join`. |
| `-node-ttl` | `45s` | Remove a node after this time without a heartbeat. |
| `-debug` | `false` | Log each heartbeat. |
| `-plain` | `false` | Print the event feed as lines. Do not show the dashboard. |
| `-verbose` | `false` | Print plain log lines. Do not show the feed or the dashboard. |

Run these checks before a push:

```sh
gofmt -l .            # output must be empty
go vet ./...
go test ./... -count=1
```

## Endpoints

| Method | Path | Use |
|--------|------|-----|
| GET | `/healthz` | Status, node count, uptime |
| GET | `/api/nodes` | Live nodes |
| POST | `/api/nodes/register` | Node register |
| POST | `/api/nodes/heartbeat` | Node heartbeat (same payload as register) |
| GET | `/v1/models` | Models of the live nodes |
| POST | `/v1/chat/completions` | Forward to a node that serves the model |
| POST | `/v1/completions` | Forward to a node that serves the model |
| POST | `/v1/embeddings` | Forward to a node that serves the model |
| POST | `/v1/images/generations` | Forward to a node that serves the model |
| GET | `/join` | Join page (HTML) |
| GET | `/join.sh` | Node install script |
| GET | `/join/peerai-node.yaml` | Default node config |
| GET | `/join/bin/{os}-{arch}` | Node binary |
| GET | `/app` | Test UI for nodes and models |

## Examples

The examples use `C=http://127.0.0.1:8080`.

### Register / heartbeat

```sh
curl -X POST $C/api/nodes/register -d '{
  "node_id": "test-node",
  "name": "laptop",
  "tailscale_ip": "127.0.0.1",
  "listen_addr": "127.0.0.1:9100",
  "version": "dev",
  "services": [{
    "id": "ollama", "type": "llm", "provider": "ollama",
    "models": ["qwen2.5"], "supports_streaming": true, "healthy": true
  }]
}'
```

Use `/api/nodes/heartbeat` with the same body.

### OpenAI API

```sh
curl $C/v1/models

curl $C/v1/chat/completions -d '{
  "model": "qwen2.5",
  "messages": [{"role": "user", "content": "Hello"}]
}'

# Streaming
curl -N $C/v1/chat/completions -d '{
  "model": "qwen2.5", "stream": true,
  "messages": [{"role": "user", "content": "Hello"}]
}'

curl $C/v1/embeddings -d '{"model": "nomic-embed-text", "input": "Hello"}'
```

To use central from an OpenAI SDK, set the base URL to `$C/v1`. The SDK needs an API key value, but central ignores the value.

### Join

```sh
curl -fsSL $C/join.sh | sh               # install and start a node
curl $C/join/peerai-node.yaml            # default config
curl -O $C/join/bin/darwin-arm64         # binary: darwin|linux - amd64|arm64
```

## Errors

Errors use the OpenAI envelope:

```json
{"error": {"message": "no live node serves model \"x\"", "type": "model_not_found", "code": 404}}
```

| Code | Type | Cause |
|------|------|-------|
| 400 | `invalid_request_error` | Bad JSON, no `model`, or bad node payload |
| 404 | `model_not_found` | No live node serves the model, or no binary |
| 413 | `invalid_request_error` | Body is larger than 32 MiB |
| 502 | `service_unavailable` | Central cannot reach the node |
