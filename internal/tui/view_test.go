package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func contains(view, want string) bool { return strings.Contains(view, want) }

func sizedModel(t *testing.T, width, height int) Model {
	t.Helper()
	m := testModel(t, "api", "web")
	m.width, m.height, m.ready = width, height, true
	applyProbe(&m, probeEvent("api", "healthy", "none"))
	failing := probeEvent("web", "unhealthy", "open")
	failing.HTTPStatus = 503
	failing.Error = "unexpected status code 503"
	failing.ConsecutiveFailures = 3
	applyProbe(&m, failing)
	return m
}

func TestDashboardRendersRealState(t *testing.T) {
	view := sizedModel(t, 120, 40).View()
	for _, want := range []string{"ABOUEI", "Dashboard", "api", "web", "healthy", "down", "LIVE"} {
		if !contains(view, want) {
			t.Errorf("dashboard missing %q:\n%s", want, view)
		}
	}
}

func TestNarrowRendersCriticalStatus(t *testing.T) {
	for _, width := range []int{60, 40, 30} {
		view := sizedModel(t, width, 30).View()
		for _, want := range []string{"ABOUEI", "api", "down"} {
			if !contains(view, want) {
				t.Errorf("width %d missing %q:\n%s", width, want, view)
			}
		}
	}
}

func TestEmptyStateRenders(t *testing.T) {
	m := NewModel(testTheme(t), nil, nil, testModel(t).startedAt)
	m.width, m.height, m.ready = 100, 30, true
	view := m.View()
	if !contains(view, "ABOUEI") || !contains(view, "no backends") {
		t.Errorf("empty view must explain itself:\n%s", view)
	}
}

func TestDetailRendersErrorSafely(t *testing.T) {
	m := sizedModel(t, 100, 30)
	m.screen = ScreenDetail
	m.detailID = "web"
	view := m.View()
	for _, want := range []string{"web", "down", "503", "streak", "history --backend web"} {
		if !contains(view, want) {
			t.Errorf("detail missing %q:\n%s", want, view)
		}
	}
}

func TestIncidentsScreenListsOpen(t *testing.T) {
	m := sizedModel(t, 100, 30)
	m.screen = ScreenIncidents
	view := m.View()
	if !contains(view, "Active incidents (1)") || !contains(view, "web") {
		t.Errorf("incidents screen wrong:\n%s", view)
	}
}

func TestHistoryLabelsSessionScope(t *testing.T) {
	m := sizedModel(t, 100, 30)
	m.screen = ScreenHistory
	view := m.View()
	if !contains(view, "Session events") || !contains(view, "abouei-checker history") {
		t.Errorf("history must be labeled session-scoped:\n%s", view)
	}
}

func TestHelpListsBindings(t *testing.T) {
	m := sizedModel(t, 100, 30)
	m.screen = ScreenHelp
	view := m.View()
	for _, want := range []string{"enter", "tab", "q / ctrl+c", "esc"} {
		if !contains(view, want) {
			t.Errorf("help missing %q:\n%s", want, view)
		}
	}
}

func TestWindowResizeKeepsSelection(t *testing.T) {
	m := sizedModel(t, 120, 40)
	m.selected = 1
	next, _ := m.Update(tea.WindowSizeMsg{Width: 50, Height: 15})
	m = next.(Model)
	if m.selected != 1 || m.width != 50 {
		t.Fatalf("resize broke state: %+v", m)
	}
	view := m.View()
	if !contains(view, "web") {
		t.Errorf("resized view lost content:\n%s", view)
	}
}

func TestThemeRespectsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	theme := NewTheme()
	if !theme.Plain() {
		t.Fatal("NO_COLOR must select the plain theme")
	}
	for _, word := range []string{"healthy", "down", "unknown", "open", "pending"} {
		if got := theme.Status(word); strings.Contains(got, "\x1b[") {
			t.Errorf("plain theme emitted ANSI for %q: %q", word, got)
		} else if !strings.Contains(got, word) {
			t.Errorf("plain theme lost word %q: %q", word, got)
		}
	}
	_ = os.Getenv
}
