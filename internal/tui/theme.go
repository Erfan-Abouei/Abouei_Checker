// Package tui implements the interactive ABOUEI monitoring dashboard.
//
// The TUI renders live state from the real application engine: it seeds from
// a Status snapshot, then runs exactly one app.Monitor loop and renders every
// MonitorEvent it emits. It never probes endpoints itself and never invents
// metrics the engine cannot support (no uptime percentages, no fabricated
// history). Session-scoped panels are labeled as such; durable history stays
// in the `history` command.
package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme holds the centralized ABOUEI visual tokens: dark-first surfaces,
// bright-white identity, muted secondary text, and a restrained semantic
// palette (green healthy, red failure, amber warning, cyan accent).
// All screens build from these tokens so styling stays consistent.
type Theme struct {
	plain bool

	brand   lipgloss.Style
	title   lipgloss.Style
	muted   lipgloss.Style
	ok      lipgloss.Style
	bad     lipgloss.Style
	warn    lipgloss.Style
	accent  lipgloss.Style
	selBar  lipgloss.Style
	selRow  lipgloss.Style
	pillOK  lipgloss.Style
	pillBad lipgloss.Style
	pillMut lipgloss.Style
	rule    lipgloss.Style
}

// NewTheme builds tokens for the current terminal. When NO_COLOR is set the
// theme carries no foreground color (bold text remains) so the interface
// stays useful on limited terminals.
func NewTheme() Theme {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return plainTheme()
	}
	fg := func(light, dark string) lipgloss.TerminalColor {
		return lipgloss.AdaptiveColor{Light: light, Dark: dark}
	}
	muted := lipgloss.NewStyle().Foreground(fg("#5B6472", "#8B93A3"))
	return Theme{
		brand:  lipgloss.NewStyle().Bold(true).Foreground(fg("#111111", "#FFFFFF")),
		title:  lipgloss.NewStyle().Bold(true).Foreground(fg("#111111", "#FFFFFF")),
		muted:  muted,
		ok:     lipgloss.NewStyle().Foreground(fg("#15803D", "#4ADE80")),
		bad:    lipgloss.NewStyle().Foreground(fg("#B91C1C", "#F87171")),
		warn:   lipgloss.NewStyle().Foreground(fg("#B45309", "#FBBF24")),
		accent: lipgloss.NewStyle().Foreground(fg("#0E7490", "#22D3EE")),
		selBar: lipgloss.NewStyle().Foreground(fg("#0E7490", "#22D3EE")),
		selRow: lipgloss.NewStyle().Bold(true),
		pillOK: lipgloss.NewStyle().Bold(true).Foreground(fg("#15803D", "#4ADE80")),
		pillBad: lipgloss.NewStyle().Bold(true).Foreground(fg("#FFFFFF", "#FFFFFF")).
			Background(fg("#B91C1C", "#7F1D1D")),
		pillMut: lipgloss.NewStyle().Bold(true).Foreground(fg("#5B6472", "#8B93A3")),
		rule:    muted,
	}
}

func plainTheme() Theme {
	bold := lipgloss.NewStyle().Bold(true)
	plain := lipgloss.NewStyle()
	return Theme{
		plain:   true,
		brand:   bold,
		title:   bold,
		muted:   plain,
		ok:      plain,
		bad:     bold,
		warn:    bold,
		accent:  plain,
		selBar:  plain,
		selRow:  bold,
		pillOK:  bold,
		pillBad: bold,
		pillMut: bold,
		rule:    plain,
	}
}

// Plain reports whether the theme emits no color.
func (t Theme) Plain() bool { return t.plain }

// Status colors a domain status word while preserving its exact text.
func (t Theme) Status(word string) string {
	switch word {
	case "healthy", "recovered", "HEALTHY":
		return t.ok.Render(word)
	case "down", "unhealthy", "error", "open", "DOWN":
		return t.bad.Render(word)
	case "unknown", "orphaned", "canceled", "pending", "loading", "stale", "UNKNOWN":
		return t.warn.Render(word)
	default:
		return t.muted.Render(word)
	}
}

// Bar renders the brand accent mark. In plain mode it is a simple pipe so
// the lockup stays recognizable without decoration.
func (t Theme) Bar() string {
	if t.plain {
		return "|"
	}
	return t.accent.Render("▌")
}

// Rule renders a muted horizontal separator capped for readability.
func (t Theme) Rule(width int) string {
	n := width - 2
	if n > 72 {
		n = 72
	}
	if n < 8 {
		n = 8
	}
	return t.rule.Render(strings.Repeat("-", n))
}
