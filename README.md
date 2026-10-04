# Mooch.ai

Steal AI compute resource from your peers. Mooch.ai runs as a local P2P session that clusters any computers together, exposing your friends' AI models to your computer.

🤩

Mooch.ai connects the computers on one Tailscale network into one AI cluster.
Each computer shares the models of its local backends, such as Ollama, vLLM, or llama.cpp.
Your tools send OpenAI requests to one address.
Mooch.ai sends each request to a computer that has the requested model.

## How it works

```text
 your tool                      central                         node                 backend
 (Zed, curl, SDK)        (mooch-central)                 (mooch-node)     (Ollama, vLLM, ...)
     │                          │                               │                     │
     │ POST /v1/chat/completions│                               │                     │
     │ {"model": "qwen2.5"}     │                               │                     │
     ├─────────────────────────▶│ find a live node with qwen2.5 │                     │
     │                          ├──────────────────────────────▶│ forward by model    │
     │                          │                               ├────────────────────▶│
     │◀─────────────────── response (streams flow chunk by chunk) ◀───────────────────┤
```

Mooch.ai has two programs:

| Program | Job |
|---------|-----|
| `mooch-central` | Keeps the list of live nodes and their models. Receives OpenAI requests and sends each request to a node. Shows a terminal dashboard. |
| `mooch-node` | Runs next to the local backends. Sends its models to central (register and heartbeat). Forwards routed requests to the correct backend. |

These words have one meaning in all Mooch.ai documents:

| Word | Meaning |
|------|---------|
| central | The server that routes requests (`mooch-central`). |
| node | A computer that runs `mooch-node`. |
| backend | A local model service on a node, such as Ollama. |
| service | One backend entry that a node advertises to central. |
| model | A model ID, such as `qwen2.5:7b`. |

## Requirements

- Go 1.24 or later, to build from source.
- Tailscale on each computer. All computers must be on the same tailnet.
- One or more OpenAI-compatible backends on the nodes, such as Ollama.

## Quick start

### 1. Start central

Start central on one computer of the tailnet:

```sh
cd server
./build-nodes.sh                    # build node binaries for the join command
go run ./cmd/mooch-central          # listen on the Tailscale IPv4, port 8080
```

Central listens only on its Tailscale IPv4.
Thus only computers on your tailnet can connect.
For local tests, use `go run ./cmd/mooch-central -addr 127.0.0.1:8080`.

### 2. Join a computer

Start a backend on the computer that shares models. Example:

```sh
ollama serve
ollama pull qwen2.5
```

Run the join command that the central dashboard shows:

```sh
curl -fsSL http://<central-tailscale-ip>:8080/join.sh | sh
```

The command installs `mooch-node` and a config in `~/.mooch`, and then starts the node.
You can also open `http://<central-tailscale-ip>:8080/join` in a browser.
For a manual setup, read [`Client/README.md`](Client/README.md).

### 3. Use the models

Set the OpenAI base URL of your tool to central:

```text
http://<central-tailscale-ip>:8080/v1
```

Send a request:

```sh
curl http://<central-tailscale-ip>:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model": "qwen2.5", "messages": [{"role": "user", "content": "Hello"}]}'
```

`GET /v1/models` lists all models of the live nodes.
Central also has an Ollama-compatible API (`/api/tags`, `/api/show`, `/api/chat`) for tools such as Zed.

## The central dashboard

Central shows a terminal dashboard when it runs in a terminal:

```text
╭───────────────────────────────────╮╭──────────────────────────────╮╭──────────────────────────────────╮
│  ███╗   ███╗ ██████╗  ██████╗ ... ││ NODES 2 connected            ││ MODELS 3 available               │
│  ████╗ ████║██╔═══██╗██╔═══██╗... ││ ● gpu-box  100.64.0.7  1s ago││ llama3.1:8b       gpu-box        │
│  ...                              ││ ● laptop   100.64.0.9  3s ago││ qwen2.5:7b        gpu-box, laptop│
│                                   ││                              ││ whisper-large-v3  laptop         │
╰───────────────────────────────────╯╰──────────────────────────────╯╰──────────────────────────────────╯
 ● tailnet 100.64.0.1:8080   OpenAI base URL http://100.64.0.1:8080/v1   join curl -fsSL …
╭──────────────────────────────────────────────────────────────────────────────────────────────╮
│ CONSOLE                                                                                      │
│ 12:00:01  JOIN  gpu-box 100.64.0.7 · models: qwen2.5:7b, llama3.1:8b                         │
│ 12:00:09  ╭─ #1 laptop asks for qwen2.5:7b → gpu-box · served by: gpu-box                    │
│ 12:00:09  │ REQ   [ laptop → gpu-box ] "Hello" POST /v1/chat/completions · #1                  │
│ 12:00:10  │ RESP  [ gpu-box → laptop ] "Hello! How can I help you?" 200 in 812ms · #1          │
│           ╰────────────────────────────────────────────────────────────                       │
╰──────────────────────────────────────────────────────────────────────────────────────────────╯
```

- **NODES** shows the live nodes. A yellow dot shows that a node has not sent a heartbeat for 20 seconds.
- **MODELS** shows each available model and the nodes that serve the model, separated by commas.
- **CONSOLE** shows the events: nodes that join or leave, routes, requests, and responses.
  A grey frame encloses each request and its response.
  REQ and RESP lines show the direction of each message, for example `[ laptop → gpu-box ]`.
  The number at the end of a line (`#1`) connects the lines of one request when two requests overlap.

Use these keys in the dashboard:

| Key | Action |
|-----|--------|
| `↑` `↓` or `k` `j` | Scroll the console one line |
| `PgUp` `PgDn` | Scroll the console one page |
| `g` `G` | Go to the top or the bottom of the console |
| `q` | Stop central |

Use `-plain` to print the events as lines without the dashboard.
Use `-verbose` to print plain log lines.
Central prints lines automatically when its output is not a terminal.

## Repository layout

```text
Client/                   mooch-node (Go module mooch-client)
  cmd/mooch-node/         the node binary
  internal/config/        config file, default paths, validation
  internal/identity/      Tailscale IP, node ID, listen address
  internal/central/       register and heartbeat to central
  internal/server/        listener, /healthz, OpenAI routes
  internal/proxy/         forward each request to the backend of its model
server/                   mooch-central (Go module mooch-serv)
  cmd/mooch-central/      the central binary
  internal/registry/      live nodes and model lookup
  internal/router/        OpenAI routes and forwarding
  internal/api/           node register and heartbeat routes
  internal/ollama/        Ollama-compatible routes
  internal/join/          join page, install script, node binaries
  internal/feed/          console event lines
  internal/tui/           terminal dashboard
REQUIREMENTS.md           node requirements
AGENTS.md                 rules for contributors and coding agents
```

More documents:

- [`Client/README.md`](Client/README.md): node setup, config, logs, and troubleshooting.
- [`server/README.md`](server/README.md): central flags, endpoints, and request flow.
- [`REQUIREMENTS.md`](REQUIREMENTS.md): the node requirements.

## Development

Run these checks in `Client/` and in `server/` before a push:

```sh
gofmt -l .                # the output must be empty
go vet ./...
go test ./... -count=1
```

Each push to a shared branch needs a GitHub issue and a pull request.
Read [`AGENTS.md`](AGENTS.md) for the full workflow and the writing rules.

## Security

Central has no authentication.
All computers that can connect to central can use all live nodes and models.
Central relies on your tailnet to control access.
Do not expose central on a public address.
