package panel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hann0w0/singbox-panel/internal/config"
)

// jwtSecretFile holds the auto-generated JWT secret, stored next to the SQLite
// database so it survives service restarts (an ephemeral secret would log
// every user out on each restart).
const jwtSecretFile = ".jwt_secret"

// ResolveJWTSecret returns the secret used to sign session tokens.
//
// An explicit JWT_SECRET (config or env) always wins. Otherwise, for the
// default SQLite deployment, a secret is generated once and persisted beside
// the database so operators never have to supply one and sessions survive
// restarts. A non-file database without an explicit secret falls back to an
// ephemeral secret (sessions reset on restart); set JWT_SECRET to avoid that.
func ResolveJWTSecret(cfg config.PanelConfig) (string, error) {
	if strings.TrimSpace(cfg.JWTSecret) != "" {
		return cfg.JWTSecret, nil
	}
	if cfg.Database.Driver != "sqlite" && cfg.Database.Driver != "" {
		return "", nil // ephemeral; advanced setups should set JWT_SECRET
	}

	dir := filepath.Dir(cfg.Database.DSN)
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, jwtSecretFile)

	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
	}

	secret := randHex(32)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create data dir for jwt secret: %w", err)
	}
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("persist jwt secret: %w", err)
	}
	return secret, nil
}
