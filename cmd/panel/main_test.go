package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRuntimeConfigUsesRecoveredSigningKey(t *testing.T) {
	const oldSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const newSecret = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	for _, tc := range []struct {
		name       string
		configured string
		want       string
		rollback   bool
		sidecar    bool
	}{
		{name: "finish YAML restore", configured: oldSecret, want: newSecret},
		{name: "rollback YAML restore", configured: newSecret, want: oldSecret, rollback: true},
		{name: "finish sidecar restore", configured: oldSecret, want: newSecret, sidecar: true},
		{name: "rollback sidecar restore", configured: newSecret, want: oldSecret, rollback: true, sidecar: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("JWT_SECRET", "")
			t.Setenv("SINGBOX_PANEL_JWT_SECRET", "")
			t.Setenv("BASE_URL", "https://panel.example.com")
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "singbox-panel.db")
			t.Setenv("SINGBOX_PANEL_DB_DSN", dbPath)
			data := []byte("database bytes after interrupted restore")
			if err := os.WriteFile(dbPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			hash := hex.EncodeToString(sum[:])
			marker := map[string]any{
				"version": 1, "target_sha256": hash,
				"jwt_secret": newSecret, "previous_jwt_secret": oldSecret,
			}
			if tc.rollback {
				marker["target_sha256"] = "a-different-target"
				marker["rollback_sha256"] = hash
			}
			raw, err := json.Marshal(marker)
			if err != nil {
				t.Fatal(err)
			}
			markerPath := dbPath + ".restore-pending.json"
			if err := os.WriteFile(markerPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			cfgPath := ""
			if tc.sidecar {
				if err := os.WriteFile(filepath.Join(dir, ".jwt_secret"), []byte(tc.configured), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				cfgPath = filepath.Join(dir, "panel.yaml")
				if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("jwt_secret: %q\n", tc.configured)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := loadRuntimeConfig(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.JWTSecret != tc.want {
				t.Fatal("runtime signing key does not match the recovered database")
			}
			if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
				t.Fatalf("restore marker still exists: %v", err)
			}
			again, err := loadRuntimeConfig(cfgPath)
			if err != nil || again.JWTSecret != cfg.JWTSecret {
				t.Fatalf("signing key changed on the next restart: %v", err)
			}
		})
	}
}
