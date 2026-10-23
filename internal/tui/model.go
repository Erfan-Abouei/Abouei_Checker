package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
)

// Screen selects the visible TUI surface.
type Screen int

const (
	// ScreenDashboard shows aggregate health and the navigable backend list.
	ScreenDashboard Screen = iota
	// ScreenDetail shows one backend's latest probe, timing, and errors.
	ScreenDetail
	// ScreenIncidents shows active and recently recovered incidents.
	ScreenIncidents
	// ScreenHistory shows this session's probe events, newest first.
	ScreenHistory
	// ScreenHelp shows keyboard shortcuts and navigation guidance.
	ScreenHelp
)

// screenTitles names every screen for headers and navigation hints.
var screenTitles = map[Screen]string{
	ScreenDashboard: "Dashboard",
	ScreenDetail:    "Backend",
	ScreenIncidents: "Incidents",
	ScreenHistory:   "Session events",
	ScreenHelp:      "Help",
}

// Title reports the human name of a screen.
func (s Screen) Title() string {
	if title, ok := screenTitles[s]; ok {
		return title
	}
	return "Dashboard"
}

// BackendState is the UI-facing snapshot of one backend: the latest live
// probe event, or an unseen placeholder before the first probe lands.
type BackendState struct {
	ID          string
	Name        string
	Seen        bool
	Outcome     string
	Healthy     bool
	HTTPStatus  int
	LatencyMs   int64
	ObservedAt  time.Time
	FailureNote string
	Error       string
	Failures    int
	Threshold   int
	Incident    string
	Cycle       int
}

// DisplayStatus maps engine state to the status word shown in the UI. Only
// real domain states are used: healthy, down, unknown, orphaned, pending
// (configured but not yet observed this session).
func (b *BackendState) DisplayStatus() string {
	if b == nil || !b.Seen {
		return "pending"
	}
	switch b.Outcome {
	case "healthy":
		return "healthy"
	case "canceled":
		return "unknown"
	case "unhealthy", "error":
		return "down"
	default:
		return "unknown"
	}
}

// FeedKind classifies session feed entries.
type FeedKind string

const (
	// FeedIncident marks incident opened/recovered transitions.
	FeedIncident FeedKind = "incident"
	// FeedFailure marks a backend turning unhealthy.
	FeedFailure FeedKind = "failure"
	// FeedRecovery marks a backend returning to healthy.
	FeedRecovery FeedKind = "recovery"
	// FeedNotice marks operational notes (feed start/end, errors).
	FeedNotice FeedKind = "notice"
)

// FeedEvent is one session-scoped timeline entry. Session feeds are never
// presented as durable history; the `history` command owns that data.
type FeedEvent struct {
	At        time.Time
	BackendID string
	Kind      FeedKind
	Summary   string
}

// maxFeed caps the session timeline so long runs stay bounded.
const maxFeed = 200

// probeMsg carries one live engine event into the Elm update loop.
type probeMsg struct{ event app.MonitorEvent }

// seedMsg carries the pre-launch Status snapshot used before the first
// probe of the session lands.
type seedMsg struct{ report app.StatusReport }

// feedEndMsg marks the monitor goroutine exiting; the UI keeps last-known
// state and labels it stale instead of clearing the screen.
type feedEndMsg struct{ err error }

// tickMsg drives "x ago" labels and session duration once per second.
type tickMsg struct{ now time.Time }

// Model is the TUI application state. Backend order is fixed from
// configuration so state changes never reorder rows under the cursor.
//
// events wires the live engine feed into the Elm loop without a forwarding
// goroutine: Init and every probe message re-arm a command that blocks until
// the next engine message. The UI therefore never polls and never blocks a
// probe. The engine outcome channel is owned by Run, so exactly one receiver
// observes it.
type Model struct {
	theme    Theme
	screen   Screen
	previous Screen

	order    []string
	backends map[string]*BackendState

	selected int
	detailID string
	offset   int

	feed      []FeedEvent
	startedAt time.Time
	now       time.Time
	live      bool
	endedErr  error
	note      string

	width  int
	height int
	ready  bool

	events <-chan tea.Msg
}

// NewModel builds the initial state. Order follows configuration and never
// changes; names map backend IDs to display names.
func NewModel(theme Theme, order []string, names map[string]string, start time.Time) Model {
	ids := append([]string(nil), order...)
	states := make(map[string]*BackendState, len(ids))
	for _, id := range ids {
		name := names[id]
		if name == "" {
			name = id
		}
		states[id] = &BackendState{ID: id, Name: name}
	}
	return Model{
		theme:     theme,
		screen:    ScreenDashboard,
		order:     ids,
		backends:  states,
		now:       start,
		startedAt: start,
		live:      true,
	}
}

// SelectedID reports the currently highlighted backend, or "" when empty.
func (m *Model) SelectedID() string {
	if len(m.order) == 0 {
		return ""
	}
	i := m.selected
	if i < 0 {
		i = 0
	}
	if i >= len(m.order) {
		i = len(m.order) - 1
	}
	return m.order[i]
}

// counts aggregates headline numbers from real state only.
func (m *Model) counts() (total, healthy, failing, incidents int) {
	total = len(m.order)
	for _, id := range m.order {
		b := m.backends[id]
		if b == nil || !b.Seen {
			continue
		}
		if b.Healthy {
			healthy++
			continue
		}
		if b.Outcome == "canceled" {
			continue
		}
		failing++
		switch b.Incident {
		case "open", "still_open":
			incidents++
		}
	}
	return total, healthy, failing, incidents
}

// pushFeed appends a timeline entry, keeping the buffer bounded.
func (m *Model) pushFeed(entry FeedEvent) {
	m.feed = append(m.feed, entry)
	if len(m.feed) > maxFeed {
		m.feed = m.feed[len(m.feed)-maxFeed:]
	}
}
