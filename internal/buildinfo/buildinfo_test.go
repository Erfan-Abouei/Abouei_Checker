package buildinfo

import (
	"strings"
	"testing"
)

func TestCurrentReportsCompileTimeMetadata(t *testing.T) {
	t.Parallel()

	info := Current()

	if info.Version == "" {
		t.Error("Version is empty")
	}
	if info.Commit == "" {
		t.Error("Commit is empty")
	}
	if info.BuildDate == "" {
		t.Error("BuildDate is empty")
	}
	if !strings.HasPrefix(info.GoVersion, "go") {
		t.Errorf("GoVersion = %q, want a value starting with %q", info.GoVersion, "go")
	}
	if !strings.Contains(info.Platform, "/") {
		t.Errorf("Platform = %q, want GOOS/GOARCH", info.Platform)
	}
}

func TestDevelopmentDefaults(t *testing.T) {
	t.Parallel()

	if Version != "dev" {
		t.Errorf("Version default = %q, want dev", Version)
	}
	if Commit != "none" {
		t.Errorf("Commit default = %q, want none", Commit)
	}
	if BuildDate != "unknown" {
		t.Errorf("BuildDate default = %q, want unknown", BuildDate)
	}
}
