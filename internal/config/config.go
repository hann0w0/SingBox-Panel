// Package config loads panel configuration from a YAML file with env overrides.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// PanelConfig is the panel server configuration.
type PanelConfig struct {
	// Listen is the HTTP listen address, e.g. ":8080".
	Listen string `yaml:"listen"`
	// BaseURL is the public origin of the panel (used to build subscription
	// links and the agent one-line install command), e.g. https://panel.example.com.
	BaseURL string `yaml:"base_url"`
	// JWTSecret signs admin/user session tokens. Auto-generated if empty.
	JWTSecret string `yaml:"jwt_secret"`

	// AgentsDir holds prebuilt agent binaries served to VPSes by the one-line
	// installer, named singpanel-agent-linux-<arch>.
	AgentsDir string `yaml:"agents_dir"`

	Database     DatabaseConfig `yaml:"database"`
	Admin        AdminBootstrap `yaml:"admin"`
	Subscription SubConfig      `yaml:"subscription"`
}

// DatabaseConfig selects the backing store.
type DatabaseConfig struct {
	// Driver: sqlite | mysql | postgres.
	Driver string `yaml:"driver"`
	// DSN: for sqlite, a file path (e.g. ./data/singpanel.db); otherwise a full DSN.
	DSN string `yaml:"dsn"`
}

// AdminBootstrap seeds the first admin account on an empty database.
type AdminBootstrap struct {
	Email    string `yaml:"email"`
	Password string `yaml:"password"`
}

// SubConfig tunes subscription output.
type SubConfig struct {
	// Prefix for the subscription path, default "/api/sub".
	PathPrefix string `yaml:"path_prefix"`
}

// Default returns a config with sensible defaults applied.
func Default() PanelConfig {
	return PanelConfig{
		Listen:    ":8080",
		BaseURL:   "http://localhost:8080",
		AgentsDir: "./dist/agents",
		Database: DatabaseConfig{
			Driver: "sqlite",
			DSN:    "./data/singpanel.db",
		},
		Subscription: SubConfig{
			PathPrefix: "/api/sub",
		},
	}
}

// Load reads a YAML config file, applies defaults for missing fields, then
// overlays environment variables (SINGPANEL_*).
func Load(path string) (PanelConfig, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config: %w", err)
		}
	}
	applyEnv(&cfg)
	applyDefaults(&cfg)
	return cfg, nil
}

// firstEnv returns the first non-empty value among the given env var names.
// Short, unprefixed names (ADMIN, ADMIN_PASSWORD, JWT_SECRET, BASE_URL) are
// accepted alongside the SINGPANEL_-prefixed ones for a simpler .env.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

func applyEnv(cfg *PanelConfig) {
	if v := firstEnv("LISTEN", "SINGPANEL_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := firstEnv("WEB", "BASE_URL", "SINGPANEL_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := firstEnv("JWT_SECRET", "SINGPANEL_JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
	}
	if v := firstEnv("SINGPANEL_AGENTS_DIR"); v != "" {
		cfg.AgentsDir = v
	}
	if v := firstEnv("SINGPANEL_DB_DRIVER"); v != "" {
		cfg.Database.Driver = v
	}
	if v := firstEnv("SINGPANEL_DB_DSN"); v != "" {
		cfg.Database.DSN = v
	}
	if v := firstEnv("ADMIN", "SINGPANEL_ADMIN_EMAIL"); v != "" {
		cfg.Admin.Email = v
	}
	if v := firstEnv("ADMIN_PASSWORD", "SINGPANEL_ADMIN_PASSWORD"); v != "" {
		cfg.Admin.Password = v
	}
}

func applyDefaults(cfg *PanelConfig) {
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.Database.Driver == "" {
		cfg.Database.Driver = "sqlite"
	}
	if cfg.Database.Driver == "sqlite" && cfg.Database.DSN == "" {
		cfg.Database.DSN = "./data/singpanel.db"
	}
	if cfg.Subscription.PathPrefix == "" {
		cfg.Subscription.PathPrefix = "/api/sub"
	}
	if cfg.AgentsDir == "" {
		cfg.AgentsDir = "./dist/agents"
	}
}
