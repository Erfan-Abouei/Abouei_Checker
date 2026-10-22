package cli

import (
	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
)

// Process exit codes. These are part of the public CLI contract documented in
// docs/cli-contract.md and must not be renumbered without a compatibility
// review.
const (
	// ExitSuccess indicates that the command completed successfully.
	ExitSuccess = 0
	// ExitUnhealthy indicates that a health check failed or that the status
	// query found at least one open incident.
	ExitUnhealthy = 1
	// ExitUsage indicates invalid command usage or invalid configuration.
	ExitUsage = 2
	// ExitInternal indicates an internal or operational failure.
	ExitInternal = 3
	// ExitNotificationFailure indicates that a required notification delivery
	// attempt failed during a one-shot command. The event remains queued for a
	// later retry.
	ExitNotificationFailure = 4
	// ExitInterrupted is the conventional 128+SIGINT status used when a forced
	// termination follows an interrupted graceful shutdown. It is not produced
	// until continuous monitoring exists.
	ExitInterrupted = 130
	// ExitTerminated is the conventional 128+SIGTERM status used when a forced
	// termination follows an interrupted graceful shutdown. It is not produced
	// until continuous monitoring exists.
	ExitTerminated = 143
)

// ExitCodeFor maps a returned error to the documented process exit code.
// Nil is success, classified application errors use their category, and
// unclassified errors are internal. Usage and configuration share code 2.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitSuccess
	}

	switch app.CategoryOf(err) {
	case app.CategoryUsage, app.CategoryConfiguration:
		return ExitUsage
	case app.CategoryUnhealthy:
		return ExitUnhealthy
	case app.CategoryNotification:
		return ExitNotificationFailure
	case app.CategoryInternal:
		return ExitInternal
	default:
		return ExitInternal
	}
}
