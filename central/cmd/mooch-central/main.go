// Command mooch-central routes OpenAI requests from tools to Mooch.ai nodes on the tailnet.
package main

import (
	"bufio"
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
	"strings"
	"syscall"
	"time"

	"mooch-central/internal/api"
	"mooch-central/internal/app"
	"mooch-central/internal/feed"
	"mooch-central/internal/join"
	"mooch-central/internal/ollama"
	"mooch-central/internal/registry"
	"mooch-central/internal/router"
	"mooch-central/internal/stats"
	"mooch-central/internal/tui"
)

var tailscaleRange = netip.MustParsePrefix("100.64.0.0/10")

func main() {
	if err := loadEnvFile(".env"); err != nil {
		fmt.Fprintln(os.Stderr, "read .env:", err)
		os.Exit(1)
	}
	defaultAddr := ":8080"
	if a := os.Getenv("MOOCH_ADDR"); a != "" {
		defaultAddr = a
	}
	addr := flag.String("addr", defaultAddr, "listen address, or MOOCH_ADDR. An empty host means the Tailscale IPv4 of this machine")
	analyticsAddr := flag.String("analytics-addr", "127.0.0.1:3000", "listen address of the analytics page. Keep it on loopback, so only this machine can open it")
	binDir := flag.String("bin", "dist", "directory with node binaries for /join")
	ttl := flag.Duration("node-ttl", 45*time.Second, "remove a node after this time without a heartbeat")
	debug := flag.Bool("debug", false, "log each heartbeat")
	verbose := flag.Bool("verbose", false, "print plain log lines instead of the coloured feed")
	plain := flag.Bool("plain", false, "print the coloured feed without the dashboard")
	details := flag.Bool("details", false, "show the method, the path, and the status on REQUEST and RESPONSE lines")
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
	if err := run(*addr, *analyticsAddr, *binDir, *ttl, mode, level, *details); err != nil {
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

func run(addr, analyticsAddr, binDir string, ttl time.Duration, mode outputMode, level slog.Level, details bool) error {
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
	aln, err := net.Listen("tcp", analyticsAddr)
	if err != nil {
		ln.Close()
		return fmt.Errorf("analytics listener: %w", err)
	}
	analyticsURL := "http://" + aln.Addr().String() + "/analytics"
	slog.Info("analytics listening", "url", analyticsURL)
	// Also listen on loopback at the same port, so programs on this machine can connect.
	// macOS does not let a loopback source address connect to the Tailscale IPv4. The demo scripts need this.
	var lln net.Listener
	if local, ok := loopbackAddr(ln.Addr().String()); ok {
		if lln, err = net.Listen("tcp", local); err != nil {
			slog.Warn("central does not listen on loopback", "addr", local, "error", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := registry.New(ttl)
	var f *feed.Feed
	var ui *tui.UI
	switch mode {
	case modeDashboard:
		ui = tui.New(ctx, reg, ln.Addr().String(), tailscaleIP.IsValid())
		f = feed.NewDashboard(ui)
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
		fmt.Fprintf(os.Stdout, "  Analytics (this machine only): %s\n\n", analyticsURL)
	}
	if f != nil {
		f.Details = details
	}
	if !tailscaleIP.IsValid() && ui == nil {
		slog.Warn("central does not listen on a Tailscale IP, so other machines cannot join")
	}

	store := openStats(ctx)
	defer store.Close()
	apiHandler, routes, ollamaHandler := api.New(reg, f), router.New(reg, f), ollama.New(reg)
	if store != nil {
		apiHandler.Heartbeat = store.Heartbeat
		routes.Record = store.Record
		ollamaHandler.Record = store.Record
		reg.SetLoad(store.Load)
		go store.RefreshLoad(ctx)
	}

	mux := http.NewServeMux()
	apiHandler.Register(mux)
	routes.Register(mux)
	ollamaHandler.Register(mux)
	// The analytics page and its API listen only on analyticsAddr. The tailnet gets only the leaderboard.
	analyticsMux := http.NewServeMux()
	store.Register(analyticsMux)
	mux.Handle("GET /leaderboard.json", analyticsMux)
	app.Register(mux)
	join.New(binDir).Register(mux)
	go expireNodesLoop(ctx, reg, f, ttl/3)

	asrv := &http.Server{Handler: analyticsMux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = asrv.Serve(aln) }()

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	if lln != nil {
		go func() { _ = srv.Serve(lln) }()
	}
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
	_ = asrv.Shutdown(shutdown)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("central stopped")
	return nil
}

// openStats connects to the analytics database at MOOCH_DB_URL. Without the variable, or when the
// connection fails, it returns nil and central runs without analytics and with round-robin routing.
func openStats(ctx context.Context) *stats.Store {
	url := os.Getenv("MOOCH_DB_URL")
	if url == "" {
		slog.Info("analytics off: MOOCH_DB_URL is not set")
		return nil
	}
	openCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	store, err := stats.Open(openCtx, url)
	if err != nil {
		slog.Warn("analytics off: cannot open the database", "error", err)
		return nil
	}
	slog.Info("analytics on")
	return store
}

// loadEnvFile sets each KEY=value line of path as an environment variable. A variable that is already set
// keeps its value, so the shell wins over the file. A missing file is not an error.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			return fmt.Errorf("%s:%d: want KEY=value", path, n)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, value)
		}
	}
	return sc.Err()
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

// loopbackAddr returns 127.0.0.1 with the port of listen. It reports false when listen already accepts loopback connections.
func loopbackAddr(listen string) (string, bool) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", false
	}
	if ip, err := netip.ParseAddr(host); err != nil || ip.IsLoopback() || ip.IsUnspecified() {
		return "", false
	}
	return net.JoinHostPort("127.0.0.1", port), true
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
	fmt.Fprintf(w, "\nMooch.ai central is up on the tailnet.\n\n  Join page:   %[1]s/join\n  Join command (macOS/Linux):\n    curl -fsSL %[1]s/join.sh | sh\n  OpenAI base URL for tools: %[1]s/v1\n", base)
}
