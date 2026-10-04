// Package join serves the pages that a new node uses to join central.
//
// A person on the tailnet opens GET /join in a browser. The page shows one
// command. The command gets GET /join.sh, which downloads the node binary
// and a node config from central, and then starts the node.
//
// Central fills the config with the address from the request Host header.
// This is the address that the person used to reach central.
package join

import (
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	texttemplate "text/template"

	"peerai-serv/internal/openaierr"
)

// BinaryName returns the file name of the node binary for goos and goarch.
func BinaryName(goos, goarch string) string {
	return "peerai-node-" + goos + "-" + goarch
}

// platformRE accepts only "<os>-<arch>" values, so a request cannot
// read a file outside the binary directory.
var platformRE = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`)

// Handler serves the join routes.
type Handler struct {
	binDir string
	log    *slog.Logger
}

// New returns a Handler. binDir contains the node binaries with names from
// BinaryName. A nil log means slog.Default.
func New(binDir string, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{binDir: binDir, log: log.With("component", "join")}
}

// Register adds the join routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /join", h.handlePage)
	mux.HandleFunc("GET /join.sh", h.handleScript)
	mux.HandleFunc("GET /join/peerai-node.yaml", h.handleConfig)
	mux.HandleFunc("GET /join/bin/{platform}", h.handleBinary)
}

type params struct {
	Host string // central host as the caller reached it
	Port string // central port as the caller reached it
	Base string // http://host:port
}

func paramsFor(r *http.Request) params {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, "80"
	}
	return params{Host: host, Port: port, Base: "http://" + net.JoinHostPort(host, port)}
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Join PeerAI</title>
<style>
body { font: 16px/1.5 system-ui, sans-serif; max-width: 44rem; margin: 2rem auto; padding: 0 1rem; color: #1a1a1a; background: #fff; }
pre { background: #f2f2f2; padding: 1rem; overflow-x: auto; border-radius: 6px; }
code { font-family: ui-monospace, Menlo, monospace; }
@media (prefers-color-scheme: dark) { body { color: #e6e6e6; background: #161616; } pre { background: #262626; } }
</style>
</head>
<body>
<h1>Join PeerAI</h1>
<p>This page shares your local models with PeerAI central at <code>{{.Base}}</code>.</p>
<h2>Before you start</h2>
<ol>
<li>Connect Tailscale to the same tailnet as central. You see this page, so this step is done.</li>
<li>Start a local model server. Example: <code>ollama serve</code> and <code>ollama pull qwen2.5</code>.</li>
</ol>
<h2>Start the node</h2>
<p>Run this command in a terminal (macOS or Linux):</p>
<pre><code>curl -fsSL {{.Base}}/join.sh | sh</code></pre>
<p>The command installs the node in <code>~/.peerai</code> and starts it.
Keep the terminal open. Press Ctrl+C to stop the node.
Run the same command again to start the node later.</p>
<h2>Change the model server</h2>
<p>The default config uses Ollama at <code>http://127.0.0.1:11434</code>.
For a different server, edit <code>~/.peerai/peerai-node.yaml</code> and run the command again.</p>
<h2>Use the models</h2>
<p>Set the OpenAI base URL of your app to <code>{{.Base}}/v1</code>.
See all models at <a href="{{.Base}}/v1/models">{{.Base}}/v1/models</a>.</p>
</body>
</html>
`))

var configTmpl = texttemplate.Must(texttemplate.New("config").Parse(`# PeerAI node config. Made by central at {{.Base}}.
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
# PeerAI node installer. Made by central at {{.Base}}.
set -eu

CENTRAL="{{.Base}}"
DIR="${PEERAI_DIR:-$HOME/.peerai}"
mkdir -p "$DIR/bin"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "peerai: CPU type $(uname -m) is not supported" >&2; exit 1 ;;
esac

# The node reads its Tailscale IP from the tailscale command.
# The macOS app does not always put that command on PATH.
if ! command -v tailscale >/dev/null 2>&1; then
  app=/Applications/Tailscale.app/Contents/MacOS/Tailscale
  if [ -x "$app" ]; then
    printf '#!/bin/sh\nexec %s "$@"\n' "$app" > "$DIR/bin/tailscale"
    chmod +x "$DIR/bin/tailscale"
  else
    echo "peerai: the tailscale command is not found. Install Tailscale first." >&2
    exit 1
  fi
fi
PATH="$DIR/bin:$PATH"
export PATH
if ! tailscale ip -4 >/dev/null 2>&1; then
  echo "peerai: Tailscale is not connected. Connect Tailscale and try again." >&2
  exit 1
fi

echo "peerai: download node for $os-$arch"
curl -fsSL "$CENTRAL/join/bin/$os-$arch" -o "$DIR/bin/peerai-node.tmp"
chmod +x "$DIR/bin/peerai-node.tmp"
mv "$DIR/bin/peerai-node.tmp" "$DIR/bin/peerai-node"

# Keep an existing config, so that local edits stay.
if [ ! -f "$DIR/peerai-node.yaml" ]; then
  curl -fsSL "$CENTRAL/join/peerai-node.yaml" -o "$DIR/peerai-node.yaml"
  echo "peerai: wrote $DIR/peerai-node.yaml"
fi

if ! curl -fsS -m 2 http://127.0.0.1:11434/v1/models >/dev/null 2>&1; then
  echo "peerai: WARNING: Ollama does not respond at 127.0.0.1:11434." >&2
  echo "peerai: Start it with 'ollama serve', or edit $DIR/peerai-node.yaml." >&2
fi

echo "peerai: start node. Press Ctrl+C to stop."
exec "$DIR/bin/peerai-node" --config "$DIR/peerai-node.yaml"
`))

func (h *Handler) handlePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pageTmpl.Execute(w, paramsFor(r))
}

func (h *Handler) handleScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_ = scriptTmpl.Execute(w, paramsFor(r))
	h.log.Info("join script sent", "remote", r.RemoteAddr)
}

func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_ = configTmpl.Execute(w, paramsFor(r))
}

func (h *Handler) handleBinary(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	if !platformRE.MatchString(platform) {
		openaierr.Write(w, http.StatusBadRequest, openaierr.TypeInvalidRequest,
			fmt.Sprintf("platform %q is invalid", platform))
		return
	}
	path := filepath.Join(h.binDir, "peerai-node-"+platform)
	f, err := os.Open(path)
	if err != nil {
		h.log.Warn("node binary missing", "platform", platform, "path", path)
		openaierr.Write(w, http.StatusNotFound, openaierr.TypeNotFound,
			fmt.Sprintf("central has no node binary for %s", platform))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		openaierr.Write(w, http.StatusNotFound, openaierr.TypeNotFound,
			fmt.Sprintf("central has no node binary for %s", platform))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
