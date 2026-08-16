package panel

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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
	"github.com/hann0w0/singbox-panel/internal/model"
)

func TestValidReleaseTag(t *testing.T) {
	good := []string{"v1.0.0", "v1.2.3-beta1", "1.0.0", "v10.20.30_rc2"}
	bad := []string{"", "v1.0.0; rm -rf /", "v1 0", "$(whoami)", "v1.0.0/../../etc", "v1.0.0`id`", strings.Repeat("v", 65)}
	for _, s := range good {
		if !validReleaseTag(s) {
			t.Errorf("validReleaseTag(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if validReleaseTag(s) {
			t.Errorf("validReleaseTag(%q) = true, want false", s)
		}
	}
}

func TestSameVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.0.0", "1.0.0", true},
		{"v1.0.0", "v1.0.0", true},
		{"1.2.3", "1.2.3", true},
		{"v1.0.0", "v1.0.1", false},
		{"v1.0.0", "", false},
	}
	for _, c := range cases {
		if got := sameVersion(c.a, c.b); got != c.want {
			t.Errorf("sameVersion(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// maintenanceInfo must always report db_driver and a non-negative uptime, even
// on the SQLite default where the driver is left blank in config.
func TestMaintenanceInfoReportsDriverAndUptime(t *testing.T) {
	a := &App{
		cfg:       config.PanelConfig{Database: config.DatabaseConfig{Driver: ""}}, // blank => sqlite
		db:        testDB(t),
		version:   "v1.0.0",
		startedAt: time.Now().Add(-90 * time.Second),
	}
	r := gin.New()
	r.GET("/info", a.maintenanceInfo)
	req := httptest.NewRequest(http.MethodGet, "/info", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["db_driver"] != "sqlite" {
		t.Errorf("db_driver = %v, want sqlite", got["db_driver"])
	}
	if up, _ := got["uptime_seconds"].(float64); up < 80 {
		t.Errorf("uptime_seconds = %v, want >= 80", got["uptime_seconds"])
	}
}

func TestInstallDirFromDSN(t *testing.T) {
	cases := map[string]string{
		"/opt/singbox-panel/data/singbox-panel.db": "/opt/singbox-panel",
		"/srv/panel/data/singbox-panel.db":         "/srv/panel",
		"":                                         "/opt/singbox-panel", // memory/empty falls back
	}
	for dsn, want := range cases {
		if got := installDirFromDSN(dsn); got != want {
			t.Errorf("installDirFromDSN(%q) = %q, want %q", dsn, got, want)
		}
	}
}

// shellQuote (shared with agentinstall) must neutralize embedded quotes so the
// swap script cannot be broken out of. Guard the property the self-update path
// relies on.
func TestShellQuoteNeutralizesInjection(t *testing.T) {
	got := shellQuote("v1.0.0'; rm -rf /; echo '")
	if strings.Contains(got, "; rm -rf /") && !strings.Contains(got, `'"'"'`) {
		t.Fatalf("shellQuote failed to escape: %s", got)
	}
	// The result must start and end with a single quote (one shell word).
	if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
		t.Fatalf("shellQuote result is not a single quoted word: %s", got)
	}
}

// The backup endpoint must stream a gzip tar containing the DB snapshot and the
// jwt_secret — the two things a migration needs so agents auto-reconnect.
func TestDownloadBackupContainsDBAndSecret(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	db := testDBAt(t, dbPath)
	// Seed a server so the snapshot is non-trivial and carries an agent token.
	if err := db.Create(&model.Server{Name: "n1", AgentToken: "tok-abc"}).Error; err != nil {
		t.Fatal(err)
	}

	cfg := config.PanelConfig{
		JWTSecret: "test-secret-value",
		Database:  config.DatabaseConfig{Driver: "sqlite", DSN: dbPath},
	}
	a := &App{cfg: cfg, db: db, version: "v1.0.0"}

	r := gin.New()
	r.GET("/backup", a.downloadBackup)
	req := httptest.NewRequest(http.MethodGet, "/backup", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type = %q, want application/gzip", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, ".tar.gz") {
		t.Errorf("Content-Disposition = %q, want a .tar.gz filename", cd)
	}

	names, contents := readTarGz(t, w.Body.Bytes())
	if _, ok := names["singbox-panel.db"]; !ok {
		t.Errorf("backup missing singbox-panel.db; got %v", keys(names))
	}
	if got := contents["jwt_secret"]; strings.TrimSpace(got) != "test-secret-value" {
		t.Errorf("jwt_secret = %q, want test-secret-value", got)
	}
	if _, ok := names["MANIFEST.txt"]; !ok {
		t.Errorf("backup missing MANIFEST.txt")
	}
	if _, ok := names["backup.json"]; !ok {
		t.Fatalf("backup missing backup.json")
	}
	var metadata backupMetadata
	if err := json.Unmarshal([]byte(contents["backup.json"]), &metadata); err != nil {
		t.Fatalf("backup.json: %v", err)
	}
	if metadata.FormatVersion != backupFormatVersion || metadata.SchemaVersion != currentSchemaVersion() || metadata.PanelVersion != "v1.0.0" {
		t.Fatalf("backup metadata = %#v", metadata)
	}
	dbSum := sha256.Sum256([]byte(contents["singbox-panel.db"]))
	if metadata.DatabaseSHA256 != hex.EncodeToString(dbSum[:]) {
		t.Fatalf("database sha256 = %q, want %x", metadata.DatabaseSHA256, dbSum)
	}
	if metadata.CredentialVersion != backupCredentialVersion || metadata.CredentialCount != 0 || metadata.CredentialSHA256 == "" {
		t.Fatalf("credential metadata = %#v", metadata)
	}
	// The DB snapshot must be a real SQLite file (magic header), not empty.
	if dbBytes := contents["singbox-panel.db"]; !strings.HasPrefix(dbBytes, "SQLite format 3") {
		t.Errorf("DB snapshot is not a valid SQLite file (len=%d)", len(dbBytes))
	}
}

func TestDownloadBackupRejectsNonSQLite(t *testing.T) {
	cfg := config.PanelConfig{Database: config.DatabaseConfig{Driver: "mysql", DSN: "user:pass@/db"}}
	a := &App{cfg: cfg, db: testDB(t), version: "v1.0.0"}
	r := gin.New()
	r.GET("/backup", a.downloadBackup)
	req := httptest.NewRequest(http.MethodGet, "/backup", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// --- helpers ---

func testDBAt(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	return db
}

func readTarGz(t *testing.T, data []byte) (map[string]bool, map[string]string) {
	t.Helper()
	gz, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	contents := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		names[hdr.Name] = true
		b, _ := io.ReadAll(tr)
		contents[hdr.Name] = string(b)
	}
	return names, contents
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
