package panel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/model"
)

// makeBackupArchive builds an in-memory .tar.gz like downloadBackup produces:
// a real SQLite DB (seeded via a temp gorm) plus a jwt_secret entry.
func makeBackupArchive(t *testing.T, secret string, seed func(db *gorm.DB)) []byte {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "src.db")
	db := testDBAt(t, dbPath)
	if seed != nil {
		seed(db)
	}
	// Close underlying handle so the file is fully flushed before we read it.
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	writeEntry := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	writeEntry("singbox-panel.db", dbBytes)
	if secret != "" {
		writeEntry("jwt_secret", []byte(secret+"\n"))
	}
	writeEntry("MANIFEST.txt", []byte("test manifest"))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func uploadRestore(t *testing.T, a *App, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	// Stub the restart so the test binary does not exit itself.
	restarted := make(chan struct{}, 1)
	orig := scheduleRestartFn
	scheduleRestartFn = func() {
		select {
		case restarted <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { scheduleRestartFn = orig })

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "backup.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(archive); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	r := gin.New()
	r.POST("/restore", a.restoreBackup)
	req := httptest.NewRequest(http.MethodPost, "/restore", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A restore must replace the live DB with the archive's contents and write the
// imported jwt_secret into panel.yaml. We disable the exit-on-restart by running
// on non-linux test hosts; on Linux CI selfUpdateSupported() is false without a
// systemd unit, so the goroutine that calls os.Exit never fires either.
func TestRestoreReplacesDBAndSecret(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	cfgPath := filepath.Join(dir, "panel.yaml")

	// Live DB: one server named "old". Keep the pool OPEN — this mirrors the
	// running panel, and the restore path itself does the rollback snapshot and
	// close in the right order.
	live := testDBAt(t, dbPath)
	if err := live.Create(&model.Server{Name: "old", AgentToken: "old-tok"}).Error; err != nil {
		t.Fatal(err)
	}

	os.WriteFile(cfgPath, []byte("listen: \"127.0.0.1:32334\"\njwt_secret: \"OLDSECRET\"\n"), 0o600)

	// Archive: a different server named "new" + a new secret.
	archive := makeBackupArchive(t, "NEWSECRET-from-other-host", func(db *gorm.DB) {
		if err := db.Create(&model.Server{Name: "new", AgentToken: "new-tok"}).Error; err != nil {
			t.Fatal(err)
		}
	})

	cfg := config.PanelConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}}
	a := &App{cfg: cfg, db: live, version: "v1.0.0", cfgPath: cfgPath}

	w := uploadRestore(t, a, archive)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	// The restored DB must now contain "new", not "old".
	restored := testDBAt(t, dbPath)
	var names []string
	restored.Model(&model.Server{}).Order("id").Pluck("name", &names)
	if len(names) != 1 || names[0] != "new" {
		t.Errorf("restored servers = %v, want [new]", names)
	}

	// The rollback copy must exist and still hold the old data, so an operator
	// can recover if the restored backup turns out to be wrong.
	matches, _ := filepath.Glob(dbPath + ".pre-restore-*")
	if len(matches) != 1 {
		t.Fatalf("expected 1 pre-restore backup, got %d", len(matches))
	}
	rb := testDBAt(t, matches[0])
	var rbNames []string
	rb.Model(&model.Server{}).Pluck("name", &rbNames)
	if len(rbNames) != 1 || rbNames[0] != "old" {
		t.Errorf("rollback snapshot servers = %v, want [old]", rbNames)
	}

	// panel.yaml must now carry the imported secret.
	cfgRaw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(cfgRaw), `jwt_secret: "NEWSECRET-from-other-host"`) {
		t.Errorf("jwt_secret not rewritten; config = %s", cfgRaw)
	}
	if strings.Contains(string(cfgRaw), "OLDSECRET") {
		t.Errorf("old secret still present")
	}
}

func TestRestoreRejectsNonSQLiteArchive(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	live := testDBAt(t, dbPath)

	// Archive whose "singbox-panel.db" is not actually SQLite.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	junk := []byte("this is not a database")
	tw.WriteHeader(&tar.Header{Name: "singbox-panel.db", Mode: 0o600, Size: int64(len(junk))})
	tw.Write(junk)
	tw.Close()
	gz.Close()

	cfg := config.PanelConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}}
	a := &App{cfg: cfg, db: live, version: "v1.0.0", cfgPath: filepath.Join(dir, "panel.yaml")}

	w := uploadRestore(t, a, buf.Bytes())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	// The junk must not have overwritten the live DB.
	if err := validateSQLiteFile(dbPath); err != nil {
		t.Errorf("live DB was clobbered by invalid restore: %v", err)
	}
}

func TestRestoreRejectsArchiveWithoutDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	live := testDBAt(t, dbPath)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "jwt_secret", Mode: 0o600, Size: 3})
	tw.Write([]byte("abc"))
	tw.Close()
	gz.Close()

	cfg := config.PanelConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}}
	a := &App{cfg: cfg, db: live, version: "v1.0.0"}
	w := uploadRestore(t, a, buf.Bytes())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestValidateSQLiteFileRejectsCorruptDatabaseWithMagicHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	data := append([]byte(sqliteMagic), bytes.Repeat([]byte{0}, 128)...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSQLiteFile(path); err == nil {
		t.Fatal("database with only a valid magic header must be rejected")
	}
}

func TestValidateSQLiteFileRejectsDirtySchema(t *testing.T) {
	path := panelDBWithMigrationRows(t, []model.SchemaMigration{{
		Version:   1,
		Name:      "dirty",
		Dirty:     true,
		AppliedAt: time.Now(),
	}})
	if err := validateSQLiteFile(path); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("error = %v, want dirty migration rejection", err)
	}
}

func TestValidateSQLiteFileRejectsFutureSchema(t *testing.T) {
	future := applicationMigrations[len(applicationMigrations)-1].version + 1
	path := panelDBWithMigrationRows(t, []model.SchemaMigration{{
		Version:   future,
		Name:      "future",
		AppliedAt: time.Now(),
	}})
	if err := validateSQLiteFile(path); err == nil || !strings.Contains(err.Error(), "newer than or unknown") {
		t.Fatalf("error = %v, want future migration rejection", err)
	}
}

func TestValidateSQLiteFileRejectsNonPrefixSchema(t *testing.T) {
	path := panelDBWithMigrationRows(t, []model.SchemaMigration{
		{Version: 1, Name: "one", AppliedAt: time.Now()},
		{Version: 3, Name: "three", AppliedAt: time.Now()},
	})
	if err := validateSQLiteFile(path); err == nil || !strings.Contains(err.Error(), "ordered prefix") {
		t.Fatalf("error = %v, want non-prefix migration rejection", err)
	}
}

func TestValidateSQLiteFileAcceptsLegacyPanelDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db := testDBAt(t, path)
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	if err := validateSQLiteFile(path); err != nil {
		t.Fatalf("legacy panel database rejected: %v", err)
	}
}

func TestValidateSQLiteFileRejectsUnrelatedSQLiteDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unrelated.db")
	db := testDBAt(t, path)
	if err := db.Migrator().DropTable(&model.User{}); err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	if err := validateSQLiteFile(path); err == nil || !strings.Contains(err.Error(), "users") {
		t.Fatalf("error = %v, want missing core table rejection", err)
	}
}

func TestExtractBackupRejectsOversizedDatabaseEntry(t *testing.T) {
	archive := archiveWithDeclaredEntry(t, "singbox-panel.db", maxBackupDatabase+1)
	if _, _, err := extractBackup(bytes.NewReader(archive), t.TempDir()); err == nil || !strings.Contains(err.Error(), "数据库文件过大") {
		t.Fatalf("error = %v, want oversized database rejection", err)
	}
}

func TestExtractBackupRejectsOversizedSecretEntry(t *testing.T) {
	archive := archiveWithDeclaredEntry(t, "jwt_secret", maxBackupSecret+1)
	if _, _, err := extractBackup(bytes.NewReader(archive), t.TempDir()); err == nil || !strings.Contains(err.Error(), "jwt_secret 过大") {
		t.Fatalf("error = %v, want oversized secret rejection", err)
	}
}

func TestExtractBackupRejectsTruncatedArchive(t *testing.T) {
	archive := makeBackupArchive(t, "test-secret", nil)
	archive = archive[:len(archive)-4]
	if _, _, err := extractBackup(bytes.NewReader(archive), t.TempDir()); err == nil {
		t.Fatal("truncated gzip/tar archive must be rejected")
	}
}

func TestRestoreWithoutConfigPathPersistsSecretSidecar(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("SINGBOX_PANEL_JWT_SECRET", "")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	live := testDBAt(t, dbPath)
	archive := makeBackupArchive(t, "restored-sidecar-secret", nil)

	a := &App{
		cfg: config.PanelConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}},
		db:  live,
	}
	w := uploadRestore(t, a, archive)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	secretPath := filepath.Join(dir, jwtSecretFile)
	raw, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != "restored-sidecar-secret" {
		t.Fatalf("sidecar secret = %q", raw)
	}
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar mode = %o, want 600", info.Mode().Perm())
	}
}

func TestPersistRestoredJWTSecretWarnsWhenEnvironmentDiffers(t *testing.T) {
	t.Setenv("JWT_SECRET", "environment-secret")
	t.Setenv("SINGBOX_PANEL_JWT_SECRET", "")
	dbPath := filepath.Join(t.TempDir(), "singbox-panel.db")
	applied, warning := persistRestoredJWTSecret("", dbPath, "imported-secret")
	if applied || !strings.Contains(warning, "JWT_SECRET") || !strings.Contains(warning, "不一致") {
		t.Fatalf("applied = %v, warning = %q", applied, warning)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dbPath), jwtSecretFile)); !os.IsNotExist(err) {
		t.Fatalf("environment-backed secret must not create a sidecar: %v", err)
	}
}

func TestPruneRestoreRollbacksKeepsNewestFive(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 7; i++ {
		path := dbPath + ".pre-restore-" + time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format("20060102T150405.000000000Z")
		if err := os.WriteFile(path, []byte("snapshot"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneRestoreRollbacks(dbPath, 5); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(dbPath + ".pre-restore-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 5 {
		t.Fatalf("rollback count = %d, want 5", len(matches))
	}
	for _, removedSecond := range []int{0, 1} {
		removed := dbPath + ".pre-restore-" + time.Date(2026, 1, 1, 0, 0, removedSecond, 0, time.UTC).Format("20060102T150405.000000000Z")
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("old rollback still exists: %s", removed)
		}
	}
}

func TestRewriteJWTSecretAppendsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "panel.yaml")
	os.WriteFile(p, []byte("listen: \"127.0.0.1:32334\"\nbase_url: \"https://x\"\n"), 0o600)
	if err := rewriteJWTSecret(p, "abc123"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), `jwt_secret: "abc123"`) {
		t.Errorf("secret not appended: %s", raw)
	}
	// Existing lines preserved.
	if !strings.Contains(string(raw), "base_url:") {
		t.Errorf("existing config lost: %s", raw)
	}
}

func panelDBWithMigrationRows(t *testing.T, rows []model.SchemaMigration) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "panel.db")
	db := testDBAt(t, path)
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	return path
}

func archiveWithDeclaredEntry(t *testing.T, name string, size int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size}); err != nil {
		t.Fatal(err)
	}
	// Closing the tar writer reports the deliberately missing body. The header
	// is nevertheless complete, which is enough to exercise size validation
	// before extraction attempts to read or allocate the declared entry.
	_ = tw.Close()
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
