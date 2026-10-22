package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Sentinel errors let the application classify init failures into usage and
// internal exit-code categories without importing filesystem details.
var (
	// ErrTargetExists reports that init refused to overwrite an existing file.
	ErrTargetExists = errors.New("configuration template target exists")
	// ErrInvalidTarget reports a target path that cannot safely receive a
	// configuration template.
	ErrInvalidTarget = errors.New("configuration template target is invalid")
)

type targetExistsError struct {
	path string
}

func (e *targetExistsError) Error() string {
	return fmt.Sprintf("configuration file %q already exists; use --force to overwrite", e.path)
}

func (e *targetExistsError) Is(target error) bool {
	return target == ErrTargetExists
}

type invalidTargetError struct {
	message string
}

func (e *invalidTargetError) Error() string {
	return e.message
}

func (e *invalidTargetError) Is(target error) bool {
	return target == ErrInvalidTarget
}

func newInvalidTargetError(format string, args ...any) error {
	return &invalidTargetError{message: fmt.Sprintf(format, args...)}
}

// WriteTemplate creates a starter configuration at path.
//
// New files are created exclusively with owner-only permissions where the
// platform supports them. Existing regular files are preserved unless force is
// true, in which case they are replaced and their mode is reset to owner-only.
// Symbolic links and non-regular files are rejected. It returns true when an
// existing file was overwritten.
func WriteTemplate(path string, force bool) (bool, error) {
	if path == "" {
		return false, newInvalidTargetError("configuration template path must not be empty")
	}

	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return false, newInvalidTargetError("configuration template path %q is a symbolic link", path)
		}
		if info.IsDir() {
			return false, newInvalidTargetError("configuration template path %q is a directory", path)
		}
		if !info.Mode().IsRegular() {
			return false, newInvalidTargetError("configuration template path %q is not a regular file", path)
		}
		if !force {
			return false, &targetExistsError{path: path}
		}
		return writeExisting(path)
	case errors.Is(err, fs.ErrNotExist):
		return false, writeNew(path)
	default:
		return false, newInvalidTargetError("cannot inspect configuration template path %q: %v", path, err)
	}
}

func writeExisting(path string) (bool, error) {
	// #nosec G304 -- init writes the operator-selected target path.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return false, fmt.Errorf("cannot write configuration file %q: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("cannot protect configuration file %q: %w", path, err)
	}
	if err := writeAndClose(file, path); err != nil {
		return false, err
	}
	return true, nil
}

func writeNew(path string) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		if parentInfo, statErr := os.Stat(parent); statErr == nil && !parentInfo.IsDir() {
			return newInvalidTargetError("parent of configuration template path %q is not a directory", path)
		}
		return fmt.Errorf("cannot create parent directory for %q: %w", path, err)
	}

	// #nosec G304 -- init writes the operator-selected target path.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return &targetExistsError{path: path}
		}
		return fmt.Errorf("cannot write configuration file %q: %w", path, err)
	}
	if err := writeAndClose(file, path); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func writeAndClose(file *os.File, path string) error {
	if _, err := file.Write(Template()); err != nil {
		_ = file.Close()
		return fmt.Errorf("cannot write configuration file %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cannot finalize configuration file %q: %w", path, err)
	}
	return nil
}
