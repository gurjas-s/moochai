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
	"sync"
	"sync/atomic"
	"time"

	"mooch-central/internal/feed"
	"mooch-central/internal/registry"
	"mooch-central/internal/respond"
	"mooch-central/internal/router"
	"mooch-central/internal/stats"
)

const maxBody = 32 << 20

type Handler struct {
	reg    *registry.Registry
	client *http.Client
	feed   *feed.Feed
	contextMu sync.Mutex
	contexts  map[int][]map[string]any
	sessions  map[string][]map[string]any
	nextContext atomic.Int64
	// Record gets one row for each chat request that goes to a node. Nil records nothing.
	Record func(stats.Request)
}

func New(reg *registry.Registry, feeds ...*feed.Feed) *Handler {
	var f *feed.Feed
	if len(feeds) > 0 {
		f = feeds[0]
	}
	return &Handler{
		reg: reg, feed: f, contexts: make(map[int][]map[string]any), sessions: make(map[string][]map[string]any),
		client: &http.Client{Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			ResponseHeaderTimeout: 5 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
			// Keep connections open for parallel requests. The default of 2 closes most connections under load,
			// and each closed connection holds a local port for about 30 s.
			MaxIdleConnsPerHost: 256,
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
	}
	if len(model.Capabilities) > 0 {
		result["capabilities"] = model.Capabilities
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
	start, status := time.Now(), http.StatusOK
	requester, destination := router.Requester(h.reg, r), feed.Name(node)
	if h.feed != nil {
		h.feed.Route(requester, req.Model, destination)
		h.feed.Request(requester, destination, r.Method, r.URL.Path, feed.Preview(mustJSON(openAIRequest(req))))
	}
	sessionKey := requester + "\x00" + req.Model
	req.Messages = h.contextMessages(req, sessionKey)
	var tail stats.Tail // the end of the node response, for the token usage and feed preview
	if h.Record != nil || h.feed != nil {
		defer func() {
			if h.feed != nil {
				h.feed.Response(destination, requester, status, time.Since(start), feed.ResponsePreview(tail.Bytes()))
			}
			if h.Record == nil {
				return
			}
			prompt, completion := stats.Usage(tail.Bytes())
			h.Record(stats.Request{Time: start, Requester: requester, Node: destination,
				Model: req.Model, Path: r.URL.Path, Status: status, Duration: time.Since(start),
				PromptTokens: prompt, CompletionTokens: completion})
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
	nodeBody := io.TeeReader(resp.Body, &tail)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, nodeBody)
		return
	}
	if req.Stream {
		h.stream(w, nodeBody, req.Model, req, sessionKey)
		return
	}
	h.complete(w, nodeBody, req.Model, req, sessionKey)
}

const maxStoredContexts = 4096

func (h *Handler) contextMessages(req chatRequest, sessionKey string) []map[string]any {
	h.contextMu.Lock()
	defer h.contextMu.Unlock()
	var previous []map[string]any
	if len(req.Context) > 0 {
		previous = h.contexts[req.Context[0]]
	} else {
		previous = h.sessions[sessionKey]
	}
	if len(previous) == 0 || hasMessagesPrefix(req.Messages, previous) {
		return req.Messages
	}
	messages := make([]map[string]any, 0, len(previous)+len(req.Messages))
	messages = append(messages, previous...)
	messages = append(messages, req.Messages...)
	return messages
}

func hasMessagesPrefix(messages, prefix []map[string]any) bool {
	if len(messages) < len(prefix) {
		return false
	}
	for i := range prefix {
		if string(mustJSON(messages[i])) != string(mustJSON(prefix[i])) {
			return false
		}
	}
	return true
}

func (h *Handler) saveContext(req chatRequest, assistant map[string]any, sessionKey string) []int {
	if len(req.Messages) == 0 {
		return nil
	}
	id := int(h.nextContext.Add(1))
	messages := make([]map[string]any, 0, len(req.Messages)+1)
	messages = append(messages, req.Messages...)
	if assistant != nil {
		messages = append(messages, assistant)
	}
	h.contextMu.Lock()
	if len(h.contexts) >= maxStoredContexts {
		for key := range h.contexts {
			delete(h.contexts, key)
			break
		}
	}
	h.contexts[id] = messages
	if len(h.sessions) >= maxStoredContexts {
		for key := range h.sessions {
			delete(h.sessions, key)
			break
		}
	}
	h.sessions[sessionKey] = messages
	h.contextMu.Unlock()
	return []int{id}
}

func mustJSON(value any) []byte {
	body, _ := json.Marshal(value)
	return body
}

type chatRequest struct {
	Model    string           `json:"model"`
	Messages []map[string]any `json:"messages"`
	Context  []int            `json:"context,omitempty"`
	Stream   bool             `json:"stream"`
	Tools    []map[string]any `json:"tools,omitempty"`
	Options  map[string]any   `json:"options,omitempty"`
}

func openAIRequest(req chatRequest) map[string]any {
	out := map[string]any{"model": req.Model, "messages": req.Messages, "stream": req.Stream}
	if len(req.Context) > 0 {
		out["context"] = req.Context
	}
	if req.Stream {
		// Ask for a last chunk with the token usage. The stream loop skips that chunk, because it has no choices.
		out["stream_options"] = map[string]any{"include_usage": true}
	}
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

func (h *Handler) stream(w http.ResponseWriter, body io.Reader, model string, req chatRequest, sessionKey string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	rc := http.NewResponseController(w) // Flush finds the http.Flusher through Unwrap
	scanner := bufio.NewScanner(body)
	var usage *ollamaUsage
	var context []int
	var content strings.Builder
	var toolCalls []any
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
					ToolCalls []any `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *openAIUsage `json:"usage"`
			Context []int `json:"context"`
		}
		if json.Unmarshal([]byte(line), &chunk) != nil {
			continue
		}
		if len(chunk.Context) > 0 {
			// Ollama can return context on the final response. Keep it for
			// clients that use the stateful Ollama protocol.
			context = append([]int(nil), chunk.Context...)
		}
		if chunk.Usage != nil {
			usage = &ollamaUsage{
				PromptEvalCount: chunk.Usage.PromptTokens,
				EvalCount:       chunk.Usage.CompletionTokens,
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		content.WriteString(choice.Delta.Content)
		normalizedCalls := normalizedToolCalls(choice.Delta.ToolCalls)
		toolCalls = append(toolCalls, normalizedCalls...)
		message := map[string]any{"role": "assistant", "content": choice.Delta.Content}
		if len(choice.Delta.ToolCalls) > 0 {
			message["tool_calls"] = normalizedCalls
		}
		writeLine(w, map[string]any{"model": model, "created_at": time.Now().UTC(), "message": message, "done": false})
		_ = rc.Flush()
	}
	final := map[string]any{
		"model":       model,
		"created_at":  time.Now().UTC(),
		"message":     map[string]any{"role": "assistant", "content": ""},
		"done_reason": "stop",
		"done":        true,
	}
	if usage != nil {
		final["prompt_eval_count"] = usage.PromptEvalCount
		final["eval_count"] = usage.EvalCount
	}
	if len(context) > 0 {
		final["context"] = context
	}
	if len(context) == 0 {
		assistant := map[string]any{"role": "assistant", "content": content.String()}
		if len(toolCalls) > 0 {
			assistant["tool_calls"] = toolCalls
		}
		context = h.saveContext(req, assistant, sessionKey)
		final["context"] = context
	}
	writeLine(w, final)
	_ = rc.Flush()
}

func (h *Handler) complete(w http.ResponseWriter, body io.Reader, model string, req chatRequest, sessionKey string) {
	var result struct {
		Choices []struct {
			Message map[string]any `json:"message"`
		} `json:"choices"`
		Usage *openAIUsage `json:"usage"`
		Context []int `json:"context"`
	}
	if err := json.NewDecoder(body).Decode(&result); err != nil || len(result.Choices) == 0 {
		ollamaError(w, http.StatusBadGateway, "invalid response from node")
		return
	}
	resultBody := map[string]any{
		"model": model, "created_at": time.Now().UTC(),
		"message": result.Choices[0].Message, "done": true,
	}
	if result.Usage != nil {
		resultBody["prompt_eval_count"] = result.Usage.PromptTokens
		resultBody["eval_count"] = result.Usage.CompletionTokens
	}
	if len(result.Context) > 0 {
		resultBody["context"] = result.Context
	}
	message := ollamaMessage(result.Choices[0].Message)
	resultBody["message"] = message
	if len(result.Context) == 0 {
		resultBody["context"] = h.saveContext(req, message, sessionKey)
	}
	respond.JSON(w, resultBody)
}

func ollamaMessage(message map[string]any) map[string]any {
	normalized := make(map[string]any, len(message))
	for key, value := range message {
		normalized[key] = value
	}
	calls, ok := normalized["tool_calls"].([]any)
	if !ok {
		return normalized
	}
	normalized["tool_calls"] = normalizedToolCalls(calls)
	return normalized
}

func normalizedToolCalls(calls []any) []any {
	out := make([]any, 0, len(calls))
	for _, rawCall := range calls {
		if call, ok := rawCall.(map[string]any); ok {
			out = append(out, normalizeToolCall(call))
		} else {
			out = append(out, rawCall)
		}
	}
	return out
}

func normalizeToolCall(call map[string]any) map[string]any {
	normalized := make(map[string]any, len(call))
	for key, value := range call {
		normalized[key] = value
	}
	function, ok := normalized["function"].(map[string]any)
	if !ok {
		return normalized
	}
	function = normalizeFunction(function)
	normalized["function"] = function
	return normalized
}

func normalizeFunction(function map[string]any) map[string]any {
	normalized := make(map[string]any, len(function))
	for key, value := range function {
		normalized[key] = value
	}
	arguments, ok := normalized["arguments"].(string)
	if !ok {
		return normalized
	}
	var value any
	if json.Unmarshal([]byte(arguments), &value) == nil {
		normalized["arguments"] = value
	}
	return normalized
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type ollamaUsage struct {
	PromptEvalCount int
	EvalCount       int
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
