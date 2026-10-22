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
	"time"

	"github.com/spf13/cobra"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/buildinfo"
)

const commandName = "abouei-checker"

// errInternal marks failures that are not caused by invalid usage.
var errInternal = errors.New("internal error")

type outputFormat string

const (
	outputText outputFormat = "text"
	outputJSON outputFormat = "json"
)

// Execute runs the CLI with the given arguments and returns a process exit
// code. It never calls os.Exit, which keeps command behavior testable.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := NewRootCommand(stdout, stderr)
	root.SetArgs(args)

	if err := root.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		if errors.Is(err, errInternal) {
			return ExitInternal
		}
		return ExitUsage
	}

	return ExitSuccess
}

// NewRootCommand builds the CLI command tree.
func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:   commandName,
		Short: "Monitor backend health endpoints and notify on incidents",
		Long: `AboueiChecker periodically probes backend health endpoints, records
observations and incidents, and delivers configurable webhook notifications.

Only "version" is implemented in the Phase 0 foundation. The operational
commands are specified in docs/cli-contract.md and will be implemented in
later phases.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	root.SetOut(stdout)
	root.SetErr(stderr)
	root.PersistentFlags().String("output", string(outputText), "output format: text or json")

	root.AddCommand(newVersionCommand(stdout))

	return root
}

func newVersionCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormatFrom(cmd)
			if err != nil {
				return err
			}

			info := buildinfo.Current()

			if format == outputJSON {
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
					return err
				}
				return nil
			}

			_, err = fmt.Fprintf(
				stdout,
				"%s %s (commit %s, built %s, %s %s)\n",
				commandName,
				info.Version,
				info.Commit,
				info.BuildDate,
				info.GoVersion,
				info.Platform,
			)
			if err != nil {
				return fmt.Errorf("%w: write version output: %w", errInternal, err)
			}

			return nil
		},
	}
}

func outputFormatFrom(cmd *cobra.Command) (outputFormat, error) {
	raw, err := cmd.Flags().GetString("output")
	if err != nil {
		return "", fmt.Errorf("%w: read --output: %w", errInternal, err)
	}

	switch outputFormat(raw) {
	case outputText, outputJSON:
		return outputFormat(raw), nil
	default:
		return "", fmt.Errorf("invalid --output value %q: expected %q or %q", raw, outputText, outputJSON)
	}
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
		return fmt.Errorf("%w: write JSON output: %w", errInternal, err)
	}
	return nil
}
