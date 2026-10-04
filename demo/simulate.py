#!/usr/bin/env python3
"""Simulate a Mooch.ai cluster of 20 users against a local central, then clean up.

Each user gets its own loopback IP (127.0.0.10, .11, ...), so central can tell the users apart
and the analytics show real give and take. Givers run a fake backend and advertise models.
Takers advertise no models and only send requests. Most users do both.

Usage (start central first with `make start` in server/):
    python3 demo/simulate.py                 # 20 users for 5 minutes
    python3 demo/simulate.py --users 15 --minutes 10 --seed 7
    python3 demo/simulate.py --single-ip     # no sudo, but central sees one requester

On macOS the script asks for sudo once to add the loopback aliases, and removes them at the end.
Press Ctrl+C to stop early. Cleanup still runs.
"""
import argparse
import http.client
import json
import random
import signal
import subprocess
import sys
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

NODE_PORT = 9100  # user i listens on NODE_PORT + i
FIRST_IP = 10  # users get 127.0.0.10, 127.0.0.11, ...

# Relative cost of each model. A 70b reply takes longer than a 7b reply on the same machine.
MODEL_COST = {
    "llama3.1:70b": 3.0, "qwen2.5:14b": 1.6, "llama3.1:8b": 1.0, "qwen2.5:7b": 0.9,
    "mistral:7b": 0.9, "phi3:mini": 0.5, "nomic-embed-text": 0.1,
}
# How often takers ask for each model.
MODEL_POPULARITY = {
    "qwen2.5:7b": 30, "llama3.1:8b": 25, "qwen2.5:14b": 15, "mistral:7b": 10,
    "llama3.1:70b": 8, "phi3:mini": 7, "nomic-embed-text": 15,
}

# name, models it serves, speed (seconds for a 7b reply), requests per minute, error rate, flaky
USERS = [
    ("ubuntu-24-04-server",  ["llama3.1:70b", "qwen2.5:14b", "llama3.1:8b"],      0.6, 1,  0.00, False),
    ("lab-workstation-03",   ["llama3.1:70b", "qwen2.5:7b", "nomic-embed-text"],  0.7, 3,  0.01, False),
    ("ava-mac-studio",       ["qwen2.5:14b", "llama3.1:8b", "mistral:7b"],        0.9, 4,  0.00, False),
    ("janes-macbook-pro",    ["qwen2.5:7b", "llama3.1:8b"],                       1.1, 8,  0.01, False),
    ("popos-gpu-box",        ["qwen2.5:7b", "mistral:7b", "nomic-embed-text"],    0.8, 2,  0.02, False),
    ("dorm-gaming-pc",       ["llama3.1:8b", "qwen2.5:7b"],                       0.9, 10, 0.03, True),
    ("fedora-40-tower",      ["mistral:7b", "phi3:mini"],                         1.3, 5,  0.02, False),
    ("leos-macbook-pro-m1",  ["phi3:mini", "qwen2.5:7b"],                         1.8, 9,  0.02, True),
    ("debian-homelab",       ["nomic-embed-text", "phi3:mini"],                   1.5, 1,  0.00, False),
    ("priyas-mac-mini",      ["phi3:mini"],                                       2.0, 6,  0.01, False),
    ("arch-desktop",         ["qwen2.5:14b", "llama3.1:8b"],                      1.0, 7,  0.08, True),
    ("library-imac",         ["llama3.1:8b"],                                     2.6, 3,  0.05, True),
    ("raspberry-pi-5",       ["phi3:mini"],                                       4.0, 2,  0.04, False),
    ("kyles-laptop",         [],                                                  0,   18, 0,    False),
    ("emmas-macbook-air",    [],                                                  0,   14, 0,    False),
    ("sams-thinkpad",        [],                                                  0,   11, 0,    False),
    ("omars-surface-pro",    [],                                                  0,   9,  0,    False),
    ("chloes-zenbook",       [],                                                  0,   7,  0,    False),
    ("ci-runner-07",         [],                                                  0,   25, 0,    True),
    ("windows-11-desktop",   ["mistral:7b"],                                      1.4, 12, 0.03, False),
]

MAX_USERS = 100  # user IPs run from 127.0.0.10 to 127.0.0.109

FIRST_NAMES = """jane kyle emma sam omar chloe leo priya ava noah mia liam zoe raj ana ben ivy max nina tom
lucy eli sara jack maya owen ruby finn lily dev kai nora alex hana luis mei theo aria jon tara""".split()
LAPTOPS = ["macbook-pro", "macbook-air", "thinkpad", "xps-13", "zenbook", "surface-pro", "framework-13",
           "legion-5", "rog-zephyrus", "spectre-x360", "pixelbook", "laptop", "chromebook", "yoga-slim"]
DESKTOPS = ["gaming-pc", "mac-mini", "imac", "desktop", "tower", "mac-studio"]
MACHINES = ["ubuntu-22-04", "ubuntu-24-04", "fedora-40", "arch", "debian-12", "popos", "nixos", "mint-21",
            "windows-11", "manjaro", "rocky-9", "opensuse"]
MACHINE_KINDS = ["desktop", "tower", "server", "homelab", "gpu-box", "rig", "workstation"]
SMALL_MODELS = ["phi3:mini", "qwen2.5:7b", "llama3.1:8b", "mistral:7b"]
MID_MODELS = ["qwen2.5:14b", "llama3.1:8b", "qwen2.5:7b", "mistral:7b", "nomic-embed-text"]


def make_users(n, rng):
    """Return n users: the 20 in USERS first, then generated users with the same mix of roles."""
    users = list(USERS[:n])
    names = {u[0] for u in users}

    def unique(name):
        base, k = name, 2
        while name in names:
            name, k = f"{base}-{k}", k + 1
        names.add(name)
        return name

    person = lambda devices: f"{rng.choice(FIRST_NAMES)}s-{rng.choice(devices)}"  # noqa: E731
    while len(users) < n:
        roll = rng.random()
        if roll < 0.35:  # taker: a laptop without models
            users.append((unique(person(LAPTOPS)), [], 0, rng.randint(5, 25), 0, rng.random() < 0.15))
        elif roll < 0.65:  # light giver: one or two small models
            users.append((unique(person(LAPTOPS + DESKTOPS)), rng.sample(SMALL_MODELS, rng.randint(1, 2)),
                          rng.uniform(0.9, 2.5), rng.randint(3, 12), rng.uniform(0, 0.04), rng.random() < 0.25))
        elif roll < 0.9:  # workstation: mid-size models
            users.append((unique(f"{rng.choice(MACHINES)}-{rng.choice(MACHINE_KINDS)}"),
                          rng.sample(MID_MODELS, rng.randint(2, 3)),
                          rng.uniform(0.6, 1.3), rng.randint(1, 6), rng.uniform(0, 0.03), rng.random() < 0.1))
        else:  # server: big models, rarely asks for anything
            users.append((unique(f"lab-server-{rng.randint(1, 99):02d}"),
                          ["llama3.1:70b"] + rng.sample(MID_MODELS, 2),
                          rng.uniform(0.5, 0.8), rng.randint(1, 3), rng.uniform(0, 0.01), False))
    return users


stop = threading.Event()
counts = {"sent": 0, "ok": 0, "error": 0, "no_node": 0}
counts_lock = threading.Lock()
live_models = []  # models that central lists now
live_lock = threading.Lock()


def count(key):
    with counts_lock:
        counts[key] += 1


def wait(seconds):
    """Sleep, but return early when the run stops. Returns True if the run stopped."""
    return stop.wait(seconds)


class User:
    def __init__(self, ip, port, name, models, speed, rpm, error_rate, flaky, central):
        self.ip, self.port, self.name, self.models = ip, port, name, models
        self.speed, self.rpm, self.error_rate, self.flaky = speed, rpm, error_rate, flaky
        self.central = central
        self.online = True
        self.server = None

    # ---- backend: answers the requests that central routes to this user ----
    def start_backend(self):
        user = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
                model = body.get("model", "")
                # Lognormal noise: most replies are near the typical time, a few are slow.
                time.sleep(user.speed * MODEL_COST.get(model, 1) * random.lognormvariate(0, 0.35))
                if random.random() < user.error_rate:
                    self.reply(500, {"error": {"message": "backend out of memory", "type": "server_error"}})
                elif self.path == "/v1/embeddings":
                    n = random.randint(8, 300)
                    self.reply(200, {"object": "list", "model": model,
                                     "data": [{"object": "embedding", "index": 0, "embedding": [0.0] * 8}],
                                     "usage": {"prompt_tokens": n, "completion_tokens": 0, "total_tokens": n}})
                else:
                    p, c = random.randint(15, 450), int(random.lognormvariate(5, 0.7))
                    self.reply(200, {"object": "chat.completion", "model": model,
                                     "choices": [{"index": 0, "message": {"role": "assistant",
                                                  "content": f"answer from {user.name}"}, "finish_reason": "stop"}],
                                     "usage": {"prompt_tokens": p, "completion_tokens": c, "total_tokens": p + c}})

            def reply(self, status, obj):
                data = json.dumps(obj).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self.server = ThreadingHTTPServer((self.ip, self.port), Handler)
        self.server.daemon_threads = True
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    # ---- control plane: heartbeat like mooch-node, and go offline now and then if flaky ----
    def heartbeat_loop(self):
        services = [{"id": "fake", "name": "fake", "type": "llm", "provider": "ollama",
                     "endpoint": f"http://{self.ip}:{self.port}", "models": self.models,
                     "supports_streaming": False, "healthy": True}] if self.models else []
        payload = json.dumps({"node_id": self.name, "name": self.name, "tailscale_ip": self.ip,
                              "listen_addr": f"{self.ip}:{self.port}", "version": "sim",
                              "services": services}).encode()
        next_drop = time.time() + random.uniform(60, 240)
        while not stop.is_set():
            if self.flaky and time.time() > next_drop:
                # Stop heartbeats long enough for central to expire the node (TTL 45 s).
                self.online = False
                if wait(random.uniform(50, 110)):
                    return
                self.online = True
                next_drop = time.time() + random.uniform(90, 300)
            try:
                self.post("/api/nodes/heartbeat", payload)
            except OSError as e:
                print(f"  {self.name}: heartbeat failed: {e}", file=sys.stderr)
            if wait(5):
                return

    # ---- tool: send requests to central from this user's IP ----
    def traffic_loop(self):
        wait(random.uniform(0, 8))  # do not start all users at the same moment
        while not stop.is_set():
            # Poisson arrivals, with a slow drift in how busy the user is.
            busy = self.rpm * random.uniform(0.5, 1.5)
            if wait(random.expovariate(busy / 60)):
                return
            if not self.online:
                continue
            with live_lock:
                choices = list(live_models)
            if not choices:
                continue
            model = random.choices(choices, [MODEL_POPULARITY.get(m, 5) for m in choices])[0]
            if model == "nomic-embed-text":
                path, body = "/v1/embeddings", {"model": model, "input": "a sentence to embed"}
            else:
                path, body = "/v1/chat/completions", {"model": model, "messages": [
                    {"role": "user", "content": random.choice(PROMPTS)}]}
            count("sent")
            try:
                status = self.post(path, json.dumps(body).encode())
                count("ok" if status < 400 else "no_node" if status == 404 else "error")
            except OSError:
                count("error")

    def post(self, path, data):
        conn = http.client.HTTPConnection(self.central.hostname, self.central.port or 80,
                                          timeout=120, source_address=(self.ip, 0))
        try:
            conn.request("POST", path, data, {"Content-Type": "application/json"})
            resp = conn.getresponse()
            resp.read()
            return resp.status
        finally:
            conn.close()


PROMPTS = [
    "Explain Raft leader election in two sentences.", "Write a haiku about GPUs.",
    "Refactor this Go function to return an error.", "Summarize the plot of Dune.",
    "What is a hypertable?", "Translate 'good morning' to Japanese.",
    "Give me three names for a cat.", "Why is the sky blue?",
]


def refresh_models(central):
    while not stop.is_set():
        try:
            conn = http.client.HTTPConnection(central.hostname, central.port or 80, timeout=5)
            conn.request("GET", "/v1/models")
            data = json.loads(conn.getresponse().read())
            conn.close()
            with live_lock:
                live_models[:] = [m["id"] for m in data.get("data", [])]
        except (OSError, ValueError):
            pass
        if wait(5):
            return


def stop_on_signals():
    """Make kill (SIGTERM) and a closed terminal (SIGHUP) stop the run like Ctrl+C, so the cleanup runs."""
    def interrupt(*_):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupt)
    signal.signal(signal.SIGHUP, interrupt)


def ignore_signals():
    """Call at the start of the cleanup, so a second Ctrl+C does not stop the cleanup."""
    for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(sig, signal.SIG_IGN)


def user_ips(n, single_ip):
    """Each user gets its own loopback IP, so central can tell the users apart."""
    return ["127.0.0.1"] * n if single_ip else [f"127.0.0.{FIRST_IP + i}" for i in range(n)]


def set_aliases(ips, add):
    """macOS has only 127.0.0.1 on loopback, so add an alias for each user IP. Linux needs nothing."""
    ips = [ip for ip in ips if ip != "127.0.0.1"]
    if sys.platform != "darwin" or not ips:
        return
    if add:
        print("Add loopback aliases for the users (sudo):")
        subprocess.run(["sudo", "-v"], check=True)
    for ip in ips:
        cmd = ["sudo", "ifconfig", "lo0", "alias", ip, "up"] if add else ["sudo", "ifconfig", "lo0", "-alias", ip]
        subprocess.run(cmd, check=add, stderr=subprocess.DEVNULL)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--central", default="http://127.0.0.1:8080")
    ap.add_argument("--users", type=int, default=20,
                    help=f"number of users, at most {MAX_USERS}. The first 20 are fixed, the rest are generated")
    ap.add_argument("--minutes", type=float, default=5)
    ap.add_argument("--seed", type=int, help="random seed, for a run that you can repeat")
    ap.add_argument("--single-ip", action="store_true",
                    help="put all users on 127.0.0.1: no sudo, but central counts all requests for one requester")
    args = ap.parse_args()
    random.seed(args.seed)
    central = urllib.parse.urlparse(args.central)

    picked = make_users(max(1, min(args.users, MAX_USERS)), random)
    ips = user_ips(len(picked), args.single_ip)
    users = [User(ip, NODE_PORT + i, *spec, central) for i, (ip, spec) in enumerate(zip(ips, picked))]

    # Ctrl+C, kill, and a closed terminal all end the run through the same cleanup.
    stop_on_signals()
    aliased = False
    try:
        set_aliases(ips, add=True)
        aliased = True
        for u in users:
            if u.models:
                u.start_backend()
        threads = [threading.Thread(target=refresh_models, args=(central,), daemon=True)]
        for u in users:
            threads += [threading.Thread(target=u.heartbeat_loop, daemon=True),
                        threading.Thread(target=u.traffic_loop, daemon=True)]
        for t in threads:
            t.start()

        givers = sum(1 for u in users if u.models)
        print(f"Run {len(users)} users ({givers} share models, {len(users) - givers} only use them) "
              f"for {args.minutes:g} min against {args.central}. Press Ctrl+C to stop early.")
        print("Open http://127.0.0.1:3000/analytics on the central machine to watch.")
        end = time.time() + args.minutes * 60
        try:
            while time.time() < end and not stop.is_set():
                stop.wait(min(10, max(0, end - time.time())))
                offline = [u.name for u in users if not u.online]
                with counts_lock:
                    c = dict(counts)
                print(f"  {time.strftime('%H:%M:%S')}  sent {c['sent']}  ok {c['ok']}  error {c['error']}  "
                      f"no node {c['no_node']}  offline: {', '.join(offline) or 'none'}")
        except KeyboardInterrupt:
            print("\nStop requested.")
    finally:
        ignore_signals()
        stop.set()
        for u in users:
            if u.server:
                u.server.server_close()  # the serve thread is a daemon thread, so it ends with the process
        if aliased:
            set_aliases(ips, add=False)
        print("Stopped all fake users and removed the aliases. Central expires the nodes in about 45 s.")


if __name__ == "__main__":
    main()
