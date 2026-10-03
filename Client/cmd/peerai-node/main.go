package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"peer-ai-client/internal/central"
	"peer-ai-client/internal/config"
	"peer-ai-client/internal/identity"
	"peer-ai-client/internal/proxy"
	"peer-ai-client/internal/server"
)

const probeTimeout = 3 * time.Second

func main() {
	configPath := flag.String("config", "", "path to node YAML configuration")
	flag.Parse()
	if err := run(*configPath); err != nil {
		slog.Error("node stopped", "error", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, _, err := config.Load(configPath)
	if err != nil {
		return err
	}
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var servicesMu sync.RWMutex
	probe := func(ctx context.Context) {
		servicesMu.Lock()
		defer servicesMu.Unlock()
		for i := range cfg.Services {
			if err := server.ProbeService(ctx, &cfg.Services[i], probeTimeout); err != nil {
				slog.Warn("backend probe failed", "component", "health", "service", cfg.Services[i].ID, "error", err)
			}
		}
	}
	probe(ctx)

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
	payload := func() central.Payload {
		probe(ctx)
		discover(ctx)
		return central.Payload{
			NodeID: nodeID, Name: name, TailscaleIP: tailscaleIP.String(),
			ListenAddr: listenAddr, Version: central.Version,
			Services: merged(),
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

	select {
	case <-ctx.Done():
	case err := <-errs:
		stop()
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return nodeServer.Shutdown(shutdownCtx)
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
