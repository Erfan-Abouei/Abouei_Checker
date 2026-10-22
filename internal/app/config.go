package app

import (
	"errors"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/logging"
)

// ConfigSummary is the safe, renderable subset of a loaded configuration. It
// intentionally excludes URLs, headers, and resolved secrets.
type ConfigSummary struct {
	// Version is the configuration schema version.
	Version int
	// BackendIDs lists configured backend identities in configuration order.
	BackendIDs []string
	// SourcePath is the path the configuration was loaded from.
	SourcePath string
}

// LoadConfig discovers, loads, validates, and normalizes configuration for
// commands that need it. It registers resolved environment secrets with the
// redactor and applies configuration logging settings when the CLI did not
// override them explicitly. No network or database operation is performed.
func (a *App) LoadConfig() (*ConfigSummary, error) {
	if a == nil {
		return nil, NewError(CategoryInternal, CodeInternal, "application is not initialized")
	}

	cfg, err := config.Load(config.Options{
		ConfigPath:    a.options.ConfigPath,
		LookupEnv:     a.options.LookupEnv,
		UserConfigDir: a.options.UserConfigDir,
	})
	if err != nil {
		return nil, classifyConfigError(err)
	}

	cfg.RegisterSecrets(a.redactor)
	if err := a.applyConfigLogging(cfg); err != nil {
		return nil, err
	}
	a.config = cfg

	return &ConfigSummary{
		Version:    cfg.Version,
		BackendIDs: cfg.BackendIDs(),
		SourcePath: cfg.Source.Path,
	}, nil
}

// WriteConfigTemplate writes the init template. Existing files are refused
// unless force is true. Invalid targets are usage errors; filesystem failures
// are internal errors.
func (a *App) WriteConfigTemplate(path string, force bool) (bool, error) {
	if a == nil {
		return false, NewError(CategoryInternal, CodeInternal, "application is not initialized")
	}

	overwritten, err := config.WriteTemplate(path, force)
	if err != nil {
		switch {
		case errors.Is(err, config.ErrTargetExists), errors.Is(err, config.ErrInvalidTarget):
			return false, WrapError(CategoryUsage, CodeUsageInvalid, err.Error(), err)
		default:
			return false, WrapError(CategoryInternal, CodeInternal, err.Error(), err)
		}
	}
	return overwritten, nil
}

func (a *App) applyConfigLogging(cfg *config.Config) error {
	levelName := a.options.LogLevel
	if !a.options.LogLevelSet && cfg.Logging.Level != "" {
		levelName = cfg.Logging.Level
	}
	level, err := logging.ParseLevel(levelName)
	if err != nil {
		return WrapError(
			CategoryConfiguration,
			CodeConfigInvalid,
			"configuration is invalid: logging.level is invalid",
			err,
		)
	}

	format := a.options.LogFormat
	if format == "" {
		format = logging.Format(cfg.Logging.Format)
	}

	logger, err := logging.New(logging.Config{
		Level:    level,
		Format:   format,
		Writer:   a.options.LogWriter,
		Redactor: a.redactor,
	})
	if err != nil {
		return WrapError(CategoryInternal, CodeInternal, "failed to configure logging", err)
	}
	a.logger = logger
	return nil
}

func classifyConfigError(err error) *Error {
	var notFound *config.NotFoundError
	if errors.As(err, &notFound) {
		return WrapError(CategoryConfiguration, CodeConfigNotFound, err.Error(), err)
	}

	var validation *config.ValidationError
	if errors.As(err, &validation) {
		details := validation.Details()
		mapped := make([]Detail, len(details))
		for i, fieldError := range details {
			mapped[i] = Detail{Field: fieldError.Field, Rule: fieldError.Message}
		}
		return &Error{
			Category: CategoryConfiguration,
			Code:     CodeConfigInvalid,
			Message:  validation.Error(),
			Details:  mapped,
			cause:    err,
		}
	}

	return WrapError(CategoryConfiguration, CodeConfigInvalid, err.Error(), err)
}
