// Package feed prints a short, coloured event feed for the operator of central.
package feed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mooch-central/internal/registry"
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
	grey    = "\033[90m"
)

const previewLen = 120

// Cut separates the three parts of a framed line: the kept start, the text that the dashboard can cut,
// and the kept end.
const Cut = "\x1f"

// frameWidth is the length of the top and bottom borders of a message frame.
// dividerWidth is the length of the divider before each request. The dashboard draws both at its own width.
const (
	frameWidth   = 60
	dividerWidth = 71
)

// Feed writes one line for each cluster event. A nil Feed writes nothing.
type Feed struct {
	mu    sync.Mutex
	w     io.Writer
	color bool
	now   func() time.Time
	reqID int
	sep   string // goes before and after the text that the dashboard can cut
	// members is true when the feed prints JOIN and LEAVE. The dashboard shows the nodes in its NODES box.
	members bool
	// Details adds the method and the path to REQUEST lines, and the status to RESPONSE lines.
	// RESPONSE lines always show an error status.
	Details bool
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
	return &Feed{w: w, color: color, now: time.Now, sep: " ", members: true}
}

// NewDashboard returns a Feed for the central dashboard. The Feed always writes colour codes to w.
// Framed lines mark the text that the dashboard can cut with Cut, so the dashboard can fit each line to its width.
func NewDashboard(w io.Writer) *Feed {
	return &Feed{w: w, color: true, now: time.Now, sep: Cut}
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

// Joined prints a frame when a new node registers. The dashboard feed does not print it.
func (f *Feed) Joined(n registry.Node) {
	if f == nil || !f.members {
		return
	}
	f.message(green, "JOIN", f.paint(bold+cyan, Name(n))+" "+f.paint(grey, n.TailscaleIP),
		f.paint(grey, "· models: ")+f.paint(magenta, strings.Join(Models(n), ", ")), "")
}

// Left prints a frame when a node expires. The dashboard feed does not print it.
func (f *Feed) Left(n registry.Node) {
	if f == nil || !f.members {
		return
	}
	f.message(red, "LEAVE", f.paint(bold+cyan, Name(n)), f.paint(grey, "no heartbeat"), "")
}

// Route starts a request: a divider with the request number, and then a MODEL frame
// with the selected model and the node that gets the request.
func (f *Feed) Route(from, model, to string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.reqID++
	id := f.reqID
	f.mu.Unlock()
	label := fmt.Sprintf(" #%d ", id)
	side := strings.Repeat("─", (dividerWidth-utf8.RuneCountInString(label))/2)
	f.printf("%s\n%s", f.paint(grey, side+label+side),
		f.frame(yellow, "MODEL", f.chat(yellow, from, to)+" "+f.paint(bold+magenta, model), "", ""))
}

// Request prints the direction and the prompt of the forwarded request in a frame.
// With Details, the line also shows the method and the path.
func (f *Feed) Request(from, to, method, path, preview string) {
	if f == nil {
		return
	}
	keep, text := f.chat(blue, from, to), f.text(preview)
	if f.Details {
		keep += " " + f.paint(bold+blue, method)
		text += " " + f.paint(grey, path)
	}
	f.message(blue, "REQUEST", keep, text, "")
}

// Response prints the direction, the answer, and the duration in a frame.
// The line shows the status with Details, or when the status is an error.
func (f *Feed) Response(from, to string, status int, d time.Duration, preview string) {
	if f == nil {
		return
	}
	code := green
	if status >= 400 {
		code = red
	}
	keep := f.chat(code, from, to)
	if f.Details || status >= 400 {
		keep += " " + f.paint(bold+code, fmt.Sprint(status))
	}
	f.message(code, "RESPONSE", keep, f.text(preview), f.paint(grey, "in "+d.Round(time.Millisecond).String()))
}

// chat returns the direction of a message, for example "[ laptop → gpu-box ]".
func (f *Feed) chat(code, from, to string) string {
	return f.paint(bold+code, "[ ") + f.paint(bold+cyan, from) + f.paint(dim, " → ") +
		f.paint(bold+cyan, to) + f.paint(bold+code, " ]")
}

// text returns the quoted message in the normal text colour, so that it is easy to read.
func (f *Feed) text(s string) string {
	if s == "" {
		return f.paint(dim, "(no text)")
	}
	return fmt.Sprintf("%q", s)
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

// message prints a frame. The three lines go out in one write, so other events cannot split the frame.
func (f *Feed) message(code, tag, keep, text, tail string) {
	if f == nil {
		return
	}
	f.printf("%s", f.frame(code, tag, keep, text, tail))
}

// frame returns a frame in the colour of the tag: a top border, the line on a rail, and a bottom border.
// keep and tail are important. The dashboard cuts only text when the line is too long.
func (f *Feed) frame(code, tag, keep, text, tail string) string {
	parts := []string{keep, text, tail}
	if f.sep != Cut {
		parts = slices.DeleteFunc(parts, func(p string) bool { return p == "" })
	}
	pad, rule := strings.Repeat(" ", 10), strings.Repeat("─", frameWidth)
	return fmt.Sprintf("%s%s\n%s  %s %s %s\n%s%s\n",
		pad, f.paint(code, "╭"+rule),
		f.paint(dim, f.now().Format("15:04:05")), f.paint(code, "│"), f.paint(bold+code, fmt.Sprintf("%-8s", tag)), strings.Join(parts, f.sep),
		pad, f.paint(code, "╰"+rule))
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
