// Friendly slog handler for the node dashboard.

package tui

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
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
	reqID int // number of the last routed request
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
	get := func(key string) string { return attrValue(attrs, key) }
	h.mu.Lock()
	defer h.mu.Unlock()
	var out string
	switch {
	case r.Message == "proxy forward":
		h.reqID++
		host := cmp.Or(get("node_name"), "node")
		side := strings.Repeat("─", 30)
		out = paint("90", fmt.Sprintf("%s #%d %s", side, h.reqID, side)) + "\n" +
			frame(r.Time, "33", "MODEL", chat("33", "central", host)+" "+paint("1;35", get("model")), "", "") +
			frame(r.Time, "34", "REQUEST", chat("34", host, get("service")), quote(get("prompt")), "")
	case r.Message == "proxy response":
		code, keep := "32", ""
		status, _ := attrAny(attrs, "status").(int64)
		if status >= 400 {
			code, keep = "31", " "+paint("1;31", get("status"))
		}
		d, _ := attrAny(attrs, "duration").(time.Duration)
		out = frame(r.Time, code, "RESPONSE", chat(code, get("service"), cmp.Or(get("node_name"), "node"))+keep,
			quote(get("answer")), paint("90", "in "+d.Round(time.Millisecond).String()))
	case r.Message == "request" && get("method") == "POST" && strings.HasPrefix(get("path"), "/v1/"):
		return nil // The proxy frames already show routed requests.
	default:
		tag, msg := friendlyLine(r.Message, attrs)
		out = fmt.Sprintf("\033[2m%s\033[0m  \033[1m%-5s\033[0m %s\n", r.Time.Format("15:04:05"), tag, msg)
	}
	_, err := io.WriteString(h.w, out)
	return err
}

// cut separates the three parts of a frame line: the kept start, the text that the dashboard can cut,
// and the kept end.
const cut = "\x1f"

// frame returns a message frame in the colour code of the tag: a top border, the line on a rail,
// and a bottom border. The dashboard redraws the frame at its width and cuts only text.
func frame(t time.Time, code, tag, keep, text, tail string) string {
	pad, rule := strings.Repeat(" ", 10), strings.Repeat("─", 60)
	return fmt.Sprintf("%s%s\n%s  %s %s %s\n%s%s\n",
		pad, paint(code, "╭"+rule),
		paint("2", t.Format("15:04:05")), paint(code, "│"), paint("1;"+code, fmt.Sprintf("%-8s", tag)),
		keep+cut+text+cut+tail,
		pad, paint(code, "╰"+rule))
}

// chat returns the direction of a message, for example "[ my-mac → ollama ]".
func chat(code, from, to string) string {
	return paint("1;"+code, "[ ") + paint("1;36", from) + paint("2", " → ") + paint("1;36", to) + paint("1;"+code, " ]")
}

// quote returns the quoted message, or a dim note when the message has no text.
func quote(s string) string {
	if s == "" {
		return paint("2", "(no text)")
	}
	return fmt.Sprintf("%q", s)
}

func paint(code, s string) string {
	return "\033[" + code + "m" + s + "\033[0m"
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
	if v := attrAny(attrs, key); v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func attrAny(attrs []slog.Attr, key string) any {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value.Any()
		}
	}
	return nil
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
	case "proxy reject":
		model := get("model")
		if model != "" {
			return "SKIP", fmt.Sprintf("Skip a request for %q. The model is unknown or down.", model)
		}
		return "SKIP", "Skip a bad request. The model name is wrong or lost."
	case "proxy upstream error":
		return "ERROR", "The backend failed. The request gets an error page."
	case "request":
		return "WEB", fmt.Sprintf("%s %s → %s.", get("method"), get("path"), cmp.Or(get("status"), "?"))
	case "listener start":
		return "WEB", fmt.Sprintf("Listen on %s.", get("addr"))
	case "node shutdown":
		return "NODE", "Node stops. Goodbye."
	case "shutdown signal":
		return "NODE", "Stop request. The node shuts down."
	}
	rest := []string{}
	for _, a := range attrs {
		if a.Key == "component" || a.Key == "time" || a.Key == "level" || a.Key == "node_name" {
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
