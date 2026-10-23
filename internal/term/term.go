// Package term provides the shared ABOUEI terminal presentation layer.
//
// It is the only place that emits ANSI escape sequences. All command handlers
// build human-readable output through Style so color, width, and fallback
// behavior stay consistent. JSON serialization, exit codes, stdout/stderr
// routing, and domain logic never depend on this package.
//
// Compatibility contract:
//
//   - JSON output never contains ANSI sequences or banners.
//   - Logs never contain banners.
//   - When color is disabled (NO_COLOR, CLICOLOR=0, TERM=dumb, non-TTY, or a
//     redirected pipe) every method returns plain text byte-identical to the
//     historical format, so scripts and tests keep working.
//   - The genuine logo PNG is preserved under assets/brand; the terminal uses
//     a restrained text wordmark fallback and never claims it is the logo.
package term

import (
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mattn/go-isatty"
)

// Brand identity constants. The PNG masters live in assets/brand; the
// terminal renders only the wordmark below.
const (
	BrandName   = "ABOUEI"
	ProductName = "ABOUEICHECKER"
	Descriptor  = "Backend Health Monitoring"

	// LogoAsset is the project-owned copy of the genuine personal logo
	// selected for dark terminals: white artwork on a transparent
	// background, legible on near-black surfaces.
	LogoAsset = "assets/brand/logo/primary/logo-white-768.png"
	// LogoAssetLight is the same artwork recolored black for light surfaces.
	LogoAssetLight = "assets/brand/logo/primary/logo-black-768.png"
)

// Style describes how to render human-readable output.
type Style struct {
	// Enabled reports whether ANSI styling may be used.
	Enabled bool
	// Width is the terminal width used for truncation. Always >= 20.
	Width int
}

// Plain returns a Style that never emits ANSI sequences.
func Plain() Style {
	return Style{Enabled: false, Width: DefaultWidth()}
}

// DefaultWidth returns the fallback width when detection fails.
func DefaultWidth() int {
	if w := widthFromEnv(); w >= 20 {
		return w
	}
	return 80
}

// ForWriter detects styling support for a human-readable stream.
//
//   - JSON, logs, and tests must not call this for machine output; they use
//     Plain or write JSON directly.
//   - Color is enabled only when the writer is a character-device TTY, unless
//     CLICOLOR_FORCE forces it. NO_COLOR, CLICOLOR=0, and TERM=dumb always win.
//   - Width comes from ABOUEI_WIDTH, then COLUMNS, then 80.
func ForWriter(w io.Writer) Style {
	return Style{
		Enabled: colorEnabled(w),
		Width:   detectWidth(),
	}
}

// ForTesting builds a deterministic Style for unit tests.
func ForTesting(enabled bool, width int) Style {
	if width < 20 {
		width = 20
	}
	return Style{Enabled: enabled, Width: width}
}

func colorEnabled(w io.Writer) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if v, ok := os.LookupEnv("CLICOLOR_FORCE"); ok && v != "" && v != "0" {
		// Explicit force for testing/previewing the palette on non-TTY
		//writers. NO_COLOR above still wins.
		return true
	}
	if strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	if os.Getenv("CLICOLOR") == "0" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func detectWidth() int {
	if v, ok := os.LookupEnv("ABOUEI_WIDTH"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 20 && n <= 1000 {
			return n
		}
	}
	if w := widthFromEnv(); w >= 20 {
		return w
	}
	return 80
}

func widthFromEnv() int {
	if raw, ok := os.LookupEnv("COLUMNS"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n >= 20 && n <= 1000 {
			return n
		}
	}
	return 0
}

// Narrow reports whether the terminal needs compact, truncated layout.
func (s Style) Narrow() bool {
	return s.Width < 60
}

// Truncate shortens value to at most limit runes, appending "..." when cut.
// A non-positive limit returns value unchanged. It never emits ANSI.
func Truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

// Fit truncates value to fit the style width budget.
func (s Style) Fit(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	if s.Width < 60 && limit > s.Width/2 {
		limit = s.Width / 2
		if limit < 12 {
			limit = 12
		}
	}
	return Truncate(value, limit)
}
