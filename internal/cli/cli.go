// Package cli implements the command-line interface and translates command
// results into process exit codes. It is the only package that depends on the
// command framework.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
)

const (
	commandName = "abouei-checker"

	flagConfig   = "config"
	flagLogLevel = "log-level"
	flagOutput   = "output"

	defaultLogLevel = "info"
)

type outputFormat string

const (
	outputText outputFormat = "text"
	outputJSON outputFormat = "json"
)

type appFactory func(app.Options) (*app.App, error)

// commandState carries values parsed by the root persistent pre-run into the
// command implementations. It is scoped to one command tree and is not global
// mutable state.
type commandState struct {
	application *app.App
	output      outputFormat
}

// Execute runs the CLI with the given arguments and returns a process exit
// code. It never calls os.Exit, which keeps command behavior testable.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return executeWith(ctx, args, stdout, stderr, app.New)
}

// executeWith is the test seam for application-construction failures.
func executeWith(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	newApp appFactory,
) int {
	root, state := newRootCommand(stdout, stderr, newApp)
	root.SetArgs(args)

	if err := root.ExecuteContext(ctx); err != nil {
		// Command implementations return *app.Error for every failure they
		// originate. Remaining errors come from Cobra's parse/validation layer
		// (unknown command, flag, or argument), which is invalid usage.
		var appErr *app.Error
		if !errors.As(err, &appErr) {
			err = app.WrapError(app.CategoryUsage, app.CodeUsageInvalid, err.Error(), err)
		}
		return reportError(err, stderr, state)
	}

	return ExitSuccess
}

// NewRootCommand builds the CLI command tree.
func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	root, _ := newRootCommand(stdout, stderr, app.New)
	return root
}

func newRootCommand(stdout, stderr io.Writer, newApp appFactory) (*cobra.Command, *commandState) {
	state := &commandState{output: outputText}

	root := &cobra.Command{
		Use:   commandName,
		Short: "Monitor backend health endpoints and notify on incidents",
		Long: `AboueiChecker periodically probes backend health endpoints, records
observations and incidents, and delivers configurable webhook notifications.

Only "version" is implemented. The global --config and --log-level flags are
accepted by every command, but configuration loading and the operational
commands remain planned.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmd.Help(); err != nil {
				return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write help output", err)
			}
			return nil
		},
	}

	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return app.WrapError(app.CategoryUsage, app.CodeUsageInvalid, err.Error(), err)
	})

	flags := root.PersistentFlags()
	flags.String(
		flagConfig,
		"",
		"path to the configuration file (accepted but not loaded in this phase)",
	)
	flags.String(
		flagLogLevel,
		defaultLogLevel,
		"log level: debug, info, warn, or error (case-insensitive)",
	)
	flags.String(flagOutput, string(outputText), "output format: text or json")

	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		configPath, err := cmd.Flags().GetString(flagConfig)
		if err != nil {
			return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to read --config", err)
		}
		logLevel, err := cmd.Flags().GetString(flagLogLevel)
		if err != nil {
			return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to read --log-level", err)
		}
		outputRaw, err := cmd.Flags().GetString(flagOutput)
		if err != nil {
			return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to read --output", err)
		}
		output, err := parseOutputFormat(outputRaw)
		if err != nil {
			return app.NewUsageError(err.Error())
		}

		application, err := newApp(app.Options{
			ConfigPath: configPath,
			LogLevel:   logLevel,
			LogWriter:  stderr,
		})
		if err != nil {
			return err
		}

		state.application = application
		state.output = output
		return nil
	}

	root.AddCommand(newVersionCommand(state, stdout))
	root.SetHelpCommand(newHelpCommand(root))
	// Initialize Cobra's help and completion commands before Execute so the
	// unknown-command pre-check can find them. Cobra also calls these during
	// execution; repeated initialization is idempotent.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()

	return root, state
}

func newVersionCommand(state *commandState, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build information",
		Long: `Print the application name, version, commit, build date, Go version, and
platform. Build metadata is injected at link time and falls back to development
defaults when it is not supplied.`,
		Example: `  abouei-checker version
  abouei-checker version --output json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state.application == nil {
				return app.NewError(app.CategoryInternal, app.CodeInternal, "application is not initialized")
			}

			info := state.application.BuildInfo()

			if state.output == outputJSON {
				payload := envelope{
					SchemaVersion: "1",
					Command:       "version",
					GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
					Status:        "success",
					Data: versionData{
						Name:      commandName,
						Version:   info.Version,
						Commit:    info.Commit,
						BuildDate: info.BuildDate,
						GoVersion: info.GoVersion,
						Platform:  info.Platform,
					},
				}
				if err := writeJSON(stdout, payload); err != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write JSON version output", err)
				}
			} else {
				if _, err := fmt.Fprintf(
					stdout,
					"%s %s (commit %s, built %s, %s %s)\n",
					commandName,
					info.Version,
					info.Commit,
					info.BuildDate,
					info.GoVersion,
					info.Platform,
				); err != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write version output", err)
				}
			}

			state.application.Logger().DebugContext(
				cmd.Context(),
				"version command completed",
				"component", "cli",
				"command", "version",
				"operation", "version",
			)
			return nil
		},
	}
}

func newHelpCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		Long:  "Show help for the root command or a specific command.",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			target, _, err := root.Find(args)
			if err != nil || target == nil {
				return app.NewUsageError(fmt.Sprintf("unknown help topic %q", strings.Join(args, " ")))
			}
			target.InitDefaultHelpFlag()
			if err := target.Help(); err != nil {
				return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write help output", err)
			}
			return nil
		},
	}
}

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return app.NewUsageError(fmt.Sprintf("%q accepts no arguments, received %d", cmd.CommandPath(), len(args)))
}

func parseOutputFormat(raw string) (outputFormat, error) {
	switch outputFormat(raw) {
	case outputText, outputJSON:
		return outputFormat(raw), nil
	default:
		return "", fmt.Errorf("invalid --output value %q: expected %q or %q", raw, outputText, outputJSON)
	}
}

func reportError(err error, stderr io.Writer, state *commandState) int {
	message := app.SafeMessage(err)
	if message == "" {
		message = "internal error"
	}
	if state != nil && state.application != nil {
		message = state.application.Redactor().RedactString(message)
	}
	_, _ = fmt.Fprintf(stderr, "Error: %s\n", message)
	return ExitCodeFor(err)
}

type envelope struct {
	SchemaVersion string `json:"schema_version"`
	Command       string `json:"command"`
	GeneratedAt   string `json:"generated_at"`
	Status        string `json:"status"`
	Data          any    `json:"data"`
	Error         any    `json:"error"`
}

type versionData struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode JSON output: %w", err)
	}
	return nil
}
