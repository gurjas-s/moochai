# PeerAI Client Setup

The PeerAI client runs beside one or more local AI backends.
The client advertises the backends to central over Tailscale.
The client also forwards model requests to the correct backend.

## Requirements

Install these tools before you start:

- Go 1.24 or later.
- Tailscale.
- A local OpenAI-compatible backend.
- Access to the PeerAI central server.

The client uses the Tailscale IPv4 address for node-to-central traffic.
Make sure the client and central server share a Tailscale network.

## Build the client

Run these commands from the `Client` directory:

```sh
cd Client
make build
```

The command creates one executable at `bin/peerai-node`.
For debug work, build the dev binary instead:

```sh
make build-dev
```

The command creates `bin/peerai-node-dev`.
Both binaries use the same optimized build.
Dev differs only in log output.
Use it for debug work:

```sh
make run-dev CONFIG=./peerai-node.yaml
```

`make run` starts the standard binary with request logs.
`make run-dev` starts the dev binary with dev logs.
Set `CONFIG` to select the config file and `LOG_FORMAT` to select `text` or `json`.
Run `make check` before a push (format check, vet, tests) and `make clean` to remove `bin/`.

## Create the configuration

Copy the sample file:

```sh
cp peerai-node.yaml.example peerai-node.yaml
```

Edit `peerai-node.yaml`:

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
The client stores the generated ID in `~/.config/peerai/node.id`.
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

Run the binary. It prints a local website link on start:

```sh
./peerai-node --config ./peerai-node.yaml
```

No key yet? Open the website (usually `http://127.0.0.1:9100/manage`):

1. Section 1: type the central address and a user name, press Register.
   The node registers on central and saves the key by itself.
   Setup applies on the next heartbeat. No restart.
2. Section 2: check that your backends appear (Ollama, llama.cpp,
   and vLLM are found by themselves). Add the rest by hand.
3. Share models from central `/app`.

When `--config` is not provided, the client checks these paths:

1. `~/.config/peerai/node.yaml`
2. `./peerai-node.yaml`

## Logging

The client has two log modes.
Set the mode with `--log-mode` or `PEERAI_LOG_MODE`.
Set the format with `--log-format` or `PEERAI_LOG_FORMAT` (`text` or `json`).

```sh
./peerai-node --config ./peerai-node.yaml --log-mode requests --log-format text
PEERAI_LOG_MODE=dev PEERAI_LOG_FORMAT=json ./peerai-node --config ./peerai-node.yaml
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
kill -TERM <peerai-node-pid>
```

The client stops the heartbeat loop and shuts down the HTTP server.

## Troubleshooting

### Configuration file not found

Pass the file path explicitly:

```sh
./peerai-node --config /path/to/node.yaml
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
