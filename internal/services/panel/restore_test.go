package panel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

const (
	testLiveJWTSecret     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testRestoredJWTSecret = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

// makeBackupArchive builds an in-memory .tar.gz like downloadBackup produces:
// a real SQLite DB (seeded via a temp gorm) plus a jwt_secret entry.
func makeBackupArchive(t *testing.T, secret string, seed func(db *gorm.DB)) []byte {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "src.db")
	db := testDBAt(t, dbPath)
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	for _, migration := range applicationMigrations {
		if err := db.Create(&model.SchemaMigration{
			Version: migration.version, Name: migration.name, AppliedAt: time.Now(),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	adminHash, err := bcrypt.GenerateFromPassword([]byte("test-backup-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.User{
		Email: "backup-admin", Password: string(adminHash), Role: model.RoleAdmin,
		Enabled: true, SubToken: "backup-admin-sub", ProxyToken: "backup-admin-proxy",
	}).Error; err != nil {
		t.Fatal(err)
	}
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

	os.WriteFile(cfgPath, []byte("listen: \"127.0.0.1:32334\"\njwt_secret: \""+testLiveJWTSecret+"\"\n"), 0o600)

	// Archive: a different server named "new" + a new secret.
	archive := makeBackupArchive(t, testRestoredJWTSecret, func(db *gorm.DB) {
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
	if !strings.Contains(string(cfgRaw), `jwt_secret: "`+testRestoredJWTSecret+`"`) {
		t.Errorf("jwt_secret not rewritten; config = %s", cfgRaw)
	}
	if strings.Contains(string(cfgRaw), testLiveJWTSecret) {
		t.Errorf("old secret still present")
	}
}

func TestRestorePreservesAdminPasswordAndMultiUserCredential(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	live := testDBAt(t, dbPath)
	if err := live.Create(&model.User{
		Email: "temporary-admin", Password: "temporary-hash", Role: model.RoleAdmin,
		Enabled: true, SubToken: "temporary-sub", ProxyToken: "temporary-proxy",
	}).Error; err != nil {
		t.Fatal(err)
	}

	restoredHash, err := bcrypt.GenerateFromPassword([]byte("restored-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	restoredPasswordHash := string(restoredHash)
	archive := makeBackupArchive(t, testRestoredJWTSecret, func(db *gorm.DB) {
		var admin model.User
		if err := db.First(&admin, "email = ?", "backup-admin").Error; err != nil {
			t.Fatal(err)
		}
		server := model.Server{Name: "managed", AgentToken: "agent-token"}
		if err := db.Create(&server).Error; err != nil {
			t.Fatal(err)
		}
		settings, err := json.Marshal(singbox.InboundSettings{MultiUser: true})
		if err != nil {
			t.Fatal(err)
		}
		inbound := model.Inbound{
			ServerID: server.ID, Tag: "admin-vless", Type: model.InboundVLESS,
			ListenPort: 443, Enabled: true, Settings: settings,
		}
		if err := db.Create(&inbound).Error; err != nil {
			t.Fatal(err)
		}
		admin.Password = restoredPasswordHash
		admin.ProxyToken = "stable-admin-proxy-token"
		admin.ServerIDs = []uint{server.ID}
		admin.InboundIDs = []uint{inbound.ID}
		if err := db.Select("Password", "ProxyToken", "ServerIDs", "InboundIDs").Save(&admin).Error; err != nil {
			t.Fatal(err)
		}
	})

	a := &App{
		cfg: config.PanelConfig{JWTSecret: testLiveJWTSecret, Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}},
		db:  live, version: "v1.0.7",
	}
	w := uploadRestore(t, a, archive)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}

	restored := testDBAt(t, dbPath)
	var admin model.User
	if err := restored.First(&admin, "email = ?", "backup-admin").Error; err != nil {
		t.Fatal(err)
	}
	if admin.ID != 1 || admin.Password != restoredPasswordHash || admin.ProxyToken != "stable-admin-proxy-token" {
		t.Fatalf("restored admin = %#v", admin)
	}
	var inbound model.Inbound
	if err := restored.First(&inbound, "tag = ?", "admin-vless").Error; err != nil {
		t.Fatal(err)
	}
	got := proxyIdentity(&admin, inbound.ID)
	want := proxyIdentity(&model.User{
		ID: 1, Email: "backup-admin", ProxyToken: "stable-admin-proxy-token",
	}, inbound.ID)
	if got != want {
		t.Fatalf("proxy credential changed: got %#v want %#v", got, want)
	}
}

func TestRestoreIsNotBlockedByUnreadableCurrentOneDriveSettings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	live := testDBAt(t, dbPath)
	if err := live.Save(&model.Setting{Key: oneDriveSettingKey, Value: "{not-json"}).Error; err != nil {
		t.Fatal(err)
	}

	archive := makeBackupArchive(t, testRestoredJWTSecret, nil)
	a := &App{
		cfg: config.PanelConfig{JWTSecret: testLiveJWTSecret, Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}},
		db:  live, version: "v1.0.7",
	}
	w := uploadRestore(t, a, archive)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}

	restored := testDBAt(t, dbPath)
	var admin model.User
	if err := restored.First(&admin, "email = ?", "backup-admin").Error; err != nil {
		t.Fatal(err)
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
	archive := makeBackupArchive(t, testRestoredJWTSecret, nil)

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
	if strings.TrimSpace(string(raw)) != testRestoredJWTSecret {
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

func TestPreflightRestoredJWTSecretUsesMatchingEnvironment(t *testing.T) {
	t.Setenv("JWT_SECRET", testRestoredJWTSecret)
	t.Setenv("SINGBOX_PANEL_JWT_SECRET", "")
	missingConfig := filepath.Join(t.TempDir(), "missing", "panel.yaml")
	if err := preflightRestoredJWTSecretPersistence(missingConfig, "", testRestoredJWTSecret); err != nil {
		t.Fatalf("matching environment-backed secret must not require config write access: %v", err)
	}
}

func TestPreflightRestoredJWTSecretRejectsEnvironmentMismatch(t *testing.T) {
	t.Setenv("JWT_SECRET", testLiveJWTSecret)
	t.Setenv("SINGBOX_PANEL_JWT_SECRET", "")
	if err := preflightRestoredJWTSecretPersistence("", "", testRestoredJWTSecret); err == nil {
		t.Fatal("mismatched environment-backed secret must be rejected")
	}
}

func TestValidateRestoredAdministratorsRejectsExpiredOnly(t *testing.T) {
	db := testDBAt(t, filepath.Join(t.TempDir(), "panel.db"))
	hash, err := bcrypt.GenerateFromPassword([]byte("valid-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Hour)
	if err := db.Create(&model.User{
		Email: "expired-admin", Password: string(hash), Role: model.RoleAdmin,
		Enabled: true, ExpireAt: &expired, SubToken: "expired-sub", ProxyToken: "expired-proxy",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateRestoredAdministrators(db); err == nil || !strings.Contains(err.Error(), "未到期") {
		t.Fatalf("error = %v, want expired administrator rejection", err)
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

func TestComparePanelVersionsAllowsMatchingDevelopmentBuild(t *testing.T) {
	if comparison, err := comparePanelVersions("dev", "dev"); err != nil || comparison != 0 {
		t.Fatalf("matching development versions: comparison=%d err=%v", comparison, err)
	}
	if _, err := comparePanelVersions("dev", "v1.0.1"); err == nil {
		t.Fatal("different unparseable and semantic versions must not be silently ordered")
	}
}

func TestRecoverPendingRestoreFinishesSecretPersistence(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("SINGBOX_PANEL_JWT_SECRET", "")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	if err := os.WriteFile(dbPath, []byte("restored database bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	targetHash, err := hashFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	marker := pendingRestore{
		Version: 1, TargetSHA256: targetHash, RollbackSHA256: strings.Repeat("0", 64),
		JWTSecret: testRestoredJWTSecret, PreviousJWTSecret: testLiveJWTSecret,
	}
	raw, _ := json.Marshal(marker)
	if err := writeFileAtomic(dbPath+restoreMarkerSuffix, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.PanelConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}}
	if err := RecoverPendingRestore(cfg, ""); err != nil {
		t.Fatal(err)
	}
	secret, err := os.ReadFile(filepath.Join(dir, jwtSecretFile))
	if err != nil || strings.TrimSpace(string(secret)) != testRestoredJWTSecret {
		t.Fatalf("secret = %q err=%v", secret, err)
	}
	if _, err := os.Stat(dbPath + restoreMarkerSuffix); !os.IsNotExist(err) {
		t.Fatalf("restore marker still exists: %v", err)
	}
}

func TestRecoverPendingRestoreRejectsUnknownDatabaseState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	if err := os.WriteFile(dbPath, []byte("unexpected bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := pendingRestore{
		Version: 1, TargetSHA256: strings.Repeat("1", 64), RollbackSHA256: strings.Repeat("2", 64),
		JWTSecret: testRestoredJWTSecret,
	}
	raw, _ := json.Marshal(marker)
	if err := writeFileAtomic(dbPath+restoreMarkerSuffix, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.PanelConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}}
	if err := RecoverPendingRestore(cfg, ""); err == nil {
		t.Fatal("unknown database state was silently accepted")
	}
}

func TestRecoverPendingRestoreBeforeDatabaseSwitchKeepsCurrentSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("SINGBOX_PANEL_JWT_SECRET", "")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	if err := os.WriteFile(dbPath, []byte("live database bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	liveHash, _ := hashFile(dbPath)
	if err := os.WriteFile(filepath.Join(dir, jwtSecretFile), []byte(testLiveJWTSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := pendingRestore{
		Version: 1, TargetSHA256: strings.Repeat("1", 64), LiveSHA256: liveHash,
		RollbackSHA256: liveHash, JWTSecret: testRestoredJWTSecret, PreviousJWTSecret: testLiveJWTSecret,
	}
	raw, _ := json.Marshal(marker)
	if err := writeFileAtomic(dbPath+restoreMarkerSuffix, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.PanelConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}}
	if err := RecoverPendingRestore(cfg, ""); err != nil {
		t.Fatal(err)
	}
	secret, _ := os.ReadFile(filepath.Join(dir, jwtSecretFile))
	if strings.TrimSpace(string(secret)) != testLiveJWTSecret {
		t.Fatalf("pre-switch recovery changed secret: %q", secret)
	}
}

func TestRecoverPendingRestoreRestoresMissingDatabaseFromSnapshot(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("SINGBOX_PANEL_JWT_SECRET", "")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	snapshotPath := dbPath + ".pre-restore-20260101T000000.000000000Z"
	if err := os.WriteFile(snapshotPath, []byte("snapshot database bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshotHash, err := hashFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	// The live database is gone. That does not prove the imported database never
	// became live — the marker outlives a completed restore when removing it failed —
	// so recovery treats it as the unknown state and puts the checksum-verified
	// snapshot back instead of refusing to start.
	marker := pendingRestore{
		Version: 1, TargetSHA256: strings.Repeat("1", 64), LiveSHA256: strings.Repeat("2", 64),
		RollbackPath: snapshotPath, RollbackSHA256: snapshotHash,
		JWTSecret: testRestoredJWTSecret, PreviousJWTSecret: testLiveJWTSecret,
	}
	raw, _ := json.Marshal(marker)
	if err := writeFileAtomic(dbPath+restoreMarkerSuffix, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.PanelConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}}
	if err := RecoverPendingRestore(cfg, ""); err != nil {
		t.Fatalf("a recoverable state was refused: %v", err)
	}
	live, err := os.ReadFile(dbPath)
	if err != nil || string(live) != "snapshot database bytes" {
		t.Fatalf("live database = %q err=%v; want the snapshot contents", live, err)
	}
	// The database that is live again is the pre-restore one, so its signing key
	// must come back too or encrypted settings stop decrypting.
	secret, err := os.ReadFile(filepath.Join(dir, jwtSecretFile))
	if err != nil || strings.TrimSpace(string(secret)) != testLiveJWTSecret {
		t.Fatalf("secret = %q err=%v; want the pre-restore key", secret, err)
	}
	if _, err := os.Stat(dbPath + restoreMarkerSuffix); !os.IsNotExist(err) {
		t.Fatalf("restore marker still exists: %v", err)
	}
}

// pendingRestoreConfig writes a restore marker next to dbPath and returns the
// configuration that points at it, so a rejection case only has to state what it
// leaves on disk.
func pendingRestoreConfig(t *testing.T, dbPath string, marker pendingRestore) config.PanelConfig {
	t.Helper()
	raw, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(dbPath+restoreMarkerSuffix, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return config.PanelConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}}
}

func TestRecoverPendingRestoreRejectsMissingDatabaseWithoutSnapshot(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	// No live database and no snapshot recorded. Nothing may be invented, so the
	// panel has to keep refusing to start and leave the marker for manual recovery.
	cfg := pendingRestoreConfig(t, dbPath, pendingRestore{
		Version: 1, TargetSHA256: strings.Repeat("1", 64), LiveSHA256: strings.Repeat("2", 64),
		JWTSecret: testRestoredJWTSecret,
	})
	err := RecoverPendingRestore(cfg, "")
	if err == nil {
		t.Fatal("a missing database with no snapshot was silently accepted")
	}
	// The operator needs to know which database the panel was looking for, and the
	// failure has to come from the recovery step rather than from reading the file:
	// reporting the raw hash error is exactly the unfixed behaviour.
	if !strings.Contains(err.Error(), dbPath) {
		t.Fatalf("the error should name the database path: %v", err)
	}
	if strings.Contains(err.Error(), "hash database") {
		t.Fatalf("a missing database was reported as a hash failure: %v", err)
	}
	if _, statErr := os.Stat(dbPath + restoreMarkerSuffix); statErr != nil {
		t.Fatalf("marker was removed after a failed recovery: %v", statErr)
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Fatalf("recovery invented a database file: %v", statErr)
	}
}

func TestRecoverPendingRestoreRejectsSnapshotThatDoesNotMatch(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	snapshotPath := dbPath + ".pre-restore-20260101T000000.000000000Z"
	if err := os.WriteFile(snapshotPath, []byte("a different snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The file beside the database is not the snapshot this restore recorded, so it
	// must not become the live database. This checksum gate is what makes recovering
	// over a missing database safe, so it needs its own coverage.
	cfg := pendingRestoreConfig(t, dbPath, pendingRestore{
		Version: 1, TargetSHA256: strings.Repeat("1", 64), LiveSHA256: strings.Repeat("2", 64),
		RollbackPath: snapshotPath, RollbackSHA256: strings.Repeat("3", 64),
		JWTSecret: testRestoredJWTSecret, PreviousJWTSecret: testLiveJWTSecret,
	})
	err := RecoverPendingRestore(cfg, "")
	if err == nil {
		t.Fatal("a snapshot that does not match the recorded checksum was installed")
	}
	if !strings.Contains(err.Error(), snapshotPath) {
		t.Fatalf("the error should name the snapshot to restore by hand: %v", err)
	}
	if _, statErr := os.Stat(dbPath + restoreMarkerSuffix); statErr != nil {
		t.Fatalf("marker was removed after a failed recovery: %v", statErr)
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Fatalf("recovery invented a database file: %v", statErr)
	}
}

func TestRecoverPendingRestoreRejectsUnreadableDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	// A directory at the database path fails with something other than "does not
	// exist". Treating that as a missing database would copy a snapshot over
	// whatever is really there, so it has to stay a hard error.
	if err := os.Mkdir(dbPath, 0o700); err != nil {
		t.Fatal(err)
	}
	// The snapshot is valid, so an implementation that mistook "unreadable" for
	// "missing" would really start restoring it: the rollback removes the WAL
	// sidecars before copying the snapshot over the path. Keep those sidecars and
	// the directory itself as sentinels, so reporting some error is not enough to
	// pass — recovery must have refused before touching anything.
	snapshotPath := dbPath + ".pre-restore-20260101T000000.000000000Z"
	if err := os.WriteFile(snapshotPath, []byte("snapshot database bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshotHash, err := hashFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var sidecars []string
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar := dbPath + suffix
		if err := os.WriteFile(sidecar, []byte("sidecar"), 0o600); err != nil {
			t.Fatal(err)
		}
		sidecars = append(sidecars, sidecar)
	}
	cfg := pendingRestoreConfig(t, dbPath, pendingRestore{
		Version: 1, TargetSHA256: strings.Repeat("1", 64), LiveSHA256: strings.Repeat("2", 64),
		RollbackPath: snapshotPath, RollbackSHA256: snapshotHash,
		JWTSecret: testRestoredJWTSecret, PreviousJWTSecret: testLiveJWTSecret,
	})
	err = RecoverPendingRestore(cfg, "")
	if err == nil {
		t.Fatal("an unreadable database path was treated as a missing database")
	}
	info, statErr := os.Stat(dbPath)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("the database path was replaced: %v", statErr)
	}
	for _, sidecar := range sidecars {
		if _, statErr := os.Stat(sidecar); statErr != nil {
			t.Fatalf("recovery touched %s instead of refusing the unreadable database: %v", sidecar, statErr)
		}
	}
	if _, statErr := os.Stat(dbPath + restoreMarkerSuffix); statErr != nil {
		t.Fatalf("marker was removed after a failed recovery: %v", statErr)
	}
}

func TestRecoverPendingRestoreRefusesDanglingSymlinkDatabase(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.db")
	dbPath := filepath.Join(dir, "singbox-panel.db")
	// A database kept on another mount is reached through a symlink, and a mount
	// that is not up yet leaves that symlink pointing at nothing. Recovering would
	// replace the symlink with a plain file and silently fork the data once the
	// mount returns, so recovery has to refuse and say why.
	if err := os.Symlink(target, dbPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	snapshotPath := dbPath + ".pre-restore-20260101T000000.000000000Z"
	if err := os.WriteFile(snapshotPath, []byte("snapshot database bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshotHash, err := hashFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := pendingRestoreConfig(t, dbPath, pendingRestore{
		Version: 1, TargetSHA256: strings.Repeat("1", 64), LiveSHA256: strings.Repeat("2", 64),
		RollbackPath: snapshotPath, RollbackSHA256: snapshotHash,
		JWTSecret: testRestoredJWTSecret, PreviousJWTSecret: testLiveJWTSecret,
	})
	err = RecoverPendingRestore(cfg, "")
	if err == nil {
		t.Fatal("a dangling symlink was recovered over")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("the error should name the symlink as the cause: %v", err)
	}
	info, lstatErr := os.Lstat(dbPath)
	if lstatErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the database symlink was replaced: mode=%v err=%v", info, lstatErr)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("the symlink target was written to: %v", statErr)
	}
	if _, statErr := os.Stat(dbPath + restoreMarkerSuffix); statErr != nil {
		t.Fatalf("marker was removed after a failed recovery: %v", statErr)
	}
}
