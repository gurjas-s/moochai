package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"mooch-serv/internal/registry"
)

func TestView(t *testing.T) {
	reg := registry.New(time.Minute)
	reg.Upsert(registry.Node{NodeID: "n1", Name: "gpu-box", TailscaleIP: "100.64.0.7",
		Services: []registry.Service{{Models: []string{"qwen"}, Healthy: true}}})
	reg.Upsert(registry.Node{NodeID: "n2", Name: "mini", TailscaleIP: "100.64.0.8",
		Services: []registry.Service{{Models: []string{"qwen"}, Healthy: true}}})
	m := &model{reg: reg, addr: "100.64.0.1:8080", tailnet: true, now: time.Now()}
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	for i := range 30 {
		m.Update(lineMsg(fmt.Sprintf("12:00:00  JOIN  line %d", i)))
	}
	view := ansi.Strip(m.View())
	lines := strings.Split(view, "\n")
	if len(lines) != 20 {
		t.Fatalf("view has %d lines, want 20:\n%s", len(lines), view)
	}
	for _, want := range []string{"Mooch.ai Central", "100.64.0.1:8080", "gpu-box", "qwen", "line 29"} {
		if !strings.Contains(view, want) {
			t.Errorf("view misses %q:\n%s", want, view)
		}
	}

	// A wide terminal shows the logo, the NODES box, and the MODELS box on one row.
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	view = ansi.Strip(m.View())
	top := strings.Split(view, "\n")[2]
	for _, want := range []string{"███╗", "gpu-box", "qwen"} {
		if !strings.Contains(top, want) {
			t.Fatalf("top row misses %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "qwen  gpu-box, mini") {
		t.Fatalf("MODELS box does not list the machines of qwen:\n%s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})

	// Scroll up past the top. The view stops at the first line and keeps its height.
	m.Update(tea.KeyMsg{Type: tea.KeyHome})
	view = ansi.Strip(m.View())
	if !strings.Contains(view, "line 0") || strings.Contains(view, "line 29") || len(strings.Split(view, "\n")) != 20 {
		t.Fatalf("scrolled view is wrong:\n%s", view)
	}
}

func TestWrap(t *testing.T) {
	got := wrap("12:00:00  REQ   "+strings.Repeat("x", 40), 30)
	want := []string{
		"12:00:00  REQ   xxxxxxxxxxxxxx",
		"                xxxxxxxxxxxxxx",
		"                xxxxxxxxxxxx",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// A line in a request block keeps the rail on its continuation lines.
	got = wrap("12:00:00  │ REQ   "+strings.Repeat("x", 20), 30)
	want = []string{
		"12:00:00  │ REQ   xxxxxxxxxxxx",
		"          │       xxxxxxxx",
	}
	if ansi.Strip(strings.Join(got, "\n")) != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
