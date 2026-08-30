package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPanelConfigValidateRequiresHTTPSOutsideDevelopment(t *testing.T) {
	for _, cfg := range []PanelConfig{
		{Environment: "production", BaseURL: "http://panel.example.com"},
		{Environment: "", BaseURL: "http://panel.example.com"},
	} {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate() accepted insecure production config: %#v", cfg)
		}
	}
}

func TestPanelConfigValidateAllowsLocalHTTPOnlyInDevelopment(t *testing.T) {
	if err := (PanelConfig{Environment: "development", BaseURL: "http://127.0.0.1:8080"}).Validate(); err != nil {
		t.Fatalf("development HTTP config rejected: %v", err)
	}
	if err := (PanelConfig{Environment: "production", BaseURL: "https://panel.example.com"}).Validate(); err != nil {
		t.Fatalf("production HTTPS config rejected: %v", err)
	}
}

func TestPanelConfigValidateRejectsAmbiguousURLs(t *testing.T) {
	for _, raw := range []string{
		"panel.example.com",
		"https://user:pass@panel.example.com",
		"https://panel.example.com/?token=secret",
		"https://panel.example.com/#admin",
	} {
		if err := (PanelConfig{Environment: "production", BaseURL: raw}).Validate(); err == nil {
			t.Fatalf("Validate() accepted unsafe URL %q", raw)
		}
	}
}

func TestDatabasePoolConfigLoadsDurationsAndEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.yaml")
	if err := os.WriteFile(path, []byte(`
environment: development
base_url: http://127.0.0.1:8080
database:
  driver: postgres
  dsn: test
  max_open_conns: 40
  max_idle_conns: 12
  conn_max_lifetime: 45m
  conn_max_idle_time: 7m
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SINGBOX_PANEL_DB_MAX_OPEN_CONNS", "50")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.MaxOpenConns != 50 || cfg.Database.MaxIdleConns != 12 ||
		cfg.Database.ConnMaxLifetime != 45*time.Minute || cfg.Database.ConnMaxIdleTime != 7*time.Minute {
		t.Fatalf("unexpected pool config: %+v", cfg.Database)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDatabasePoolConfigRejectsInvalidEnvironmentValue(t *testing.T) {
	t.Setenv("SINGBOX_PANEL_DB_CONN_MAX_LIFETIME", "tomorrow")
	if _, err := Load(""); err == nil {
		t.Fatal("invalid pool duration was accepted")
	}
}
