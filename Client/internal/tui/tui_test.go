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
	logger.Info("proxy forward", "component", "proxy", "model", "qwen2.5", "service", "local-qwen")
	logger.Warn("backend probe failed", "component", "health", "service", "local-qwen")
	out := buf.String()
	for _, want := range []string{`Serve "qwen2.5"`, "is down"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "component=") || strings.Contains(out, "level=") {
		t.Errorf("log output keeps raw fields:\n%s", out)
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
