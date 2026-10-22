package app

import (
	"errors"
	"fmt"
	"testing"
)

func TestCategoryOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want Category
	}{
		{name: "usage", err: NewError(CategoryUsage, CodeUsageInvalid, "bad usage"), want: CategoryUsage},
		{name: "configuration", err: NewError(CategoryConfiguration, CodeConfigInvalid, "bad config"), want: CategoryConfiguration},
		{name: "unhealthy", err: NewError(CategoryUnhealthy, CodeInternal, "unhealthy"), want: CategoryUnhealthy},
		{name: "notification", err: NewError(CategoryNotification, CodeNotificationFailed, "failed"), want: CategoryNotification},
		{name: "internal", err: NewError(CategoryInternal, CodeInternal, "internal"), want: CategoryInternal},
		{name: "unclassified", err: errors.New("plain error"), want: CategoryInternal},
		{
			name: "wrapped application error",
			err:  fmt.Errorf("while running: %w", NewError(CategoryNotification, CodeNotificationFailed, "failed")),
			want: CategoryNotification,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := CategoryOf(tt.err); got != tt.want {
				t.Errorf("CategoryOf(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestCodeOf(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("context: %w", NewError(CategoryConfiguration, CodeConfigNotFound, "missing"))
	if got := CodeOf(err); got != CodeConfigNotFound {
		t.Errorf("CodeOf(err) = %q, want %q", got, CodeConfigNotFound)
	}
	if got := CodeOf(errors.New("plain")); got != CodeInternal {
		t.Errorf("CodeOf(plain) = %q, want %q", got, CodeInternal)
	}
}

func TestErrorSeparatesSafeMessageFromCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("token=super-secret-value")
	err := WrapError(CategoryInternal, CodeInternal, "operation failed", cause)

	if err.Error() != "operation failed" {
		t.Errorf("Error() = %q, want the safe message", err.Error())
	}
	if err.SafeMessage() != "operation failed" {
		t.Errorf("SafeMessage() = %q, want the safe message", err.SafeMessage())
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is(err, cause) = false, want true")
	}
	var gotCause *Error
	if !errors.As(fmt.Errorf("outer: %w", err), &gotCause) {
		t.Fatal("errors.As failed to inspect the wrapped application error")
	}
	if gotCause.Code != CodeInternal {
		t.Errorf("Code = %q, want %q", gotCause.Code, CodeInternal)
	}
}

func TestSafeMessage(t *testing.T) {
	t.Parallel()

	if got := SafeMessage(nil); got != "" {
		t.Errorf("SafeMessage(nil) = %q, want empty", got)
	}
	if got := SafeMessage(errors.New("plain failure")); got != "plain failure" {
		t.Errorf("SafeMessage(plain) = %q, want the original text", got)
	}
	err := WrapError(CategoryUsage, CodeUsageInvalid, "safe failure", errors.New("secret cause"))
	if got := SafeMessage(err); got != "safe failure" {
		t.Errorf("SafeMessage(app error) = %q, want safe failure", got)
	}
}

func TestNewUsageError(t *testing.T) {
	t.Parallel()

	err := NewUsageError("bad flag")
	if err.Category != CategoryUsage {
		t.Errorf("Category = %v, want usage", err.Category)
	}
	if err.Code != CodeUsageInvalid {
		t.Errorf("Code = %q, want %q", err.Code, CodeUsageInvalid)
	}
	if err.Unwrap() != nil {
		t.Error("Unwrap() != nil, want no cause")
	}
}

func TestCategoryStrings(t *testing.T) {
	t.Parallel()

	tests := map[Category]string{
		CategoryUsage:         "usage",
		CategoryConfiguration: "configuration",
		CategoryUnhealthy:     "unhealthy",
		CategoryNotification:  "notification",
		CategoryInternal:      "internal",
		Category(99):          "internal",
	}

	for category, want := range tests {
		if got := category.String(); got != want {
			t.Errorf("Category(%d).String() = %q, want %q", category, got, want)
		}
	}
}
