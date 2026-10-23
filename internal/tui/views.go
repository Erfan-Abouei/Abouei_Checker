package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// viewDetail shows one backend's latest probe: state, timing, streaks, and
// the safe error summary. Secrets never appear: MonitorEvent errors are
// pre-sanitized by the engine.
func (m Model) viewDetail(width int) string {
	b := m.backends[m.detailID]
	if b == nil {
		if id := m.SelectedID(); id != "" {
			b = m.backends[id]
		}
	}
	var s strings.Builder
	if b == nil {
		s.WriteString(m.theme.muted.Render("no backend selected"))
		return s.String()
	}

	head := m.theme.title.Render(b.Name) + "  " + m.theme.Status(b.DisplayStatus())
	if b.Name != b.ID {
		head += m.theme.muted.Render("  (" + b.ID + ")")
	}
	s.WriteString(head + "\n")
	s.WriteString(m.theme.Rule(width))

	row := func(label, value string) {
		s.WriteString("\n" + m.theme.muted.Render(padRight(label, 14)) + value)
	}
	if !b.Seen {
		row("state", m.theme.Status("pending"))
		row("detail", m.theme.muted.Render("waiting for first probe this session"))
		row("hint", m.theme.muted.Render("probes run immediately on launch"))
		return s.String()
	}

	row("state", m.theme.Status(b.DisplayStatus()))
	observed := b.ObservedAt.Format("2006-01-02 15:04:05 UTC") + " (" + ago(b.ObservedAt, m.now) + ")"
	row("last check", m.theme.muted.Render(fitCell(observed, width-18)))
	if b.Healthy {
		row("response", fmt.Sprintf("%d in %s", b.HTTPStatus, fmtMs(b.LatencyMs)))
	} else {
		status := "no response"
		if b.HTTPStatus != 0 {
			status = fmt.Sprintf("status %d", b.HTTPStatus)
		}
		row("response", m.theme.bad.Render(fitCell(status, width-18)))
	}
	row("streak", fmt.Sprintf("%d failure(s) / threshold %d", b.Failures, b.Threshold))
	if b.Incident != "" && b.Incident != "none" {
		row("incident", m.theme.accent.Render(fitCell(b.Incident, width-18)))
	}
	if b.FailureNote != "" && b.FailureNote != b.Outcome {
		row("reason", fitCell(b.FailureNote, width-18))
	}
	if b.Error != "" {
		row("error", m.theme.warn.Render(fitCell(b.Error, width-18)))
	}
	row("cycle", fmt.Sprintf("#%d this session", b.Cycle))

	s.WriteString("\n" + m.theme.Rule(width))
	s.WriteString("\n" + m.theme.muted.Render("durable history: abouei-checker history --backend "+b.ID))
	return s.String()
}

// viewIncidents lists active incidents plus session recoveries.
func (m Model) viewIncidents(width int) string {
	var s strings.Builder
	active := activeIncidents(&m)
	s.WriteString(m.theme.title.Render(fmt.Sprintf("Active incidents (%d)", len(active))))
	s.WriteString("\n" + m.theme.Rule(width))
	if len(active) == 0 {
		s.WriteString("\n" + m.theme.muted.Render("  none — no open incidents"))
	} else {
		for _, b := range active {
			line := fmt.Sprintf("%s  %s  streak %d/%d  since %s",
				padCell(b.Name, nameWidth(width)),
				m.theme.Status("open"),
				b.Failures, b.Threshold,
				ago(b.ObservedAt, m.now))
			s.WriteString("\n  " + fitCell(line, width-4))
		}
	}

	s.WriteString("\n\n" + m.theme.title.Render("Recovered this session"))
	s.WriteString("\n" + m.theme.Rule(width))
	recovered := 0
	for i := len(m.feed) - 1; i >= 0; i-- {
		e := m.feed[i]
		if e.Kind != FeedIncident || !strings.Contains(e.Summary, "recovered") {
			continue
		}
		s.WriteString("\n  " + fitCell(ago(e.At, m.now)+"  "+e.Summary, width-4))
		recovered++
		if recovered >= 10 {
			break
		}
	}
	if recovered == 0 {
		s.WriteString("\n" + m.theme.muted.Render("  none yet"))
	}
	return s.String()
}

// viewHistory shows the session probe timeline, newest first, and points at
// the durable `history` command for anything beyond this session.
func (m Model) viewHistory(width int) string {
	var s strings.Builder
	s.WriteString(m.theme.title.Render(fmt.Sprintf("Session events (%d)", len(m.feed))))
	s.WriteString("\n" + m.theme.Rule(width))
	if len(m.feed) == 0 {
		s.WriteString("\n" + m.theme.muted.Render("  no events yet — probes appear here as they complete"))
	} else {
		shown := 0
		budget := m.historyBudget()
		for i := len(m.feed) - 1 - m.offset; i >= 0 && shown < budget; i-- {
			s.WriteString("\n  " + fitCell(m.feedLine(m.feed[i], width), width-4))
			shown++
		}
		if m.offset > 0 {
			s.WriteString("\n" + m.theme.muted.Render("  … scrolled"))
		}
	}
	s.WriteString("\n\n" + m.theme.muted.Render("durable history: abouei-checker history [--backend ID] [--limit N]"))
	return s.String()
}

func (m Model) historyBudget() int {
	if m.height <= 0 {
		return 30
	}
	budget := m.height - 10
	if budget < 5 {
		budget = 5
	}
	if budget > 50 {
		budget = 50
	}
	return budget
}

// viewHelp documents every binding; bindings live in update.go and this
// table is written from the same constants.
func (m Model) viewHelp(width int) string {
	var s strings.Builder
	s.WriteString(m.theme.title.Render("Keyboard shortcuts"))
	s.WriteString("\n" + m.theme.Rule(width))
	rows := [][2]string{
		{"↑ ↓ / k j", "move selection or scroll"},
		{"enter", "open backend details / back to dashboard"},
		{"← → / h l", "previous / next backend in details"},
		{"1 2 3 4", "dashboard · backend · incidents · session events"},
		{"tab / shift+tab", "next / previous screen"},
		{"?", "this help (esc returns)"},
		{"esc", "back to dashboard"},
		{"q / ctrl+c", "quit (monitoring shuts down gracefully)"},
	}
	for _, row := range rows {
		s.WriteString("\n  " + m.theme.accent.Render(padRight(row[0], 20)) + fitCell(row[1], width-26))
	}
	s.WriteString("\n\n" + m.theme.muted.Render("live data only — use check/status/history for scripts and JSON"))
	return s.String()
}

func padRight(value string, n int) string {
	if len(value) >= n {
		return value
	}
	return value + strings.Repeat(" ", n-len(value))
}

func padCell(value string, n int) string {
	value = fitCell(value, n)
	if lipgloss.Width(value) >= n {
		return value
	}
	return value + strings.Repeat(" ", n-lipgloss.Width(value))
}
