# Mooch.ai Node – Minimal Requirements (MVP)

Goal: Build a single Go binary that advertises local AI services to the central Mooch.ai server over Tailscale, and forwards routed OpenAI-compatible requests to the correct local backend.

**Broadcasting to central = CUSTOM JSON. The runtime API for tools = OpenAI-compatible.**

## 1. Identity & Tailscale

- [ ] **Get Tailscale IP** – Resolve own Tailscale IPv4 using `tailscale.com/client/tailscale` or `tailscale ip -4`. Only use Tailscale IPs for client<->central comms.
- [ ] **Stable node_id** – Generate once, persist to `~/.config/mooch/node.id`. Fallback: `hostname` + short random suffix if missing.
- [ ] **Own listen addr** – Know `tailscale_ip:listen_port` to send in register/heartbeat.

## 2. Config

- [ ] **CLI flag** – `--config <path>` to load config file.
- [ ] **Default paths** – Also check `~/.config/mooch/node.yaml`, `./mooch-node.yaml` if flag not given.
- [ ] **Required fields** – `network.central_host`, `network.central_port`, `network.listen_host`, `network.listen_port`
- [ ] **Optional with defaults** – `network.heartbeat_interval` (default `15s`), `node.name` (fallback to hostname), `node.id` (fallback to generated)
- [ ] **Services list** – `services[]` must be valid (see #3)
- [ ] **Validation** – Reject invalid URLs, empty `id`, empty `models[]`, or missing required fields on startup
- [ ] **YAML format** – Use a simple YAML parser (`gopkg.in/yaml.v3`)

## 3. Service Broadcast (Custom, Node -> Central)

The client **must** send this custom payload on `register` and `heartbeat`. Do **not** scrape `/v1/models` from local backends and forward it as-is.

Each service object must include:

- [ ] `id` – Unique per service on this node (e.g. `qwen-32b-vllm`)
- [ ] `name` – Human readable name
- [ ] `type` – One of: `llm`, `embedding`, `whisper`, `vision`, `image`, `browser`, `code_exec`, `vm`, `custom`
- [ ] `provider` – One of: `vllm`, `ollama`, `llamacpp`, `openai-like`, `whisper`, `vision`, `image`, `browser`, `code_exec`, `vm`, `custom`
- [ ] `endpoint` – Local backend HTTP URL (e.g. `http://127.0.0.1:8000`)
- [ ] `api_base` – Base path for OpenAI routes (e.g. `/v1`)
- [ ] `models` – Array of model IDs this backend actually serves (e.g. `["qwen2.5-32b-instruct"]`)
- [ ] `supports_streaming` – `true/false`
- [ ] `healthy` – `true/false` (reflect local backend reachability)
- [ ] `meta` – Optional (e.g. `context_window`, `max_tokens`)

## 4. Control Plane: Register & Heartbeat

- [ ] **Register** – `POST http://<CENTRAL_TS_IP>:<PORT>/api/nodes/register`
- [ ] **Heartbeat** – `POST http://<CENTRAL_TS_IP>:<PORT>/api/nodes/heartbeat` every `heartbeat_interval`
- [ ] **Payloads** – Send exactly: `node_id`, `name`, `tailscale_ip`, `listen_addr`, `version`, `services`
- [ ] **Minimal ack** – Treat `200 OK` as success
- [ ] **Retries** – On failure use exponential backoff (max ~1–2 min). Never busy-loop.
- [ ] **Auto-retry** – If central goes down mid-run, keep retrying register/heartbeat until it comes back
- [ ] **Idempotent** – Re-registering on restart is fine; central decides deduplication

**Register body example:**

```json
{
  "node_id": "alice-rtx",
  "name": "Alice RTX 4090",
  "tailscale_ip": "100.64.0.5",
  "listen_addr": "100.64.0.5:9100",
  "version": "0.1.0",
  "services": [...]
}
```

## 5. Local Routed Listener (Data Plane)

- [ ] **Bind** – Listen on `listen_host:listen_port`. Prefer binding to Tailscale-reachable address (`0.0.0.0` is acceptable if only Tailscale can reach via ACLs)
- [ ] **Health endpoint** – `GET /healthz` returns `200` with JSON: `{ "node_id": "...", "tailscale_ip": "...", "uptime": "...", "version": "..." }`
- [ ] **OpenAI routes** – Accept at minimum: `POST /v1/chat/completions`, `POST /v1/completions`
- [ ] **Extra routes (if configured)** – `POST /v1/embeddings`, `POST /v1/audio/transcriptions`, `POST /v1/images/generations`, `GET /v1/models` (forward if exists, not required)
- [ ] **Only accept routed traffic** – Don't add any unauthenticated public UI. Keep minimal.

## 6. Request Forwarding (Node -> Local Backend)

- [ ] **Route by model** – On incoming OpenAI request, find which configured service has `request.model` in its `models[]`. Use that service.
- [ ] **Unknown model** – If no match, return `400 Bad Request` with clear JSON error + `X-Mooch-Node: <node_id>`
- [ ] **Build upstream URL** – `service.endpoint + service.api_base + <remaining path>` (preserve query params)
- [ ] **Header passthrough** – Copy `Content-Type`, `Accept`, `Authorization`, `User-Agent`. Remove hop-by-hop: `Connection`, `TE`, `Transfer-Encoding` (handled), `Upgrade`, `Proxy-*`
- [ ] **Body passthrough** – Forward raw request body unchanged
- [ ] **Streaming** – If `stream: true`, stream chunks back unmodified with correct `Content-Type: text/event-stream` and chunked transfer
- [ ] **Non-streaming** – Use short upstream timeout for connect/read, but **never** force a total deadline that cuts off an active stream
- [ ] **Use ReverseProxy** – Prefer `net/http/httputil.ReverseProxy`. Set `Director` to rewrite `req.URL`. Add minimal `ErrorHandler`.
- [ ] **Preserve errors** – If upstream returns error JSON, passthrough status + body when possible
- [ ] **Debug header** – Add `X-Mooch-Node: <node_id>` to error responses

## 7. Health, Resilience & Ops

- [ ] **Local backend health** – Periodically probe configured backends (e.g. `GET /v1/models` or `HEAD` on `endpoint`) with short timeout. Update `healthy` for next heartbeat.
- [ ] **Graceful shutdown** – On `SIGINT`/`SIGTERM`: stop heartbeat loop, close routed HTTP server cleanly, exit within ~5s
- [ ] **Structured logging** – Use `log/slog`. Include at least: `level`, `msg`, `node_id`, `component`, `method`, `path`, `model` on proxy logs
- [ ] **Version constant** – Inject `version` (e.g. `v0.1.0-dev`) into register/heartbeat/healthz
- [ ] **Single binary** – `go build ./cmd/mooch-node` must produce exactly one executable
- [ ] **Minimal deps** – Avoid web frameworks. Stick to stdlib + only what's needed (`yaml.v3`, `tailscale.com/*` if used)
- [ ] **Config reload (nice)** – Support `SIGHUP` to reload config and refresh services (or just re-register) – minimal but fine

## 8. Acceptance Criteria

- [ ] **Startup** – Loads config, resolves Tailscale IP, loads/persists `node_id`
- [ ] **Register** – Hits central `/api/nodes/register` and gets `200 OK`
- [ ] **Heartbeat** – Sends every configured interval with current `services` + `healthy`
- [ ] **Listener up** – Routed server listens on configured addr and `GET /healthz` returns correct info
- [ ] **Chat forward** – Central routes `POST /v1/chat/completions` with known model -> client forwards to local backend -> valid OpenAI JSON returned
- [ ] **Streaming works** – `stream: true` returns SSE chunks end-to-end without truncation
- [ ] **Unknown model** – Returns `400` with helpful message
- [ ] **Resilient** – If central is offline at start, client keeps retrying without crashing
- [ ] **Local backend down** – `healthy=false` reflected on next heartbeat; proxy still returns sane errors if hit
- [ ] **OpenCode compatible** – Works with OpenCode pointed at central's OpenAI endpoint (central->client path)
