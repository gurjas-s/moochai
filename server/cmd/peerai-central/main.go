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
	"peerai-serv/internal/feed"
	"peerai-serv/internal/join"
	"peerai-serv/internal/registry"
	"peerai-serv/internal/router"
	"peerai-serv/internal/tui"
)

var tailscaleRange = netip.MustParsePrefix("100.64.0.0/10")

func main() {
	addr := flag.String("addr", ":8080", "listen address. An empty host means the Tailscale IPv4 of this machine")
	binDir := flag.String("bin", "dist", "directory with node binaries for /join")
	ttl := flag.Duration("node-ttl", 45*time.Second, "remove a node after this time without a heartbeat")
	debug := flag.Bool("debug", false, "log each heartbeat")
	verbose := flag.Bool("verbose", false, "print plain log lines instead of the coloured feed")
	plain := flag.Bool("plain", false, "print the coloured feed without the dashboard")
	flag.Parse()

	// The feed replaces the info logs. Warnings and errors still go to the log output.
	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelInfo
	}
	if *debug {
		level = slog.LevelDebug
	}
	setLogOutput(os.Stderr, level)

	mode := modeDashboard
	switch {
	case *verbose:
		mode = modeLog
	case *plain || !isTerminal(os.Stdout):
		mode = modeFeed
	}
	if err := run(*addr, *binDir, *ttl, mode, level); err != nil {
		slog.Error("central stopped", "error", err)
		os.Exit(1)
	}
}

type outputMode int

const (
	modeDashboard outputMode = iota // full-screen dashboard
	modeFeed                        // coloured feed lines on stdout
	modeLog                         // plain slog lines
)

func run(addr, binDir string, ttl time.Duration, mode outputMode, level slog.Level) error {
	listen, tailscaleIP, err := resolveListenAddr(addr)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	slog.Info("central listening", "addr", ln.Addr().String())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := registry.New(ttl)
	var f *feed.Feed
	var ui *tui.UI
	switch mode {
	case modeDashboard:
		ui = tui.New(ctx, reg, ln.Addr().String(), tailscaleIP.IsValid())
		f = feed.NewColor(ui)
		// The dashboard owns the terminal, so warnings go to the console.
		slog.SetDefault(slog.New(slog.NewTextHandler(consoleLog{ui}, &slog.HandlerOptions{Level: level,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey) {
					return slog.Attr{}
				}
				return a
			}})))
	case modeFeed:
		f = feed.New(os.Stdout)
		f.Banner(ln.Addr().String())
		if tailscaleIP.IsValid() {
			printJoinInfo(os.Stdout, tailscaleIP, port)
		}
	}
	if !tailscaleIP.IsValid() && ui == nil {
		slog.Warn("central does not listen on a Tailscale IP, so other machines cannot join")
	}

	mux := http.NewServeMux()
	api.New(reg, f).Register(mux)
	router.New(reg, f).Register(mux)
	app.Register(mux)
	join.New(binDir).Register(mux)
	go expireNodesLoop(ctx, reg, f, ttl/3)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() {
		served <- srv.Serve(ln)
		stop()
	}()
	if ui != nil {
		// The dashboard reads keys in raw mode, so q and ctrl+c arrive here and not as signals.
		if err := ui.Run(); err != nil {
			slog.Error("dashboard stopped", "error", err)
		}
		stop()
		setLogOutput(os.Stderr, level)
	}
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("central stopped")
	return nil
}

func setLogOutput(w io.Writer, level slog.Level) {
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})))
}

// consoleLog formats slog lines like feed events in the dashboard console.
type consoleLog struct{ w io.Writer }

func (c consoleLog) Write(p []byte) (int, error) {
	fmt.Fprintf(c.w, "\033[2m%s\033[0m  \033[1;33mLOG  \033[0m %s", time.Now().Format("15:04:05"), p)
	return len(p), nil
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func expireNodesLoop(ctx context.Context, reg *registry.Registry, f *feed.Feed, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, n := range reg.Sweep() {
				slog.Info("node expired", "node_id", n.NodeID)
				f.Left(n)
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
