// Package tui shows the node dashboard: join status, hosted models, and activity.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	maxLines     = 500
	maxModelRows = 10
	tickEvery    = time.Second
	wrapIndent   = 16
)

var (
	accent  = lipgloss.Color("4")
	green   = lipgloss.Color("2")
	yellow  = lipgloss.Color("3")
	red     = lipgloss.Color("1")
	cyan    = lipgloss.Color("6")
	magenta = lipgloss.Color("5")
	faint   = lipgloss.Color("8")

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(accent).Padding(0, 1)
	labelStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	dimStyle   = lipgloss.NewStyle().Foreground(faint)
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(faint).Padding(0, 1)
)

// Status is the join state of the node.
type Status int

const (
	// StatusConnecting means the node still tries the first register.
	StatusConnecting Status = iota
	// StatusJoined means central confirmed the last register or heartbeat.
	StatusJoined
	// StatusRetrying means central is quiet and the node tries again.
	StatusRetrying
)

// Text returns a short label for the status.
func (s Status) Text() string {
	switch s {
	case StatusJoined:
		return "joined"
	case StatusRetrying:
		return "retrying"
	default:
		return "connecting"
	}
}

// Service is one advertised backend capability.
type Service struct {
	ID       string
	Name     string
	Provider string
	Models   []string
	Healthy  bool
}

// Info is the node snapshot for one render.
type Info struct {
	NodeName    string
	NodeID      string
	TailscaleIP string
	ListenAddr  string
	CentralAddr string
	Version     string
	UpSince     time.Time
	Services    []Service
}

// Provider returns the current node snapshot.
type Provider func() Info

// UI is the dashboard program. UI is an io.Writer: each written line lands in the activity view.
type UI struct {
	prog  *tea.Program
	lines chan string
}

// New returns a dashboard. Provider supplies fresh node state on each tick.
// Extra program options serve tests (fixed input and output).
func New(ctx context.Context, provider Provider, opts ...tea.ProgramOption) *UI {
	if provider == nil {
		provider = func() Info { return Info{} }
	}
	u := &UI{lines: make(chan string, 1024)}
	args := append([]tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx)}, opts...)
	u.prog = tea.NewProgram(&model{provider: provider, now: time.Now()}, args...)
	// The buffer keeps Write from a block before Run starts.
	go func() {
		for l := range u.lines {
			u.prog.Send(lineMsg(l))
		}
	}()
	return u
}

// Run shows the dashboard until the operator quits or ctx ends.
func (u *UI) Run() error {
	_, err := u.prog.Run()
	if errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}

// Write stores raw log lines for the activity view.
func (u *UI) Write(p []byte) (int, error) {
	for _, l := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		u.lines <- l
	}
	return len(p), nil
}

// SetStatus updates the header join state.
func (u *UI) SetStatus(s Status, detail string) {
	u.prog.Send(statusMsg{status: s, detail: detail})
}

// Event adds one friendly activity line with a tag.
// Kind is one of: ok, warn, err, info.
func (u *UI) Event(kind, msg string) {
	u.lines <- formatEvent(kind, msg)
}

// Joined marks the node as joined and notes the event.
func (u *UI) Joined(addr string) {
	u.SetStatus(StatusJoined, "")
	u.Event("ok", "Joined Mooch.ai through "+addr+". Other peers can use your models now.")
}

// Retrying marks a retry and notes the cause.
func (u *UI) Retrying(cause string) {
	u.SetStatus(StatusRetrying, cause)
	u.Event("warn", "Central is quiet. The node tries again soon. ("+cause+")")
}

// Hosting notes the hosted model count for the operator.
func (u *UI) Hosting(models []string) {
	if len(models) == 0 {
		u.Event("warn", "No models found. Start the backend and check the expose list.")
		return
	}
	u.Event("ok", fmt.Sprintf("Hosting %d model(s): %s.", len(models), strings.Join(models, ", ")))
}

func formatEvent(kind, msg string) string {
	tag, code := "INFO", cyan
	switch kind {
	case "ok":
		tag, code = "READY", green
	case "warn":
		tag, code = "NOTE", yellow
	case "err":
		tag, code = "FIX", red
	}
	return fmt.Sprintf("\033[2m%s\033[0m  \033[1;%sm%-5s\033[0m %s",
		time.Now().Format("15:04:05"), colorCode(code), tag, msg)
}

func colorCode(c lipgloss.Color) string {
	switch c {
	case green:
		return "32"
	case yellow:
		return "33"
	case red:
		return "31"
	case cyan:
		return "36"
	default:
		return "0"
	}
}

type lineMsg string
type tickMsg time.Time
type statusMsg struct {
	status Status
	detail string
}

type model struct {
	provider Provider
	info     Info
	status   Status
	detail   string
	lines    []string
	scroll   int // lines above the bottom of the activity view
	width    int
	height   int
	now      time.Time
}

func tick() tea.Cmd {
	return tea.Tick(tickEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) Init() tea.Cmd {
	m.info = m.provider()
	return tick()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.now = time.Time(msg)
		m.info = m.provider()
		return m, tick()
	case statusMsg:
		m.status = msg.status
		m.detail = msg.detail
	case lineMsg:
		m.lines = append(m.lines, string(msg))
		if len(m.lines) > maxLines {
			m.lines = m.lines[len(m.lines)-maxLines:]
		}
		if m.scroll > 0 {
			m.scroll++ // Keep the view still while the operator reads old lines.
		}
	case tea.KeyMsg:
		page := max(1, m.height/2)
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			m.scroll++
		case "down", "j":
			m.scroll--
		case "pgup", "b":
			m.scroll += page
		case "pgdown", "f", " ":
			m.scroll -= page
		case "home", "g":
			m.scroll = len(m.lines)
		case "end", "G":
			m.scroll = 0
		}
		m.scroll = max(0, m.scroll)
	}
	return m, nil
}

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}
	inner := m.width - 4 // border and padding
	header := m.header()
	models := boxStyle.Width(m.width - 2).Render(m.modelList(inner))
	footer := m.footer()
	h := m.height - lipgloss.Height(header) - lipgloss.Height(models) - lipgloss.Height(footer) - 2
	h = max(1, h-1) // The label takes one line.
	console := boxStyle.Width(m.width - 2).Render(labelStyle.Render("ACTIVITY") + "\n" + m.activity(inner, h))
	return lipgloss.JoinVertical(lipgloss.Left, header, models, console, footer)
}

func (m *model) header() string {
	dot := lipgloss.NewStyle().Foreground(yellow).Render("●")
	state := lipgloss.NewStyle().Foreground(yellow).Render(m.status.Text() + " " + m.info.CentralAddr)
	hint := "Joining Mooch.ai. This takes a few seconds."
	showHint := true
	if m.status == StatusJoined {
		dot = lipgloss.NewStyle().Foreground(green).Render("●")
		state = lipgloss.NewStyle().Foreground(green).Render("joined " + m.info.CentralAddr)
		showHint = false // The models panel already states the shared state.
	}
	if m.status == StatusRetrying {
		dot = lipgloss.NewStyle().Foreground(red).Render("●")
		state = lipgloss.NewStyle().Foreground(yellow).Render("retrying " + m.info.CentralAddr)
		hint = "Central is quiet. Check the address and Tailscale."
		if m.detail != "" {
			hint += " (" + m.detail + ")"
		}
	}
	name := m.info.NodeName
	if name == "" {
		name = m.info.NodeID
	}
	up := ""
	if !m.info.UpSince.IsZero() {
		up = " · up " + m.now.Sub(m.info.UpSince).Truncate(time.Second).String()
	}
	top := titleStyle.Render("Mooch.ai Node") + "  " + dot + " " + state
	sub := dimStyle.Render(name + " · " + m.info.ListenAddr + " · " + m.info.Version + up)
	out := ansi.Truncate(" "+top, m.width, "…") + "\n" +
		ansi.Truncate(" "+sub, m.width, "…")
	if showHint {
		out += "\n" + ansi.Truncate(" "+hint, m.width, "…")
	}
	return out
}

// modelList returns the hosted models with health dots.
func (m *model) modelList(width int) string {
	names := []string{}
	healthy := 0
	for _, s := range m.info.Services {
		for _, name := range s.Models {
			names = append(names, name)
			if s.Healthy {
				healthy++
			}
		}
	}
	label := labelStyle.Render("MODELS ON THIS NODE") +
		dimStyle.Render(fmt.Sprintf(" %d hosted · %d ready · peers can use these", len(names), healthy))
	if len(names) == 0 {
		return label + "\n" + dimStyle.Render("No models yet. Start the backend, then check the expose list.")
	}
	rows := []string{label}
	shown := 0
	for _, s := range m.info.Services {
		for _, name := range s.Models {
			if shown == maxModelRows {
				rows = append(rows, dimStyle.Render(fmt.Sprintf("+ %d more", len(names)-shown)))
				return strings.Join(rows, "\n")
			}
			shown++
			dot := lipgloss.NewStyle().Foreground(green).Render("●")
			health := "ready"
			if !s.Healthy {
				dot = lipgloss.NewStyle().Foreground(yellow).Render("●")
				health = "down"
			}
			row := fmt.Sprintf("%s %s  %s", dot,
				lipgloss.NewStyle().Foreground(cyan).Bold(true).Render(name),
				dimStyle.Render(s.ID+" · "+string(s.Provider)+" · "+health))
			rows = append(rows, ansi.Truncate(row, width, "…"))
		}
	}
	return strings.Join(rows, "\n")
}

// activity returns the last height screen lines above the scroll position. Long lines wrap.
func (m *model) activity(width, height int) string {
	var screen []string
	for _, l := range m.lines {
		screen = append(screen, wrap(l, width)...)
	}
	if len(screen) == 0 {
		screen = []string{dimStyle.Render("Waiting for events: join results, model updates, and served requests.")}
	}
	m.scroll = min(m.scroll, max(0, len(screen)-height))
	end := len(screen) - m.scroll
	view := screen[max(0, end-height):end]
	// Pad to the full height so the activity box fills the screen.
	return strings.Join(append(view, make([]string, height-len(view))...), "\n")
}

// wrap cuts line into screen lines of width. Continuation lines start under the message, after the time and tag.
func wrap(line string, width int) []string {
	const indent = wrapIndent // "15:04:05  READY "
	if ansi.StringWidth(line) <= width || width <= indent+10 {
		return strings.Split(ansi.Hardwrap(line, width, true), "\n")
	}
	out := []string{ansi.Truncate(line, width, "")}
	for _, l := range strings.Split(ansi.Hardwrap(ansi.TruncateLeft(line, width, ""), width-indent, true), "\n") {
		out = append(out, strings.Repeat(" ", indent)+l)
	}
	return out
}

func (m *model) footer() string {
	keys := dimStyle.Render(" ↑/↓ scroll · pgup/pgdn page · g/G top/bottom · q quit")
	if m.scroll > 0 {
		keys += lipgloss.NewStyle().Foreground(yellow).Render(fmt.Sprintf("   ▼ %d lines below", m.scroll))
	}
	return ansi.Truncate(keys, m.width, "…")
}
