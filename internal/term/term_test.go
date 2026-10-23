package term

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestPlainNeverEmitsANSI(t *testing.T) {
	s := Plain()
	for _, got := range []string{
		s.Bold("api"),
		s.Muted("x"),
		s.Success("healthy"),
		s.Danger("down"),
		s.Warning("unknown"),
		s.Accent("|"),
		s.Status("healthy"),
		s.Banner(),
		s.ErrorLine("boom"),
	} {
		if strings.Contains(got, "\x1b[") {
			t.Fatalf("plain style emitted ANSI: %q", got)
		}
	}
}

func TestEnabledStyleEmitsANSIButPreservesWords(t *testing.T) {
	s := ForTesting(true, 80)
	for _, word := range []string{"healthy", "down", "unknown", "orphaned", "recovered", "open"} {
		got := s.Status(word)
		if !strings.Contains(got, word) {
			t.Errorf("styled status %q lost the word", got)
		}
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("enabled style has no ANSI: %q", got)
		}
	}
}

func TestForWriterDisablesColorForBuffers(t *testing.T) {
	var buf bytes.Buffer
	s := ForWriter(&buf)
	if s.Enabled {
		t.Error("bytes.Buffer must never enable color (tests/pipes stay plain)")
	}
}

func TestNoColorDisablesColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "1")
	var buf bytes.Buffer
	if got := ForWriter(&buf); got.Enabled {
		t.Errorf("NO_COLOR must win over CLICOLOR_FORCE, got enabled=true")
	}
}

func TestForceEnablesColorForPreview(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if got := ForWriter(&buf); !got.Enabled {
		t.Errorf("CLICOLOR_FORCE should enable color for previews")
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("hello", 0); got != "hello" {
		t.Errorf("got %q", got)
	}
	if got := Truncate("hello world", 8); got != "hello..." {
		t.Errorf("got %q", got)
	}
	if got := Truncate("api", 10); got != "api" {
		t.Errorf("got %q", got)
	}
}

func TestBannerCollapsesOnNarrow(t *testing.T) {
	wide := ForTesting(false, 80).Banner()
	if !strings.Contains(wide, BrandName) || !strings.Contains(wide, ProductName) || !strings.Contains(wide, Descriptor) {
		t.Errorf("wide banner missing brand: %q", wide)
	}
	if strings.Count(wide, "\n") < 1 {
		t.Errorf("wide banner should be two lines: %q", wide)
	}
	narrow := ForTesting(false, 30).Banner()
	if strings.Count(narrow, "\n") != 0 {
		t.Errorf("narrow banner should collapse to one line: %q", narrow)
	}
	if !strings.Contains(narrow, BrandName) {
		t.Errorf("narrow banner lost brand: %q", narrow)
	}
}

func TestErrorLineKeepsShape(t *testing.T) {
	plain := ForTesting(false, 80).ErrorLine("boom")
	if plain != "Error: boom" {
		t.Errorf("got %q", plain)
	}
	styled := ForTesting(true, 80).ErrorLine("boom")
	if !strings.Contains(styled, "Error:") || !strings.Contains(styled, "boom") {
		t.Errorf("got %q", styled)
	}
}

func TestWidthEnv(t *testing.T) {
	t.Setenv("ABOUEI_WIDTH", "40")
	t.Setenv("COLUMNS", "120")
	if got := detectWidth(); got != 40 {
		t.Errorf("ABOUEI_WIDTH should win, got %d", got)
	}
	_ = os.Unsetenv("ABOUEI_WIDTH")
}
