package cli

import (
	"context"
	"io"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/tui"
)

// runDashboard launches the interactive dashboard. It is a package-level
// variable so the default no-args path can be tested without taking over a
// terminal.
var runDashboard = func(ctx context.Context, application *app.App) error {
	return tui.Run(ctx, application, tui.Options{})
}

func newTUICommand(state *commandState, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive ABOUEI monitoring dashboard",
		Long: `Open the interactive terminal dashboard for live backend monitoring.

The dashboard seeds from persisted status, then runs one continuous monitor:
every probe updates the screen without restarting the process. It shows
aggregate health, the navigable backend list, backend details, active
incidents, and this session's event timeline.

Keys: arrow keys or j/k navigate, enter opens details, 1-4 switch screens,
tab cycles screens, ? shows all shortcuts, esc goes back, q quits.

tui is human-only: --output json is rejected, and both stdin and stdout
must be interactive terminals. Scripts and CI should use check, status,
history, or monitor instead. Quitting shuts monitoring down gracefully
and exits 0; configuration problems exit 2.`,
		Example: `  abouei-checker tui
  abouei-checker tui --config ./abouei.yaml`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state.application == nil {
				return app.NewError(app.CategoryInternal, app.CodeInternal, "application is not initialized")
			}
			if state.output == outputJSON {
				return app.NewUsageError("tui does not support --output json; use check, status, or history for machine-readable output")
			}
			if !isInteractive(stdout) {
				return app.NewUsageError("tui requires an interactive terminal; use check, status, or history for scripts and pipes")
			}
			if _, err := state.application.LoadConfig(); err != nil {
				return err
			}
			// Per-probe log records would corrupt the fullscreen display;
			// outcomes remain available as live engine events.
			state.application.MuteLogs()
			if err := runDashboard(cmd.Context(), state.application); err != nil {
				return err
			}
			return nil
		},
	}
}

// isInteractive reports whether the dashboard can take over the terminal:
// stdout must be a TTY and stdin must be a terminal (not a pipe).
// It is a variable so tests can simulate an interactive terminal without
// allocating a real TTY.
var isInteractive = func(stdout io.Writer) bool {
	out, ok := stdout.(*os.File)
	if !ok || !isatty.IsTerminal(out.Fd()) {
		return false
	}
	return isatty.IsTerminal(os.Stdin.Fd())
}
