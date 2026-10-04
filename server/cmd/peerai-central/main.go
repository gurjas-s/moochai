// Command peerai-central is the PeerAI central server.
//
// Nodes register and send heartbeats to central over Tailscale. Tools send
// OpenAI-compatible requests to central. Central forwards each request to a
// node that serves the requested model.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"strings"
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
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	if err := run(*addr, *binDir, *ttl, log); err != nil {
		log.Error("central stopped", "error", err)
		os.Exit(1)
	}
}

func run(addr, binDir string, ttl time.Duration, log *slog.Logger) error {
	listen, tsIP, err := hostAddr(addr)
	if err != nil {
		return err
	}

	reg := registry.New(ttl)
	authStore := auth.New()
	mux := http.NewServeMux()
	api.New(reg, authStore, log).Register(mux)
	router.New(reg, authStore, log).Register(mux)
	app.New(log).Register(mux)
	join.New(binDir, log).Register(mux)

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	log.Info("central listening", "addr", ln.Addr().String())
	if tsIP.IsValid() {
		printJoinInfo(os.Stdout, tsIP, tailscaleDNSName(), port)
	} else {
		log.Warn("central does not listen on a Tailscale IP, so other machines cannot join")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go sweep(ctx, reg, ttl/3, log)

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
	log.Info("central stopped")
	return nil
}

func sweep(ctx context.Context, reg *registry.Registry, every time.Duration, log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, id := range reg.Sweep() {
				log.Info("node expired", "node_id", id)
			}
		}
	}
}

// hostAddr returns the address to listen on. An empty host becomes the
// Tailscale IPv4 of this machine, so only the tailnet can reach central.
// tsIP is valid when the listen host is a Tailscale IPv4.
func hostAddr(addr string) (listen string, tsIP netip.Addr, err error) {
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

// tailscaleDNSName returns the MagicDNS name of this machine, or "".
func tailscaleDNSName() string {
	for _, bin := range []string{"tailscale", "/Applications/Tailscale.app/Contents/MacOS/Tailscale"} {
		out, err := exec.Command(bin, "status", "--self", "--json").Output()
		if err != nil {
			continue
		}
		var st struct{ Self struct{ DNSName string } }
		if json.Unmarshal(out, &st) == nil {
			return strings.TrimSuffix(st.Self.DNSName, ".")
		}
	}
	return ""
}

func printJoinInfo(w io.Writer, ip netip.Addr, dnsName, port string) {
	base := "http://" + net.JoinHostPort(ip.String(), port)
	fmt.Fprintf(w, "\nPeerAI central is up on the tailnet.\n\n")
	fmt.Fprintf(w, "  Join page:   %s/join\n", base)
	fmt.Fprintf(w, "  Join command (macOS/Linux):\n    curl -fsSL %s/join.sh | sh\n", base)
	fmt.Fprintf(w, "  OpenAI base URL for tools: %s/v1\n", base)
	if dnsName != "" {
		fmt.Fprintf(w, "  MagicDNS name: %s\n", dnsName)
	}
	fmt.Fprintf(w, "\nManual node config (peerai-node.yaml):\nnetwork:\n  central_host: %q\n  central_port: %s\n\n", ip.String(), port)
}
