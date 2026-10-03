# AGENTS.md — PeerAI Client

## 1. Project context

PeerAI Client is a single Go binary (`go build ./cmd/peerai-node`) that runs
next to local AI backends (vLLM, Ollama, llama.cpp, Whisper, and others).
It has two jobs:

- **Control plane:** advertise local services to the central PeerAI server
  over Tailscale (register + heartbeat with a custom JSON payload).
- **Data plane:** listen for routed OpenAI-compatible requests from central
  and forward each request to the local backend that serves the requested
  `model`.

Broadcasting to central uses custom JSON. The runtime API for tools is
OpenAI-compatible. The full requirements live in `REQUIREMENTS.md`.
Read that file before you change behavior.

## 2. Folder structure

The Go module lives in `Client/`. The target layout is:

```text
Client/
  cmd/peerai-node/      single binary entrypoint (must build to one executable)
  internal/
    config/             --config flag, default paths, validation, YAML (control plane)
    identity/           Tailscale IP, persisted node.id, listen_addr (control plane)
    central/            register + heartbeat, backoff/retries (control plane)
    server/             listener, /healthz, OpenAI routes, shutdown, logs (data plane)
    proxy/              forwarding: route by model, ReverseProxy, streaming (data plane)
```

Work split: one workstream owns the control plane
(`config`, `identity`, `central`), another owns the data plane
(`server`, `proxy`). Each workstream works on its own worktree and branch.
Do not add packages outside your workstream without agreement.
Do not add web frameworks. Use the standard library plus only necessary
dependencies (`yaml.v3`, `tailscale.com/*`).

## 3. Push workflow: issue + PR are mandatory

Every push to a shared branch needs both of these:

1. **GitHub issue** created with `gh issue create`. It describes the scope,
   the interface contract for the other workstream (if any), and open
   questions.
2. **Pull request** created with `gh pr create`, linked to the issue
   (`Closes #N`). The branch must be pushed before you open the PR.

### PR requirements

Each PR description must contain these two sections:

- **Summary** — what changed and why, including effects on the other
  workstream's packages.
- **Verification** — the exact commands you ran and their result
  (minimum: `go vet ./...` and `go test ./... -count=1`).

Register each PR you open with the session's PR-linking tool so the
thread tracks it.

## 4. Repository rules

- **Test your code.** New behavior needs tests. Mock external systems with
  `httptest` (fake central, fake backends) — never depend on a live
  Tailscale network, a real central server, or real model backends in tests.
- **Run before you push:** `gofmt -l .` (empty output), `go vet ./...`,
  `go test ./... -count=1`. A PR with failing checks is not ready.
- **Keep the binary single.** `go build ./cmd/peerai-node` must produce
  exactly one executable.
- **Keep dependencies minimal.** Standard library first. Justify each new
  dependency in the PR summary.
- **Do not commit secrets.** Tokens, Tailscale keys, and node IDs from
  real machines do not belong in the repository.
- **Match the existing code style.** Small exported surface, `log/slog`
  for logging, OpenAI-shaped JSON error envelopes with an `X-PeerAI-Node`
  header on proxy errors.

## 5. Language: ASD-STE100 Simplified Technical English

Write all issues, PRs, review comments, and code comments in
ASD-STE100 Simplified Technical English (STE). Follow these rules:

- Use short sentences. Limit: 20 words maximum for instructions,
  25 words maximum for descriptions.
- Give only one instruction per sentence.
- Give only one topic per paragraph.
- Use the approved STE dictionary words with their approved meanings.
  If a technical noun is not in the dictionary, you may use it as a
  technical name, but use it the same way each time.
- Use the active voice. Example: "Send the heartbeat every 15 seconds."
  Not: "The heartbeat should be sent every 15 seconds."
- Use simple verb tenses. Use the present tense for general truths and
  the imperative for instructions.
- Do not use the `-ing` form as a verb. Example: "Make sure that the
  server listens on the port." Not: "Make sure the server is listening."
- Do not use phrasal verbs, idioms, slang, or humor. Example: "Stop the
  server." Not: "Kill the server."
- Do not use ambiguous pronouns. Repeat the noun when clarity requires it.
- Use warnings and cautions first, before the related instruction, when
  data loss or downtime is possible.
- Use consistent terminology. The same word always means the same thing:
  `central` (the server), `node` (this client), `backend` (a local model
  service), `service` (an advertised capability entry), `model` (a model ID).
