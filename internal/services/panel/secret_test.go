package panel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/config"
)

func TestResolveJWTSecretPersistsAndReuses(t *testing.T) {
	dir := t.TempDir()
	cfg := config.PanelConfig{}
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = filepath.Join(dir, "singbox-panel.db")

	first, err := ResolveJWTSecret(cfg)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if len(first) < minSecretLen {
		t.Fatalf("generated secret too short: %d", len(first))
	}

	// The secret file must be created with owner-only permissions.
	fi, err := os.Stat(filepath.Join(dir, jwtSecretFile))
	if err != nil {
		t.Fatalf("secret file not written: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("secret file mode = %o, want 600", perm)
	}

	// A second call must return the same persisted secret, so sessions
	// survive restarts.
	second, err := ResolveJWTSecret(cfg)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if second != first {
		t.Errorf("secret changed across calls: %q != %q", first, second)
	}
}

func TestResolveJWTSecretExplicitWins(t *testing.T) {
	dir := t.TempDir()
	cfg := config.PanelConfig{JWTSecret: "an-explicitly-configured-secret-value"}
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = filepath.Join(dir, "singbox-panel.db")

	got, err := ResolveJWTSecret(cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != cfg.JWTSecret {
		t.Errorf("got %q, want explicit %q", got, cfg.JWTSecret)
	}
	if _, err := os.Stat(filepath.Join(dir, jwtSecretFile)); !os.IsNotExist(err) {
		t.Errorf("secret file should not be created when JWT_SECRET is set")
	}
}

func TestResolveJWTSecretRequiresExplicitSecretForNetworkDatabase(t *testing.T) {
	for _, driver := range []string{"mysql", "postgres"} {
		cfg := config.PanelConfig{Database: config.DatabaseConfig{Driver: driver, DSN: "example"}}
		if _, err := ResolveJWTSecret(cfg); err == nil {
			t.Fatalf("%s without jwt_secret was accepted", driver)
		}
	}
}

func TestResolveJWTSecretParsesFileURIAndInMemoryDSN(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	uriCfg := config.PanelConfig{
		Database: config.DatabaseConfig{
			Driver: "sqlite",
			DSN:    "file:" + filepath.ToSlash(dbPath) + "?cache=shared&_pragma=busy_timeout(5000)",
		},
	}
	secret, err := ResolveJWTSecret(uriCfg)
	if err != nil {
		t.Fatalf("ResolveJWTSecret(file URI): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, jwtSecretFile)); err != nil {
		t.Fatalf("expected sidecar in %s: %v", dir, err)
	}
	if _, err := os.Stat("file:"); err == nil {
		t.Fatalf("unexpected literal file: directory created")
	}
	again, err := ResolveJWTSecret(uriCfg)
	if err != nil || again != secret {
		t.Fatalf("second ResolveJWTSecret(file URI) = %q, err = %v; want %q", again, err, secret)
	}

	wd := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origWD) }()

	memCfg := config.PanelConfig{
		Database: config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"},
	}
	memSecret, err := ResolveJWTSecret(memCfg)
	if err != nil || len(memSecret) < minSecretLen {
		t.Fatalf("ResolveJWTSecret(:memory:) = %q, err = %v", memSecret, err)
	}
	if _, err := os.Stat(filepath.Join(wd, jwtSecretFile)); !os.IsNotExist(err) {
		t.Fatalf(":memory: DSN must not write sidecar into cwd: %v", err)
	}
}
