// Command peerai-central is the central PeerAI server.
//
// Nodes register with central and send heartbeats. Callers send
// OpenAI-compatible requests to central. Central forwards each request to
// a node that serves the requested model.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tailscale.com/tsnet"

	"peerai-serv/internal/api"
	"peerai-serv/internal/registry"
	"peerai-serv/internal/router"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "v0.1.0-dev"

const shutdownTimeout = 5 * time.Second

func main() {
	var (
		addr     = flag.String("addr", ":8080", "address to listen on")
		hostname = flag.String("hostname", "peerai-central", "hostname of central on the tailnet")
		stateDir = flag.String("state-dir", "", "tsnet state directory (default: tsnet default)")
		nodeTTL  = flag.Duration("node-ttl", registry.DefaultTTL, "time without heartbeat before a node is stale")
		dev      = flag.Bool("dev", false, "listen on plain TCP without tsnet (local testing only)")
		debug    = flag.Bool("debug", false, "enable debug logs")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	if err := run(*addr, *hostname, *stateDir, *nodeTTL, *dev, log); err != nil {
		log.Error("central stopped", "err", err)
		os.Exit(1)
	}
}

func run(addr, hostname, stateDir string, nodeTTL time.Duration, dev bool, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var (
		ln   net.Listener
		dial router.DialFunc
		err  error
	)
	if dev {
		log.Warn("dev mode: tsnet is off, central listens on plain TCP")
		ln, err = net.Listen("tcp", addr)
	} else {
		ts := &tsnet.Server{Hostname: hostname, Dir: stateDir}
		defer ts.Close()
		if _, err := ts.Up(ctx); err != nil {
			return err
		}
		ln, err = ts.Listen("tcp", addr)
		dial = ts.Dial
	}
	if err != nil {
		return err
	}

	reg := registry.New(nodeTTL, nil)
	go sweep(ctx, reg, nodeTTL)

	mux := http.NewServeMux()
	api.New(reg, version, log).Register(mux)
	router.New(reg, dial, log).Register(mux)

	// No WriteTimeout: a write deadline stops long SSE streams.
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	log.Info("central listening", "addr", ln.Addr().String(), "hostname", hostname, "version", version)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// sweep removes stale nodes from memory until ctx stops.
func sweep(ctx context.Context, reg *registry.Registry, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reg.Sweep()
		}
	}
}
