package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleConfigurationIsValid(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "abouei.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // repository-relative fixture path
	if err != nil {
		t.Fatalf("read example configuration: %v", err)
	}

	cfg, err := Parse(data, ParseOptions{
		Dir: filepath.Dir(path),
		LookupEnv: environment(map[string]string{
			"EXAMPLE_API_TOKEN": "example-token",
			"EXAMPLE_API_KEY":   "example-key",
		}),
	})
	if err != nil {
		t.Fatalf("example configuration is invalid: %v", err)
	}

	if cfg.Version != SchemaVersion {
		t.Errorf("example version = %d, want %d", cfg.Version, SchemaVersion)
	}
	if len(cfg.Backends) != 2 {
		t.Fatalf("example backend count = %d, want 2", len(cfg.Backends))
	}
	if cfg.Backends[0].ID != "example-site" || cfg.Backends[1].ID != "example-api" {
		t.Errorf("example backend IDs = %v", cfg.BackendIDs())
	}
	if cfg.Notifications.Webhook.URL == "" {
		t.Error("example should document the webhook transport")
	}
}

func TestExampleConfigurationRequiresReferencedSecrets(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "abouei.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // repository-relative fixture path
	if err != nil {
		t.Fatalf("read example configuration: %v", err)
	}

	_, err = Parse(data, ParseOptions{Dir: filepath.Dir(path), LookupEnv: emptyEnvironment})
	if err == nil {
		t.Fatal("Parse() error = nil, want missing-secret errors")
	}
	message := err.Error()
	if !strings.Contains(message, "EXAMPLE_API_TOKEN") || !strings.Contains(message, "EXAMPLE_API_KEY") {
		t.Errorf("error = %q, want both missing variable names", message)
	}
}
