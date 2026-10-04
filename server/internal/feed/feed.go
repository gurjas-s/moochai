// Package feed prints a short, coloured event feed for the operator of central.
package feed

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"peerai-serv/internal/registry"
)

const (
	reset  = "\033[0m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	bold   = "\033[1m"
	cyan   = "\033[36m"
)

const previewLen = 120

// Feed writes one line for each cluster event. A nil Feed writes nothing.
type Feed struct {
	mu    sync.Mutex
	w     io.Writer
	color bool
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
	return &Feed{w: w, color: color}
}

// Name returns the display name of node.
func Name(n registry.Node) string {
	if n.Name != "" {
		return n.Name
	}
	return n.NodeID
}

// Banner prints a bordered start message with the listen address.
func (f *Feed) Banner(addr string) {
	lines := []string{"PEER AI HAS STARTED", "listening on " + addr}
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
	models := 0
	for _, s := range n.Services {
		models += len(s.Models)
	}
	f.printf("%s %s has joined the cluster %s\n", f.paint(green, "->"), f.paint(cyan, Name(n)),
		f.paint(dim, fmt.Sprintf("(%s, %d model%s)", n.TailscaleIP, models, plural(models))))
}

// Left prints a line when a node expires.
func (f *Feed) Left(n registry.Node) {
	f.printf("%s %s has left the cluster %s\n", f.paint(red, "<-"), f.paint(cyan, Name(n)), f.paint(dim, "(no heartbeat)"))
}

// Route prints the model selection and the node that gets the request.
func (f *Feed) Route(from, model string, available []string, to, path string) {
	f.printf("\n%s %s selected model %s %s\n%s %s -> %s %s\n",
		f.paint(yellow, "! "), f.paint(cyan, from), f.paint(yellow, model),
		f.paint(dim, "· available: "+strings.Join(available, ", ")),
		f.paint(yellow, "! "), f.paint(cyan, from), f.paint(cyan, to), f.paint(dim, path))
}

// Content prints a prompt preview.
func (f *Feed) Content(text string) {
	if text == "" {
		return
	}
	f.printf("   %s %s\n", f.paint(dim, ":"), f.paint(dim, fmt.Sprintf("%q", text)))
}

// Done prints the status and the duration of a forwarded request.
func (f *Feed) Done(status int, d time.Duration) {
	code := dim
	if status >= 400 {
		code = red
	}
	f.printf("   %s\n", f.paint(code, fmt.Sprintf("<- %d in %s", status, d.Round(10*time.Millisecond))))
}

// Unreachable prints a line when central cannot connect to a node.
func (f *Feed) Unreachable(addr string) {
	f.printf("%s\n", f.paint(red, "x  node at "+addr+" is unreachable"))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
// It collapses white space and cuts the text to previewLen characters.
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
