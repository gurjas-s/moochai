package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func testInfo() Info {
	return Info{
		NodeName:    "desk-gpu",
		NodeID:      "desk-gpu-1234",
		TailscaleIP: "100.64.0.5",
		ListenAddr:  "100.64.0.5:9100",
		CentralAddr: "100.64.0.10:8080",
		Version:     "v0.1.0-dev",
		UpSince:     time.Now(),
		Services: []Service{
			{ID: "local-qwen", Name: "qwen", Provider: "ollama", Models: []string{"qwen2.5"}, Healthy: true},
			{ID: "local-embed", Name: "embed", Provider: "openai-like", Models: []string{"nomic-embed"}, Healthy: false},
		},
	}
}

func testModel() *model {
	return &model{provider: testInfo, now: time.Now()}
}

func TestViewShowsStatusModelsAndEvents(t *testing.T) {
	m := testModel()
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	m.Update(statusMsg{status: StatusJoined})
	for i := range 5 {
		m.Update(lineMsg(fmt.Sprintf("12:00:00  READY line %d", i)))
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Mooch.ai Node", "joined", "qwen2.5", "nomic-embed", "line 4"} {
		if !strings.Contains(view, want) {
			t.Errorf("view misses %q:\n%s", want, view)
		}
	}
	if lines := strings.Split(view, "\n"); len(lines) != 24 {
		t.Fatalf("view has %d lines, want 24:\n%s", len(lines), view)
	}
}

func TestViewShowsConnectingAndEmptyModels(t *testing.T) {
	m := &model{provider: func() Info { return Info{} }, now: time.Now()}
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m.Update(statusMsg{status: StatusRetrying, detail: "boom"})
	view := ansi.Strip(m.View())
	for _, want := range []string{"retrying", "No models yet", "boom"} {
		if !strings.Contains(view, want) {
			t.Errorf("view misses %q:\n%s", want, view)
		}
	}
}

func TestViewScrollKeepsHeight(t *testing.T) {
	m := testModel()
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	for i := range 30 {
		m.Update(lineMsg(fmt.Sprintf("12:00:00  READY line %d", i)))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyHome})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "line 0") || strings.Contains(view, "line 29") {
		t.Fatalf("scrolled view is wrong:\n%s", view)
	}
	if lines := strings.Split(view, "\n"); len(lines) != 20 {
		t.Fatalf("view has %d lines, want 20:\n%s", len(lines), view)
	}
}

func TestWrap(t *testing.T) {
	got := wrap("12:00:00  READY "+strings.Repeat("x", 40), 30)
	want := []string{
		"12:00:00  READY xxxxxxxxxxxxxx",
		"                xxxxxxxxxxxxxx",
		"                xxxxxxxxxxxx",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestLogHandlerWritesFriendlyLines(t *testing.T) {
	var buf bytes.Buffer
	h := NewLogHandler(&buf, slog.LevelDebug)
	logger := slog.New(h)
	logger.Warn("backend probe failed", "component", "health", "service", "local-qwen")
	out := buf.String()
	for _, want := range []string{"is down"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "component=") || strings.Contains(out, "level=") {
		t.Errorf("log output keeps raw fields:\n%s", out)
	}
}

func TestLogHandlerFramesRequests(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewLogHandler(&buf, slog.LevelInfo)).With("node_name", "gpu-box")
	logger.Info("proxy forward", "model", "qwen2.5", "service", "local-qwen", "prompt", "hi")
	logger.Info("request", "method", "POST", "path", "/v1/chat/completions", "status", 200)
	logger.Info("proxy response", "service", "local-qwen", "status", 502, "duration", 1234*time.Millisecond, "answer", "")
	logger.Info("request", "method", "GET", "path", "/v1/models", "status", 200)
	out := ansi.Strip(buf.String())
	for _, want := range []string{" #1 ", "MODEL    [ central → gpu-box ] qwen2.5",
		"REQUEST  [ gpu-box → local-qwen ]" + cut + `"hi"`, "RESPONSE [ local-qwen → gpu-box ] 502" + cut + "(no text)" + cut + "in 1.234s",
		"GET /v1/models"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "POST") {
		t.Errorf("output keeps the POST request line:\n%s", out)
	}
}

func TestRedrawFitsFramesToWidth(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewLogHandler(&buf, slog.LevelInfo))
	logger.Info("proxy forward", "model", "m", "service", "s", "prompt", strings.Repeat("x", 100))
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for _, l := range lines {
		got, ok := redraw(l, 50)
		if !ok {
			t.Fatalf("redraw rejects frame line %q", l)
		}
		if w := ansi.StringWidth(got); w != 50 && !strings.HasPrefix(ansi.Strip(got), "─") {
			t.Errorf("width %d, want 50: %q", w, ansi.Strip(got))
		}
	}
	if got, _ := redraw(lines[5], 50); !strings.Contains(ansi.Strip(got), "REQUEST  [ node → s ] \"xx") ||
		!strings.Contains(ansi.Strip(got), "…") {
		t.Errorf("long text line is not cut: %q", ansi.Strip(got))
	}
	if _, ok := redraw("12:00:00  READY plain line", 50); ok {
		t.Error("redraw accepts a plain line")
	}
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestProgramRun starts the real program with piped input and output.
// It sends a window size, one event line, and quit. The output keeps
// the header, the hosted models, and the event line.
func TestProgramRun(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	inR, inW := io.Pipe()
	out := &safeBuffer{}
	ui := New(ctx, testInfo, tea.WithInput(inR), tea.WithOutput(out))
	done := make(chan error, 1)
	go func() { done <- ui.Run() }()
	time.Sleep(300 * time.Millisecond)
	ui.prog.Send(tea.WindowSizeMsg{Width: 80, Height: 30})
	time.Sleep(300 * time.Millisecond)
	ui.Event("ok", "Joined Mooch.ai through central.")
	time.Sleep(500 * time.Millisecond)
	_, _ = inW.Write([]byte("q"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not quit after q")
	}
	view := ansi.Strip(out.String())
	for _, want := range []string{"Mooch.ai Node", "qwen2.5", "Joined Mooch.ai through central"} {
		if !strings.Contains(view, want) {
			t.Errorf("program output misses %q:\n%s", want, view)
		}
	}
}
