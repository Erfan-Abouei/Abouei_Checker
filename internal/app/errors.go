// Package app is the composition root for the AboueiChecker application. It
// wires global options, logging, build metadata, and shared error handling so
// that the CLI stays focused on parsing and presentation.
package app

import "errors"

// Category classifies an application failure for central exit-code mapping.
// Categories are stable and documented in docs/cli-contract.md.
type Category int

const (
	// CategoryInternal covers unexpected or operational failures: I/O errors,
	// database failures, and unclassified errors. It is the default for errors
	// that were not explicitly classified.
	CategoryInternal Category = iota
	// CategoryUsage covers invalid commands, flags, arguments, and invalid
	// configuration input.
	CategoryUsage
	// CategoryConfiguration covers configuration loading and validation
	// failures. It shares exit code 2 with usage errors.
	CategoryConfiguration
	// CategoryUnhealthy indicates a negative health result, such as a failed
	// probe or an open incident found by status.
	CategoryUnhealthy
	// CategoryNotification indicates that a required notification attempt
	// failed during a one-shot command.
	CategoryNotification
)

// String returns a stable lowercase name for logging and tests.
func (c Category) String() string {
	switch c {
	case CategoryUsage:
		return "usage"
	case CategoryConfiguration:
		return "configuration"
	case CategoryUnhealthy:
		return "unhealthy"
	case CategoryNotification:
		return "notification"
	default:
		return "internal"
	}
}

// Code is a stable machine-readable error code from the JSON output contract.
type Code string

// Stable error codes from docs/cli-contract.md section 6.1.
const (
	CodeUsageInvalid         Code = "usage_invalid"
	CodeConfigInvalid        Code = "config_invalid"
	CodeConfigNotFound       Code = "config_not_found"
	CodeBackendNotFound      Code = "backend_not_found"
	CodeDatabaseUnavailable  Code = "database_unavailable"
	CodeDatabaseIncompatible Code = "database_incompatible"
	CodeNotificationFailed   Code = "notification_failed"
	CodeInternal             Code = "internal"
)

// Detail is a safe field-level failure description for JSON error output.
type Detail struct {
	// Field is the configuration field path, for example backends[0].timeout.
	Field string `json:"field"`
	// Rule is the violated rule or validation message.
	Rule string `json:"rule"`
}

// Error is an application failure with a safe user-facing message, optional
// safe field details, and an optional internal cause. Error() returns the safe
// message; Unwrap exposes the cause for diagnostics. Logs and CLI output must
// use SafeMessage.
type Error struct {
	Category Category
	Code     Code
	Message  string
	Details  []Detail
	cause    error
}

// Error implements error using the safe user-facing message.
func (e *Error) Error() string {
	return e.Message
}

// Unwrap exposes the internal cause for errors.Is and errors.As.
func (e *Error) Unwrap() error {
	return e.cause
}

// SafeMessage implements the logging safe-error boundary.
func (e *Error) SafeMessage() string {
	return e.Message
}

// NewError builds an application error without a cause.
func NewError(category Category, code Code, message string) *Error {
	return &Error{Category: category, Code: code, Message: message}
}

// WrapError builds an application error that preserves cause for diagnostics.
// The cause is never rendered by Error or SafeMessage.
func WrapError(category Category, code Code, message string, cause error) *Error {
	return &Error{Category: category, Code: code, Message: message, cause: cause}
}

// NewUsageError builds an invalid-usage error.
func NewUsageError(message string) *Error {
	return NewError(CategoryUsage, CodeUsageInvalid, message)
}

// CategoryOf returns the category of err. Unclassified errors are internal so
// that unexpected failures never masquerade as usage errors.
func CategoryOf(err error) Category {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.Category
	}
	return CategoryInternal
}

// CodeOf returns the stable code of err, or CodeInternal when err is not an
// application error.
func CodeOf(err error) Code {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return CodeInternal
}

// SafeMessage returns the user-facing message for err. Application errors are
// safe by construction; any other error is returned unchanged so that callers
// can apply the logging redactor at the output boundary.
func SafeMessage(err error) string {
	if err == nil {
		return ""
	}

	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.Message
	}
	return err.Error()
}
