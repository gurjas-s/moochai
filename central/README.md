# Mooch.ai Central

Central tracks the live nodes on the tailnet and forwards OpenAI requests to a node that serves the requested model. The node list is in memory. Usage history is in an optional TimescaleDB database (see [Analytics](#analytics)).

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

1. `make build` builds `mooch-node-<os>-<arch>` binaries into `dist/`.
2. A person on the tailnet opens `GET /join`. The page shows one command.
3. `curl -fsSL <central>/join.sh | sh` downloads the binary and a default
   config from central, and then starts the node. The config points to the
   central address that the person used.

## Packages

| Package | Job |
|---------|-----|
| `cmd/mooch-central` | Flags, Tailscale listen address, wiring, expiry loop, graceful shutdown |
| `internal/api` | Control-plane routes: node register/heartbeat/list, `/healthz` |
| `internal/router` | OpenAI routes: `/v1/models` and forwarding by `model` |
| `internal/registry` | In-memory node list: upsert, expiry, model lookup |
| `internal/join` | Join page, install script, default node config, node binaries |
| `internal/app` | Embedded test UI (`page.html`) at `/app` |
| `internal/feed` | Event lines: join, leave, route, request, response |
| `internal/tui` | Terminal dashboard: logo, NODES and MODELS boxes, event console |
| `internal/respond` | JSON responses and the OpenAI error envelope |
| `internal/stats` | Usage history in TimescaleDB, analytics routes, `/analytics` page, load for fair routing |

## Run

Use the Makefile in `central/`:

```sh
make build        # build central and the node binaries into dist/, so /join can serve them
make start        # build central, start TimescaleDB, then start central with analytics
make start ANALYTICS=0  # start central without analytics; Docker is not necessary
make check        # format check, vet, and tests
make help         # list all targets
```

`make start` listens on the Tailscale IPv4, port 8080. Central also listens on `127.0.0.1` at the same port,
so programs on the central computer (for example the demo scripts) can connect.
Set `ADDR` to change the listen address, for example `make start ADDR=127.0.0.1:8080` for tests without Tailscale.
Set `FLAGS` to give more flags to central, for example `make start FLAGS=-plain` or `make start FLAGS=-details`.

You can also run the commands directly:

```sh
make build                        # build node binaries into dist/ (for /join)
go run ./cmd/mooch-central           # listen on the Tailscale IPv4, port 8080
go run ./cmd/mooch-central -addr 127.0.0.1:8080   # local use only
```

| Flag | Default | Use |
|------|---------|-----|
| `-addr` | `:8080`, or `MOOCH_ADDR` | Listen address. An empty host means the Tailscale IPv4. Central also listens on `127.0.0.1` at the same port. |
| `-analytics-addr` | `127.0.0.1:3000` | Listen address of the analytics page and its API. Keep it on loopback, so only central can open it. |
| `-bin` | `dist` | Directory with node binaries for `/join`. |
| `-node-ttl` | `45s` | Remove a node after this time without a heartbeat. |
| `-debug` | `false` | Log each heartbeat. |
| `-plain` | `false` | Print the event feed as lines. Do not show the dashboard. |
| `-details` | `false` | Show the method, the path, and the status on REQUEST and RESPONSE lines. |
| `-verbose` | `false` | Print plain log lines. Do not show the feed or the dashboard. |

Run `make check` before a push.

`python3 fakenode.py <name> <port> <delay>` starts a fake node on loopback for local tests.
The fake node serves the model `qwen` and waits `<delay>` seconds for each request.

## Analytics

Central can keep a history of requests and heartbeats in TimescaleDB, the open-source database of Tiger Data.
Analytics is optional. Without a database, central works as before and does not need Docker.
The analytics routes then return 503, and central uses round-robin routing.
Use `make start ANALYTICS=0`, or start central without `MOOCH_DB_URL`.

```sh
make db-up        # start TimescaleDB in Docker on 127.0.0.1:5432
make start        # start the database and central with MOOCH_DB_URL set to the local database (also does db-up)
make test-db      # run the database tests in a temporary database
make db-down      # stop the database; the data stays in the mooch-db volume
```

Open `http://127.0.0.1:3000/analytics` on the central machine. The tailnet cannot open the analytics page or its API.
The tailnet gets only `GET /leaderboard.json` on port 8080.

To use another database, set `MOOCH_DB_URL` before you start central.

Central also reads `MOOCH_DB_URL` and `MOOCH_ADDR` from a `.env` file in the directory where it starts.
Copy `.env.example` to `.env` to start. Then `go run ./cmd/mooch-central` needs no flags.
`.env` is in `.gitignore`. A variable that you set in the shell wins over the file.
For Tiger Cloud, add `sslmode=require` to the URL.

### What central keeps

| Table | Rows |
|-------|------|
| `requests` | One row for each routed request: time, requester, node, model, path, status, duration, bytes, token counts |
| `heartbeats` | One row for each register or heartbeat: time, node, model count |
| `usage_hourly` | Continuous aggregate: requests, OK count, work, and tokens per hour, requester, node, and model |

- Central compresses `requests` chunks after 7 days and deletes them after 90 days.
- Central deletes `heartbeats` after 30 days.
- **Tokens** come from the `usage` field of each backend response. A response without `usage` counts zero tokens.
  For an Ollama chat stream, central asks the backend for usage. Central does not change other request bodies.
- The **balance** of a participant is tokens served minus tokens used.
- Fair routing uses **work**: the time that a node spends on requests.
- A participant is a node, or a tool at an IP that is not a node.

### Fair routing

When more than one live node serves a model, central sends the request to the node with the least work in the last hour.
Two nodes with equal work share requests in rotation.
Central reads the work from the database every 15 seconds.
Central also adds the work of each request when the request ends.
Without a database, central uses rotation only.

### Security

- The database port listens on `127.0.0.1` only. Nodes and the tailnet cannot connect to the database.
- Only central has `MOOCH_DB_URL`. Central reads it from the environment, not from a flag, so `ps` does not show it.
- The node does not change. The node payload does not change.
- Central does not keep prompt text or response text. The `requests` table has metadata only.
- The analytics routes use the tailnet listener of central, so only tailnet members can see them.
- When the database stops, central continues to route requests. Central drops the rows and logs one warning.

## Endpoints

| Method | Path | Use |
|--------|------|-----|
| GET | `/healthz` | Status, node count, uptime |
| GET | `/health` | Health check for vLLM discovery |
| GET | `/api/nodes` | Live nodes |
| POST | `/api/nodes/register` | Node register |
| POST | `/api/nodes/heartbeat` | Node heartbeat (same payload as register) |
| GET | `/v1/models` | Models of the live nodes in vLLM format |
| POST | `/v1/chat/completions` | Forward to a node that serves the model |
| POST | `/v1/completions` | Forward to a node that serves the model |
| POST | `/v1/embeddings` | Forward to a node that serves the model |
| POST | `/v1/images/generations` | Forward to a node that serves the model |
| GET | `/api/tags` | List live models for Ollama clients |
| POST | `/api/show` | Return model metadata for Ollama clients |
| POST | `/api/chat` | Translate Ollama chat to OpenAI chat |
| GET | `/join` | Join page (HTML) |
| GET | `/join.sh` | Node install script |
| GET | `/join/mooch-node.yaml` | Default node config |
| GET | `/join/bin/{os}-{arch}` | Node binary |
| GET | `/app` | Test UI for nodes and models |
| GET | `/analytics` | Analytics page: token share, balance, live rate, models. Updates every 0.5 s. |
| GET | `/analytics/chart.js` | Chart.js 4.5.1 (MIT), embedded in central for the page |
| GET | `/api/analytics/summary?window=` | Give and take of each participant. `window` is `1h`, `24h`, `7d`, or `30d`. |
| GET | `/api/analytics/models?window=` | Use of each model |
| GET | `/leaderboard.json` | Last 24 hours in the format of `frontend/public/leaderboard.example.json` |

Zed can use the Ollama-compatible surface with automatic model discovery:

```json
{
  "language_models": {
    "ollama": {
      "api_url": "http://100.64.0.10:8080",
      "auto_discover": true
    }
  }
}
```

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
To add central as a provider in opencode or other tools, read [Add central as a provider](../README.md#4-add-central-as-a-provider).

### Join

```sh
curl -fsSL $C/join.sh | sh               # install and start a node
curl $C/join/mooch-node.yaml            # default config
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
