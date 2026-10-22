package app

import (
	"io"
	"log/slog"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/buildinfo"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/logging"
)

const (
	defaultLogLevel  = "info"
	defaultLogFormat = logging.FormatText
)

// Options are the global application options parsed from CLI flags and, in
// later phases, configuration. ConfigPath is only recorded in Phase 1; no
// configuration file is read.
type Options struct {
	ConfigPath string
	LogLevel   string
	LogFormat  logging.Format
	// LogWriter receives all diagnostics. Application logs never go to stdout.
	LogWriter io.Writer
}

// App is the composition root shared by all commands. It owns global options,
// the configured logger, and the secret redactor used at logging boundaries.
type App struct {
	options  Options
	logger   *slog.Logger
	redactor *logging.Redactor
}

// New builds the application composition skeleton. It validates global
// logging options but deliberately does not load configuration.
func New(options Options) (*App, error) {
	levelName := options.LogLevel
	if levelName == "" {
		levelName = defaultLogLevel
	}

	level, err := logging.ParseLevel(levelName)
	if err != nil {
		return nil, WrapError(CategoryUsage, CodeUsageInvalid, err.Error(), err)
	}

	format := options.LogFormat
	if format == "" {
		format = defaultLogFormat
	}

	redactor := logging.NewRedactor()
	logger, err := logging.New(logging.Config{
		Level:    level,
		Format:   format,
		Writer:   options.LogWriter,
		Redactor: redactor,
	})
	if err != nil {
		return nil, WrapError(CategoryInternal, CodeInternal, "failed to configure logging", err)
	}

	return &App{
		options:  options,
		logger:   logger,
		redactor: redactor,
	}, nil
}

// Logger returns the application logger. It writes to the injected writer,
// which the CLI sets to stderr.
func (a *App) Logger() *slog.Logger {
	if a == nil {
		return slog.New(slog.DiscardHandler)
	}
	return a.logger
}

// Redactor returns the redactor used by the logger. Later phases register
// resolved configuration secrets here.
func (a *App) Redactor() *logging.Redactor {
	if a == nil {
		return logging.NewRedactor()
	}
	return a.redactor
}

// ConfigPath returns the accepted --config path. Phase 1 does not read it.
func (a *App) ConfigPath() string {
	if a == nil {
		return ""
	}
	return a.options.ConfigPath
}

// BuildInfo returns linker-injected build metadata.
func (a *App) BuildInfo() buildinfo.Info {
	return buildinfo.Current()
}
