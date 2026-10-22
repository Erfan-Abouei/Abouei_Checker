package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
)

func TestExitCodeFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil", err: nil, want: ExitSuccess},
		{name: "usage", err: app.NewError(app.CategoryUsage, app.CodeUsageInvalid, "bad usage"), want: ExitUsage},
		{name: "configuration", err: app.NewError(app.CategoryConfiguration, app.CodeConfigInvalid, "bad config"), want: ExitUsage},
		{name: "unhealthy", err: app.NewError(app.CategoryUnhealthy, app.CodeInternal, "unhealthy"), want: ExitUnhealthy},
		{name: "notification", err: app.NewError(app.CategoryNotification, app.CodeNotificationFailed, "failed"), want: ExitNotificationFailure},
		{name: "internal", err: app.NewError(app.CategoryInternal, app.CodeInternal, "internal"), want: ExitInternal},
		{name: "unclassified", err: errors.New("plain error"), want: ExitInternal},
		{
			name: "wrapped category is preserved",
			err:  fmt.Errorf("outer: %w", app.NewError(app.CategoryNotification, app.CodeNotificationFailed, "failed")),
			want: ExitNotificationFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ExitCodeFor(tt.err); got != tt.want {
				t.Errorf("ExitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestSignalExitCodeValues(t *testing.T) {
	t.Parallel()

	if ExitInterrupted != 130 {
		t.Errorf("ExitInterrupted = %d, want 130", ExitInterrupted)
	}
	if ExitTerminated != 143 {
		t.Errorf("ExitTerminated = %d, want 143", ExitTerminated)
	}
}
