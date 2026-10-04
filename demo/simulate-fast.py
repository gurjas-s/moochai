#!/usr/bin/env python3
"""Load test: 50 users (the 20 of simulate.py plus 30 generated users) at about 100 requests per second.

Use it to watch TimescaleDB take a steady write rate while /analytics updates live.
Each user has a fast fake backend and workers on keep-alive connections.
The users share one worker process for each CPU core.

Usage (start central first with `make start` in central/):
    python3 demo/simulate-fast.py                     # 50 users, about 100 requests/s, 2 minutes
    python3 demo/simulate-fast.py --users 100 --rps 200 --minutes 5
    python3 demo/simulate-fast.py --single-ip         # no sudo, but central sees one requester

Tip: start central with -plain and send its output to /dev/null. The dashboard draws every request,
so at a high rate it uses more CPU than the routing.
"""
import argparse
import http.client
import json
import math
import multiprocessing as mp
import os
import random
import resource
import sys
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from simulate import (MAX_USERS, MODEL_COST, MODEL_POPULARITY, NODE_PORT, PROMPTS,  # noqa: E402
                      ignore_signals, make_users, set_aliases, stop_on_signals, user_ips)

SPEEDUP = 50  # a reply takes 1/50 of the time that it takes in simulate.py


def run_group(group, central_url, counters, stop):
    """One worker process: start each user of group, then wait for the end of the run."""
    ignore_signals()  # the parent process stops the run
    raise_file_limit()
    parent = os.getppid()

    def watch_parent():
        # If the parent dies without cleanup (kill -9), stop here too, so no user keeps running.
        while not stop.wait(0.5):
            if os.getppid() != parent:
                os._exit(0)
    threading.Thread(target=watch_parent, daemon=True).start()

    central = urllib.parse.urlparse(central_url)
    for i, ip, spec, rps in group:
        start_user(i, ip, spec, central, rps, counters, stop)
    stop.wait()
    os._exit(0)  # exit at once: the user threads hold no data to save


def raise_file_limit():
    """macOS allows 256 open files by default. Each user needs a few sockets."""
    soft, hard = resource.getrlimit(resource.RLIMIT_NOFILE)
    want = 8192 if hard == resource.RLIM_INFINITY else min(8192, hard)
    if soft < want:
        resource.setrlimit(resource.RLIMIT_NOFILE, (want, hard))


def start_user(i, ip, spec, central, rps, counters, stop):
    """One user: a fake backend, heartbeats, and workers that send rps requests per second to central."""
    name, models, speed, _, error_rate, _ = spec
    port = NODE_PORT + i

    if models:
        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"  # keep-alive, so central reuses the connection

            def log_message(self, *args):
                pass

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
                model = body.get("model", "")
                time.sleep(speed * MODEL_COST.get(model, 1) * random.lognormvariate(0, 0.35) / SPEEDUP)
                if random.random() < error_rate:
                    status, obj = 500, {"error": {"message": "backend out of memory", "type": "server_error"}}
                else:
                    p = random.randint(15, 450)
                    c = 0 if self.path == "/v1/embeddings" else int(random.lognormvariate(5, 0.7))
                    status, obj = 200, {"choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}}],
                                        "usage": {"prompt_tokens": p, "completion_tokens": c, "total_tokens": p + c}}
                data = json.dumps(obj).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        ThreadingHTTPServer.request_queue_size = 1024  # the default of 5 refuses connections at a high rate
        server = ThreadingHTTPServer((ip, port), Handler)
        server.daemon_threads = True
        threading.Thread(target=server.serve_forever, daemon=True).start()

    live = []

    def heartbeat():
        services = [{"id": "fake", "name": "fake", "type": "llm", "provider": "ollama", "models": models,
                     "endpoint": f"http://{ip}:{port}", "supports_streaming": False, "healthy": True}] if models else []
        payload = json.dumps({"node_id": name, "name": name, "tailscale_ip": ip, "listen_addr": f"{ip}:{port}",
                              "version": "sim-fast", "services": services}).encode()
        stop.wait(random.uniform(0, 5))  # spread the heartbeats of all users over 5 s
        while not stop.is_set():
            try:
                c = http.client.HTTPConnection(central.hostname, central.port or 80, timeout=5, source_address=(ip, 0))
                c.request("POST", "/api/nodes/heartbeat", payload, {"Content-Type": "application/json"})
                c.getresponse().read()
                c.request("GET", "/v1/models")
                live[:] = [m["id"] for m in json.loads(c.getresponse().read()).get("data", [])]
                c.close()
            except (OSError, ValueError):
                pass
            stop.wait(5)

    def worker(rate):
        conn = None
        next_at = time.time() + random.uniform(0, 2)
        while not stop.is_set():
            next_at += random.expovariate(rate)
            delay = next_at - time.time()
            if delay > 0:
                time.sleep(delay)
            if not live:
                continue
            model = random.choices(live, [MODEL_POPULARITY.get(m, 5) for m in live])[0]
            if model == "nomic-embed-text":
                path, body = "/v1/embeddings", {"model": model, "input": "text"}
            else:
                path, body = "/v1/chat/completions", {"model": model, "messages": [
                    {"role": "user", "content": random.choice(PROMPTS)}]}
            try:
                if conn is None:
                    conn = http.client.HTTPConnection(central.hostname, central.port or 80, timeout=30,
                                                      source_address=(ip, 0))
                conn.request("POST", path, json.dumps(body), {"Content-Type": "application/json"})
                resp = conn.getresponse()
                resp.read()
                key = 0 if resp.status < 400 else 1
            except (OSError, http.client.HTTPException):
                conn, key = None, 1
            with counters.get_lock():
                counters[key] += 1

    threading.Thread(target=heartbeat, daemon=True).start()
    # Enough workers that a slow reply does not hold back the rate. One worker sends at most about 1/latency.
    workers = max(1, min(32, math.ceil(rps * 0.15)))
    for _ in range(workers):
        threading.Thread(target=worker, args=(rps / workers,), daemon=True).start()


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--central", default="http://127.0.0.1:8080")
    ap.add_argument("--rps", type=float, default=100, help="total requests per second, for all users")
    ap.add_argument("--minutes", type=float, default=2)
    ap.add_argument("--users", type=int, default=50, help=f"number of users, at most {MAX_USERS}")
    ap.add_argument("--seed", type=int, help="random seed for the generated users")
    ap.add_argument("--single-ip", action="store_true",
                    help="put all users on 127.0.0.1: no sudo, but central counts all requests for one requester")
    args = ap.parse_args()

    picked = make_users(max(1, min(args.users, MAX_USERS)), random.Random(args.seed))
    ips = user_ips(len(picked), args.single_ip)
    appetite = sum(spec[3] for spec in picked)  # requests per minute in simulate.py sets the share of each user
    stop = mp.Event()
    counters = mp.Array("q", 2)  # ok, error

    stop_on_signals()
    procs, aliased = [], False
    try:
        set_aliases(ips, add=True)
        aliased = True
        users = [(i, ip, spec, args.rps * spec[3] / appetite) for i, (ip, spec) in enumerate(zip(ips, picked))]
        nproc = min(os.cpu_count() or 4, len(users))
        for k in range(nproc):
            p = mp.Process(target=run_group, args=(users[k::nproc], args.central, counters, stop), daemon=True)
            p.start()
            procs.append(p)
        print(f"Run {len(picked)} users at about {args.rps:g} requests/s for {args.minutes:g} min "
              f"against {args.central}. Press Ctrl+C to stop early.")
        print("Open http://127.0.0.1:3000/analytics on the central machine to watch.")
        end, last, last_t = time.time() + args.minutes * 60, 0, time.time()
        while time.time() < end:
            time.sleep(min(5, max(0.1, end - time.time())))
            ok, err = counters[0], counters[1]
            now = time.time()
            print(f"  {time.strftime('%H:%M:%S')}  {(ok + err - last) / (now - last_t):7.0f} requests/s   "
                  f"total {ok + err:,}   errors {err:,}")
            last, last_t = ok + err, now
    except KeyboardInterrupt:
        print("\nStop requested.")
    finally:
        ignore_signals()
        stop.set()
        deadline = time.time() + 2
        for p in procs:
            p.join(timeout=max(0, deadline - time.time()))
        for p in procs:
            if p.is_alive():
                p.kill()
                p.join()
        if aliased:
            set_aliases(ips, add=False)
        print("Stopped all fake users and removed the aliases. Central expires the nodes in about 45 s.")


if __name__ == "__main__":
    main()
