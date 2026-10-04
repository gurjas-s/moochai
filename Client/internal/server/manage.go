package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"peer-ai-client/internal/config"
	"peer-ai-client/internal/manage"
)

// backendRequest is the JSON shape of the website backend form.
// Field names stay lowercase for the browser.
type backendRequest struct {
	Name     string          `json:"name"`
	Endpoint string          `json:"endpoint"`
	APIBase  string          `json:"api_base"`
	Provider config.Provider `json:"provider"`
	Expose   []string        `json:"expose"`
}

// toBackend converts the website shape to a config entry.
// Empty APIBase means /v1. Empty expose means all models.
func (b backendRequest) toBackend() config.Backend {
	expose := b.Expose
	if len(expose) == 0 {
		expose = []string{"*"}
	}
	return config.Backend{
		Name:     strings.TrimSpace(b.Name),
		Endpoint: strings.TrimSpace(b.Endpoint),
		APIBase:  strings.TrimSpace(b.APIBase),
		Provider: b.Provider,
		Expose:   expose,
	}
}

// requireLoopback rejects non-local callers of the website.
// The website edits local backends, so the tailnet must not reach it.
func requireLoopback(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	if host == "localhost" {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error": map[string]any{"message": "website accepts loopback only", "type": "forbidden"},
	})
	return false
}

func (s *Server) registerManage() {
	s.mux.HandleFunc("GET /manage", s.handleManagePage)
	s.mux.HandleFunc("GET /manage/setup", s.handleSetupStatus)
	s.mux.HandleFunc("PUT /manage/setup", s.handleSetupSave)
	s.mux.HandleFunc("POST /manage/register", s.handleRegister)
	s.mux.HandleFunc("GET /manage/backends", s.handleManageList)
	s.mux.HandleFunc("POST /manage/backends", s.handleManageAdd)
	s.mux.HandleFunc("DELETE /manage/backends/{name}", s.handleManageDelete)
}

// setupStatus is the GET /manage/setup answer. It never holds the key.
func (s *Server) setupStatus() map[string]any {
	creds := manage.Credentials{}
	if s.opts.Setup != nil {
		creds, _ = s.opts.Setup.Load()
	}
	return map[string]any{
		"node_id":      s.opts.NodeID,
		"central_host": creds.CentralHost,
		"central_port": creds.CentralPort,
		"api_key_set":  creds.KeySet(),
	}
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if !requireLoopback(w, r) {
		return
	}
	if s.opts.Setup == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{"message": "setup store is disabled", "type": "unavailable"},
		})
		return
	}
	if _, err := s.opts.Setup.Load(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "internal"},
		})
		return
	}
	writeJSON(w, http.StatusOK, s.setupStatus())
}

// handleSetupSave stores central address and API key from the browser.
// New values apply on the next heartbeat, no restart.
func (s *Server) handleSetupSave(w http.ResponseWriter, r *http.Request) {
	if !requireLoopback(w, r) {
		return
	}
	if s.opts.Setup == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{"message": "setup store is disabled", "type": "unavailable"},
		})
		return
	}
	var creds manage.Credentials
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&creds); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "invalid JSON body", "type": "invalid_request"},
		})
		return
	}
	if err := s.opts.Setup.Save(creds); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "invalid_request"},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleRegister creates a user on central and saves the key.
// The browser sends central address plus the wanted user name.
// The node calls central, so the user never touches curl.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !requireLoopback(w, r) {
		return
	}
	if s.opts.Setup == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{"message": "setup store is disabled", "type": "unavailable"},
		})
		return
	}
	var req struct {
		CentralHost string `json:"central_host"`
		CentralPort int    `json:"central_port"`
		Name        string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "invalid JSON body", "type": "invalid_request"},
		})
		return
	}
	if req.CentralPort == 0 {
		req.CentralPort = 8080
	}
	reg, err := manage.RegisterUser(r.Context(), nil, req.CentralHost, req.CentralPort, req.Name)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "central_unreachable"},
		})
		return
	}
	if err := s.opts.Setup.Save(manage.Credentials{
		CentralHost: req.CentralHost,
		CentralPort: req.CentralPort,
		APIKey:      reg.APIKey,
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "internal"},
		})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "ok", "user_id": reg.UserID,
	})
}

func (s *Server) handleManagePage(w http.ResponseWriter, r *http.Request) {
	if !requireLoopback(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(managePage))
}

func (s *Server) handleManageList(w http.ResponseWriter, r *http.Request) {
	if !requireLoopback(w, r) {
		return
	}
	if s.opts.Manage == nil {
		writeJSON(w, http.StatusOK, map[string]any{"backends": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backends": s.opts.Manage.List()})
}

func (s *Server) handleManageAdd(w http.ResponseWriter, r *http.Request) {
	if !requireLoopback(w, r) {
		return
	}
	if s.opts.Manage == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{"message": "manage store is disabled", "type": "unavailable"},
		})
		return
	}
	var req backendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "invalid JSON body", "type": "invalid_request"},
		})
		return
	}
	backend := req.toBackend()
	if err := s.opts.Manage.Add(backend); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "invalid_request"},
		})
		return
	}
	writeJSON(w, http.StatusCreated, backend)
}

func (s *Server) handleManageDelete(w http.ResponseWriter, r *http.Request) {
	if !requireLoopback(w, r) {
		return
	}
	if s.opts.Manage == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{"message": "manage store is disabled", "type": "unavailable"},
		})
		return
	}
	if err := s.opts.Manage.Delete(r.PathValue("name")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "not_found"},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

const managePage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PeerAI Node</title>
<style>
body { font: 14px/1.55 system-ui, sans-serif; max-width: 48rem; margin: 2rem auto; padding: 0 1rem; }
section { border: 1px solid #ccc; border-radius: 6px; padding: 1rem; margin-bottom: 1rem; }
.row { display: flex; gap: 0.5rem; margin-bottom: 0.5rem; flex-wrap: wrap; align-items: center; }
input { font: inherit; padding: 0.4rem; border: 1px solid #ccc; border-radius: 4px; width: 18rem; max-width: 100%; }
button { font: inherit; padding: 0.4rem 0.8rem; cursor: pointer; }
pre { background: #f4f4f4; padding: 0.75rem; overflow-x: auto; }
.hint { color: #666; font-size: 0.85rem; }
</style>
</head>
<body>
<h1>PeerAI Node</h1>
<p>Local only. Register once, then add the services this node shares.</p>
<section>
<h2>1. Register with central</h2>
<p>Pick a user name. The node registers it on central and saves the key by itself.</p>
<div class="row">
<input id="rCentral" placeholder="central host, e.g. 100.64.0.10">
<input id="rPort" placeholder="central port, e.g. 8080">
</div>
<div class="row">
<input id="rName" placeholder="user name, e.g. alice">
<button onclick="register()">Register</button>
</div>
<pre id="regOut"></pre>
<p class="hint">Registered before? Paste the details below instead. New values apply by themselves.</p>
<div class="row">
<input id="sCentral" placeholder="central host">
<input id="sPort" placeholder="central port">
</div>
<div class="row">
<input id="sKey" placeholder="API key, e.g. peerai_..." style="width: 28rem">
<button onclick="saveSetup()">Save setup</button>
</div>
<pre id="setupOut"></pre>
</section>
<section>
<h2>2. Services on this computer</h2>
<p>The node finds Ollama, llama.cpp, and vLLM by itself. Add the rest here.</p>
<div class="row"><button onclick="listBackends()">Refresh list</button></div>
<div class="row">
<input id="bName" placeholder="name, e.g. ollama">
<input id="bEndpoint" placeholder="endpoint, e.g. http://127.0.0.1:11434">
</div>
<div class="row">
<input id="bProvider" placeholder="provider: ollama, llamacpp, vllm, openai-like">
<input id="bExpose" placeholder="expose: * or comma separated models">
<button onclick="addBackend()">Add backend</button>
</div>
<div class="row">
<input id="bDelete" placeholder="name to delete">
<button onclick="deleteBackend()">Delete</button>
</div>
<pre id="out"></pre>
</section>
<script>
function show(id, v) { document.getElementById(id).textContent = typeof v === 'string' ? v : JSON.stringify(v, null, 2); }
function centralOf(prefix) {
  const host = document.getElementById(prefix + 'Central').value.trim();
  const port = document.getElementById(prefix + 'Port').value.trim() || '8080';
  return {host, port: parseInt(port, 10)};
}
async function status() {
  const r = await fetch('/manage/setup');
  const t = await r.text();
  show('setupOut', r.status + ' ' + t);
  try {
    const d = JSON.parse(t);
    if (d.central_host) { document.getElementById('rCentral').value = d.central_host; document.getElementById('sCentral').value = d.central_host; }
    if (d.central_port) { document.getElementById('rPort').value = d.central_port; document.getElementById('sPort').value = d.central_port; }
    if (d.api_key_set) show('regOut', 'Registered. The node saves your key.');
  } catch (e) {}
}
async function register() {
  const c = centralOf('r');
  const body = {central_host: c.host, central_port: c.port, name: document.getElementById('rName').value.trim()};
  const r = await fetch('/manage/register', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
  const t = await r.text();
  show('regOut', r.status + ' ' + t + '\nThe node picks this up on the next heartbeat.');
  status();
}
async function saveSetup() {
  const c = centralOf('s');
  const body = {central_host: c.host, central_port: c.port, api_key: document.getElementById('sKey').value.trim()};
  const r = await fetch('/manage/setup', {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
  show('setupOut', r.status + ' ' + await r.text() + '\nThe node picks this up on the next heartbeat.');
  document.getElementById('sKey').value = '';
}
async function listBackends() {
  const r = await fetch('/manage/backends');
  show('out', r.status + ' ' + await r.text());
}
async function addBackend() {
  const expose = document.getElementById('bExpose').value.split(',').map(function (s) { return s.trim(); }).filter(Boolean);
  const body = {
    name: document.getElementById('bName').value.trim(),
    endpoint: document.getElementById('bEndpoint').value.trim(),
    provider: document.getElementById('bProvider').value.trim() || 'openai-like',
    expose: expose.length ? expose : ['*']
  };
  const r = await fetch('/manage/backends', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
  show('out', r.status + ' ' + await r.text());
}
async function deleteBackend() {
  const name = document.getElementById('bDelete').value.trim();
  const r = await fetch('/manage/backends/' + encodeURIComponent(name), {method: 'DELETE'});
  show('out', r.status + ' ' + await r.text());
}
status();
listBackends();
</script>
</body>
</html>`
