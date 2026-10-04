// Package tui shows the central dashboard: a header, the live nodes, and the event console.
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

	"mooch-serv/internal/feed"
	"mooch-serv/internal/registry"
)

const (
	maxLines   = 2000
	staleAfter = 20 * time.Second
)

var (
	accent  = lipgloss.Color("#2dd4bf")
	green   = lipgloss.Color("2")
	yellow  = lipgloss.Color("3")
	cyan    = lipgloss.Color("6")
	magenta = lipgloss.Color("5")
	faint   = lipgloss.Color("8")

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(accent).Padding(0, 1)
	labelStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	dimStyle   = lipgloss.NewStyle().Foreground(faint)
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(faint).Padding(0, 1)
)

// UI is the dashboard program. UI is an io.Writer: each written line goes to the console.
type UI struct {
	prog  *tea.Program
	lines chan string
}

// New returns a dashboard for central at addr. Set tailnet when addr is a Tailscale address.
func New(ctx context.Context, reg *registry.Registry, addr string, tailnet bool) *UI {
	u := &UI{lines: make(chan string, 1024)}
	u.prog = tea.NewProgram(&model{reg: reg, addr: addr, tailnet: tailnet, now: time.Now()},
		tea.WithAltScreen(), tea.WithContext(ctx))
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

func (u *UI) Write(p []byte) (int, error) {
	for _, l := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		u.lines <- l
	}
	return len(p), nil
}

type lineMsg string
type tickMsg time.Time

type model struct {
	reg           *registry.Registry
	addr          string
	tailnet       bool
	nodes         []registry.Node
	lines         []string
	scroll        int // lines above the bottom of the console
	width, height int
	now           time.Time
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) Init() tea.Cmd {
	m.nodes = m.reg.Nodes()
	return tick()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.now = time.Time(msg)
		m.nodes = m.reg.Nodes()
		return m, tick()
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
	header := m.header()
	address := m.address()
	footer := m.footer()
	h := m.height - lipgloss.Height(header) - lipgloss.Height(address) - lipgloss.Height(footer) - 2
	h = max(1, h-1) // The label takes one line.
	console := boxStyle.Width(m.width - 2).Render(labelStyle.Render("CONSOLE") + "\n" + m.console(m.width-4, h))
	return lipgloss.JoinVertical(lipgloss.Left, header, address, console, footer)
}

// logoArt is the title in the ANSI Shadow figlet font.
var logoArt = []string{
	"███╗   ███╗ ██████╗  ██████╗  ██████╗██╗  ██╗    █████╗ ██╗",
	"████╗ ████║██╔═══██╗██╔═══██╗██╔════╝██║  ██║   ██╔══██╗██║",
	"██╔████╔██║██║   ██║██║   ██║██║     ███████║   ███████║██║",
	"██║╚██╔╝██║██║   ██║██║   ██║██║     ██╔══██║   ██╔══██║██║",
	"██║ ╚═╝ ██║╚██████╔╝╚██████╔╝╚██████╗██║  ██║██╗██║  ██║██║",
	"╚═╝     ╚═╝ ╚═════╝  ╚═════╝  ╚═════╝╚═╝  ╚═╝╚═╝╚═╝  ╚═╝╚═╝",
}

// Gradient end points: teal at the top left, blue at the bottom right.
var gradientFrom, gradientTo = [3]float64{0x2d, 0xe2, 0xc9}, [3]float64{0x3b, 0x6c, 0xf6}

// logo is the boxed title with a diagonal teal to blue gradient. The colours do not change, so logo renders once.
var logo = func() string {
	width := ansi.StringWidth(logoArt[0])
	rows := make([]string, len(logoArt))
	for y, line := range logoArt {
		var b strings.Builder
		for x, r := range []rune(line) {
			t := float64(x+3*y) / float64(width+3*len(logoArt))
			var c [3]int
			for i := range c {
				c[i] = int(gradientFrom[i] + t*(gradientTo[i]-gradientFrom[i]))
			}
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2]))).Render(string(r)))
		}
		rows[y] = b.String()
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#2dd4bf")).
		Padding(0, 2).Render(strings.Join(rows, "\n"))
}()

// header returns the top row: the logo and the NODES box at the same height.
// A narrow terminal shows a one-line title in place of the logo.
func (m *model) header() string {
	rows, left, title := lipgloss.Height(logo)-2, logo, ""
	if m.width < lipgloss.Width(logo)+50 {
		left = ""
		title = titleStyle.Render("Mooch.ai Central") + "\n"
	}
	width := m.width - lipgloss.Width(left)
	nodes := boxStyle.Width(width - 2).Height(rows).Render(m.nodeList(width-4, rows))
	return title + lipgloss.JoinHorizontal(lipgloss.Top, left, nodes)
}

// address returns one line with the central address, the OpenAI base URL, and the join command.
func (m *model) address() string {
	line := lipgloss.NewStyle().Foreground(green).Render("● tailnet "+m.addr) +
		dimStyle.Render("   OpenAI base URL ") + "http://" + m.addr + "/v1" +
		dimStyle.Render("   join ") + "curl -fsSL http://" + m.addr + "/join.sh | sh"
	if !m.tailnet {
		line = lipgloss.NewStyle().Foreground(yellow).Render("● local only "+m.addr) +
			dimStyle.Render("   Other machines cannot join. Listen on a Tailscale IP to share models.")
	}
	return ansi.Truncate(" "+line, m.width, "…")
}

// fit cuts rows to height lines. The last line counts the rows that do not fit.
func fit(rows []string, height int) []string {
	if len(rows) <= height {
		return rows
	}
	return append(rows[:height-1], dimStyle.Render(fmt.Sprintf("+ %d more", len(rows)-height+1)))
}

func (m *model) nodeList(width, height int) string {
	label := labelStyle.Render("NODES") + dimStyle.Render(fmt.Sprintf(" %d connected", len(m.nodes)))
	if len(m.nodes) == 0 {
		return label + "\n" + dimStyle.Render("No nodes yet. Run the join command on a machine with a model backend.")
	}
	nameW, ipW := 0, 0
	for _, n := range m.nodes {
		nameW = max(nameW, len(feed.Name(n)))
		ipW = max(ipW, len(n.TailscaleIP))
	}
	var rows []string
	for _, n := range m.nodes {
		age := m.now.Sub(n.LastSeen)
		dot := lipgloss.NewStyle().Foreground(green).Render("●")
		if age > staleAfter {
			dot = lipgloss.NewStyle().Foreground(yellow).Render("●")
		}
		row := fmt.Sprintf("%s %s  %s  %s  %s", dot,
			lipgloss.NewStyle().Foreground(cyan).Bold(true).Render(fmt.Sprintf("%-*s", nameW, feed.Name(n))),
			dimStyle.Render(fmt.Sprintf("%-*s", ipW, n.TailscaleIP)),
			dimStyle.Render(fmt.Sprintf("%3ds ago", int(max(0, age.Seconds())))),
			lipgloss.NewStyle().Foreground(magenta).Render(strings.Join(feed.Models(n), ", ")))
		rows = append(rows, ansi.Truncate(row, width, "…"))
	}
	return strings.Join(append([]string{label}, fit(rows, height-1)...), "\n")
}

// console returns the last height screen lines above the scroll position. Long lines wrap.
func (m *model) console(width, height int) string {
	var screen []string
	for _, l := range m.lines {
		screen = append(screen, wrap(l, width)...)
	}
	if len(screen) == 0 {
		screen = []string{dimStyle.Render("Waiting for events: nodes that join or leave, routed requests, and responses.")}
	}
	m.scroll = min(m.scroll, max(0, len(screen)-height))
	end := len(screen) - m.scroll
	view := screen[max(0, end-height):end]
	// Pad to the full height so the console box fills the screen.
	return strings.Join(append(view, make([]string, height-len(view))...), "\n")
}

// wrap cuts line into screen lines of width. Continuation lines start under the message, after the time and tag.
func wrap(line string, width int) []string {
	const indent = 16 // "15:04:05  ROUTE "
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
