package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// View renders the current screen, adapting to terminal dimensions. Narrow
// windows progressively drop lower-priority detail; critical status always
// stays visible.
func (m Model) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	var body string
	switch m.screen {
	case ScreenDetail:
		body = m.viewDetail(width)
	case ScreenIncidents:
		body = m.viewIncidents(width)
	case ScreenHistory:
		body = m.viewHistory(width)
	case ScreenHelp:
		body = m.viewHelp(width)
	default:
		body = m.viewDashboard(width)
	}
	page := m.renderHeader(width) + "\n" + body + "\n" + m.renderFooter(width)
	return lipgloss.NewStyle().MaxWidth(width).Render(page)
}

// renderHeader shows the ABOUEI lockup, product line, and liveness pill.
func (m Model) renderHeader(width int) string {
	brand := m.theme.Bar() + " " + m.theme.brand.Render("ABOUEI")
	pill := m.livePill()
	line := brand
	if width >= 60 {
		line += m.theme.muted.Render("  " + screenTitles[m.screen])
	}
	gap := width - lipgloss.Width(line) - lipgloss.Width(pill)
	if gap < 1 {
		return line + "\n" + pill
	}
	return line + strings.Repeat(" ", gap) + pill
}

// livePill reports whether the engine feed is live or stale.
func (m Model) livePill() string {
	if m.live {
		return m.theme.pillOK.Render("● LIVE")
	}
	return m.theme.pillBad.Render("■ STALE")
}

// renderFooter shows contextual shortcuts for the active screen.
func (m Model) renderFooter(width int) string {
	var hints string
	switch m.screen {
	case ScreenDashboard:
		hints = "↑↓ navigate · enter details · 1-4 screens · ? help · q quit"
	case ScreenDetail:
		hints = "←→ switch backend · esc dashboard · 1-4 screens · q quit"
	case ScreenIncidents, ScreenHistory:
		hints = "↑↓ scroll · esc dashboard · 1-4 screens · q quit"
	default:
		hints = "esc back · 1-4 screens · q quit"
	}
	if width < 60 {
		hints = "? help · q quit"
	}
	meta := ""
	if !m.live {
		meta = m.theme.warn.Render("stale") + " "
	}
	line := meta + m.theme.muted.Render(hints)
	if lipgloss.Width(line) > width && width > 0 {
		line = m.theme.muted.Render("? help · q quit")
	}
	return line
}

// summaryLine aggregates headline numbers from real state only.
func (m Model) summaryLine() string {
	total, healthy, failing, incidents := m.counts()
	pending := total - healthy - failing
	parts := []string{
		fmt.Sprintf("%d backend%s", total, plural(total)),
		m.theme.Status("healthy") + fmt.Sprintf(" %d", healthy),
	}
	if failing > 0 {
		parts = append(parts, m.theme.Status("down")+fmt.Sprintf(" %d", failing))
	}
	if pending > 0 {
		parts = append(parts, m.theme.Status("pending")+fmt.Sprintf(" %d", pending))
	}
	if incidents > 0 {
		parts = append(parts, m.theme.accent.Render(fmt.Sprintf("%d open incident%s", incidents, plural(incidents))))
	}
	parts = append(parts, m.theme.muted.Render("session "+sessionAge(m.startedAt, m.now)))
	return strings.Join(parts, m.theme.muted.Render(" · "))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func sessionAge(start, now time.Time) string {
	if start.IsZero() || now.IsZero() {
		return "0s"
	}
	d := now.Sub(start)
	if d < 0 {
		d = 0
	}
	return fmtDuration(d)
}

// fmtDuration renders compact durations: 42ms, 3.1s, 12m, 2h.
func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		if s := d.Seconds(); s < 10 {
			return fmt.Sprintf("%.1fs", s)
		}
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// ago renders "12s ago" style timestamps, or "—" when unknown.
func ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return "just now"
	}
	return fmtDuration(d) + " ago"
}

// fitCell truncates a cell to a display width, accounting for wide runes.
func fitCell(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	runes := []rune(value)
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		if lipgloss.Width(string(runes[:mid])) <= width-3 {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return string(runes[:low]) + "..."
}

// ensure tea import is used even when chrome helpers are tested alone.
var _ tea.Msg = tickMsg{}
