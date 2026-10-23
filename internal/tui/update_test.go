package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyMsg(key string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

func specialKey(t tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: t}
}

func updateModel(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	if converted, ok := next.(Model); ok {
		return converted
	}
	panic("Update did not return a Model")
}

func TestDashboardNavigationClamps(t *testing.T) {
	m := testModel(t, "a", "b", "c")
	m = updateModel(m, keyMsg("j"))
	m = updateModel(m, keyMsg("j"))
	if m.SelectedID() != "c" {
		t.Fatalf("selected = %q, want c", m.SelectedID())
	}
	m = updateModel(m, keyMsg("j"))
	if m.SelectedID() != "c" {
		t.Fatalf("selection must clamp at end, got %q", m.SelectedID())
	}
	m = updateModel(m, keyMsg("k"))
	if m.SelectedID() != "b" {
		t.Fatalf("selected = %q, want b", m.SelectedID())
	}
	m = updateModel(m, specialKey(tea.KeyHome))
	if m.SelectedID() != "a" {
		t.Fatalf("home = %q, want a", m.SelectedID())
	}
	m = updateModel(m, specialKey(tea.KeyEnd))
	if m.SelectedID() != "c" {
		t.Fatalf("end = %q, want c", m.SelectedID())
	}
}

func TestEnterOpensDetailAndEscReturns(t *testing.T) {
	m := testModel(t, "a", "b")
	m = updateModel(m, keyMsg("j"))
	m = updateModel(m, specialKey(tea.KeyEnter))
	if m.screen != ScreenDetail || m.detailID != "b" {
		t.Fatalf("screen = %v detail = %q", m.screen, m.detailID)
	}
	m = updateModel(m, specialKey(tea.KeyEsc))
	if m.screen != ScreenDashboard {
		t.Fatalf("esc must return to dashboard, got %v", m.screen)
	}
}

func TestScreenShortcutsAndTabCycle(t *testing.T) {
	m := testModel(t, "a")
	for key, want := range map[string]Screen{"1": ScreenDashboard, "2": ScreenDetail, "3": ScreenIncidents, "4": ScreenHistory} {
		m = updateModel(m, keyMsg(key))
		if m.screen != want {
			t.Fatalf("key %q -> screen %v, want %v", key, m.screen, want)
		}
	}
	m.screen = ScreenDashboard
	m = updateModel(m, specialKey(tea.KeyTab))
	if m.screen != ScreenDetail {
		t.Fatalf("tab -> %v, want detail", m.screen)
	}
	m = updateModel(m, specialKey(tea.KeyShiftTab))
	if m.screen != ScreenDashboard {
		t.Fatalf("shift+tab -> %v, want dashboard", m.screen)
	}
}

func TestHelpToggle(t *testing.T) {
	m := testModel(t, "a")
	m = updateModel(m, keyMsg("?"))
	if m.screen != ScreenHelp {
		t.Fatalf("? must open help, got %v", m.screen)
	}
	m = updateModel(m, keyMsg("?"))
	if m.screen != ScreenDashboard {
		t.Fatalf("? again must return, got %v", m.screen)
	}
}

func TestDetailStepsAcrossBackends(t *testing.T) {
	m := testModel(t, "a", "b", "c")
	m.screen = ScreenDetail
	m.detailID = "b"
	m.selected = 1
	m = updateModel(m, specialKey(tea.KeyRight))
	if m.detailID != "c" || m.selected != 2 {
		t.Fatalf("right -> %q/%d", m.detailID, m.selected)
	}
	m = updateModel(m, specialKey(tea.KeyRight))
	if m.detailID != "a" {
		t.Fatalf("right must wrap, got %q", m.detailID)
	}
	m = updateModel(m, specialKey(tea.KeyLeft))
	if m.detailID != "c" {
		t.Fatalf("left must wrap back, got %q", m.detailID)
	}
}

func TestSelectionSurvivesProbeUpdates(t *testing.T) {
	m := testModel(t, "a", "b")
	m.selected = 1
	next, _ := m.Update(probeMsg{event: probeEvent("a", "unhealthy", "none")})
	m = next.(Model)
	if m.selected != 1 || m.SelectedID() != "b" {
		t.Fatalf("probe update moved selection: %d %q", m.selected, m.SelectedID())
	}
}

func TestQuitKeys(t *testing.T) {
	m := testModel(t, "a")
	for _, key := range []tea.KeyMsg{keyMsg("q"), {Type: tea.KeyCtrlC}} {
		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatalf("key %v must return a command", key)
		}
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); !ok {
			t.Fatalf("key %v must quit, got %T", key, msg)
		}
	}
}

func TestFeedEndMarksStale(t *testing.T) {
	m := testModel(t, "a")
	m = updateModel(m, feedEndMsg{err: nil})
	if m.live {
		t.Error("feed end must clear live flag")
	}
	view := m.View()
	if !contains(view, "STALE") {
		t.Errorf("stale view must say STALE:\n%s", view)
	}
}

func TestTickAdvancesClock(t *testing.T) {
	m := testModel(t, "a")
	before := m.now
	m = updateModel(m, tickMsg{now: before.Add(5_000_000_000)})
	if !m.now.After(before) {
		t.Error("tick must advance the clock")
	}
}
