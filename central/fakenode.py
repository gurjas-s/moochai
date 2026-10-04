import json, sys, time, threading, urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
name, port, delay = sys.argv[1], int(sys.argv[2]), float(sys.argv[3])

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        time.sleep(delay)  # pretend to run the model
        body = json.dumps({"choices": [{"message": {"role": "assistant", "content": "hi from " + name}}],
                           "usage": {"prompt_tokens": 5, "completion_tokens": 20}}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
        self.wfile.write(body)

def heartbeat():
    payload = json.dumps({"node_id": name, "name": name, "tailscale_ip": "127.0.0.1",
        "listen_addr": f"127.0.0.1:{port}", "services": [{"id": "s", "models": ["qwen"], "healthy": True}]}).encode()
    while True:
        urllib.request.urlopen(urllib.request.Request("http://127.0.0.1:8080/api/nodes/heartbeat",
                               payload, {"Content-Type": "application/json"}))
        time.sleep(5)

threading.Thread(target=heartbeat, daemon=True).start()
ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
