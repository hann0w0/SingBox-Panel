package panel

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestRemoteSubscriptionRejectsSpecialAndNonStandardIPs(t *testing.T) {
	blockedIPs := []string{
		"127.0.0.1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"169.254.169.254",
		"100.64.0.1",
		"100.100.100.200",
		"0.1.2.3",
		"::1",
		"::ffff:127.0.0.1",
		"::ffff:169.254.169.254",
		"64:ff9b::127.0.0.1",
		"64:ff9b:1::1",
		"100::1",
		"fec0::1",
		"fe80::1",
		"2001:db8::1",
	}
	for _, raw := range blockedIPs {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("failed to parse test IP %q", raw)
		}
		if err := requirePublicSubscriptionIPs([]net.IP{ip}); err == nil {
			t.Errorf("non-public IP %q was allowed", raw)
		}
	}

	allowedIPs := []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"}
	for _, raw := range allowedIPs {
		ip := net.ParseIP(raw)
		if err := requirePublicSubscriptionIPs([]net.IP{ip}); err != nil {
			t.Errorf("public IP %q was rejected: %v", raw, err)
		}
	}

	// Non-standard numeric/octal/hex loopback representations must never pass URL validation.
	for _, rawURL := range []string{
		"http://2130706433/sub",
		"http://0177.0.0.1/sub",
		"http://0x7f000001/sub",
		"http://127.1/sub",
		"http://[::ffff:127.0.0.1]/sub",
		"http://[::ffff:7f00:1]/sub",
		"http://[64:ff9b::7f00:1]/sub",
		"http://localhost/sub",
	} {
		parsed, ok := singleRemoteSubscriptionURL(rawURL)
		if !ok {
			continue
		}
		if err := validateRemoteSubscriptionURL(context.Background(), parsed); err == nil {
			t.Errorf("unsafe subscription URL %q was allowed", rawURL)
		}
	}
}

func TestIsWeakSecretRejectsLowEntropyStrings(t *testing.T) {
	for _, weak := range []string{
		"short",
		strings.Repeat("a", 32),
		strings.Repeat("0", 40),
		"abababababababababababababab",
		"my-super-long-password-12345678",
	} {
		if !isWeakSecret(weak) {
			t.Errorf("weak secret %q was accepted", weak)
		}
	}
	if isWeakSecret("0123456789abcdef0123456789abcdef") {
		t.Error("valid hex secret was rejected")
	}
}

func TestResolveJWTSecretTightensWideFilePermissions(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "panel.db")
	secretPath := filepath.Join(dir, jwtSecretFile)
	if err := os.WriteFile(secretPath, []byte("0123456789abcdef0123456789abcdef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveJWTSecret(config.PanelConfig{
		Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath},
	})
	if err != nil || got != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("ResolveJWTSecret = %q, err=%v", got, err)
	}
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret file permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func TestRotateAgentTokenUpdatesTokenAndReturnsInstallCommand(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "node-1", AgentToken: "old-token-0123456789abcdef"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	app := &App{
		db:   db,
		cfg:  config.PanelConfig{BaseURL: "https://panel.example"},
		hub:  NewHub(db),
		orch: NewOrchestrator(db, NewHub(db)),
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/servers/:id/rotate-agent-token", app.rotateAgentToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/servers/1/rotate-agent-token", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "install_command") {
		t.Fatalf("rotateAgentToken = %d %s", w.Code, w.Body.String())
	}
	var updated model.Server
	if err := db.First(&updated, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.AgentToken == "" || updated.AgentToken == "old-token-0123456789abcdef" {
		t.Fatalf("agent token was not rotated: %q", updated.AgentToken)
	}
}
