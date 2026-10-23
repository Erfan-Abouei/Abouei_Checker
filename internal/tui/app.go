package tui

import (
	"context"
	"io"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
)

// eventBuffer sizes the non-blocking bridge between the engine goroutine
// and the Elm update loop. The UI never blocks a probe: on overflow the
// oldest queued event is dropped because a newer event for the same backend
// supersedes it.
const eventBuffer = 64

// Options configures a TUI run. Input/Output override the terminal streams
// (used by tests); nil selects the process stdin/stdout.
type Options struct {
	Input  io.Reader
	Output io.Writer
}

// Run launches the interactive dashboard. It seeds from a Status snapshot,
// then runs exactly one app.Monitor loop; engine messages reach the Elm
// update loop through a bounded channel. Shutdown is graceful: quitting the
// UI cancels monitoring and waits for the engine, mirroring the `monitor`
// command contract.
func Run(ctx context.Context, application *app.App, options Options) error {
	if application == nil || application.Config() == nil {
		return app.NewError(app.CategoryInternal, app.CodeInternal, "configuration is not loaded")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cfg := application.Config()
	order := append([]string(nil), cfg.BackendIDs()...)
	names := make(map[string]string, len(cfg.Backends))
	for _, backend := range cfg.Backends {
		names[backend.ID] = backend.Name
	}
	start := time.Now().UTC()
	model := NewModel(NewTheme(), order, names, start)
	model.now = start

	// Seed from persisted state before the first session probe lands. A
	// missing database (fresh setup) is not fatal: every backend simply
	// starts as pending and the first probe cycle fills the screen.
	if report, err := application.Status(ctx, ""); err == nil {
		applySeed(&model, report)
		model.clamp()
	}

	events := make(chan tea.Msg, eventBuffer)
	stopped := make(chan error, 1)
	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	go func() {
		err := application.Monitor(monitorCtx, app.MonitorOptions{
			OnEvent: func(event app.MonitorEvent) {
				select {
				case events <- probeMsg{event: event}:
				default:
					// Never block the engine: drop the oldest queued
					// event; a newer event supersedes it.
					select {
					case <-events:
					default:
					}
					select {
					case events <- probeMsg{event: event}:
					default:
					}
				}
			},
		})
		// Surface termination to the UI through the same channel the update
		// loop already drains, then report the outcome to Run. stopped has
		// exactly one receiver, so the outcome is never lost to a race.
		select {
		case events <- feedEndMsg{err: err}:
		default:
		}
		stopped <- err
	}()
	model = model.WithFeed(events)

	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if options.Input != nil {
		programOptions = append(programOptions, tea.WithInput(options.Input))
	}
	if options.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(options.Output))
	}
	program := tea.NewProgram(model, programOptions...)

	_, err := program.Run()
	stopMonitor()
	monitorErr := <-stopped
	if err != nil {
		return app.WrapError(app.CategoryInternal, app.CodeInternal, "interactive dashboard failed", err)
	}
	return monitorErr
}
