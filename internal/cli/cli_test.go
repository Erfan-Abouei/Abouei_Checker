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

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
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

func executeWithFactoryForTest(
	t *testing.T,
	factory appFactory,
	args ...string,
) (int, string, string) {
	t.Helper()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWith(context.Background(), args, &stdout, &stderr, factory)

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

func TestVersionWithUnspecifiedBuildMetadata(t *testing.T) {
	setTestBuildInfo(t)
	buildinfo.Version = ""
	buildinfo.Commit = ""
	buildinfo.BuildDate = ""

	code, stdout, stderr := executeForTest(t, "version")
	if code != ExitSuccess {
		t.Fatalf("text exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if !strings.Contains(stdout, commandName) {
		t.Errorf("text stdout = %q, want the command name", stdout)
	}

	code, stdout, stderr = executeForTest(t, "version", "--output", "json")
	if code != ExitSuccess {
		t.Fatalf("JSON exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}

	var got struct {
		Data struct {
			Version   string `json:"version"`
			Commit    string `json:"commit"`
			BuildDate string `json:"build_date"`
			GoVersion string `json:"go_version"`
			Platform  string `json:"platform"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.Data.Version != "" || got.Data.Commit != "" || got.Data.BuildDate != "" {
		t.Errorf("data = %+v, want empty unspecified metadata", got.Data)
	}
	if got.Data.GoVersion == "" || got.Data.Platform == "" {
		t.Errorf("data = %+v, want runtime metadata", got.Data)
	}
}

func TestGlobalFlagDefaults(t *testing.T) {
	root := NewRootCommand(io.Discard, io.Discard)

	tests := map[string]string{
		flagConfig:   "",
		flagLogLevel: "info",
		flagOutput:   "text",
	}
	for name, want := range tests {
		flag := root.PersistentFlags().Lookup(name)
		if flag == nil {
			t.Fatalf("persistent flag %q is missing", name)
		}
		if flag.DefValue != want {
			t.Errorf("--%s default = %q, want %q", name, flag.DefValue, want)
		}
	}
}

func TestGlobalFlagsAreInheritedAndParsed(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "flags before command",
			args: []string{
				"--config", "/tmp/example.yaml",
				"--log-level", "DEBUG",
				"--output", "json",
				"version",
			},
		},
		{
			name: "flags after command",
			args: []string{
				"version",
				"--config", "/tmp/example.yaml",
				"--log-level", "DEBUG",
				"--output", "json",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured app.Options
			var stderrUsed io.Writer
			factory := func(options app.Options) (*app.App, error) {
				captured = options
				stderrUsed = options.LogWriter
				return app.New(options)
			}

			code, stdout, stderr := executeWithFactoryForTest(t, factory, tt.args...)
			if code != ExitSuccess {
				t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
			}
			if captured.ConfigPath != "/tmp/example.yaml" {
				t.Errorf("ConfigPath = %q, want the parsed path", captured.ConfigPath)
			}
			if captured.LogLevel != "DEBUG" {
				t.Errorf("LogLevel = %q, want DEBUG", captured.LogLevel)
			}
			if stderrUsed == nil {
				t.Error("LogWriter was not provided to the application")
			}
			if !json.Valid([]byte(stdout)) {
				t.Errorf("stdout is not valid JSON:\n%s", stdout)
			}
		})
	}
}

func TestConfigPathIsAcceptedWithoutReading(t *testing.T) {
	tests := []string{
		"/definitely/not/a/real/config.yaml",
		t.TempDir(),
	}

	for _, path := range tests {
		code, stdout, stderr := executeForTest(t, "version", "--config", path)
		if code != ExitSuccess {
			t.Errorf("--config %q exit code = %d, want %d; stderr = %q", path, code, ExitSuccess, stderr)
		}
		if !strings.Contains(stdout, commandName) {
			t.Errorf("--config %q stdout = %q, want version output", path, stdout)
		}
		if stderr != "" {
			t.Errorf("--config %q stderr = %q, want empty", path, stderr)
		}
	}
}

func TestValidLogLevelsAreCaseInsensitive(t *testing.T) {
	for _, level := range []string{"DEBUG", "INFO", "WARN", "ERROR", "debug", "Info"} {
		code, _, stderr := executeForTest(t, "version", "--log-level", level)
		if code != ExitSuccess {
			t.Errorf("--log-level %q exit code = %d, want %d; stderr = %q", level, code, ExitSuccess, stderr)
		}
	}
}

func TestInvalidLogLevelFailsWithUsageExitCode(t *testing.T) {
	tests := [][]string{
		{"version", "--log-level", "verbose"},
		{"--log-level", "verbose", "version"},
		{"--log-level", " info", "version"},
	}

	for _, args := range tests {
		code, stdout, stderr := executeForTest(t, args...)
		if code != ExitUsage {
			t.Errorf("args %v exit code = %d, want %d", args, code, ExitUsage)
		}
		if stdout != "" {
			t.Errorf("args %v stdout = %q, want empty", args, stdout)
		}
		if !strings.Contains(stderr, "invalid log level") {
			t.Errorf("args %v stderr = %q, want an invalid-level error", args, stderr)
		}
	}
}

func TestInvalidOutputFormatFailsWithUsageExitCode(t *testing.T) {
	tests := [][]string{
		{"version", "--output", "yaml"},
		{"--output", "yaml"},
	}

	for _, args := range tests {
		code, stdout, stderr := executeForTest(t, args...)
		if code != ExitUsage {
			t.Errorf("args %v exit code = %d, want %d", args, code, ExitUsage)
		}
		if stdout != "" {
			t.Errorf("args %v stdout = %q, want empty", args, stdout)
		}
		if !strings.Contains(stderr, "invalid --output") {
			t.Errorf("args %v stderr = %q, want an invalid-output error", args, stderr)
		}
	}
}

func TestRootWithoutArgumentsShowsHelp(t *testing.T) {
	code, stdout, stderr := executeForTest(t)

	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	for _, want := range []string{"Usage:", "version", "--config", "--log-level", "--output"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestVersionHelpShowsGlobalFlagsAndExamples(t *testing.T) {
	code, stdout, stderr := executeForTest(t, "version", "--help")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	for _, want := range []string{"Examples:", "--output json", "--config", "--log-level"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestHelpCommandPrintsSubcommandHelp(t *testing.T) {
	code, stdout, stderr := executeForTest(t, "help", "version")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if !strings.Contains(stdout, "Print the application name") {
		t.Errorf("stdout = %q, want version help", stdout)
	}
	if !strings.Contains(stdout, "-h, --help") {
		t.Errorf("stdout = %q, want the help flag", stdout)
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

func TestUnknownHelpTopicFailsWithUsageExitCode(t *testing.T) {
	code, stdout, stderr := executeForTest(t, "help", "does-not-exist")

	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "unknown help topic") {
		t.Errorf("stderr = %q, want an unknown-help-topic error", stderr)
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	code, _, stderr := executeForTest(t, "version", "extra")

	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "accepts no arguments") {
		t.Errorf("stderr = %q, want an argument validation error", stderr)
	}
}

func TestUnknownFlagFailsWithUsageExitCode(t *testing.T) {
	code, stdout, stderr := executeForTest(t, "version", "--not-a-flag")

	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "unknown flag") {
		t.Errorf("stderr = %q, want an unknown-flag error", stderr)
	}
}

func TestMissingFlagValueFailsWithUsageExitCode(t *testing.T) {
	for _, args := range [][]string{
		{"version", "--config"},
		{"--log-level"},
	} {
		code, stdout, stderr := executeForTest(t, args...)
		if code != ExitUsage {
			t.Errorf("args %v exit code = %d, want %d", args, code, ExitUsage)
		}
		if stdout != "" {
			t.Errorf("args %v stdout = %q, want empty", args, stdout)
		}
		if !strings.Contains(stderr, "flag needs an argument") {
			t.Errorf("args %v stderr = %q, want a missing-value error", args, stderr)
		}
		if strings.Contains(stderr, "unknown command") {
			t.Errorf("args %v stderr = %q, missing value misreported as unknown command", args, stderr)
		}
	}
}

func TestErrorsArePrintedOnceWithoutUsage(t *testing.T) {
	tests := [][]string{
		{"does-not-exist"},
		{"version", "--not-a-flag"},
		{"version", "extra"},
		{"version", "--log-level", "verbose"},
		{"version", "--output", "yaml"},
		{"version", "--config"},
		{"help", "does-not-exist"},
	}

	for _, args := range tests {
		code, _, stderr := executeForTest(t, args...)
		if code == ExitSuccess {
			t.Errorf("args %v exit code = %d, want failure", args, code)
		}
		if got := strings.Count(stderr, "Error:"); got != 1 {
			t.Errorf("args %v stderr = %q, want exactly one Error line", args, stderr)
		}
		if strings.Contains(stderr, "Usage:") {
			t.Errorf("args %v stderr = %q, want no usage dump", args, stderr)
		}
	}
}

func TestDebugLogsGoToStderrAndKeepJSONStdoutValid(t *testing.T) {
	setTestBuildInfo(t)

	code, stdout, stderr := executeForTest(t, "version", "--output", "json", "--log-level", "DEBUG")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Errorf("stdout is not valid JSON:\n%s", stdout)
	}
	if strings.Contains(stdout, "level=DEBUG") || strings.Contains(stdout, "version command completed") {
		t.Errorf("stdout was contaminated by logs:\n%s", stdout)
	}
	for _, want := range []string{"level=DEBUG", "command=version", "component=cli"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want debug log containing %q", stderr, want)
		}
	}
}

func TestDebugLogsDoNotContaminateTextStdout(t *testing.T) {
	setTestBuildInfo(t)

	code, stdout, stderr := executeForTest(t, "version", "--log-level", "debug")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if strings.Contains(stdout, "level=") {
		t.Errorf("stdout was contaminated by logs:\n%s", stdout)
	}
	if !strings.Contains(stderr, "level=DEBUG") {
		t.Errorf("stderr = %q, want a debug log", stderr)
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
	if !strings.Contains(stderr.String(), "failed to write version output") {
		t.Errorf("stderr = %q, want the safe write error", stderr.String())
	}
	if strings.Contains(stderr.String(), "synthetic write failure") {
		t.Errorf("stderr = %q, want the internal cause hidden", stderr.String())
	}
}

func TestJSONOutputWriteFailureIsInternalError(t *testing.T) {
	var stderr bytes.Buffer
	code := Execute(context.Background(), []string{"version", "--output", "json"}, failingWriter{}, &stderr)

	if code != ExitInternal {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitInternal, stderr.String())
	}
	if !strings.Contains(stderr.String(), "failed to write JSON version output") {
		t.Errorf("stderr = %q, want the safe JSON write error", stderr.String())
	}
}

func TestApplicationConstructionFailureIsSafe(t *testing.T) {
	factory := func(app.Options) (*app.App, error) {
		return nil, app.WrapError(
			app.CategoryInternal,
			app.CodeInternal,
			"failed to configure application",
			errors.New("token=super-secret-value"),
		)
	}

	code, stdout, stderr := executeWithFactoryForTest(t, factory, "version")

	if code != ExitInternal {
		t.Fatalf("exit code = %d, want %d", code, ExitInternal)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "failed to configure application") {
		t.Errorf("stderr = %q, want the safe construction error", stderr)
	}
	if strings.Contains(stderr, "super-secret-value") {
		t.Errorf("stderr = %q, want the cause hidden", stderr)
	}
	if got := strings.Count(stderr, "Error:"); got != 1 {
		t.Errorf("stderr = %q, want exactly one error line", stderr)
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
