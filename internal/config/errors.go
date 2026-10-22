package config

import (
	"fmt"
	"strings"
)

const (
	// maxDetailedErrors bounds the number of field errors rendered at once.
	maxDetailedErrors = 20
)

// FieldError is one safe configuration validation failure. Field is a dotted
// path such as "backends[0].timeout"; Message describes the violated rule and
// never contains a resolved secret value.
type FieldError struct {
	Field   string
	Message string
}

// Error renders the field path and rule description.
func (e FieldError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

// ValidationError aggregates safe field errors in deterministic order.
type ValidationError struct {
	Errors []FieldError
}

// Error renders up to 20 field errors plus a count of omitted ones.
func (e *ValidationError) Error() string {
	if e == nil || len(e.Errors) == 0 {
		return "configuration is invalid"
	}

	shown := e.Errors
	if len(shown) > maxDetailedErrors {
		shown = shown[:maxDetailedErrors]
	}

	parts := make([]string, len(shown))
	for i := range shown {
		parts[i] = shown[i].Error()
	}

	message := "configuration is invalid: " + strings.Join(parts, "; ")
	if remaining := len(e.Errors) - len(shown); remaining > 0 {
		message += fmt.Sprintf(" (%d more errors not shown)", remaining)
	}
	return message
}

// Details returns at most 20 field errors for structured output.
func (e *ValidationError) Details() []FieldError {
	if e == nil {
		return nil
	}
	details := e.Errors
	if len(details) > maxDetailedErrors {
		details = details[:maxDetailedErrors]
	}
	out := make([]FieldError, len(details))
	copy(out, details)
	return out
}

// NotFoundError reports that no configuration file was found by discovery.
type NotFoundError struct {
	message string
}

// Error returns the safe discovery failure message.
func (e *NotFoundError) Error() string {
	if e == nil {
		return "configuration file not found"
	}
	return e.message
}

func newNotFoundError(format string, args ...any) *NotFoundError {
	return &NotFoundError{message: fmt.Sprintf(format, args...)}
}

func validationError(field, format string, args ...any) *ValidationError {
	return &ValidationError{Errors: []FieldError{{
		Field:   field,
		Message: fmt.Sprintf(format, args...),
	}}}
}
