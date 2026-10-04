// Package feed prints a short, coloured event feed for the operator of central.
package feed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mooch-serv/internal/registry"
)

const (
	reset   = "\033[0m"
	dim     = "\033[2m"
	bold    = "\033[1m"
	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	blue    = "\033[34m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
)

const previewLen = 120

// Feed writes one line for each cluster event. A nil Feed writes nothing.
type Feed struct {
	mu    sync.Mutex
	w     io.Writer
	color bool
	now   func() time.Time
	reqID int
}

// New returns a Feed that writes to w.
// Colour is on only when w is a terminal and NO_COLOR is not set.
func New(w io.Writer) *Feed {
	color := false
	if f, ok := w.(*os.File); ok && os.Getenv("NO_COLOR") == "" {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			color = true
		}
	}
	return &Feed{w: w, color: color, now: time.Now}
}

// NewColor returns a Feed that always writes colour codes to w.
func NewColor(w io.Writer) *Feed {
	return &Feed{w: w, color: true, now: time.Now}
}

// Name returns the display name of node.
func Name(n registry.Node) string {
	if n.Name != "" {
		return n.Name
	}
	return n.NodeID
}

// Models returns the models of all services of node.
func Models(n registry.Node) []string {
	var models []string
	for _, s := range n.Services {
		models = append(models, s.Models...)
	}
	return models
}

// Banner prints a bordered start message with the listen address.
func (f *Feed) Banner(addr string) {
	lines := []string{"MOOCH.AI HAS STARTED", "listening on " + addr}
	width := 0
	for _, l := range lines {
		width = max(width, utf8.RuneCountInString(l))
	}
	border := strings.Repeat("─", width+4)
	out := f.paint(green, "╭"+border+"╮") + "\n"
	for i, l := range lines {
		text := fmt.Sprintf("%-*s", width, l)
		if i == 0 {
			text = f.paint(bold+green, text)
		} else {
			text = f.paint(dim, text)
		}
		out += f.paint(green, "│") + "  " + text + "  " + f.paint(green, "│") + "\n"
	}
	f.printf("\n%s%s\n", out, f.paint(green, "╰"+border+"╯"))
}

// Joined prints a line when a new node registers.
func (f *Feed) Joined(n registry.Node) {
	f.event(green, "JOIN", "%s %s", f.paint(cyan, Name(n)),
		f.paint(dim, n.TailscaleIP+" · models: ")+f.paint(magenta, strings.Join(Models(n), ", ")))
}

// Left prints a line when a node expires.
func (f *Feed) Left(n registry.Node) {
	f.event(red, "LEAVE", "%s %s", f.paint(cyan, Name(n)), f.paint(dim, "no heartbeat"))
}

// Route prints the model selection and the node that gets the request.
// Route returns a request number for Request and Response.
func (f *Feed) Route(from, model string, available []string, to string) int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	f.reqID++
	id := f.reqID
	f.mu.Unlock()
	f.event(yellow, "ROUTE", "%s %s asks for %s %s %s %s", f.id(id), f.paint(cyan, from), f.paint(magenta, model),
		f.paint(dim, "→"), f.paint(cyan, to), f.paint(dim, "· served by: "+strings.Join(available, ", ")))
	return id
}

// Request prints the forwarded request, its direction, and a preview of the prompt.
func (f *Feed) Request(id int, from, to, path, preview string) {
	f.event(blue, "REQ", "%s %s %s %s", f.id(id), f.paint(dim, from+" → "+to), path, f.quote(preview))
}

// Response prints the direction, the status, the duration, and a preview of the answer.
func (f *Feed) Response(id int, from, to string, status int, d time.Duration, preview string) {
	code := green
	if status >= 400 {
		code = red
	}
	f.event(code, "RESP", "%s %s %s %s %s", f.id(id), f.paint(dim, from+" → "+to), f.paint(bold+code, fmt.Sprint(status)),
		f.paint(dim, "in "+d.Round(time.Millisecond).String()), f.quote(preview))
}

// Unreachable prints a line when central cannot connect to a node.
func (f *Feed) Unreachable(addr string) {
	f.event(red, "ERROR", "%s", f.paint(red, "node at "+addr+" is unreachable"))
}

// event prints one line: the time, a coloured tag, and the message.
func (f *Feed) event(code, tag, format string, args ...any) {
	if f == nil {
		return
	}
	msg := strings.TrimRight(fmt.Sprintf(format, args...), " ")
	f.printf("%s  %s %s\n", f.paint(dim, f.now().Format("15:04:05")), f.paint(bold+code, fmt.Sprintf("%-5s", tag)), msg)
}

func (f *Feed) id(id int) string { return f.paint(dim, fmt.Sprintf("#%d", id)) }

func (f *Feed) quote(text string) string {
	if text == "" {
		return ""
	}
	return f.paint(dim, fmt.Sprintf("%q", text))
}

func (f *Feed) printf(format string, args ...any) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fmt.Fprintf(f.w, format, args...)
}

func (f *Feed) paint(code, s string) string {
	if f == nil || !f.color {
		return s
	}
	return code + s + reset
}

// Preview returns the last user message of an OpenAI request body, or its prompt.
func Preview(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Prompt json.RawMessage `json:"prompt"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	text := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			text = contentText(req.Messages[i].Content)
			break
		}
	}
	if text == "" {
		_ = json.Unmarshal(req.Prompt, &text)
	}
	return clip(text)
}

// ResponsePreview returns the answer text of an OpenAI response body, or its error message.
// It reads plain JSON and server-sent event streams.
func ResponsePreview(body []byte) string {
	type answer struct {
		Choices []struct {
			Text    string `json:"text"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	chunks := [][]byte{body}
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("data:")) {
		chunks = nil
		for _, line := range bytes.Split(body, []byte("\n")) {
			if data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
				chunks = append(chunks, data)
			}
		}
	}
	var text strings.Builder
	for _, c := range chunks {
		var a answer
		if json.Unmarshal(c, &a) != nil {
			continue
		}
		if a.Error.Message != "" {
			return clip("error: " + a.Error.Message)
		}
		for _, ch := range a.Choices {
			text.WriteString(ch.Text + ch.Message.Content + ch.Delta.Content)
		}
	}
	return clip(text.String())
}

// clip collapses white space and cuts text to previewLen characters.
func clip(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > previewLen {
		text = string(r[:previewLen]) + "…"
	}
	return text
}

// contentText reads message content as a string or as an array of text parts.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var texts []string
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, " ")
}
