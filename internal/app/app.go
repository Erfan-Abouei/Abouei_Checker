package app

import (
	"io"
	"log/slog"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/buildinfo"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/logging"
)

const (
	defaultLogLevel  = "info"
	defaultLogFormat = logging.FormatText
)

// Options are the global application options parsed from CLI flags and
// configuration. ConfigPath is loaded by LoadConfig.
type Options struct {
	// ConfigPath is the explicit --config value. Empty selects discovery.
	ConfigPath string
	// LogLevel is the --log-level value. When LogLevelSet is false,
	// configuration logging.level takes precedence unless it is absent.
	LogLevel string
	// LogLevelSet reports whether --log-level was supplied explicitly.
	LogLevelSet bool
	// LogFormat optionally overrides the configuration log format. The CLI has
	// no log-format flag, so this is normally empty.
	LogFormat logging.Format
	// LogWriter receives all diagnostics. Application logs never go to stdout.
	LogWriter io.Writer
	// LookupEnv resolves configuration environment references. Nil uses the
	// process environment.
	LookupEnv func(string) (string, bool)
	// UserConfigDir resolves the platform configuration directory during
	// discovery. Nil uses os.UserConfigDir.
	UserConfigDir func() (string, error)
}

// App is the composition root shared by all commands. It owns global options,
// the configured logger, the secret redactor used at logging boundaries, and
// the loaded configuration.
type App struct {
	options  Options
	logger   *slog.Logger
	redactor *logging.Redactor
	config   *config.Config
}

// New builds the application composition root. It validates the initial
// logging options. Configuration is loaded later by LoadConfig for commands
// that need it, so commands such as version and init never read YAML.
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

// Redactor returns the redactor used by the logger. Resolved configuration
// secrets are registered with it by LoadConfig.
func (a *App) Redactor() *logging.Redactor {
	if a == nil {
		return logging.NewRedactor()
	}
	return a.redactor
}

// ConfigPath returns the accepted --config path. Use LoadConfig to read it.
func (a *App) ConfigPath() string {
	if a == nil {
		return ""
	}
	return a.options.ConfigPath
}

// Config returns the configuration loaded by LoadConfig, or nil when it has not
// been loaded. Callers must treat the value as read-only.
func (a *App) Config() *config.Config {
	if a == nil {
		return nil
	}
	return a.config
}

// BuildInfo returns linker-injected build metadata.
func (a *App) BuildInfo() buildinfo.Info {
	return buildinfo.Current()
}
