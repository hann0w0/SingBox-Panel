// Package config loads panel configuration from a YAML file with env overrides.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PanelConfig is the panel server configuration.
type PanelConfig struct {
	// Environment controls security-sensitive development exceptions. Production
	// rejects insecure public URLs; development may use localhost HTTP.
	Environment string `yaml:"environment"`
	// Listen is the HTTP listen address, e.g. ":8080".
	Listen string `yaml:"listen"`
	// BaseURL is the public origin of the panel (used to build subscription
	// links and the agent one-line install command), e.g. https://panel.example.com.
	BaseURL string `yaml:"base_url"`
	// JWTSecret signs admin/user session tokens. Auto-generated if empty.
	JWTSecret string `yaml:"jwt_secret"`

	// AgentsDir holds prebuilt agent binaries served to VPSes by the one-line
	// installer, named singbox-panel-agent-linux-<arch>.
	AgentsDir string `yaml:"agents_dir"`

	// WebDir is the path to the compiled frontend static files (web/dist).
	// Defaults to "./web/dist"; override with SINGBOX_PANEL_WEB_DIR env or
	// the web_dir YAML field for binary installs.
	WebDir string `yaml:"web_dir"`

	Database     DatabaseConfig `yaml:"database"`
	Admin        AdminBootstrap `yaml:"admin"`
	Subscription SubConfig      `yaml:"subscription"`
}

// DatabaseConfig selects the backing store.
type DatabaseConfig struct {
	// Driver: sqlite | mysql | postgres.
	Driver string `yaml:"driver"`
	// DSN: for sqlite, a file path (e.g. ./data/singbox-panel.db); otherwise a full DSN.
	DSN string `yaml:"dsn"`
	// Pool settings apply to MySQL and Postgres. SQLite is always constrained
	// to one connection to preserve deterministic transaction ordering.
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
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
		Environment: "production",
		Listen:      ":8080",
		BaseURL:     "http://localhost:8080",
		AgentsDir:   "./dist/agents",
		WebDir:      "./web/dist",
		Database: DatabaseConfig{
			Driver:          "sqlite",
			DSN:             "./data/singbox-panel.db",
			MaxOpenConns:    25,
			MaxIdleConns:    10,
			ConnMaxLifetime: 30 * time.Minute,
			ConnMaxIdleTime: 5 * time.Minute,
		},
		Subscription: SubConfig{
			PathPrefix: "/api/sub",
		},
	}
}

// Load reads a YAML config file, applies defaults for missing fields, then
// overlays environment variables (SINGBOX_PANEL_*).
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
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}
	applyDefaults(&cfg)
	return cfg, nil
}

// firstEnv returns the first non-empty value among the given env var names.
// Short, unprefixed names (ADMIN, ADMIN_PASSWORD, JWT_SECRET, BASE_URL) are
// accepted alongside the SINGBOX_PANEL_-prefixed ones for a simpler .env.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

func applyEnv(cfg *PanelConfig) error {
	if v := firstEnv("SINGBOX_PANEL_ENV"); v != "" {
		cfg.Environment = v
	}
	if v := firstEnv("LISTEN", "SINGBOX_PANEL_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := firstEnv("WEB", "BASE_URL", "SINGBOX_PANEL_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := firstEnv("JWT_SECRET", "SINGBOX_PANEL_JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
	}
	if v := firstEnv("SINGBOX_PANEL_AGENTS_DIR"); v != "" {
		cfg.AgentsDir = v
	}
	if v := firstEnv("SINGBOX_PANEL_WEB_DIR"); v != "" {
		cfg.WebDir = v
	}
	if v := firstEnv("SINGBOX_PANEL_DB_DRIVER"); v != "" {
		cfg.Database.Driver = v
	}
	if v := firstEnv("SINGBOX_PANEL_DB_DSN"); v != "" {
		cfg.Database.DSN = v
	}
	for _, item := range []struct {
		name string
		dest *int
	}{
		{"SINGBOX_PANEL_DB_MAX_OPEN_CONNS", &cfg.Database.MaxOpenConns},
		{"SINGBOX_PANEL_DB_MAX_IDLE_CONNS", &cfg.Database.MaxIdleConns},
	} {
		if v := firstEnv(item.name); v != "" {
			parsed, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s must be an integer: %w", item.name, err)
			}
			*item.dest = parsed
		}
	}
	for _, item := range []struct {
		name string
		dest *time.Duration
	}{
		{"SINGBOX_PANEL_DB_CONN_MAX_LIFETIME", &cfg.Database.ConnMaxLifetime},
		{"SINGBOX_PANEL_DB_CONN_MAX_IDLE_TIME", &cfg.Database.ConnMaxIdleTime},
	} {
		if v := firstEnv(item.name); v != "" {
			parsed, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s must be a duration: %w", item.name, err)
			}
			*item.dest = parsed
		}
	}
	if v := firstEnv("ADMIN", "SINGBOX_PANEL_ADMIN_EMAIL"); v != "" {
		cfg.Admin.Email = v
	}
	if v := firstEnv("ADMIN_PASSWORD", "SINGBOX_PANEL_ADMIN_PASSWORD"); v != "" {
		cfg.Admin.Password = v
	}
	return nil
}

func applyDefaults(cfg *PanelConfig) {
	if cfg.Environment == "" {
		cfg.Environment = "production"
	}
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.Database.Driver == "" {
		cfg.Database.Driver = "sqlite"
	}
	if cfg.Database.Driver == "sqlite" && cfg.Database.DSN == "" {
		cfg.Database.DSN = "./data/singbox-panel.db"
	}
	if cfg.Database.MaxOpenConns == 0 {
		cfg.Database.MaxOpenConns = 25
	}
	if cfg.Database.MaxIdleConns == 0 {
		cfg.Database.MaxIdleConns = 10
	}
	if cfg.Database.ConnMaxLifetime == 0 {
		cfg.Database.ConnMaxLifetime = 30 * time.Minute
	}
	if cfg.Database.ConnMaxIdleTime == 0 {
		cfg.Database.ConnMaxIdleTime = 5 * time.Minute
	}
	if cfg.Subscription.PathPrefix == "" {
		cfg.Subscription.PathPrefix = "/api/sub"
	}
	if cfg.AgentsDir == "" {
		cfg.AgentsDir = "./dist/agents"
	}
	if cfg.WebDir == "" {
		cfg.WebDir = "./web/dist"
	}
}

// Validate checks settings that must be safe before the HTTP server starts.
// TLS terminates at the configured reverse proxy, so the panel itself still
// listens on HTTP internally; BaseURL is the externally reachable origin.
func (cfg PanelConfig) Validate() error {
	if cfg.Database.MaxOpenConns < 0 || cfg.Database.MaxIdleConns < 0 ||
		cfg.Database.ConnMaxLifetime < 0 || cfg.Database.ConnMaxIdleTime < 0 {
		return fmt.Errorf("database pool settings must not be negative")
	}
	if cfg.Database.MaxOpenConns > 0 && cfg.Database.MaxIdleConns > cfg.Database.MaxOpenConns {
		return fmt.Errorf("database max_idle_conns must not exceed max_open_conns")
	}
	env := strings.ToLower(strings.TrimSpace(cfg.Environment))
	if env == "" {
		env = "production"
	}
	if env != "production" && env != "development" {
		return fmt.Errorf("environment must be production or development")
	}
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || u.Host == "" {
		return fmt.Errorf("base_url must be an absolute URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base_url must not contain credentials, query, or fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("base_url must be an origin without a path")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("base_url scheme must be https or http")
	}
	if env != "development" && u.Scheme != "https" {
		return fmt.Errorf("production base_url must use HTTPS; set SINGBOX_PANEL_ENV=development only for local HTTP testing")
	}
	return nil
}
