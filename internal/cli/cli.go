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
	"github.com/Erfan-Abouei/Abouei_Checker/internal/term"
)

const (
	commandName = "abouei-checker"

	flagConfig   = "config"
	flagLogLevel = "log-level"
	flagOutput   = "output"

	defaultLogLevel = "info"
	defaultInitPath = "abouei.yaml"
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
	command     string
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
		return reportError(err, stdout, stderr, state)
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
		Short: "ABOUEI backend health monitoring",
		Long: `ABOUEICHECKER — Backend Health Monitoring.

AboueiChecker periodically probes backend health endpoints, records
observations and incidents, and delivers configurable webhook notifications.

The version, init, validate, check, monitor, status, and history commands are
implemented. check and monitor persist threshold state, incidents, and
notification intent in SQLite across restarts; status and history read that
state read-only.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Default experience: launch the interactive TUI when stdout
			// and stdin are both terminals. In pipes, CI, JSON mode, or
			// any non-interactive context, show help with guidance
			// instead of taking over the screen.
			if state.output == outputJSON {
				if err := cmd.Help(); err != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write help output", err)
				}
				return nil
			}
			if isInteractive(stdout) {
				if state.application == nil {
					return app.NewError(app.CategoryInternal, app.CodeInternal, "application is not initialized")
				}
				if _, err := state.application.LoadConfig(); err != nil {
					return err
				}
				state.application.MuteLogs()
				if err := runDashboard(cmd.Context(), state.application); err != nil {
					return err
				}
				return nil
			}
			if err := cmd.Help(); err != nil {
				return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write help output", err)
			}
			_, _ = fmt.Fprintln(
				cmd.OutOrStdout(),
				"\nTip: run without arguments in an interactive terminal to open the live dashboard (or run `abouei-checker tui` explicitly).",
			)
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
		"path to the configuration file (default: discovery via ABOUEI_CONFIG, ./abouei.yaml, or the user config directory)",
	)
	flags.String(
		flagLogLevel,
		defaultLogLevel,
		"override configuration logging.level: debug, info, warn, or error (case-insensitive)",
	)
	flags.String(flagOutput, string(outputText), "output format: text or json")

	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		state.command = cmd.Name()

		configPath, err := cmd.Flags().GetString(flagConfig)
		if err != nil {
			return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to read --config", err)
		}
		logLevel, err := cmd.Flags().GetString(flagLogLevel)
		if err != nil {
			return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to read --log-level", err)
		}
		logLevelSet := cmd.Flags().Changed(flagLogLevel)
		outputRaw, err := cmd.Flags().GetString(flagOutput)
		if err != nil {
			return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to read --output", err)
		}
		output, err := parseOutputFormat(outputRaw)
		if err != nil {
			return app.NewUsageError(err.Error())
		}
		state.output = output

		application, err := newApp(app.Options{
			ConfigPath:  configPath,
			LogLevel:    logLevel,
			LogLevelSet: logLevelSet,
			LogWriter:   stderr,
		})
		if err != nil {
			return err
		}

		state.application = application
		return nil
	}

	root.AddCommand(newVersionCommand(state, stdout))
	root.AddCommand(newValidateCommand(state, stdout))
	root.AddCommand(newInitCommand(state, stdout))
	root.AddCommand(newCheckCommand(state, stdout))
	root.AddCommand(newMonitorCommand(state, stdout))
	root.AddCommand(newStatusCommand(state, stdout))
	root.AddCommand(newHistoryCommand(state, stdout))
	root.AddCommand(newTUICommand(state, stdout))
	root.SetHelpCommand(newHelpCommand(root))
	// Initialize Cobra's help and completion commands before Execute so the
	// unknown-command pre-check can find them. Cobra also calls these during
	// execution; repeated initialization is idempotent.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	applyBrandHelp(root, state)

	return root, state
}

// humanStyle returns the presentation style for human-readable output. JSON
// mode always uses a plain style so no banner or ANSI leaks into machine
// output; text mode detects TTY/NO_COLOR support from the writer.
func humanStyle(state *commandState, stdout io.Writer) term.Style {
	if state != nil && state.output == outputJSON {
		return term.Plain()
	}
	return term.ForWriter(stdout)
}

// applyBrandHelp prepends the compact ABOUEI banner to every help screen.
// Help is human-readable by definition, so the banner is shown even though
// per-command output stays concise. JSON envelopes are untouched because
// help never emits JSON.
func applyBrandHelp(root *cobra.Command, state *commandState) {
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if !isJSONRequested(cmd) {
			style := humanStyle(state, cmd.OutOrStdout())
			// Plain (piped/test) mode prints the same banner without ANSI:
			// recognizable, no escape sequences, and all "Usage:" markers
			// stay intact for existing tests and scripts.
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), style.Banner()); err == nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), style.Rule())
			}
		}
		defaultHelp(cmd, args)
	})
}

// isJSONRequested reports whether --output json was passed anywhere on the
// command line. Used only to keep help free of styling surprises in machine
// mode; command RunE functions use the parsed state.output instead.
func isJSONRequested(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if flag := cmd.Root().PersistentFlags().Lookup(flagOutput); flag != nil {
		return flag.Value.String() == string(outputJSON)
	}
	return false
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
				style := humanStyle(state, stdout)
				// Branded version presentation: compact banner, the
				// historical version line verbatim (keeps scripts/tests
				// matching), and a muted product descriptor.
				if _, err := fmt.Fprintln(stdout, style.Banner()); err != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write version output", err)
				}
				if _, err := fmt.Fprintf(
					stdout,
					"%s\n",
					style.Bold(term.VersionLineFallback(
						commandName,
						info.Version,
						info.Commit,
						info.BuildDate,
						info.GoVersion,
						info.Platform,
					)),
				); err != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write version output", err)
				}
				if _, err := fmt.Fprintln(stdout, style.Muted("Backend Health Monitoring · by Erfan Abouei")); err != nil {
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

func newValidateCommand(state *commandState, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Load and validate configuration without network or database access",
		Long: `Discover or load the selected configuration, resolve ${NAME} environment
references, apply documented defaults and per-backend inheritance, and run all
schema and cross-field validation.

validate never probes an endpoint, opens SQLite, or delivers notifications. It
prints a safe summary and never prints resolved header values, tokens, or the
configuration file.`,
		Example: `  abouei-checker validate
  abouei-checker validate --config ./abouei.yaml
  abouei-checker validate --config ./abouei.yaml --output json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state.application == nil {
				return app.NewError(app.CategoryInternal, app.CodeInternal, "application is not initialized")
			}

			summary, err := state.application.LoadConfig()
			if err != nil {
				return err
			}

			if state.output == outputJSON {
				payload := envelope{
					SchemaVersion: "1",
					Command:       "validate",
					GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
					Status:        "success",
					Data: validateData{
						Valid:         true,
						ConfigVersion: summary.Version,
						BackendCount:  len(summary.BackendIDs),
						BackendIDs:    summary.BackendIDs,
					},
				}
				if err := writeJSON(stdout, payload); err != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write JSON validate output", err)
				}
			} else {
				style := humanStyle(state, stdout)
				// Keep the historical first line verbatim; only the
				// "configuration is valid" phrase carries success color in
				// TTY mode, so plain output stays byte-identical.
				if _, err := fmt.Fprintf(
					stdout,
					"%s (schema version %d, %d backends)\n",
					style.Success("configuration is valid"),
					summary.Version,
					len(summary.BackendIDs),
				); err != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write validate output", err)
				}
				for _, id := range summary.BackendIDs {
					if _, err := fmt.Fprintf(stdout, "  %s %s\n", style.Muted("-"), style.Bold(style.Fit(id, 64))); err != nil {
						return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write validate output", err)
					}
				}
			}

			state.application.Logger().DebugContext(
				cmd.Context(),
				"configuration validated",
				"component", "cli",
				"command", "validate",
				"operation", "validate_configuration",
				"backend_count", len(summary.BackendIDs),
				"config_schema_version", summary.Version,
			)
			return nil
		},
	}
}

func newInitCommand(state *commandState, stdout io.Writer) *cobra.Command {
	var force bool

	command := &cobra.Command{
		Use:   "init [PATH]",
		Short: "Write a commented configuration template",
		Long: `Write a commented configuration template to PATH, which defaults to
./abouei.yaml. Existing files are not overwritten unless --force is supplied.

init does not read configuration, resolve environment references, probe
endpoints, or create a database. New files use owner-only permissions where the
platform supports them.`,
		Example: `  abouei-checker init
  abouei-checker init ./config/abouei.yaml
  abouei-checker init ./abouei.yaml --force`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if state.application == nil {
				return app.NewError(app.CategoryInternal, app.CodeInternal, "application is not initialized")
			}

			path := defaultInitPath
			if len(args) == 1 {
				path = args[0]
			}

			overwritten, err := state.application.WriteConfigTemplate(path, force)
			if err != nil {
				return err
			}

			if state.output == outputJSON {
				payload := envelope{
					SchemaVersion: "1",
					Command:       "init",
					GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
					Status:        "success",
					Data: initData{
						Path:        path,
						Overwritten: overwritten,
					},
				}
				if err := writeJSON(stdout, payload); err != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write JSON init output", err)
				}
				return nil
			}

			verb := "created"
			if overwritten {
				verb = "overwrote"
			}
			style := humanStyle(state, stdout)
			if _, err := fmt.Fprintf(stdout, "%s %s\n", style.Success(verb), style.Bold(style.Fit(path, 120))); err != nil {
				return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write init output", err)
			}
			if _, err := fmt.Fprintln(stdout, style.Muted("Next: edit the template, then run abouei-checker validate")); err != nil {
				return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write init output", err)
			}
			return nil
		},
	}

	command.Flags().BoolVar(&force, "force", false, "overwrite an existing configuration file")
	return command
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

func reportError(err error, stdout, stderr io.Writer, state *commandState) int {
	redact := func(message string) string {
		if message == "" {
			message = "internal error"
		}
		if state != nil && state.application != nil {
			message = state.application.Redactor().RedactString(message)
		}
		return message
	}
	// Errors are human-readable diagnostics on stderr: style only the
	// "Error:" lead so the safe message stays greppable and redaction
	// boundaries are unchanged. JSON mode still emits a pure envelope.
	style := term.ForWriter(stderr)
	if _, alreadyWritten := asOutputWritten(err); alreadyWritten {
		_, _ = fmt.Fprintln(stderr, style.ErrorLine(redact(app.SafeMessage(err))))
		return ExitCodeFor(err)
	}
	message := redact(app.SafeMessage(err))

	if state != nil && state.output == outputJSON {
		payload := envelope{
			SchemaVersion: "1",
			Command:       state.command,
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
			Status:        "error",
			Data:          nil,
			Error:         errorObjectFor(err, message),
		}
		_ = writeJSON(stdout, payload)
	}

	_, _ = fmt.Fprintln(stderr, style.ErrorLine(message))
	return ExitCodeFor(err)
}

func errorObjectFor(err error, message string) errorObject {
	object := errorObject{
		Code:    string(app.CodeOf(err)),
		Message: message,
	}
	var appErr *app.Error
	if errors.As(err, &appErr) {
		object.Details = appErr.Details
	}
	return object
}

type envelope struct {
	SchemaVersion string `json:"schema_version"`
	Command       string `json:"command"`
	GeneratedAt   string `json:"generated_at"`
	Status        string `json:"status"`
	Data          any    `json:"data"`
	Error         any    `json:"error"`
}

type errorObject struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Details []app.Detail `json:"details,omitempty"`
}

type versionData struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

type validateData struct {
	Valid         bool     `json:"valid"`
	ConfigVersion int      `json:"config_version"`
	BackendCount  int      `json:"backend_count"`
	BackendIDs    []string `json:"backend_ids"`
}

type initData struct {
	Path        string `json:"path"`
	Overwritten bool   `json:"overwritten"`
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode JSON output: %w", err)
	}
	return nil
}
