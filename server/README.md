# PeerAI Central

Central keeps a list of live nodes and routes OpenAI-compatible requests to them.

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

The examples below use `C=http://127.0.0.1:8080`.

## Endpoints

| Method | Path | Use |
|--------|------|-----|
| GET | `/healthz` | Status, node count, uptime |
| GET | `/api/nodes` | All live nodes |
| POST | `/api/nodes/register` | Node register |
| POST | `/api/nodes/heartbeat` | Node heartbeat (same payload as register) |
| GET | `/v1/models` | All models on live nodes |
| POST | `/v1/chat/completions` | Forward to a node |
| POST | `/v1/completions` | Forward to a node |
| POST | `/v1/embeddings` | Forward to a node |
| POST | `/v1/images/generations` | Forward to a node |
| GET | `/join` | Join page (HTML) |
| GET | `/join.sh` | Node install script |
| GET | `/join/peerai-node.yaml` | Default node config |
| GET | `/join/bin/{os}-{arch}` | Node binary |

### Health and nodes

```sh
curl $C/healthz
curl $C/api/nodes
```

### Register / heartbeat

The request must come from `tailscale_ip` or loopback.
The host of `listen_addr` must equal `tailscale_ip`.

```sh
curl -X POST $C/api/nodes/register -H 'Content-Type: application/json' -d '{
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

Central picks a node by the `model` field. Requests rotate between nodes with the same model.

```sh
curl $C/v1/models

curl $C/v1/chat/completions -H 'Content-Type: application/json' -d '{
  "model": "qwen2.5",
  "messages": [{"role": "user", "content": "Hello"}]
}'

# Streaming
curl -N $C/v1/chat/completions -H 'Content-Type: application/json' -d '{
  "model": "qwen2.5", "stream": true,
  "messages": [{"role": "user", "content": "Hello"}]
}'

curl $C/v1/completions -H 'Content-Type: application/json' \
  -d '{"model": "qwen2.5", "prompt": "Hello"}'

curl $C/v1/embeddings -H 'Content-Type: application/json' \
  -d '{"model": "nomic-embed-text", "input": "Hello"}'

curl $C/v1/images/generations -H 'Content-Type: application/json' \
  -d '{"model": "sdxl", "prompt": "a cat"}'
```

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
