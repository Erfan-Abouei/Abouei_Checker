package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func environment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestDiscoverPrecedence(t *testing.T) {
	flagDir := t.TempDir()
	flagPath := writeTestFile(t, flagDir, "flag.yaml", minimalConfig)

	envDir := t.TempDir()
	envPath := writeTestFile(t, envDir, "env.yaml", minimalConfig)

	workingDir := t.TempDir()
	writeTestFile(t, workingDir, DefaultPath, minimalConfig)
	t.Chdir(workingDir)

	userDir := t.TempDir()
	userConfigPath := filepath.Join(userDir, userConfigRelativePath)
	if err := os.MkdirAll(filepath.Dir(userConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfigPath, []byte(minimalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	userConfigDir := func() (string, error) { return userDir, nil }

	t.Run("explicit flag wins", func(t *testing.T) {
		source, err := Discover(Options{
			ConfigPath:    flagPath,
			LookupEnv:     environment(map[string]string{EnvConfig: envPath}),
			UserConfigDir: userConfigDir,
		})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
		if source.Path != flagPath || source.Origin != OriginFlag {
			t.Errorf("Source = %+v, want the explicit flag path", source)
		}
	})

	t.Run("environment wins over working directory", func(t *testing.T) {
		source, err := Discover(Options{
			LookupEnv:     environment(map[string]string{EnvConfig: envPath}),
			UserConfigDir: userConfigDir,
		})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
		if source.Path != envPath || source.Origin != OriginEnvironment {
			t.Errorf("Source = %+v, want the environment path", source)
		}
	})

	t.Run("working directory wins over user config dir", func(t *testing.T) {
		source, err := Discover(Options{
			LookupEnv:     environment(nil),
			UserConfigDir: userConfigDir,
		})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
		if source.Path != DefaultPath || source.Origin != OriginWorkingDirectory {
			t.Errorf("Source = %+v, want the working-directory default", source)
		}
	})

	t.Run("user config dir fallback", func(t *testing.T) {
		emptyDir := t.TempDir()
		t.Chdir(emptyDir)

		source, err := Discover(Options{
			LookupEnv:     environment(nil),
			UserConfigDir: userConfigDir,
		})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
		if source.Path != userConfigPath || source.Origin != OriginUserConfigDir {
			t.Errorf("Source = %+v, want the user config path", source)
		}
	})

	t.Run("empty environment value is treated as unset", func(t *testing.T) {
		source, err := Discover(Options{
			LookupEnv:     environment(map[string]string{EnvConfig: ""}),
			UserConfigDir: userConfigDir,
		})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
		if source.Origin != OriginWorkingDirectory {
			t.Errorf("Source = %+v, want working-directory discovery", source)
		}
	})
}

func TestDiscoverExplicitMissingFileDoesNotFallBack(t *testing.T) {
	workingDir := t.TempDir()
	writeTestFile(t, workingDir, DefaultPath, minimalConfig)
	t.Chdir(workingDir)

	missing := filepath.Join(t.TempDir(), "missing.yaml")
	_, err := Discover(Options{
		ConfigPath:    missing,
		LookupEnv:     environment(nil),
		UserConfigDir: func() (string, error) { return t.TempDir(), nil },
	})
	if err == nil {
		t.Fatal("Discover() error = nil, want a missing-file error")
	}
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error = %q, want the explicit path", err)
	}
}

func TestDiscoverEnvironmentMissingFileDoesNotFallBack(t *testing.T) {
	workingDir := t.TempDir()
	writeTestFile(t, workingDir, DefaultPath, minimalConfig)
	t.Chdir(workingDir)

	missing := filepath.Join(t.TempDir(), "missing.yaml")
	_, err := Discover(Options{
		LookupEnv:     environment(map[string]string{EnvConfig: missing}),
		UserConfigDir: func() (string, error) { return t.TempDir(), nil },
	})
	if err == nil {
		t.Fatal("Discover() error = nil, want a missing-file error")
	}
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error = %q, want the environment path", err)
	}
}

func TestDiscoverNoConfigurationSuggestsInit(t *testing.T) {
	t.Chdir(t.TempDir())
	userDir := t.TempDir()

	_, err := Discover(Options{
		LookupEnv:     environment(nil),
		UserConfigDir: func() (string, error) { return userDir, nil },
	})
	if err == nil {
		t.Fatal("Discover() error = nil, want a not-found error")
	}
	if !strings.Contains(err.Error(), "abouei-checker init") {
		t.Errorf("error = %q, want an init hint", err)
	}
	if !strings.Contains(err.Error(), DefaultPath) {
		t.Errorf("error = %q, want the working-directory candidate", err)
	}
}

func TestDiscoverUnavailableUserConfigDir(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := Discover(Options{
		LookupEnv:     environment(nil),
		UserConfigDir: func() (string, error) { return "", errors.New("unavailable") },
	})
	if err == nil {
		t.Fatal("Discover() error = nil, want a not-found error")
	}
	if !strings.Contains(err.Error(), "abouei-checker init") {
		t.Errorf("error = %q, want an init hint", err)
	}
}

func TestLoadReadsFileAndResolvesRelativeDatabasePath(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "custom.yaml", `version: 1
storage:
  database_path: ./state/monitor.db
backends:
  - id: api
    url: https://example.com/health
`)

	cfg, err := Load(Options{ConfigPath: path, LookupEnv: emptyEnvironment})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := filepath.Join(dir, "state", "monitor.db")
	if cfg.Storage.DatabasePath != want {
		t.Errorf("DatabasePath = %q, want %q", cfg.Storage.DatabasePath, want)
	}
	if cfg.Source.Path != path || cfg.Source.Origin != OriginFlag {
		t.Errorf("Source = %+v, want the loaded flag path", cfg.Source)
	}
}

func TestLoadEnforcesFileSizeLimit(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "large.yaml", strings.Repeat(" ", MaxFileBytes+1))

	_, err := Load(Options{ConfigPath: path, LookupEnv: emptyEnvironment})
	if err == nil {
		t.Fatal("Load() error = nil, want a size-limit error")
	}
	if !strings.Contains(err.Error(), "1 MiB") {
		t.Errorf("error = %q, want a size-limit message", err)
	}
	if strings.Contains(err.Error(), strings.Repeat(" ", 32)) {
		t.Error("error leaked file contents")
	}
}

func TestLoadRejectsNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(Options{ConfigPath: dir, LookupEnv: emptyEnvironment})
	if err == nil {
		t.Fatal("Load() error = nil, want a non-regular-file error")
	}
	if !strings.Contains(err.Error(), "not a regular file") && !strings.Contains(err.Error(), "cannot open") {
		t.Errorf("error = %q, want a non-regular-file message", err)
	}
}

func TestLoadRejectsUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission behavior is not testable in this environment")
	}

	dir := t.TempDir()
	path := writeTestFile(t, dir, "config.yaml", minimalConfig)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err := Load(Options{ConfigPath: path, LookupEnv: emptyEnvironment})
	if err == nil {
		t.Fatal("Load() error = nil, want a read error")
	}
	if !strings.Contains(err.Error(), "cannot open configuration file") {
		t.Errorf("error = %q, want a safe open failure", err)
	}
	if strings.Contains(err.Error(), "version:") {
		t.Errorf("error = %q, leaked file contents", err)
	}
}

func TestLoadRejectsSymlinkToRegularFile(t *testing.T) {
	dir := t.TempDir()
	target := writeTestFile(t, dir, "target.yaml", minimalConfig)
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	_, err := Load(Options{ConfigPath: link, LookupEnv: emptyEnvironment})
	if err == nil {
		t.Fatal("Load() error = nil, want a symlink rejection")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("error = %q, want a symlink message", err)
	}
}

func TestLoadRejectsSymlinkToDirectory(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	_, err := Load(Options{ConfigPath: link, LookupEnv: emptyEnvironment})
	if err == nil {
		t.Fatal("Load() error = nil, want a non-regular-file error")
	}
}

func TestLoadMissingFileIsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.yaml")
	_, err := Load(Options{ConfigPath: path, LookupEnv: emptyEnvironment})
	if err == nil {
		t.Fatal("Load() error = nil, want a not-found error")
	}
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
}

func TestDiscoverIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, DefaultPath, minimalConfig)
	t.Chdir(dir)

	options := Options{
		LookupEnv:     environment(nil),
		UserConfigDir: func() (string, error) { return t.TempDir(), nil },
	}
	for i := 0; i < 5; i++ {
		source, err := Discover(options)
		if err != nil {
			t.Fatalf("Discover() attempt %d error = %v", i, err)
		}
		if source.Path != DefaultPath || source.Origin != OriginWorkingDirectory {
			t.Fatalf("Discover() attempt %d = %+v, want the working-directory default", i, source)
		}
	}
}
