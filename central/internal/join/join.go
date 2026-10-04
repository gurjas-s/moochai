// Package join serves the page, script, config, and binary that a new node uses to join central.
package join

import (
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	texttemplate "text/template"

	"mooch-central/internal/respond"
)

const binaryPrefix = "mooch-node-"

// Only "<os>-<arch>", so a request cannot read files outside binDir.
var platformRE = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`)

type Handler struct {
	binDir string
	log    *slog.Logger
}

func New(binDir string) *Handler {
	return &Handler{binDir: binDir, log: slog.With("component", "join")}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /join", serveTemplate(pageTmpl, "text/html; charset=utf-8"))
	mux.HandleFunc("GET /join/mooch-node.yaml", serveTemplate(configTmpl, "application/yaml; charset=utf-8"))

	script := serveTemplate(scriptTmpl, "text/x-shellscript; charset=utf-8")
	mux.HandleFunc("GET /join.sh", func(w http.ResponseWriter, r *http.Request) {
		script(w, r)
		h.log.Info("join script sent", "remote", r.RemoteAddr)
	})
	mux.HandleFunc("GET /join/bin/{platform}", h.handleBinary)
}

type joinInfo struct {
	Host       string
	Port       string
	CentralURL string
}

func joinInfoFor(r *http.Request) joinInfo {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, "80"
	}
	return joinInfo{Host: host, Port: port, CentralURL: "http://" + net.JoinHostPort(host, port)}
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Join Mooch.ai</title>
<style>
body { font: 16px/1.5 system-ui, sans-serif; max-width: 44rem; margin: 2rem auto; padding: 0 1rem; color: #1a1a1a; background: #fff; }
pre { background: #f2f2f2; padding: 1rem; overflow-x: auto; border-radius: 6px; }
code { font-family: ui-monospace, Menlo, monospace; }
@media (prefers-color-scheme: dark) { body { color: #e6e6e6; background: #161616; } pre { background: #262626; } }
</style>
</head>
<body>
<h1>Join Mooch.ai</h1>
<p>This page shares your local models with Mooch.ai central at <code>{{.CentralURL}}</code>.</p>
<h2>Before you start</h2>
<ol>
<li>Connect Tailscale to the same tailnet as central. You see this page, so this step is done.</li>
<li>Start a local model server. Example: <code>ollama serve</code> and <code>ollama pull qwen2.5</code>.</li>
</ol>
<h2>Start the node</h2>
<p>Run this command in a terminal (macOS or Linux):</p>
<pre><code>curl -fsSL {{.CentralURL}}/join.sh | sh</code></pre>
<p>The command installs the node in <code>~/.mooch</code> and starts it.
Keep the terminal open. Press Ctrl+C to stop the node.
Run the same command again to start the node later.</p>
<h2>Change the model server</h2>
<p>The default config uses Ollama at <code>http://127.0.0.1:11434</code>.
For a different server, edit <code>~/.mooch/mooch-node.yaml</code> and run the command again.</p>
<h2>Use the models</h2>
<p>Set the OpenAI base URL of your app to <code>{{.CentralURL}}/v1</code>.
See all models at <a href="{{.CentralURL}}/v1/models">{{.CentralURL}}/v1/models</a>.</p>
</body>
</html>
`))

var configTmpl = texttemplate.Must(texttemplate.New("config").Parse(`# Mooch.ai node config. Made by central at {{.CentralURL}}.
network:
  central_host: "{{.Host}}"
  central_port: {{.Port}}
  listen_host: "0.0.0.0"
  listen_port: 9100
  heartbeat_interval: 15s

node:
  # Empty name means the hostname of this computer.
  name: ""
  id: ""

backends:
  - name: "ollama"
    endpoint: "http://127.0.0.1:11434"
    provider: "ollama"
    expose: ["*"]
`))

var scriptTmpl = texttemplate.Must(texttemplate.New("script").Parse(`#!/bin/sh
# Mooch.ai node installer. Made by central at {{.CentralURL}}.
set -eu

CENTRAL="{{.CentralURL}}"
DIR="${MOOCH_DIR:-$HOME/.mooch}"
mkdir -p "$DIR/bin"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "mooch: CPU type $(uname -m) is not supported" >&2; exit 1 ;;
esac

# The node reads its Tailscale IP from the tailscale command.
# The macOS app does not always put that command on PATH.
if ! command -v tailscale >/dev/null 2>&1; then
  app=/Applications/Tailscale.app/Contents/MacOS/Tailscale
  if [ -x "$app" ]; then
    printf '#!/bin/sh\nexec %s "$@"\n' "$app" > "$DIR/bin/tailscale"
    chmod +x "$DIR/bin/tailscale"
  else
    echo "mooch: the tailscale command is not found. Install Tailscale first." >&2
    exit 1
  fi
fi
PATH="$DIR/bin:$PATH"
export PATH
if ! tailscale ip -4 >/dev/null 2>&1; then
  echo "mooch: Tailscale is not connected. Connect Tailscale and try again." >&2
  exit 1
fi

echo "mooch: download node for $os-$arch"
curl -fsSL "$CENTRAL/join/bin/$os-$arch" -o "$DIR/bin/mooch-node.tmp"
chmod +x "$DIR/bin/mooch-node.tmp"
mv "$DIR/bin/mooch-node.tmp" "$DIR/bin/mooch-node"

# Keep an existing config, so that local edits stay.
if [ ! -f "$DIR/mooch-node.yaml" ]; then
  curl -fsSL "$CENTRAL/join/mooch-node.yaml" -o "$DIR/mooch-node.yaml"
  echo "mooch: wrote $DIR/mooch-node.yaml"
fi

if ! curl -fsS -m 2 http://127.0.0.1:11434/v1/models >/dev/null 2>&1; then
  echo "mooch: WARNING: Ollama does not respond at 127.0.0.1:11434." >&2
  echo "mooch: Start it with 'ollama serve', or edit $DIR/mooch-node.yaml." >&2
fi

echo "mooch: start node. Press Ctrl+C to stop."
exec "$DIR/bin/mooch-node" --config "$DIR/mooch-node.yaml"
`))

func serveTemplate(tmpl interface{ Execute(io.Writer, any) error }, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_ = tmpl.Execute(w, joinInfoFor(r))
	}
}

func (h *Handler) handleBinary(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	if !platformRE.MatchString(platform) {
		respond.Error(w, http.StatusBadRequest, fmt.Sprintf("platform %q is invalid", platform))
		return
	}
	path := filepath.Join(h.binDir, binaryPrefix+platform)
	f, err := os.Open(path)
	var info fs.FileInfo
	if err == nil {
		defer f.Close()
		info, err = f.Stat()
	}
	if err != nil || info.IsDir() {
		h.log.Warn("node binary missing", "platform", platform, "path", path)
		respond.Error(w, http.StatusNotFound, fmt.Sprintf("central has no node binary for %s", platform))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
