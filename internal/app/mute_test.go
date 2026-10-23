package app

import (
	"io"
	"testing"
)

func TestMuteLogsDiscardsWithoutBreakingRedactor(t *testing.T) {
	application, err := New(Options{LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	application.MuteLogs()
	if application.Logger() == nil {
		t.Fatal("logger must stay usable after muting")
	}
	if application.Redactor() == nil {
		t.Fatal("redactor must stay usable after muting")
	}
	application.Logger().Info("muted log must not fail")
}

func TestMuteLogsNilSafe(t *testing.T) {
	var application *App
	application.MuteLogs()
	if application.Logger() == nil {
		t.Error("nil application must still return a usable discard logger")
	}
}
