// Package ollama provides the small Ollama API surface that Zed uses for
// model discovery and chat.
package ollama

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"mooch-serv/internal/feed"
	"mooch-serv/internal/registry"
	"mooch-serv/internal/respond"
	"mooch-serv/internal/router"
	"mooch-serv/internal/stats"
)

const maxBody = 32 << 20

type Handler struct {
	reg    *registry.Registry
	client *http.Client
	// Record gets one row for each chat request that goes to a node. Nil records nothing.
	Record func(stats.Request)
}

func New(reg *registry.Registry) *Handler {
	return &Handler{
		reg: reg,
		client: &http.Client{Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			ResponseHeaderTimeout: 5 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
		}},
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tags", h.tags)
	mux.HandleFunc("POST /api/show", h.show)
	mux.HandleFunc("POST /api/chat", h.chat)
}

func (h *Handler) tags(w http.ResponseWriter, _ *http.Request) {
	type model struct {
		Name       string         `json:"name"`
		Model      string         `json:"model"`
		ModifiedAt time.Time      `json:"modified_at"`
		Size       int64          `json:"size"`
		Digest     string         `json:"digest"`
		Details    map[string]any `json:"details"`
	}
	out := make([]model, 0)
	for _, m := range h.reg.Models() {
		out = append(out, model{
			Name: m.ID, Model: m.ID, ModifiedAt: time.Now().UTC(),
			Digest: "mooch-" + m.ID,
			Details: map[string]any{
				"format":             "gguf",
				"family":             "mooch",
				"families":           []string{"mooch"},
				"parameter_size":     "unknown",
				"quantization_level": "unknown",
			},
		})
	}
	respond.JSON(w, map[string]any{"models": out})
}

func (h *Handler) show(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if !decode(w, r, &req) {
		return
	}
	model, ok := h.model(req.Model)
	if !ok {
		ollamaError(w, http.StatusNotFound, fmt.Sprintf("model %q not found", req.Model))
		return
	}
	info := map[string]any{
		"format":             "gguf",
		"family":             "mooch",
		"families":           []string{"mooch"},
		"parameter_size":     "unknown",
		"quantization_level": "unknown",
	}
	if model.MaxModelLen > 0 {
		info["parameter_size"] = "unknown"
	}
	result := map[string]any{
		"modelfile": "", "parameters": "", "template": "", "details": info,
		"capabilities": []string{"completion"},
	}
	if model.MaxModelLen > 0 {
		result["model_info"] = map[string]any{
			"general.architecture": "mooch",
			"mooch.context_length": model.MaxModelLen,
		}
	}
	respond.JSON(w, result)
}

func (h *Handler) chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if !decode(w, r, &req) {
		return
	}
	node, ok := h.reg.Lookup(req.Model)
	if !ok {
		ollamaError(w, http.StatusNotFound, fmt.Sprintf("model %q not found", req.Model))
		return
	}
	if h.Record != nil {
		start, status := time.Now(), http.StatusOK
		defer func() {
			h.Record(stats.Request{Time: start, Requester: router.Requester(h.reg, r), Node: feed.Name(node),
				Model: req.Model, Path: r.URL.Path, Status: status, Duration: time.Since(start)})
		}()
		w = statusWriter{w, &status}
	}
	body, err := json.Marshal(openAIRequest(req))
	if err != nil {
		ollamaError(w, http.StatusBadRequest, err.Error())
		return
	}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		"http://"+node.ListenAddr+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		ollamaError(w, http.StatusBadGateway, err.Error())
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(upstream)
	if err != nil {
		ollamaError(w, http.StatusBadGateway, "node is unreachable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	if req.Stream {
		h.stream(w, resp.Body, req.Model)
		return
	}
	h.complete(w, resp.Body, req.Model)
}

type chatRequest struct {
	Model    string           `json:"model"`
	Messages []map[string]any `json:"messages"`
	Stream   bool             `json:"stream"`
	Tools    []map[string]any `json:"tools,omitempty"`
	Options  map[string]any   `json:"options,omitempty"`
}

func openAIRequest(req chatRequest) map[string]any {
	out := map[string]any{"model": req.Model, "messages": req.Messages, "stream": req.Stream}
	if len(req.Tools) > 0 {
		out["tools"] = req.Tools
	}
	for from, to := range map[string]string{
		"num_predict": "max_tokens", "temperature": "temperature", "stop": "stop",
	} {
		if value, ok := req.Options[from]; ok {
			out[to] = value
		}
	}
	return out
}

func (h *Handler) stream(w http.ResponseWriter, body io.Reader, model string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	rc := http.NewResponseController(w) // Flush finds the http.Flusher through Unwrap
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "data: [DONE]" {
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			line = strings.TrimPrefix(line, "data: ")
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string           `json:"content"`
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(line), &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		message := map[string]any{"role": "assistant", "content": choice.Delta.Content}
		if len(choice.Delta.ToolCalls) > 0 {
			message["tool_calls"] = choice.Delta.ToolCalls
		}
		writeLine(w, map[string]any{"model": model, "created_at": time.Now().UTC(), "message": message, "done": false})
		_ = rc.Flush()
	}
	writeLine(w, map[string]any{
		"model":       model,
		"created_at":  time.Now().UTC(),
		"message":     map[string]any{"role": "assistant", "content": ""},
		"done_reason": "stop",
		"done":        true,
	})
	_ = rc.Flush()
}

func (h *Handler) complete(w http.ResponseWriter, body io.Reader, model string) {
	var result struct {
		Choices []struct {
			Message map[string]any `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(body).Decode(&result); err != nil || len(result.Choices) == 0 {
		ollamaError(w, http.StatusBadGateway, "invalid response from node")
		return
	}
	respond.JSON(w, map[string]any{
		"model": model, "created_at": time.Now().UTC(),
		"message": result.Choices[0].Message, "done": true,
	})
}

// statusWriter keeps the status of the response for Record. Unwrap lets the stream flush.
type statusWriter struct {
	http.ResponseWriter
	status *int
}

func (s statusWriter) WriteHeader(code int) {
	*s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (h *Handler) model(id string) (registry.Model, bool) {
	for _, model := range h.reg.Models() {
		if model.ID == id {
			return model, true
		}
	}
	return registry.Model{}, false
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(dst); err != nil {
		ollamaError(w, http.StatusBadRequest, "request body must be valid JSON")
		return false
	}
	return true
}

func writeLine(w io.Writer, value any) {
	_ = json.NewEncoder(w).Encode(value)
}

func ollamaError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
