// Package config discovers models from local backends.
//
// Discovery keeps the broadcast shape unchanged. It reads the
// OpenAI-standard GET {api_base}/models list, then fills extras on a
// best-effort basis: max_model_len or context_length from the list,
// n_ctx from llamacpp GET /props, and context_length from Ollama
// POST /api/show. Manual model_meta in config always wins.
package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// DefaultDiscoverTimeout bounds one backend discovery round.
const DefaultDiscoverTimeout = 5 * time.Second

// DefaultLocalCandidates are the backends the node probes when the user
// configures none. Common local ports per backend type.
var DefaultLocalCandidates = []Backend{
	{Name: "ollama", Endpoint: "http://127.0.0.1:11434", Provider: ProviderOllama, Expose: []string{"*"}},
	{Name: "llamacpp", Endpoint: "http://127.0.0.1:8080", Provider: ProviderLlamaCPP, Expose: []string{"*"}},
	{Name: "vllm", Endpoint: "http://127.0.0.1:8000", Provider: ProviderVLLM, Expose: []string{"*"}},
	{Name: "lm-studio", Endpoint: "http://127.0.0.1:1234", Provider: ProviderOpenAILike, Expose: []string{"*"}},
}

// DetectResponsive returns the candidates that answer GET models.
// The node calls this when no backend is configured, so a fresh install
// finds Ollama or llama.cpp with no setup. A 200 with a model list counts.
func DetectResponsive(ctx context.Context, client *http.Client, candidates []Backend) []Backend {
	if client == nil {
		client = http.DefaultClient
	}
	var out []Backend
	for _, backend := range candidates {
		if err := backend.Validate(); err != nil {
			continue
		}
		func() {
			reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			entries, err := fetchModels(reqCtx, client, backend)
			if err != nil || len(entries) == 0 {
				return
			}
			out = append(out, backend)
		}()
	}
	return out
}

type modelEntry struct {
	ID            string         `json:"id"`
	MaxModelLen   *int           `json:"max_model_len"`
	ContextLength *int           `json:"context_length"`
	Meta          map[string]any `json:"meta"`
}

type modelsResponse struct {
	Data []modelEntry `json:"data"`
}

type llamaProps struct {
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
	NCtx int `json:"n_ctx"`
}

type ollamaShowResponse struct {
	Details struct {
		Family string `json:"family"`
	} `json:"details"`
	ModelInfo map[string]any `json:"model_info"`
}

// DiscoverBackend queries one backend and builds broadcast Service objects.
// A list error fails the whole backend. Extras errors only skip extras.
func DiscoverBackend(ctx context.Context, client *http.Client, backend Backend) ([]Service, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if err := backend.Validate(); err != nil {
		return nil, err
	}
	entries, err := fetchModels(ctx, client, backend)
	if err != nil {
		return nil, err
	}
	entries = filterEntries(entries, backend.Expose)

	propsCtx := 0
	if backend.ResolvedProvider() == ProviderLlamaCPP {
		propsCtx, _ = fetchLlamaCtx(ctx, client, backend)
	}
	services := make([]Service, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" {
			continue
		}
		contextWindow := contextFromEntry(entry)
		if propsCtx > 0 {
			contextWindow = &propsCtx
		}
		if backend.ResolvedProvider() == ProviderOllama {
			if n, err := fetchOllamaCtx(ctx, client, backend, entry.ID); err == nil && n > 0 {
				contextWindow = &n
			}
		}
		meta := map[string]any{}
		if contextWindow != nil && *contextWindow > 0 {
			meta["context_window"] = *contextWindow
		}
		if override, ok := backend.ModelMeta[entry.ID]; ok {
			if override.ContextWindow != nil {
				meta["context_window"] = *override.ContextWindow
			}
			if override.MaxTokens != nil {
				meta["max_tokens"] = *override.MaxTokens
			}
		}
		var metaOut map[string]any
		if len(meta) > 0 {
			metaOut = meta
		}
		services = append(services, Service{
			ID:                backend.Name + "-" + entry.ID,
			Name:              entry.ID,
			Type:              backend.ResolvedType(),
			Provider:          backend.ResolvedProvider(),
			Endpoint:          backend.Endpoint,
			APIBase:           backend.ResolvedAPIBase(),
			Models:            []string{entry.ID},
			SupportsStreaming: backend.ResolvedStreaming(),
			Healthy:           true,
			Meta:              metaOut,
		})
	}
	return services, nil
}

// DiscoverAll queries every backend and merges the results. A failed
// backend is skipped. The caller keeps the last good list on total failure.
func DiscoverAll(ctx context.Context, client *http.Client, backends []Backend) []Service {
	var out []Service
	for _, backend := range backends {
		services, err := DiscoverBackend(ctx, client, backend)
		if err != nil {
			continue
		}
		out = append(out, services...)
	}
	return out
}

func fetchModels(ctx context.Context, client *http.Client, backend Backend) ([]modelEntry, error) {
	endpoint, err := url.Parse(backend.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("backend %q has invalid endpoint", backend.Name)
	}
	endpoint.Path = path.Join(endpoint.Path, strings.TrimPrefix(backend.ResolvedAPIBase(), "/"), "models")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create models request for backend %q: %w", backend.Name, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query backend %q: %w", backend.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("backend %q returned HTTP %d", backend.Name, resp.StatusCode)
	}
	var list modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decode models for backend %q: %w", backend.Name, err)
	}
	return list.Data, nil
}

func filterEntries(entries []modelEntry, expose []string) []modelEntry {
	allow := []string{}
	for _, name := range expose {
		if name == "*" || strings.TrimSpace(name) == "" {
			return entries
		}
		allow = append(allow, name)
	}
	if len(allow) == 0 {
		return entries
	}
	keep := make(map[string]bool, len(allow))
	for _, name := range allow {
		keep[name] = true
	}
	out := make([]modelEntry, 0, len(entries))
	for _, entry := range entries {
		if keep[entry.ID] {
			out = append(out, entry)
		}
	}
	return out
}

func contextFromEntry(entry modelEntry) *int {
	if entry.MaxModelLen != nil && *entry.MaxModelLen > 0 {
		return entry.MaxModelLen
	}
	if entry.ContextLength != nil && *entry.ContextLength > 0 {
		return entry.ContextLength
	}
	// meta.n_ctx is the live server context. meta.n_ctx_train is only the
	// trained maximum. Prefer the live value.
	if n, ok := metaInt(entry.Meta, "n_ctx"); ok {
		return &n
	}
	if n, ok := metaInt(entry.Meta, "n_ctx_train"); ok {
		return &n
	}
	return nil
}

func metaInt(meta map[string]any, key string) (int, bool) {
	raw, ok := meta[key]
	if !ok {
		return 0, false
	}
	switch n := raw.(type) {
	case float64:
		if n > 0 {
			return int(n), true
		}
	case int:
		if n > 0 {
			return n, true
		}
	}
	return 0, false
}

func fetchLlamaCtx(ctx context.Context, client *http.Client, backend Backend) (int, error) {
	endpoint, err := url.Parse(backend.Endpoint)
	if err != nil {
		return 0, err
	}
	endpoint.Path = "/props"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("props returned HTTP %d", resp.StatusCode)
	}
	var props llamaProps
	if err := json.NewDecoder(resp.Body).Decode(&props); err != nil {
		return 0, err
	}
	if props.DefaultGenerationSettings.NCtx > 0 {
		return props.DefaultGenerationSettings.NCtx, nil
	}
	if props.NCtx > 0 {
		return props.NCtx, nil
	}
	return 0, fmt.Errorf("props carry no n_ctx")
}

func fetchOllamaCtx(ctx context.Context, client *http.Client, backend Backend, model string) (int, error) {
	endpoint, err := url.Parse(backend.Endpoint)
	if err != nil {
		return 0, err
	}
	endpoint.Path = "/api/show"
	body, _ := json.Marshal(map[string]string{"model": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("show returned HTTP %d", resp.StatusCode)
	}
	var show ollamaShowResponse
	if err := json.NewDecoder(resp.Body).Decode(&show); err != nil {
		return 0, err
	}
	arch := show.Details.Family
	if arch == "" {
		if raw, ok := show.ModelInfo["general.architecture"]; ok {
			arch, _ = raw.(string)
		}
	}
	if arch != "" {
		if raw, ok := show.ModelInfo[arch+".context_length"]; ok {
			if n, ok := jsonNumberToInt(raw); ok {
				return n, nil
			}
		}
	}
	for key, raw := range show.ModelInfo {
		if strings.HasSuffix(key, ".context_length") {
			if n, ok := jsonNumberToInt(raw); ok {
				return n, nil
			}
		}
	}
	return 0, fmt.Errorf("show carries no context_length for %q", model)
}

func jsonNumberToInt(raw any) (int, bool) {
	switch n := raw.(type) {
	case float64:
		if n > 0 {
			return int(n), true
		}
	case int:
		if n > 0 {
			return n, true
		}
	}
	return 0, false
}
