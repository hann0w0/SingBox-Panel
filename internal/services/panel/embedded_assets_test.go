package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestEmbeddedFrontendTakesPrecedenceOverDiskWebDir(t *testing.T) {
	gin.SetMode(gin.TestMode)
	staleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staleDir, "index.html"), []byte("stale-disk-html"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{
		cfg: config.PanelConfig{WebDir: staleDir},
		embeddedWebFS: fstest.MapFS{
			"index.html":           {Data: []byte("<html>embedded-ui</html>")},
			"manifest.webmanifest": {Data: []byte(`{"name":"SingBox Panel"}`)},
			"sw.js":                {Data: []byte("self.addEventListener('fetch', () => {})")},
			"assets/app.js":        {Data: []byte("console.log('embedded')")},
		},
	}
	r := gin.New()
	app.mountFrontend(r)

	for _, tc := range []struct {
		path      string
		wantBody  string
		wantCache string
	}{
		{"/", "embedded-ui", "no-store"},
		{"/admin/servers", "embedded-ui", "no-store"},
		{"/sw.js", "addEventListener", "no-cache"},
		{"/manifest.webmanifest", "SingBox Panel", "no-cache"},
		{"/assets/app.js", "console.log('embedded')", ""},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", tc.path, w.Code)
		}
		if !strings.Contains(w.Body.String(), tc.wantBody) {
			t.Fatalf("GET %s body = %q, want substring %q", tc.path, w.Body.String(), tc.wantBody)
		}
		if tc.wantCache != "" && !strings.Contains(w.Header().Get("Cache-Control"), tc.wantCache) {
			t.Fatalf("GET %s Cache-Control = %q, want %q", tc.path, w.Header().Get("Cache-Control"), tc.wantCache)
		}
	}
}

func TestEmbeddedAgentsTakePrecedenceOverDiskAgentsDir(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	if err := db.Create(&model.Server{Name: "node-1", AgentToken: "agent-secret"}).Error; err != nil {
		t.Fatal(err)
	}
	staleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staleDir, "VERSION"), []byte("v0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "singbox-panel-agent-linux-amd64"), []byte("stale-disk-agent"), 0o755); err != nil {
		t.Fatal(err)
	}

	embeddedBin := []byte("embedded-amd64-agent-binary")
	sumBytes := sha256.Sum256(embeddedBin)
	wantSum := hex.EncodeToString(sumBytes[:])

	app := &App{
		db:  db,
		cfg: config.PanelConfig{AgentsDir: staleDir},
		embeddedAgentsFS: fstest.MapFS{
			"VERSION":                         {Data: []byte("v1.0.17\n")},
			"singbox-panel-agent-linux-amd64": {Data: embeddedBin},
			"singbox-panel-agent-linux-arm64": {Data: []byte("embedded-arm64-agent-binary")},
		},
	}

	if got := app.latestAgentVersion(); got != "v1.0.17" {
		t.Fatalf("latestAgentVersion() = %q, want v1.0.17", got)
	}

	r := gin.New()
	r.GET("/api/agent/download", app.handleAgentDownload)
	r.GET("/api/agent/checksum", app.handleAgentChecksum)

	req := httptest.NewRequest(http.MethodGet, "/api/agent/checksum?arch=amd64", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != wantSum {
		t.Fatalf("checksum = %d %q, want 200 %q", w.Code, w.Body.String(), wantSum)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/agent/download?arch=amd64", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != string(embeddedBin) {
		t.Fatalf("download = %d %q, want 200 %q", w.Code, w.Body.String(), embeddedBin)
	}
	if got := w.Header().Get("X-SingBox-Panel-SHA256"); got != wantSum {
		t.Fatalf("X-SingBox-Panel-SHA256 = %q, want %q", got, wantSum)
	}
}
