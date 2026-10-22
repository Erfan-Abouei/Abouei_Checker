package cli

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
)
