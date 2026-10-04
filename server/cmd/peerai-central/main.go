// Command peerai-central routes OpenAI requests from tools to PeerAI nodes on the tailnet.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"peerai-serv/internal/api"
	"peerai-serv/internal/app"
	"peerai-serv/internal/auth"
	"peerai-serv/internal/join"
	"peerai-serv/internal/registry"
	"peerai-serv/internal/router"
)

var tailscaleRange = netip.MustParsePrefix("100.64.0.0/10")

func main() {
	addr := flag.String("addr", ":8080", "listen address. An empty host means the Tailscale IPv4 of this machine")
	binDir := flag.String("bin", "dist", "directory with node binaries for /join")
	ttl := flag.Duration("node-ttl", 45*time.Second, "remove a node after this time without a heartbeat")
	debug := flag.Bool("debug", false, "log each heartbeat")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if err := run(*addr, *binDir, *ttl); err != nil {
		slog.Error("central stopped", "error", err)
		os.Exit(1)
	}
}

func run(addr, binDir string, ttl time.Duration) error {
	listen, tailscaleIP, err := resolveListenAddr(addr)
	if err != nil {
		return err
	}

	reg := registry.New(ttl)
	authStore := auth.New()
	mux := http.NewServeMux()
	api.New(reg, authStore).Register(mux)
	router.New(reg, authStore).Register(mux)
	app.Register(mux)
	join.New(binDir).Register(mux)

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	slog.Info("central listening", "addr", ln.Addr().String())
	if tailscaleIP.IsValid() {
		printJoinInfo(os.Stdout, tailscaleIP, port)
	} else {
		slog.Warn("central does not listen on a Tailscale IP, so other machines cannot join")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go expireNodesLoop(ctx, reg, ttl/3)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("central stopped")
	return nil
}

func expireNodesLoop(ctx context.Context, reg *registry.Registry, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, id := range reg.Sweep() {
				slog.Info("node expired", "node_id", id)
			}
		}
	}
}

// An empty host means the Tailscale IPv4 of this machine, so only the tailnet can reach central.
func resolveListenAddr(addr string) (listen string, tailscaleIP netip.Addr, err error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", netip.Addr{}, fmt.Errorf("invalid -addr %q: %w", addr, err)
	}
	if host == "" {
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return "", netip.Addr{}, err
		}
		ip, ok := findTailscaleIPv4(addrs)
		if !ok {
			return "", netip.Addr{}, errors.New("no Tailscale IPv4 found: connect Tailscale, or set -addr 127.0.0.1:8080 for local use")
		}
		return net.JoinHostPort(ip.String(), port), ip, nil
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Is4() && tailscaleRange.Contains(ip) {
		return addr, ip, nil
	}
	return addr, netip.Addr{}, nil
}

func findTailscaleIPv4(addrs []net.Addr) (netip.Addr, bool) {
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if ip = ip.Unmap(); ok && ip.Is4() && tailscaleRange.Contains(ip) {
			return ip, true
		}
	}
	return netip.Addr{}, false
}

func printJoinInfo(w io.Writer, ip netip.Addr, port string) {
	base := "http://" + net.JoinHostPort(ip.String(), port)
	fmt.Fprintf(w, "\nPeerAI central is up on the tailnet.\n\n  Join page:   %[1]s/join\n  Join command (macOS/Linux):\n    curl -fsSL %[1]s/join.sh | sh\n  OpenAI base URL for tools: %[1]s/v1\n", base)
}
