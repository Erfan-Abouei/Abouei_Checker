package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/buildinfo"
)

func setTestBuildInfo(t *testing.T) {
	t.Helper()

	originalVersion := buildinfo.Version
	originalCommit := buildinfo.Commit
	originalBuildDate := buildinfo.BuildDate
	t.Cleanup(func() {
		buildinfo.Version = originalVersion
		buildinfo.Commit = originalCommit
		buildinfo.BuildDate = originalBuildDate
	})

	buildinfo.Version = "1.2.3-test"
	buildinfo.Commit = "abc1234"
	buildinfo.BuildDate = "2026-10-22T00:00:00Z"
}

func executeForTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute(context.Background(), args, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestVersionText(t *testing.T) {
	setTestBuildInfo(t)

	code, stdout, stderr := executeForTest(t, "version")

	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{"abouei-checker 1.2.3-test", "commit abc1234", "built 2026-10-22T00:00:00Z"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestVersionJSON(t *testing.T) {
	setTestBuildInfo(t)

	code, stdout, stderr := executeForTest(t, "version", "--output", "json")

	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	var got struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		GeneratedAt   string `json:"generated_at"`
		Status        string `json:"status"`
		Data          struct {
			Name      string `json:"name"`
			Version   string `json:"version"`
			Commit    string `json:"commit"`
			BuildDate string `json:"build_date"`
			GoVersion string `json:"go_version"`
			Platform  string `json:"platform"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}

	if got.SchemaVersion != "1" {
		t.Errorf("schema_version = %q, want %q", got.SchemaVersion, "1")
	}
	if got.Command != "version" {
		t.Errorf("command = %q, want %q", got.Command, "version")
	}
	if got.Status != "success" {
		t.Errorf("status = %q, want %q", got.Status, "success")
	}
	if _, err := time.Parse(time.RFC3339, got.GeneratedAt); err != nil {
		t.Errorf("generated_at = %q is not RFC3339: %v", got.GeneratedAt, err)
	}
	if got.Data.Name != commandName {
		t.Errorf("data.name = %q, want %q", got.Data.Name, commandName)
	}
	if got.Data.Version != "1.2.3-test" {
		t.Errorf("data.version = %q, want %q", got.Data.Version, "1.2.3-test")
	}
	if got.Data.Commit != "abc1234" {
		t.Errorf("data.commit = %q, want %q", got.Data.Commit, "abc1234")
	}
	if got.Data.GoVersion == "" || got.Data.Platform == "" {
		t.Errorf("data = %+v, want non-empty go_version and platform", got.Data)
	}
	if got.Error != nil {
		t.Errorf("error = %v, want null", got.Error)
	}
}

func TestRootWithoutArgumentsShowsHelp(t *testing.T) {
	code, stdout, stderr := executeForTest(t)

	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "version") {
		t.Errorf("stdout = %q, want command help", stdout)
	}
}

func TestUnknownCommandFailsWithUsageExitCode(t *testing.T) {
	code, stdout, stderr := executeForTest(t, "does-not-exist")

	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("stderr = %q, want an unknown-command error", stderr)
	}
}

func TestInvalidOutputFormatFailsWithUsageExitCode(t *testing.T) {
	code, stdout, stderr := executeForTest(t, "version", "--output", "yaml")

	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "invalid --output") {
		t.Errorf("stderr = %q, want an invalid-output error", stderr)
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	code, _, stderr := executeForTest(t, "version", "extra")

	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "unknown command") && !strings.Contains(stderr, "accepts 0 arg") {
		t.Errorf("stderr = %q, want an argument validation error", stderr)
	}
}

type failingWriter struct{}

func (failingWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("synthetic write failure")
}

func TestOutputWriteFailureIsInternalError(t *testing.T) {
	var stderr bytes.Buffer
	code := Execute(context.Background(), []string{"version"}, failingWriter{}, &stderr)

	if code != ExitInternal {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitInternal, stderr.String())
	}
	if !strings.Contains(stderr.String(), "synthetic write failure") {
		t.Errorf("stderr = %q, want the write failure", stderr.String())
	}
}

func TestJSONOutputWriteFailureIsInternalError(t *testing.T) {
	var stderr bytes.Buffer
	code := Execute(context.Background(), []string{"version", "--output", "json"}, failingWriter{}, &stderr)

	if code != ExitInternal {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitInternal, stderr.String())
	}
}

func TestNewRootCommandWritesHelpToProvidedStreams(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root := NewRootCommand(&stdout, &stderr)

	if root.Name() != commandName {
		t.Errorf("root.Name() = %q, want %q", root.Name(), commandName)
	}
	if root.OutOrStdout() != io.Writer(&stdout) {
		t.Error("root stdout is not the provided writer")
	}
	if root.ErrOrStderr() != io.Writer(&stderr) {
		t.Error("root stderr is not the provided writer")
	}
}
