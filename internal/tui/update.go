package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Key bindings are defined once here so the help screen and the update loop
// can never disagree.
const (
	keyQuit     = "q"
	keyHelp     = "?"
	keyBack     = "esc"
	keyEnter    = "enter"
	keyUp       = "up"
	keyDown     = "down"
	keyLeft     = "left"
	keyRight    = "right"
	keyTab      = "tab"
	keyShiftTab = "shift+tab"
)

// tickCmd re-arms the one-second clock used for durations and session age.
func tickCmd() tea.Msg {
	time.Sleep(time.Second)
	return tickMsg{now: time.Now().UTC()}
}

// screenCycle is the tab order across the four operational screens.
var screenCycle = []Screen{ScreenDashboard, ScreenDetail, ScreenIncidents, ScreenHistory}

// WithFeed attaches the live engine channel. It is called once before
// launch; tests construct models without a feed and drive Update directly.
func (m Model) WithFeed(events <-chan tea.Msg) Model {
	m.events = events
	return m
}

// waitCmd blocks until the next engine message delivered on the events
// channel (a probeMsg or a terminal feedEndMsg). Blocking inside a command
// is safe: it runs off the main update path and never stalls rendering or
// probes. The stopped channel is owned by Run, so exactly one receiver
// observes the engine outcome.
func (m Model) waitCmd() tea.Cmd {
	events := m.events
	return func() tea.Msg {
		if events == nil {
			return nil
		}
		if msg, ok := <-events; ok {
			return msg
		}
		return feedEndMsg{}
	}
}

// Init starts the clock; the first data arrives via seed/probe messages.
func (m Model) Init() tea.Cmd {
	return tea.Batch(tickCmd, m.waitCmd())
}

// Update routes window, data, clock, and keyboard messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.clamp()
		return m, nil
	case seedMsg:
		applySeed(&m, msg.report)
		m.clamp()
		return m, nil
	case probeMsg:
		applyProbe(&m, msg.event)
		m.clamp()
		return m, m.waitCmd()
	case feedEndMsg:
		m.live = false
		m.endedErr = msg.err
		if msg.err != nil {
			m.note = "monitor feed ended: " + truncateRunes(msg.err.Error(), 100)
		} else {
			m.note = "monitor feed ended"
		}
		m.pushFeed(FeedEvent{At: m.now, Kind: FeedNotice, Summary: m.note})
		return m, nil
	case tickMsg:
		m.now = msg.now
		if m.now.IsZero() {
			m.now = time.Now().UTC()
		}
		return m, tickCmd
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case keyQuit:
		return m, tea.Quit
	case keyHelp:
		if m.screen == ScreenHelp {
			m.screen = m.previous
		} else {
			m.previous = m.screen
			m.screen = ScreenHelp
		}
		m.offset = 0
		return m, nil
	case keyBack:
		if m.screen == ScreenHelp {
			m.screen = m.previous
		} else if m.screen != ScreenDashboard {
			m.screen = ScreenDashboard
		}
		m.offset = 0
		return m, nil
	case "1":
		return m.gotoScreen(ScreenDashboard)
	case "2":
		return m.gotoScreen(ScreenDetail)
	case "3":
		return m.gotoScreen(ScreenIncidents)
	case "4":
		return m.gotoScreen(ScreenHistory)
	case keyTab:
		return m.stepScreen(1)
	case keyShiftTab:
		return m.stepScreen(-1)
	}

	switch m.screen {
	case ScreenDashboard:
		return m.updateDashboardKey(key)
	case ScreenDetail:
		return m.updateDetailKey(key)
	case ScreenIncidents, ScreenHistory:
		return m.updateScrollKey(key)
	}
	return m, nil
}

func (m Model) gotoScreen(next Screen) (tea.Model, tea.Cmd) {
	if next == ScreenDetail && m.SelectedID() == "" {
		m.note = "no backends configured"
		return m, nil
	}
	if next == ScreenDetail {
		m.detailID = m.SelectedID()
	}
	m.screen = next
	m.offset = 0
	return m, nil
}

func (m Model) stepScreen(delta int) (tea.Model, tea.Cmd) {
	current := 0
	for i, s := range screenCycle {
		if s == m.screen {
			current = i
			break
		}
	}
	next := screenCycle[(current+delta+len(screenCycle))%len(screenCycle)]
	return m.gotoScreen(next)
}

func (m Model) updateDashboardKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case keyUp, "k":
		if m.selected > 0 {
			m.selected--
		}
	case keyDown, "j":
		if m.selected < len(m.order)-1 {
			m.selected++
		}
	case "home", "g":
		m.selected = 0
	case "end", "G":
		if len(m.order) > 0 {
			m.selected = len(m.order) - 1
		}
	case keyEnter:
		if id := m.SelectedID(); id != "" {
			m.detailID = id
			m.screen = ScreenDetail
			m.offset = 0
		}
	}
	m.clamp()
	return m, nil
}

func (m Model) updateDetailKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case keyLeft, "[", "h":
		m.stepBackend(-1)
	case keyRight, "]", "l":
		m.stepBackend(1)
	case keyUp, "k":
		if m.offset > 0 {
			m.offset--
		}
	case keyDown, "j":
		m.offset++
	case keyEnter:
		m.screen = ScreenDashboard
		m.offset = 0
	}
	return m, nil
}

// stepBackend moves the detail view across backends in stable order.
func (m *Model) stepBackend(delta int) {
	if len(m.order) == 0 {
		return
	}
	at := 0
	for i, id := range m.order {
		if id == m.detailID {
			at = i
			break
		}
	}
	at = (at + delta + len(m.order)) % len(m.order)
	m.detailID = m.order[at]
	m.selected = at
	m.offset = 0
}

func (m Model) updateScrollKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case keyUp, "k":
		if m.offset > 0 {
			m.offset--
		}
	case keyDown, "j":
		m.offset++
	case "home", "g":
		m.offset = 0
	}
	return m, nil
}

// clamp keeps selection and offsets inside valid ranges so backend churn
// never strands the cursor.
func (m *Model) clamp() {
	if len(m.order) == 0 {
		m.selected = 0
		return
	}
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(m.order) {
		m.selected = len(m.order) - 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}
