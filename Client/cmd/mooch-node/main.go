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
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mooch-client/internal/central"
	"mooch-client/internal/config"
	"mooch-client/internal/identity"
	"mooch-client/internal/proxy"
	"mooch-client/internal/server"
	"mooch-client/internal/tui"
)

const probeTimeout = 3 * time.Second

// Log modes for --log-mode. Requests mode logs HTTP requests.
// Dev mode adds debug detail for developers.
const (
	logModeRequests = "requests"
	logModeDev      = "dev"
)

func main() {
	configPath := flag.String("config", "", "path to node YAML configuration")
	logMode := flag.String("log-mode", logDefault("MOOCH_LOG_MODE", logModeRequests), "log mode: requests or dev")
	logFormat := flag.String("log-format", logDefault("MOOCH_LOG_FORMAT", "text"), "log format: text or json")
	flag.Parse()
	if err := run(*configPath, *logMode, *logFormat); err != nil {
		slog.Error("node stopped", "error", err)
		os.Exit(1)
	}
}

// Configure the default logger from mode and format.
// Use requests mode for normal use. Use dev mode for debug work.
func setupLogging(mode, format string) error {
	return setupLoggingTo(os.Stderr, mode, format)
}

// Configure the default logger with an explicit output.
// The headless path uses stderr. The dashboard routes logs to itself.
func setupLoggingTo(w io.Writer, mode, format string) error {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case logModeRequests, "":
		level = slog.LevelInfo
	case logModeDev:
		level = slog.LevelDebug
	default:
		return fmt.Errorf("invalid log mode %q: use requests or dev", mode)
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text", "":
		handler = slog.NewTextHandler(w, opts)
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		return fmt.Errorf("invalid log format %q: use text or json", format)
	}
	slog.SetDefault(slog.New(handler))
	return nil
}

// Route logs to the dashboard with short friendly lines.
// Use requests mode for normal use. Use dev mode for debug work.
func setupDashboardLogging(ui *tui.UI, mode string) error {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case logModeRequests, "":
		level = slog.LevelInfo
	case logModeDev:
		level = slog.LevelDebug
	default:
		return fmt.Errorf("invalid log mode %q: use requests or dev", mode)
	}
	slog.SetDefault(slog.New(tui.NewLogHandler(ui, level)))
	return nil
}

// isTerminal reports whether f is a character device.
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// Read the flag default from the environment.
// Fall back when the variable is empty or unset.
func logDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func run(configPath, logMode, logFormat string) error {
	if err := setupLogging(logMode, logFormat); err != nil {
		return err
	}
	cfg, resolved, err := config.Load(configPath)
	if err != nil {
		return err
	}
	slog.Debug("config load",
		"component", "main",
		"path", resolved,
		"backends", len(cfg.Backends),
		"services", len(cfg.Services),
	)
	nodeID, err := configuredNodeID(cfg.Node.ID)
	if err != nil {
		return err
	}
	name := cfg.Node.Name
	if name == "" {
		name, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("get node hostname: %w", err)
		}
	}
	tailscaleIP, err := identity.ResolveTailscaleIPv4(context.Background())
	if err != nil {
		return err
	}
	listenAddr, err := identity.BuildListenAddr(tailscaleIP.String(), cfg.Network.ListenPort)
	if err != nil {
		return err
	}
	slog.Debug("node identity",
		"component", "main",
		"node_id", nodeID,
		"tailscale_ip", tailscaleIP.String(),
		"listen_addr", listenAddr,
	)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var servicesMu sync.RWMutex
	probe := func(ctx context.Context) {
		servicesMu.Lock()
		defer servicesMu.Unlock()
		for i := range cfg.Services {
			if err := server.ProbeService(ctx, &cfg.Services[i], probeTimeout); err != nil {
				slog.Warn("backend probe failed", "component", "health", "service", cfg.Services[i].ID, "error", err)
			} else {
				slog.Debug("backend probe ok", "component", "health", "service", cfg.Services[i].ID)
			}
		}
	}

	var discMu sync.RWMutex
	discovered := []config.Service(nil)
	discover := func(ctx context.Context) {
		if len(cfg.Backends) == 0 {
			return
		}
		discCtx, cancel := context.WithTimeout(ctx, config.DefaultDiscoverTimeout)
		defer cancel()
		found := config.DiscoverAll(discCtx, http.DefaultClient, cfg.Backends)
		if len(cfg.Backends) > 0 && len(found) == 0 {
			slog.Warn("backend discovery found no models", "component", "discovery")
			return
		}
		discMu.Lock()
		discovered = found
		discMu.Unlock()
		slog.Debug("backend discovery ok",
			"component", "discovery",
			"backends", len(cfg.Backends),
			"services", len(found),
			"models", countModels(found),
		)
	}

	merged := func() []config.Service {
		servicesMu.RLock()
		static := append([]config.Service(nil), cfg.Services...)
		servicesMu.RUnlock()
		discMu.RLock()
		defer discMu.RUnlock()
		return append(static, discovered...)
	}

	// The dashboard owns the terminal when stdout is a terminal.
	// Without a terminal the node keeps plain logs on stderr.
	centralAddr := net.JoinHostPort(cfg.Network.CentralHost, strconv.Itoa(cfg.Network.CentralPort))
	startedAt := time.Now()
	var ui *tui.UI
	if isTerminal(os.Stdout) {
		ui = tui.New(ctx, func() tui.Info {
			services := merged()
			out := make([]tui.Service, 0, len(services))
			for _, service := range services {
				out = append(out, tui.Service{
					ID: service.ID, Name: service.Name,
					Provider: string(service.Provider),
					Models:   service.Models, Healthy: service.Healthy,
				})
			}
			return tui.Info{
				NodeName: name, NodeID: nodeID,
				TailscaleIP: tailscaleIP.String(), ListenAddr: listenAddr,
				CentralAddr: centralAddr, Version: central.Version,
				UpSince: startedAt, Services: out,
			}
		})
		if err := setupDashboardLogging(ui, logMode); err != nil {
			return err
		}
	}
	slog.Info("node start",
		"component", "main",
		"node_id", nodeID,
		"listen_addr", listenAddr,
		"version", central.Version,
		"log_mode", logMode,
	)
	probe(ctx)
	discover(ctx)
	if ui != nil {
		ui.Hosting(modelNames(merged()))
	}

	proxyServices := func() []proxy.Service {
		found := merged()
		out := make([]proxy.Service, 0, len(found))
		for _, service := range found {
			out = append(out, configServiceAdapter{service: service})
		}
		return out
	}
	proxyHandler := proxy.NewHandlerFunc(proxyServices, nodeID)
	nodeServer := server.New(server.Options{
		ListenHost:  cfg.Network.ListenHost,
		ListenPort:  cfg.Network.ListenPort,
		NodeID:      nodeID,
		TailscaleIP: tailscaleIP.String(),
		Version:     central.Version,
		Proxy: server.ProxyHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			proxyHandler.ServeHTTP(w, r)
		}),
		Models: func() []config.Service {
			return merged()
		},
	})

	centralClient, err := central.New(cfg.Network.CentralHost, cfg.Network.CentralPort)
	if err != nil {
		return err
	}
	if ui != nil {
		centralClient.WithHooks(central.Hooks{
			OnRegistered: func() { ui.Joined(centralAddr) },
			OnHeartbeat:  func() { ui.SetStatus(tui.StatusJoined, "") },
			OnError:      func(err error) { ui.Retrying(err.Error()) },
		})
	}
	payload := func() central.Payload {
		probe(ctx)
		discover(ctx)
		services := merged()
		healthy := 0
		for _, service := range services {
			if service.Healthy {
				healthy++
			}
		}
		slog.Debug("heartbeat payload ready",
			"component", "main",
			"services", len(services),
			"healthy", healthy,
		)
		return central.Payload{
			NodeID: nodeID, Name: name, TailscaleIP: tailscaleIP.String(),
			ListenAddr: listenAddr, Version: central.Version,
			Services: services,
		}
	}

	errs := make(chan error, 2)
	go func() {
		err := nodeServer.ListenAndServe()
		if err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
		}
	}()
	go func() { errs <- centralClient.Run(ctx, payload, cfg.Network.HeartbeatInterval) }()

	if ui != nil {
		return runDashboard(ctx, stop, ui, nodeServer, errs, nodeID)
	}

	select {
	case <-ctx.Done():
	case err := <-errs:
		stop()
		return err
	}

	slog.Debug("node shutdown", "component", "main", "node_id", nodeID)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return nodeServer.Shutdown(shutdownCtx)
}

// runDashboard shows the dashboard until quit or stop.
// A server error stops the context, which ends the dashboard.
func runDashboard(ctx context.Context, stop context.CancelFunc, ui *tui.UI, nodeServer *server.Server, errs chan error, nodeID string) error {
	go func() {
		if err := <-errs; err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("node stopped", "component", "main", "node_id", nodeID, "error", err)
		}
		stop()
	}()
	if err := ui.Run(); err != nil {
		stop()
		return err
	}
	stop()
	slog.Debug("node shutdown", "component", "main", "node_id", nodeID)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return nodeServer.Shutdown(shutdownCtx)
}

// modelNames collects the advertised model IDs.
func modelNames(services []config.Service) []string {
	var out []string
	for _, service := range services {
		out = append(out, service.Models...)
	}
	return out
}

// Count the advertised models across discovered services.
func countModels(services []config.Service) int {
	total := 0
	for _, service := range services {
		total += len(service.Models)
	}
	return total
}

func configuredNodeID(value string) (string, error) {
	if value != "" {
		return value, nil
	}
	return identity.LoadOrCreateNodeID("")
}

type configServiceAdapter struct {
	service config.Service
}

func (s configServiceAdapter) ServiceID() string       { return s.service.ID }
func (s configServiceAdapter) BackendEndpoint() string { return s.service.Endpoint }
func (s configServiceAdapter) BackendAPIBase() string  { return s.service.APIBase }
func (s configServiceAdapter) ServedModels() []string  { return s.service.Models }
func (s configServiceAdapter) BackendHealthy() bool    { return s.service.Healthy }
