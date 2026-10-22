package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// EnvConfig is the environment variable that overrides configuration
// discovery. It contains a path, not configuration values.
const EnvConfig = "ABOUEI_CONFIG"

const userConfigRelativePath = "abouei-checker/config.yaml"

// Options controls configuration discovery and loading.
type Options struct {
	// ConfigPath is the explicit --config value. When empty, discovery uses
	// ABOUEI_CONFIG and then the documented default locations.
	ConfigPath string
	// LookupEnv resolves environment variables. Nil uses os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// UserConfigDir returns the platform user configuration directory. Nil
	// uses os.UserConfigDir.
	UserConfigDir func() (string, error)
}

// Discover selects the configuration file without reading it.
//
// Precedence is --config, ABOUEI_CONFIG, ./abouei.yaml, and then
// <user-config-dir>/abouei-checker/config.yaml. An explicit source that does
// not exist is an error and never falls through to a lower-precedence source.
// Relative paths are returned as supplied and resolve against the working
// directory.
func Discover(opts Options) (Source, error) {
	if opts.ConfigPath != "" {
		if err := requireFile(opts.ConfigPath); err != nil {
			return Source{}, err
		}
		return Source{Path: opts.ConfigPath, Origin: OriginFlag}, nil
	}

	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if path, ok := lookup(EnvConfig); ok && path != "" {
		if err := requireFile(path); err != nil {
			return Source{}, err
		}
		return Source{Path: path, Origin: OriginEnvironment}, nil
	}

	cwdSource := Source{Path: DefaultPath, Origin: OriginWorkingDirectory}
	exists, err := pathExists(cwdSource.Path)
	if err != nil {
		return Source{}, err
	}
	if exists {
		return cwdSource, nil
	}

	userConfigDir := opts.UserConfigDir
	if userConfigDir == nil {
		userConfigDir = os.UserConfigDir
	}
	if dir, dirErr := userConfigDir(); dirErr == nil && dir != "" {
		userSource := Source{
			Path:   filepath.Join(dir, userConfigRelativePath),
			Origin: OriginUserConfigDir,
		}
		exists, err = pathExists(userSource.Path)
		if err != nil {
			return Source{}, err
		}
		if exists {
			return userSource, nil
		}
		return Source{}, newNotFoundError(
			"no configuration file found; searched %q and %q; create one with %q",
			DefaultPath,
			userSource.Path,
			"abouei-checker init",
		)
	}

	return Source{}, newNotFoundError(
		"no configuration file found; searched %q; create one with %q",
		DefaultPath,
		"abouei-checker init",
	)
}

// Load discovers, reads, parses, expands, validates, and normalizes the
// configuration. It performs no network or database operation.
func Load(opts Options) (*Config, error) {
	source, err := Discover(opts)
	if err != nil {
		return nil, err
	}

	data, err := readFile(source.Path)
	if err != nil {
		return nil, err
	}

	return Parse(data, ParseOptions{
		Dir:       filepath.Dir(source.Path),
		LookupEnv: opts.LookupEnv,
		Source:    source,
	})
}

func requireFile(path string) error {
	exists, err := pathExists(path)
	if err != nil {
		return err
	}
	if !exists {
		return newNotFoundError("configuration file %q does not exist", path)
	}
	return nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("cannot access configuration path %q: %w", path, err)
	}
}

func readFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect configuration file %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("configuration path %q is a symbolic link; symlinks are not followed", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("configuration path %q is not a regular file", path)
	}

	// #nosec G304 -- the path is the operator-selected configuration file.
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open configuration file %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("cannot inspect configuration file %q: %w", path, err)
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("configuration path %q is not a regular file", path)
	}

	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read configuration file %q: %w", path, err)
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("configuration file %q exceeds the 1 MiB limit", path)
	}
	return data, nil
}
