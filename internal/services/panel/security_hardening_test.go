package panel

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestRequestIDMiddlewareAddsResponseHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(requestIDMiddleware())
	router.GET("/health", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusNoContent || recorder.Header().Get("X-Request-ID") == "" {
		t.Fatalf("status=%d request-id=%q", recorder.Code, recorder.Header().Get("X-Request-ID"))
	}
}

func TestClientIPTrustsOnlyLocalProxy(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		realIP     string
		cfIP       string
		forwarded  string
		want       string
	}{
		{
			name: "public peer cannot forge forwarding headers", remoteAddr: "203.0.113.10:1234",
			realIP: "198.51.100.20", cfIP: "198.51.100.21", forwarded: "198.51.100.22", want: "203.0.113.10",
		},
		{
			name: "spoofed Cloudflare header is ignored behind local proxy", remoteAddr: "127.0.0.1:4321",
			realIP: "198.51.100.20", cfIP: "198.51.100.99", want: "198.51.100.20",
		},
		{
			name: "loopback reverse proxy supplies real IP", remoteAddr: "127.0.0.1:4321",
			realIP: "198.51.100.20", want: "198.51.100.20",
		},
		{
			name: "private proxy supplies real IP", remoteAddr: "172.18.0.1:4321",
			realIP: "198.51.100.20", want: "198.51.100.20",
		},
		{
			name: "invalid real IP falls back to proxy peer", remoteAddr: "127.0.0.1:4321",
			realIP: "not-an-ip", want: "127.0.0.1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
			c.Request.RemoteAddr = tc.remoteAddr
			c.Request.Header.Set("X-Real-IP", tc.realIP)
			c.Request.Header.Set("CF-Connecting-IP", tc.cfIP)
			c.Request.Header.Set("X-Forwarded-For", tc.forwarded)
			if got := clientIP(c); got != tc.want {
				t.Fatalf("clientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBindJSONRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/json", func(c *gin.Context) {
		var payload map[string]any
		if bindJSON(c, &payload) {
			c.Status(http.StatusNoContent)
		}
	})
	body := `{"value":"` + strings.Repeat("x", int(maxJSONRequestBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/json", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestPanelHTTPServerHasResourceTimeouts(t *testing.T) {
	srv := newPanelHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != panelReadHeaderTimeout || srv.ReadTimeout != panelReadTimeout || srv.IdleTimeout != panelIdleTimeout {
		t.Fatalf("timeouts = read-header %s read %s idle %s", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 1<<20 {
		t.Fatalf("MaxHeaderBytes = %d", srv.MaxHeaderBytes)
	}
}

func TestSecurityHeadersAndCORSAreScopedToPanelOrigin(t *testing.T) {
	r := gin.New()
	r.Use(securityHeadersMiddleware("https://panel.example.com"), corsMiddleware("https://panel.example.com", "production"))
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	allowed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	r.ServeHTTP(allowed, req)
	if allowed.Code != http.StatusNoContent || allowed.Header().Get("Access-Control-Allow-Origin") != "https://panel.example.com" {
		t.Fatalf("allowed origin response = %d, headers=%v", allowed.Code, allowed.Header())
	}
	for _, name := range []string{"Content-Security-Policy", "Strict-Transport-Security", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if allowed.Header().Get(name) == "" {
			t.Fatalf("missing security header %s", name)
		}
	}

	denied := httptest.NewRecorder()
	deniedReq := httptest.NewRequest(http.MethodOptions, "/", nil)
	deniedReq.Header.Set("Origin", "https://evil.example")
	r.ServeHTTP(denied, deniedReq)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("denied origin status = %d, want 403", denied.Code)
	}
}

func TestSubscriptionLogSkipper(t *testing.T) {
	prefix := "/api/sub/"
	if !subscriptionRequestPath("/api/sub/secret-token", prefix) {
		t.Fatal("subscription path was not recognized")
	}
	if subscriptionRequestPath("/api/subscriptions", prefix) {
		t.Fatal("unrelated API path was recognized as a subscription token")
	}

	var logs bytes.Buffer
	r := gin.New()
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		Output: &logs,
		Skip: func(c *gin.Context) bool {
			return subscriptionRequestPath(c.Request.URL.Path, prefix)
		},
	}))
	r.GET("/*path", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for _, path := range []string{"/api/sub/secret-token", "/api/health"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if strings.Contains(logs.String(), "secret-token") {
		t.Fatalf("subscription token leaked into request log: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "/api/health") {
		t.Fatalf("ordinary request was not logged: %s", logs.String())
	}
}

func TestDatabaseLoggerRedactsQueryParameters(t *testing.T) {
	const token = "subscription-token-must-not-appear"
	var logs bytes.Buffer
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "security.db")), &gorm.Config{
		Logger: newDatabaseLogger(log.New(&logs, "", 0), gormlogger.Warn),
	})
	if err != nil {
		t.Fatal(err)
	}
	var ignored []map[string]any
	err = db.Raw("SELECT * FROM missing_table WHERE sub_token = ?", token).Scan(&ignored).Error
	if err == nil {
		t.Fatal("query unexpectedly succeeded")
	}
	if logs.Len() == 0 {
		t.Fatal("failing query was not logged")
	}
	if strings.Contains(logs.String(), token) {
		t.Fatalf("database query parameter leaked into log: %s", logs.String())
	}
}

func TestSQLiteDatabaseAndJournalsAreOwnerOnly(t *testing.T) {
	database := filepath.Join(t.TempDir(), "panel.db")
	for _, path := range []string{database, database + "-wal", database + "-shm"} {
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := protectSQLiteFiles(database); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{database, database + "-wal", database + "-shm"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %o, want 600", filepath.Base(path), got)
		}
	}
}
