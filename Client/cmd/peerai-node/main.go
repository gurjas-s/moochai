package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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

	"peer-ai-client/internal/central"
	"peer-ai-client/internal/config"
	"peer-ai-client/internal/identity"
	"peer-ai-client/internal/manage"
	"peer-ai-client/internal/proxy"
	"peer-ai-client/internal/server"
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
	apiKey := flag.String("api-key", strings.TrimSpace(os.Getenv("PEERAI_API_KEY")), "API key for central (or PEERAI_API_KEY)")
	logMode := flag.String("log-mode", logDefault("PEERAI_LOG_MODE", logModeRequests), "log mode: requests or dev")
	logFormat := flag.String("log-format", logDefault("PEERAI_LOG_FORMAT", "text"), "log format: text or json")
	flag.Parse()
	if err := run(*configPath, *apiKey, *logMode, *logFormat); err != nil {
		slog.Error("node stopped", "error", err)
		os.Exit(1)
	}
}

// Configure the default logger from mode and format.
// Use requests mode for normal use. Use dev mode for debug work.
func setupLogging(mode, format string) error {
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
		handler = slog.NewTextHandler(os.Stderr, opts)
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, opts)
	default:
		return fmt.Errorf("invalid log format %q: use text or json", format)
	}
	slog.SetDefault(slog.New(handler))
	return nil
}

// Read the flag default from the environment.
// Fall back when the variable is empty or unset.
func logDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func run(configPath, apiKeyFlag, logMode, logFormat string) error {
	if err := setupLogging(logMode, logFormat); err != nil {
		return err
	}
	cfg, resolved, err := config.Load(configPath)
	if err != nil {
		return err
	}
	// Flag wins for the API key. The browser setup file wins over config.
	if strings.TrimSpace(apiKeyFlag) != "" {
		cfg.Auth.APIKey = strings.TrimSpace(apiKeyFlag)
	}
	credsPath, err := manage.DefaultCredentialsPath(resolved)
	if err != nil {
		return fmt.Errorf("resolve credentials path: %w", err)
	}
	setup := manage.NewSetup(credsPath)
	creds, err := setup.Load()
	if err != nil {
		return err
	}
	if strings.TrimSpace(creds.CentralHost) != "" {
		cfg.Network.CentralHost = strings.TrimSpace(creds.CentralHost)
	}
	if creds.CentralPort != 0 {
		cfg.Network.CentralPort = creds.CentralPort
	}
	if strings.TrimSpace(apiKeyFlag) == "" && creds.KeySet() {
		cfg.Auth.APIKey = strings.TrimSpace(creds.APIKey)
	}
	// Backends come from the website. File entries merge with them.
	// A fresh install auto-detects Ollama or llama.cpp with no setup.
	managedPath, err := manage.DefaultPath()
	if err != nil {
		return fmt.Errorf("resolve managed backends path: %w", err)
	}
	managed := manage.New(managedPath)
	if err := managed.Load(); err != nil {
		return err
	}
	autoBackends := []config.Backend(nil)
	if len(managed.Merge(cfg.Backends)) == 0 {
		autoCtx, autoCancel := context.WithTimeout(context.Background(), 10*time.Second)
		autoBackends = config.DetectResponsive(autoCtx, http.DefaultClient, config.DefaultLocalCandidates)
		autoCancel()
		for _, b := range autoBackends {
			slog.Info("auto-detected backend", "component", "main", "name", b.Name, "endpoint", b.Endpoint)
		}
		if len(autoBackends) == 0 {
			slog.Warn("no local backends found", "component", "main")
		}
	}
	currentBackends := func() []config.Backend {
		if merged := managed.Merge(cfg.Backends); len(merged) > 0 {
			return merged
		}
		return autoBackends
	}
	slog.Debug("config load",
		"component", "main",
		"path", resolved,
		"backends", len(currentBackends()),
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
	// Bind the first free port from the configured one upward.
	// A busy default port no longer stops the node.
	ln, listenPort, err := listenWithFallback(cfg.Network.ListenHost, cfg.Network.ListenPort)
	if err != nil {
		return err
	}
	listenAddr, err := identity.BuildListenAddr(tailscaleIP.String(), listenPort)
	if err != nil {
		_ = ln.Close()
		return err
	}
	slog.Debug("node identity",
		"component", "main",
		"node_id", nodeID,
		"tailscale_ip", tailscaleIP.String(),
		"listen_addr", listenAddr,
	)
	slog.Info("node start",
		"component", "main",
		"node_id", nodeID,
		"listen_addr", listenAddr,
		"version", central.Version,
		"log_mode", logMode,
	)
	centralBase := "http://" + net.JoinHostPort(cfg.Network.CentralHost, strconv.Itoa(cfg.Network.CentralPort))
	manageURL := "http://127.0.0.1:" + strconv.Itoa(listenPort) + "/manage"
	if strings.TrimSpace(cfg.Auth.APIKey) == "" {
		slog.Warn("node has no API key",
			"component", "main",
			"setup", manageURL,
			"signup", centralBase+"/app",
		)
		slog.Info("finish setup in the browser",
			"component", "main",
			"step_1", "open "+manageURL,
			"step_2", "register with central and add services",
			"step_3", "restart the node",
		)
	} else {
		slog.Info("next steps",
			"component", "main",
			"manage_ui", manageURL,
			"share_at", centralBase+"/app",
		)
	}

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
	probe(ctx)

	var discMu sync.RWMutex
	discovered := []config.Service(nil)
	discover := func(ctx context.Context) {
		current := currentBackends()
		if len(current) == 0 {
			return
		}
		discCtx, cancel := context.WithTimeout(ctx, config.DefaultDiscoverTimeout)
		defer cancel()
		found := config.DiscoverAll(discCtx, http.DefaultClient, current)
		if len(current) > 0 && len(found) == 0 {
			slog.Warn("backend discovery found no models", "component", "discovery")
			return
		}
		discMu.Lock()
		discovered = found
		discMu.Unlock()
		slog.Debug("backend discovery ok",
			"component", "discovery",
			"backends", len(current),
			"services", len(found),
			"models", countModels(found),
		)
	}
	discover(ctx)

	merged := func() []config.Service {
		servicesMu.RLock()
		static := append([]config.Service(nil), cfg.Services...)
		servicesMu.RUnlock()
		discMu.RLock()
		defer discMu.RUnlock()
		return append(static, discovered...)
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
		ListenPort:  listenPort,
		NodeID:      nodeID,
		TailscaleIP: tailscaleIP.String(),
		Version:     central.Version,
		Manage:      managed,
		Setup:       setup,
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
	centralClient.WithAPIKey(cfg.Auth.APIKey)
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
		err := nodeServer.Serve(ln)
		if err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
		}
	}()
	go func() { errs <- centralClient.Run(ctx, payload, cfg.Network.HeartbeatInterval) }()

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

// maxPortFallback is the extra ports tried after the configured one.
const maxPortFallback = 9

// listenWithFallback binds host:port, or the next free port upward.
// A busy default port picks 9101 instead of stopping the node.
func listenWithFallback(host string, port int) (net.Listener, int, error) {
	var err error
	var ln net.Listener
	for p := port; p <= port+maxPortFallback; p++ {
		ln, err = net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			if p != port {
				slog.Warn("listen port busy, using next free port",
					"component", "main",
					"want", port,
					"got", p,
				)
			}
			return ln, p, nil
		}
	}
	return nil, 0, fmt.Errorf("listen on %s:%d: %w", host, port, err)
}

type configServiceAdapter struct {
	service config.Service
}

func (s configServiceAdapter) ServiceID() string       { return s.service.ID }
func (s configServiceAdapter) BackendEndpoint() string { return s.service.Endpoint }
func (s configServiceAdapter) BackendAPIBase() string  { return s.service.APIBase }
func (s configServiceAdapter) ServedModels() []string  { return s.service.Models }
func (s configServiceAdapter) BackendHealthy() bool    { return s.service.Healthy }
