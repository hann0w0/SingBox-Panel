package panel

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestRateLimitPeerKeyAndEvictionPrioritizesIPKeys(t *testing.T) {
	if got := rateLimitPeerKey("2001:db8:abcd:0012:1:2:3:4"); got != "2001:db8:abcd:12::/64" {
		t.Fatalf("IPv6 peer key = %q, want 2001:db8:abcd:12::/64", got)
	}
	if got := rateLimitPeerKey("203.0.113.5"); got != "203.0.113.5" {
		t.Fatalf("IPv4 peer key = %q, want 203.0.113.5", got)
	}

	g := newLoginGuard()
	now := time.Now()
	g.entries["acct:victim"] = &guardEntry{fails: 8, lastFail: now.Add(-10 * time.Minute), blocked: now.Add(time.Minute)}
	g.entries["ip:203.0.113.9"] = &guardEntry{fails: 2, lastFail: now.Add(-2 * time.Minute)}
	g.evictOldestLocked(1)
	if _, ok := g.entries["acct:victim"]; !ok {
		t.Fatal("account guard entry was evicted before ip entry")
	}
	if _, ok := g.entries["ip:203.0.113.9"]; ok {
		t.Fatal("ip entry should have been evicted first")
	}
}

func TestAgentInstallCodeReuseEvictionAndRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	srv := model.Server{Name: "node-a", AgentToken: "tok-a"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{
		db:                db,
		cfg:               config.PanelConfig{BaseURL: "https://panel.example.com"},
		agentInstallCodes: make(map[string]agentInstallCode),
	}

	firstCmd := a.newAgentInstallCommand(srv.ID)
	secondCmd := a.newAgentInstallCommand(srv.ID)
	if firstCmd != secondCmd || len(a.agentInstallCodes) != 1 {
		t.Fatalf("expected unexpired code reuse, got first=%q second=%q count=%d", firstCmd, secondCmd, len(a.agentInstallCodes))
	}

	// Fill up to 4096 entries with distinct expiry order to check deterministic earliest eviction.
	now := time.Now()
	a.agentInstallCodes = make(map[string]agentInstallCode, 4096)
	a.agentInstallCodes["earliest-code"] = agentInstallCode{ServerID: 999, Expires: now.Add(5 * time.Second)}
	for i := 0; i < 4095; i++ {
		a.agentInstallCodes[randHex(8)] = agentInstallCode{ServerID: 1000 + uint(i), Expires: now.Add(time.Minute + time.Duration(i)*time.Millisecond)}
	}
	_ = a.newAgentInstallCommand(srv.ID)
	if _, stillPresent := a.agentInstallCodes["earliest-code"]; stillPresent {
		t.Fatal("earliest-expiring agent install code was not evicted first")
	}

	router := gin.New()
	router.POST("/api/agent/register", a.exchangeAgentInstallCode)
	for i := 0; i < freeAttempts+1; i++ {
		w := postAgentRegistrationCode(router, "wrong-code")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i+1, w.Code)
		}
	}
	throttled := postAgentRegistrationCode(router, "wrong-code")
	if throttled.Code != http.StatusTooManyRequests || throttled.Header().Get("Retry-After") == "" {
		t.Fatalf("expected 429 rate limit after %d failures, got %d", freeAttempts+1, throttled.Code)
	}
}

func TestValidateOneDriveUploadURLAndSQLiteSafetyAndLeafCert(t *testing.T) {
	if _, err := validateOneDriveUploadURL("http://upload.onedrive.com/session"); err == nil {
		t.Fatal("expected HTTP OneDrive upload URL to be rejected")
	}
	if _, err := validateOneDriveUploadURL("https://169.254.169.254/upload"); err == nil {
		t.Fatal("expected literal IP OneDrive upload URL to be rejected")
	}
	if _, err := validateOneDriveUploadURL("https://evil.example.com/upload"); err == nil {
		t.Fatal("expected untrusted host OneDrive upload URL to be rejected")
	}
	if got, err := validateOneDriveUploadURL("https://public.dm.files.1drv.com/session"); err != nil || got == "" {
		t.Fatalf("expected trusted OneDrive upload URL to succeed, got %q err=%v", got, err)
	}

	dsn := sqliteDSN("/tmp/test.db")
	if !strings.Contains(dsn, "_pragma=trusted_schema(0)") || !strings.Contains(dsn, "_pragma=cell_size_check(1)") {
		t.Fatalf("sqliteDSN missing safety pragmas: %s", dsn)
	}

	badDBPath := filepath.Join(t.TempDir(), "trigger.db")
	badDB, err := gorm.Open(sqlite.Open(badDBPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := badDB.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY); CREATE TABLE servers (id INTEGER PRIMARY KEY); CREATE TRIGGER bad AFTER INSERT ON users BEGIN SELECT 1; END;`).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := badDB.DB(); err == nil {
		_ = sqlDB.Close()
	}
	if err := validateSQLiteFile(badDBPath); err == nil || !strings.Contains(err.Error(), "触发器") {
		t.Fatalf("validateSQLiteFile should reject triggers, got: %v", err)
	}

	certPEM, _, err := genSelfSignedCert("edge.example.com")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatal("invalid PEM output from genSelfSignedCert")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.IsCA {
		t.Fatal("self-signed leaf cert must have IsCA=false")
	}
	if cert.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Fatalf("unexpected leaf KeyUsage = %v", cert.KeyUsage)
	}
}

func TestRequestIDTrustSpecialIPsAndSanitizedUserOutboundError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requestIDMiddleware())
	r.GET("/ping", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	trustedReq := httptest.NewRequest(http.MethodGet, "/ping", nil)
	trustedReq.RemoteAddr = "127.0.0.1:4321"
	trustedReq.Header.Set("X-Request-ID", "proxy-req-12345")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, trustedReq)
	if w1.Header().Get("X-Request-ID") != "proxy-req-12345" {
		t.Fatalf("trusted proxy request ID = %q", w1.Header().Get("X-Request-ID"))
	}

	untrustedReq := httptest.NewRequest(http.MethodGet, "/ping", nil)
	untrustedReq.RemoteAddr = "198.51.100.10:4321"
	untrustedReq.Header.Set("X-Request-ID", "forged-req-12345")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, untrustedReq)
	if got := w2.Header().Get("X-Request-ID"); got == "" || got == "forged-req-12345" {
		t.Fatalf("untrusted client forged request ID = %q", got)
	}

	if _, ok := publicAddress("100.64.0.1", false); ok {
		t.Fatal("CGNAT address must not be treated as public")
	}
	if _, ok := publicAddress("2002:7f00:1::1", true); ok {
		t.Fatal("6to4 address must not be treated as public")
	}
	if !isSpecialUseSubscriptionIP(net.ParseIP("2001::1")) {
		t.Fatal("Teredo 2001::1 should be blocked for subscriptions")
	}

	db := testDB(t)
	user := model.User{Email: "alice", Password: "x", Role: model.RoleUser, Enabled: true, SubToken: "s1", ProxyToken: "p1"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Trigger a real 500 inside listUserOutbounds by dropping the outbounds table.
	if err := db.Exec(`DROP TABLE outbounds`).Error; err != nil {
		t.Fatal(err)
	}
	_ = sqlDB
	a := &App{db: db}
	apiRouter := gin.New()
	apiRouter.GET("/user/outbounds", func(c *gin.Context) { c.Set("uid", user.ID) }, a.listUserOutbounds)
	wErr := httptest.NewRecorder()
	apiRouter.ServeHTTP(wErr, httptest.NewRequest(http.MethodGet, "/user/outbounds", nil))
	if wErr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", wErr.Code, wErr.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(wErr.Body.Bytes(), &body)
	if strings.Contains(strings.ToLower(body["error"]), "no such table") || body["error"] != "读取专属出站失败" {
		t.Fatalf("raw SQL error leaked in 500 body: %q", body["error"])
	}
}
