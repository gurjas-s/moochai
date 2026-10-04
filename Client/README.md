# Mooch.ai Client Setup

The Mooch.ai client runs beside one or more local AI backends.
The client advertises the backends to central over Tailscale.
The client also forwards model requests to the correct backend.

## Requirements

Install these tools before you start:

- Go 1.24 or later.
- Tailscale.
- A local OpenAI-compatible backend.
- Access to the Mooch.ai central server.

The client uses the Tailscale IPv4 address for node-to-central traffic.
Make sure the client and central server share a Tailscale network.

## Build the client

Run these commands from the `Client` directory:

```sh
cd Client
make build
```

The command creates one executable at `bin/mooch-node`.
For debug work, build the dev binary instead:

```sh
make build-dev
```

The command creates `bin/mooch-node-dev`.
Both binaries use the same optimized build.
Dev differs only in log output.
Use it for debug work:

```sh
make run-dev CONFIG=./mooch-node.yaml
```

`make run` starts the standard binary with request logs.
`make run-dev` starts the dev binary with dev logs.
Set `CONFIG` to select the config file and `LOG_FORMAT` to select `text` or `json`.
Run `make check` before a push (format check, vet, tests) and `make clean` to remove `bin/`.

## Create the configuration

Copy the sample file:

```sh
cp mooch-node.yaml.example mooch-node.yaml
```

Edit `mooch-node.yaml`:

```yaml
network:
  central_host: "100.64.0.10"
  central_port: 8080
  listen_host: "0.0.0.0"
  listen_port: 9100
  heartbeat_interval: 15s

node:
  name: "local-ai-node"
  id: ""

backends:
  - name: "local-llm"
    endpoint: "http://127.0.0.1:11434"
    provider: "ollama"
    expose: ["*"]
```

Set `backends[].endpoint` to the local backend URL.
Set `backends[].provider` to enable extras (`ollama`, `llamacpp`, or empty for generic).
Leave `expose` empty or `["*"]` to advertise all discovered models.
List model names in `expose` to advertise only those models.
Set `backends[].model_meta.<model>.context_window` when the backend hides it.

The node queries `GET {endpoint}/v1/models` at startup and before each
heartbeat, then sends the full service objects to central.
The broadcast shape is unchanged. Static `services[]` entries still load
and merge with discovered ones.

Set `network.central_host` to the Tailscale IPv4 address of central.
Set `network.central_port` to the central HTTP port.
Set `network.listen_port` to the port that central can reach.
Set `backends[].endpoint` to the local backend URL.

Leave `node.id` empty to generate a stable ID.
The client stores the generated ID in `~/.config/mooch/node.id`.
Set `node.name` to an operator-readable name.
The client uses the machine hostname when `node.name` is empty.

## Start a local backend

Start the backend before starting the client.
For example, start Ollama and make sure the configured model exists:

```sh
ollama serve
ollama pull qwen2.5
```

Use the backend's actual model ID in `backends[].expose`.
The client checks backend health with `GET /v1/models`.

## Start the client

Start the client with an explicit configuration path:

```sh
./mooch-node --config ./mooch-node.yaml
```

When `--config` is not provided, the client checks these paths in order:

1. `~/.config/mooch/node.yaml`
2. `~/Library/Application Support/mooch/node.yaml` (macOS only)
3. `~/.mooch/mooch-node.yaml` (the join script installs the config here)
4. `./mooch-node.yaml`

Only `network.central_host` is required. The client uses these defaults for missing fields:

| Field | Default |
|-------|---------|
| `network.central_port` | `8080` |
| `network.listen_host` | `0.0.0.0` |
| `network.listen_port` | `9100` |
| `network.heartbeat_interval` | `15s` |
| `node.name` | the hostname |
| `node.id` | a generated ID, saved as `mooch/node.id` in the user config directory |

If the config has problems, the client shows all problems and stops.
If the config has no backends, the client starts and shows a warning, because it shares no models.

## Dashboard

The node shows a dashboard when stdout is a terminal.
Without a terminal the node keeps plain logs on stderr.

The dashboard shows three parts:

- Status: `connecting`, `joined`, or `retrying` with the central address.
- Models on this node: hosted models with health state.
  Peers can use these models through central.
- Activity: short operator lines for joins, model updates,
  served requests, and errors.

Keys: `↑/↓` scroll, `pgup/pgdn` page, `g/G` top/bottom, `q` quit.
Use `--log-mode dev` for debug detail in the activity view.

## Logging

The client has two log modes.
Set the mode with `--log-mode` or `MOOCH_LOG_MODE`.
Set the format with `--log-format` or `MOOCH_LOG_FORMAT` (`text` or `json`).

```sh
./mooch-node --config ./mooch-node.yaml --log-mode requests --log-format text
MOOCH_LOG_MODE=dev MOOCH_LOG_FORMAT=json ./mooch-node --config ./mooch-node.yaml
```

- `requests` (default): logs HTTP requests plus warnings and errors.
- `dev`: adds debug detail for developers (config, identity, backend probe results, model discovery counts, central register and heartbeat results).

At startup, the client loads configuration, resolves its Tailscale IPv4,
probes configured backends, starts the local listener, and registers with central.

## Check the local endpoints

Check node health:

```sh
curl http://127.0.0.1:9100/healthz
```

List configured models:

```sh
curl http://127.0.0.1:9100/v1/models
```

Send a chat request:

```sh
curl http://127.0.0.1:9100/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "qwen2.5",
    "messages": [
      {"role": "user", "content": "Hello"}
    ]
  }'
```

The `model` value must match one discovered or configured model.
The client forwards the request to the matching backend.

## Verify central registration

The client sends registration to:

```text
POST http://central_host:central_port/api/nodes/register
```

The client sends heartbeats at `network.heartbeat_interval`:

```text
POST http://central_host:central_port/api/nodes/heartbeat
```

Each payload contains the node identity, listen address, version, and services.
Each service contains its current health state.

## Stop the client

Press `Ctrl+C` or send `SIGTERM`:

```sh
kill -TERM <mooch-node-pid>
```

The client stops the heartbeat loop and shuts down the HTTP server.

## Troubleshooting

### Configuration file not found

The error lists each path that the client checked.
Run the join command from central, or copy `mooch-node.yaml.example` to `mooch-node.yaml`.
You can also give the file path:

```sh
./mooch-node --config /path/to/node.yaml
```

### Tailscale address cannot be resolved

Check that Tailscale is installed and connected:

```sh
tailscale status
tailscale ip -4
```

### Service reports unhealthy

Check the backend URL and model list.
Test the backend directly:

```sh
curl http://127.0.0.1:11434/v1/models
```

### Central registration fails

Check the central Tailscale address and port.
Check connectivity from the client:

```sh
curl http://100.64.0.10:8080/healthz
```

The client retries failed registration and heartbeat requests.
