package panel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/singpanel/singpanel/internal/config"
)

func TestResolveJWTSecretPersistsAndReuses(t *testing.T) {
	dir := t.TempDir()
	cfg := config.PanelConfig{}
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = filepath.Join(dir, "singpanel.db")

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
	cfg.Database.DSN = filepath.Join(dir, "singpanel.db")

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
