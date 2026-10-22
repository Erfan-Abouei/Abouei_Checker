package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteTemplateCreatesFileAndParents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config", "abouei.yaml")

	overwritten, err := WriteTemplate(path, false)
	if err != nil {
		t.Fatalf("WriteTemplate() error = %v", err)
	}
	if overwritten {
		t.Error("overwritten = true, want false for a new file")
	}

	data, err := os.ReadFile(path) //nolint:gosec // test fixture path is controlled by t.TempDir
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if string(data) != string(Template()) {
		t.Error("created file does not contain the template")
	}
	if _, err := Parse(data, ParseOptions{LookupEnv: emptyEnvironment}); err != nil {
		t.Errorf("created template is invalid: %v", err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("mode = %o, want 600", got)
		}
		parent, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if got := parent.Mode().Perm(); got != 0o700 {
			t.Errorf("parent mode = %o, want 700", got)
		}
	}
}

func TestWriteTemplateRefusesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abouei.yaml")
	const original = "original contents\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	overwritten, err := WriteTemplate(path, false)
	if err == nil {
		t.Fatal("WriteTemplate() error = nil, want a refusal")
	}
	if !errors.Is(err, ErrTargetExists) {
		t.Fatalf("error = %v, want ErrTargetExists", err)
	}
	if overwritten {
		t.Error("overwritten = true, want false")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want a --force hint", err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // test fixture path is controlled by t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Error("existing file was modified")
	}
}

func TestWriteTemplateForceOverwritesAndProtects(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission assertion")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "abouei.yaml")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // deliberately permissive to test init hardening
		t.Fatal(err)
	}

	overwritten, err := WriteTemplate(path, true)
	if err != nil {
		t.Fatalf("WriteTemplate() error = %v", err)
	}
	if !overwritten {
		t.Error("overwritten = false, want true")
	}
	data, err := os.ReadFile(path) //nolint:gosec // test fixture path is controlled by t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(Template()) {
		t.Error("file was not overwritten with the template")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

func TestWriteTemplateRejectsUnsafeTargets(t *testing.T) {
	dir := t.TempDir()

	t.Run("empty path", func(t *testing.T) {
		_, err := WriteTemplate("", false)
		if !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("error = %v, want ErrInvalidTarget", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		_, err := WriteTemplate(dir, false)
		if !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("error = %v, want ErrInvalidTarget", err)
		}
	})

	t.Run("parent is a file", func(t *testing.T) {
		parent := filepath.Join(dir, "not-a-directory")
		if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := WriteTemplate(filepath.Join(parent, "config.yaml"), false)
		if !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("error = %v, want ErrInvalidTarget", err)
		}
	})

	t.Run("symbolic link", func(t *testing.T) {
		target := filepath.Join(dir, "target.yaml")
		if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link.yaml")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks are unavailable: %v", err)
		}
		_, err := WriteTemplate(link, true)
		if !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("error = %v, want ErrInvalidTarget", err)
		}
		data, readErr := os.ReadFile(target) //nolint:gosec // test fixture path is controlled by t.TempDir
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) != "x" {
			t.Error("symlink target was modified")
		}
	})
}

func TestTemplateIsCommentedWhereEnvironmentVariablesAreRequired(t *testing.T) {
	template := string(Template())
	if !strings.Contains(template, "# notifications:") {
		t.Error("template should leave the webhook section commented out")
	}
	if !strings.Contains(template, "version: 1") {
		t.Error("template is missing the schema version")
	}
}
