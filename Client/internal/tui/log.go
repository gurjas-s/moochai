// Friendly slog handler for the node dashboard.

package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// NewLogHandler returns a handler that writes short operator lines to w.
// Use it for the dashboard. Raw slog text suits files, not people.
func NewLogHandler(w io.Writer, level slog.Level) slog.Handler {
	return &logHandler{w: w, level: level}
}

type logHandler struct {
	mu    sync.Mutex
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
}

// Enabled reports whether the handler accepts the record level.
func (h *logHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle formats one record as a short line with a tag.
func (h *logHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make([]slog.Attr, 0, len(h.attrs)+r.NumAttrs())
	attrs = append(attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	tag, msg := friendlyLine(r.Message, attrs)
	line := fmt.Sprintf("\033[2m%s\033[0m  \033[1m%-5s\033[0m %s",
		r.Time.Format("15:04:05"), tag, msg)
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := fmt.Fprintln(h.w, line)
	return err
}

// WithAttrs returns a handler with extra fixed attributes.
func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := &logHandler{w: h.w, level: h.level}
	out.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return out
}

// WithGroup returns a handler that prefixes keys with the group name.
func (h *logHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	out := &logHandler{w: h.w, level: h.level}
	for _, a := range h.attrs {
		a.Key = name + "." + a.Key
		out.attrs = append(out.attrs, a)
	}
	return out
}

func attrValue(attrs []slog.Attr, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			return fmt.Sprint(a.Value.Any())
		}
	}
	return ""
}

// friendlyLine maps tech log messages to short operator text.
// Unknown messages keep their text with key detail appended.
func friendlyLine(msg string, attrs []slog.Attr) (string, string) {
	get := func(key string) string { return attrValue(attrs, key) }
	switch msg {
	case "node start":
		return "NODE", "Node is up. It joins Mooch.ai now."
	case "backend probe failed":
		return "CHECK", fmt.Sprintf("Backend %q is down. Check the backend URL.", get("service"))
	case "backend discovery found no models":
		return "MODELS", "No models found. Check the backend and the expose list."
	case "proxy forward":
		model, service := get("model"), get("service")
		if model != "" {
			return "SERVE", fmt.Sprintf("Serve %q with %q.", model, service)
		}
		return "SERVE", "Serve one request."
	case "proxy reject":
		model := get("model")
		if model != "" {
			return "SKIP", fmt.Sprintf("Skip a request for %q. The model is unknown or down.", model)
		}
		return "SKIP", "Skip a bad request. The model name is wrong or lost."
	case "proxy upstream error":
		return "ERROR", "The backend failed. The request gets an error page."
	case "request":
		return "WEB", fmt.Sprintf("%s %s → %d.", get("method"), get("path"), statusOf(attrs))
	case "listener start":
		return "WEB", fmt.Sprintf("Listen on %s.", get("addr"))
	case "node shutdown":
		return "NODE", "Node stops. Goodbye."
	case "shutdown signal":
		return "NODE", "Stop request. The node shuts down."
	}
	rest := []string{}
	for _, a := range attrs {
		if a.Key == "component" || a.Key == "time" || a.Key == "level" {
			continue
		}
		rest = append(rest, a.Key+"="+fmt.Sprint(a.Value.Any()))
	}
	if len(rest) > 0 {
		msg += " (" + strings.Join(rest, " ") + ")"
	}
	if comp := get("component"); comp != "" {
		return strings.ToUpper(comp)[:min(5, len(comp))], msg
	}
	return "LOG", msg
}

func statusOf(attrs []slog.Attr) any {
	for _, a := range attrs {
		if a.Key == "status" {
			return a.Value.Any()
		}
	}
	return "?"
}
