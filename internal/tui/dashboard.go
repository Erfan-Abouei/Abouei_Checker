package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// viewDashboard shows aggregate health, the stable backend list, and recent
// session events. Row order never changes; the viewport follows the cursor.
func (m Model) viewDashboard(width int) string {
	var b strings.Builder
	b.WriteString(m.summaryLine())
	b.WriteString("\n")
	b.WriteString(m.theme.Rule(width))

	rows, cursor := m.dashboardRows(width)
	avail := m.listHeight()
	visible, offset := window(rows, cursor, avail)
	for i, row := range visible {
		mark := "  "
		if offset+i == cursor {
			mark = m.theme.selBar.Render("▌ ")
			row = m.theme.selRow.Render(row)
		}
		b.WriteString("\n" + mark + row)
	}
	if len(rows) == 0 {
		b.WriteString("\n" + m.theme.muted.Render("  no backends configured"))
	}

	if events := m.recentFeed(3, width); len(events) > 0 && m.heightAllowsEvents(len(visible)) {
		b.WriteString("\n" + m.theme.Rule(width))
		for _, line := range events {
			b.WriteString("\n" + line)
		}
	}
	if m.note != "" {
		b.WriteString("\n" + m.theme.warn.Render(m.note))
	}
	return b.String()
}

// dashboardRows renders one line per backend with progressive disclosure:
// latency hides below 90 columns, error detail below 70.
func (m Model) dashboardRows(width int) ([]string, int) {
	rows := make([]string, 0, len(m.order))
	for _, id := range m.order {
		rows = append(rows, m.rowLine(m.backends[id], width))
	}
	return rows, m.selected
}

func (m Model) rowLine(b *BackendState, width int) string {
	if b == nil {
		return m.theme.muted.Render("—")
	}
	name := fitCell(b.Name, nameWidth(width))
	status := m.theme.Status(b.DisplayStatus())
	line := fmt.Sprintf("%-*s  %s", displayPad(name, nameWidth(width)), name, status)
	rest := width - lipgloss.Width(line) - 2
	if rest < 0 {
		rest = 0
	}
	var detail string
	switch {
	case b == nil || !b.Seen:
		detail = "waiting for first probe"
	case b.Healthy:
		detail = fmt.Sprintf("%d in %s", b.HTTPStatus, fmtMs(b.LatencyMs))
		if width < 90 {
			detail = fmt.Sprintf("%d", b.HTTPStatus)
		}
	default:
		detail = shortError(b, width)
	}
	if detail != "" && rest > 12 {
		line += "  " + m.theme.muted.Render(fitCell(detail, rest))
	}
	return line
}

func nameWidth(width int) int {
	n := 24
	if width < 70 {
		n = 14
	}
	if width < 50 {
		n = 10
	}
	return n
}

func displayPad(_ string, n int) int { return n }

// fmtMs renders a millisecond latency compactly.
func fmtMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmtDuration(time.Duration(ms) * time.Millisecond)
}

func shortError(b *BackendState, width int) string {
	budget := 60
	if width < 70 {
		return fmt.Sprintf("streak %d/%d", b.Failures, b.Threshold)
	}
	note := strings.TrimSpace(b.Error)
	if note == "" {
		note = strings.TrimSpace(b.FailureNote)
	}
	if note == "" {
		note = b.Outcome
	}
	if lipgloss.Width(note) > budget {
		note = fitCell(note, budget)
	}
	return fmt.Sprintf("%s · streak %d/%d", note, b.Failures, b.Threshold)
}

// listHeight budgets rows from the terminal height, reserving chrome.
func (m Model) listHeight() int {
	h := m.height
	if h <= 0 {
		return len(m.order)
	}
	avail := h - 9
	if avail < 3 {
		avail = 3
	}
	if avail > 20 {
		avail = 20
	}
	return avail
}

func (m Model) heightAllowsEvents(visible int) bool {
	if m.height <= 0 {
		return true
	}
	return m.height-visible > 12
}

// recentFeed renders the newest session entries, oldest last.
func (m Model) recentFeed(limit int, width int) []string {
	if len(m.feed) == 0 || limit <= 0 {
		return nil
	}
	start := len(m.feed) - limit
	if start < 0 {
		start = 0
	}
	lines := make([]string, 0, limit)
	for i := len(m.feed) - 1; i >= start; i-- {
		lines = append(lines, m.feedLine(m.feed[i], width))
	}
	return lines
}

func (m Model) feedLine(e FeedEvent, width int) string {
	var kind string
	switch e.Kind {
	case FeedIncident:
		kind = m.theme.bad.Render("incident")
	case FeedFailure:
		kind = m.theme.warn.Render("failure")
	case FeedRecovery:
		kind = m.theme.ok.Render("recovery")
	default:
		kind = m.theme.muted.Render("notice")
	}
	text := ago(e.At, m.now) + "  " + kind + "  " + e.Summary
	return "  " + fitCell(text, width-4)
}

// window slices rows to a viewport that keeps the cursor visible.
func window(rows []string, cursor, size int) ([]string, int) {
	if size <= 0 || len(rows) <= size {
		return rows, 0
	}
	offset := cursor - size + 1
	if offset < 0 {
		offset = 0
	}
	if offset+size > len(rows) {
		offset = len(rows) - size
	}
	return rows[offset : offset+size], offset
}
