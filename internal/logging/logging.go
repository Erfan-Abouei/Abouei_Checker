// Package logging configures structured application logging and provides the
// redaction boundary used by every log record.
//
// The package intentionally stays small: it configures the standard library's
// log/slog handlers and enforces safe-field rules. It is not a logging
// framework.
package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Format selects the slog handler used for application logs.
type Format string

const (
	// FormatText writes human-readable key=value records.
	FormatText Format = "text"
	// FormatJSON writes one structured JSON object per line.
	FormatJSON Format = "json"
)

// ParseFormat validates a log format name. An empty value selects text.
func ParseFormat(raw string) (Format, error) {
	switch strings.ToLower(raw) {
	case "", string(FormatText):
		return FormatText, nil
	case string(FormatJSON):
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("invalid log format %q: expected text or json", raw)
	}
}

// ParseLevel validates a log level name case-insensitively. An empty value
// selects info, matching the documented default before configuration loading
// exists.
func ParseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: expected debug, info, warn, or error", raw)
	}
}

// Config describes a logger built by New.
type Config struct {
	// Level is the minimum enabled level. The zero value is slog.LevelInfo.
	Level slog.Level
	// Format selects text or JSON. The zero value selects text.
	Format Format
	// Writer receives log records. It is normally os.Stderr.
	Writer io.Writer
	// Redactor filters attributes and values. When nil, a new empty Redactor is
	// created and owned by the logger.
	Redactor *Redactor
}

// New builds a slog.Logger writing to cfg.Writer with UTC timestamps, stable
// field rules, and redaction applied to every attribute.
func New(cfg Config) (*slog.Logger, error) {
	if cfg.Writer == nil {
		return nil, errors.New("logging: writer is required")
	}

	format := cfg.Format
	if format == "" {
		format = FormatText
	}

	redactor := cfg.Redactor
	if redactor == nil {
		redactor = NewRedactor()
	}

	options := &slog.HandlerOptions{
		Level:       cfg.Level,
		ReplaceAttr: redactor.ReplaceAttr,
	}

	var handler slog.Handler
	switch format {
	case FormatText:
		handler = slog.NewTextHandler(cfg.Writer, options)
	case FormatJSON:
		handler = slog.NewJSONHandler(cfg.Writer, options)
	default:
		return nil, fmt.Errorf("logging: unsupported format %q", format)
	}

	return slog.New(handler), nil
}
