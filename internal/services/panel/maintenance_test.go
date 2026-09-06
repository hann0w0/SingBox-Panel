package panel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestValidReleaseTag(t *testing.T) {
	good := []string{"v1.0.0", "v1.2.3-beta1", "v10.20.30-rc.2"}
	bad := []string{"", "1.0.0", "v10.20.30_rc2", "v1.0.0; rm -rf /", "v1 0", "$(whoami)", "v1.0.0/../../etc", "v1.0.0`id`", strings.Repeat("v", 65)}
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

func TestLatestPanelReleaseExplicitRefreshBypassesCache(t *testing.T) {
	latestTag := "v1.1.1"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: latestTag})
	}))
	defer server.Close()

	panelReleaseMutex.Lock()
	previousURL := panelReleaseURL
	previousTag := panelReleaseTag
	previousAt := panelReleaseAt
	panelReleaseURL = server.URL
	panelReleaseTag = ""
	panelReleaseAt = time.Time{}
	panelReleaseMutex.Unlock()
	t.Cleanup(func() {
		panelReleaseMutex.Lock()
		panelReleaseURL = previousURL
		panelReleaseTag = previousTag
		panelReleaseAt = previousAt
		panelReleaseMutex.Unlock()
	})

	first, err := latestPanelRelease(false)
	if err != nil || first != "v1.1.1" {
		t.Fatalf("first lookup = %q, err=%v", first, err)
	}
	latestTag = "v1.1.2"
	cached, err := latestPanelRelease(false)
	if err != nil || cached != "v1.1.1" {
		t.Fatalf("cached lookup = %q, err=%v", cached, err)
	}
	fresh, err := latestPanelRelease(true)
	if err != nil || fresh != "v1.1.2" {
		t.Fatalf("refreshed lookup = %q, err=%v", fresh, err)
	}
	if requests != 2 {
		t.Fatalf("GitHub request count = %d, want 2", requests)
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
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got["instance_id"] != a.startedAt.UTC().Format(time.RFC3339Nano) {
		t.Errorf("maintenance response does not identify this running process")
	}
	if got["reinstall_supported"] != got["update_supported"] {
		t.Errorf("same-version reinstall support differs from update support")
	}
}

func TestInstallDirFromDSN(t *testing.T) {
	cases := map[string]string{
		"/opt/singbox-panel/data/singbox-panel.db": "/opt/singbox-panel",
		"/srv/panel/data/singbox-panel.db":         "/srv/panel",
		"":                                         "",
		"/srv/panel/custom/singbox-panel.db":       "",
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

func TestSwapScriptSyntaxAndRollbackGuards(t *testing.T) {
	script := buildSwapScript(
		"/opt/panel/bin", "/opt/panel/stage/new-bin", "/opt/panel/stage/web", "/opt/panel/stage/agents",
		"/opt/panel/stage", "/opt/panel/web", "/opt/panel/agents", "http://127.0.0.1:32334/api/ready",
		"/opt/panel/.update/.active", "/opt/panel/data/panel.db", "/opt/panel/stage/database.rollback",
	)
	cmd := exec.Command("/bin/sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("swap script syntax: %v: %s", err, out)
	}
	for _, required := range []string{
		`cp -p -- "$ROLLBACK_DB" "$DB.rollback-new"`,
		`ROLLBACK_FAILED=1`,
		`rollback failed; recovery files remain in $STAGE`,
		`if rollback_update; then report_result rolled_back && rm -rf -- "$STAGE"; fi`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("swap script missing rollback guard %q", required)
		}
	}
	if strings.Contains(script, `install -m 0600 "$ROLLBACK_DB"`) {
		t.Fatal("swap script would restore the database as root instead of preserving its owner")
	}
}

func TestManagedUpdatePathsRejectEscapesAndOverlaps(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"data", updateSubdir, "web", "agents"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := managedUpdatePath(filepath.Join(root, "escape"), root); err == nil {
		t.Fatal("managedUpdatePath accepted a symlink escaping the install directory")
	}
	if err := validateManagedUpdateTargets(root, filepath.Join(root, updateSubdir), filepath.Join(root, "web"), filepath.Join(root, "web", "agents")); err == nil {
		t.Fatal("nested frontend and Agent directories were accepted")
	}
	if err := validateManagedUpdateTargets(root, filepath.Join(root, updateSubdir), filepath.Join(root, "data", "web"), filepath.Join(root, "agents")); err == nil {
		t.Fatal("a frontend directory inside data was accepted")
	}
	if err := validateManagedUpdateTargets(root, filepath.Join(root, updateSubdir), filepath.Join(root, "web"), filepath.Join(root, updateSubdir, "agents")); err == nil {
		t.Fatal("an Agent directory inside the update staging area was accepted")
	}
}

func TestExtractUpdateArchiveRejectsUnsafeOrCorruptInput(t *testing.T) {
	tests := []struct {
		name  string
		write func(*testing.T, *tar.Writer)
	}{
		{
			name: "path traversal",
			write: func(t *testing.T, tw *tar.Writer) {
				writeUpdateTarEntry(t, tw, &tar.Header{Name: "../escape", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}, []byte("x"))
			},
		},
		{
			name: "symbolic link",
			write: func(t *testing.T, tw *tar.Writer) {
				writeUpdateTarEntry(t, tw, &tar.Header{Name: "link", Linkname: "/etc/passwd", Mode: 0o777, Typeflag: tar.TypeSymlink}, nil)
			},
		},
		{
			name: "normalized duplicate",
			write: func(t *testing.T, tw *tar.Writer) {
				writeUpdateTarEntry(t, tw, &tar.Header{Name: "asset", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}, []byte("a"))
				writeUpdateTarEntry(t, tw, &tar.Header{Name: "./asset", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}, []byte("b"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := createUpdateArchive(t, tt.write)
			if err := extractUpdateArchive(archive, filepath.Join(t.TempDir(), "out")); err == nil {
				t.Fatalf("extractUpdateArchive accepted %s", tt.name)
			}
		})
	}

	valid := createUpdateArchive(t, func(t *testing.T, tw *tar.Writer) {
		writeUpdateTarEntry(t, tw, &tar.Header{Name: "index.html", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg}, []byte("ok"))
	})
	raw, err := os.ReadFile(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 16 {
		t.Fatal("test archive unexpectedly short")
	}
	truncated := filepath.Join(t.TempDir(), "truncated.tar.gz")
	if err := os.WriteFile(truncated, raw[:len(raw)-8], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractUpdateArchive(truncated, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("extractUpdateArchive accepted a truncated gzip stream")
	}
}

func TestExtractUpdateArchiveValidBundle(t *testing.T) {
	archive := createUpdateArchive(t, func(t *testing.T, tw *tar.Writer) {
		writeUpdateTarEntry(t, tw, &tar.Header{Name: "bin/agent", Mode: 0o755, Size: 5, Typeflag: tar.TypeReg}, []byte("agent"))
	})
	destination := filepath.Join(t.TempDir(), "out")
	if err := extractUpdateArchive(archive, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "bin", "agent"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "agent" {
		t.Fatalf("extracted content = %q", got)
	}
	info, err := os.Stat(filepath.Join(destination, "bin", "agent"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("extracted mode = %o, want 755", info.Mode().Perm())
	}
}

func TestSwapScriptRestoresAllComponentsAndDatabaseMode(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	liveBin := filepath.Join(root, "panel")
	stagedBin := filepath.Join(stage, "panel.new")
	webDir := filepath.Join(root, "web")
	agentsDir := filepath.Join(root, "agents")
	stagedWeb := filepath.Join(stage, "web.new")
	stagedAgents := filepath.Join(stage, "agents.new")
	dbPath := filepath.Join(root, "data", "panel.db")
	rollbackDB := filepath.Join(stage, "database.rollback")
	lockPath := filepath.Join(root, "update.lock")
	for _, dir := range []string{stage, webDir, agentsDir, stagedWeb, stagedAgents, filepath.Dir(dbPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		liveBin:                              "old-binary",
		stagedBin:                            "new-binary",
		filepath.Join(webDir, "asset"):       "old-web",
		filepath.Join(agentsDir, "agent"):    "old-agent",
		filepath.Join(stagedWeb, "asset"):    "new-web",
		filepath.Join(stagedAgents, "agent"): "new-agent",
		dbPath:                               "old-database",
		rollbackDB:                           "too-early-snapshot",
		lockPath:                             "locked",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	fakeBin := filepath.Join(root, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// A final acknowledged write lands as the old service shuts down. The new
	// service then changes both the main database and WAL before failing health.
	writeExecutable(t, filepath.Join(fakeBin, "systemctl"), "#!/bin/sh\n"+
		"DB="+shellQuote(dbPath)+"\nSTATE="+shellQuote(filepath.Join(root, "service-state"))+"\n"+
		`case "$1" in
  is-active) exit 1 ;;
  stop)
    if [ ! -e "$STATE" ]; then
      printf 'old-database-with-last-write' > "$DB"
      printf 'committed-wal' > "$DB-wal"
      printf 'stopped' > "$STATE"
    fi ;;
  restart)
    if [ "$(cat "$STATE")" = stopped ]; then
      printf 'migrated-database' > "$DB"
      printf 'migrated-wal' > "$DB-wal"
      printf 'restarted' > "$STATE"
    fi ;;
esac
exit 0
`)
	writeExecutable(t, filepath.Join(fakeBin, "sleep"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(fakeBin, "curl"), "#!/bin/sh\nexit 1\n")
	writeExecutable(t, filepath.Join(fakeBin, "wget"), "#!/bin/sh\nexit 1\n")

	script := buildSwapScript(liveBin, stagedBin, stagedWeb, stagedAgents, stage, webDir, agentsDir, "http://127.0.0.1/ready", lockPath, dbPath, rollbackDB)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+":/usr/local/bin:/usr/bin:/bin")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("swap script unexpectedly succeeded: %s", out)
	}
	for path, want := range map[string]string{
		liveBin:                           "old-binary",
		filepath.Join(webDir, "asset"):    "old-web",
		filepath.Join(agentsDir, "agent"): "old-agent",
		dbPath:                            "old-database-with-last-write",
		dbPath + "-wal":                   "committed-wal",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("restored database mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("successful rollback did not remove staging directory: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("swap lock was not removed: %v", err)
	}
	operation, err := readPanelUpdateOperation(filepath.Join(root, "result.json"))
	if err != nil || operation.ID != filepath.Base(stage) || operation.Status != "rolled_back" {
		t.Fatalf("same-version rollback must be distinguishable from success: status=%s err=%v", operation.Status, err)
	}
}

func TestMaintenanceGateSurvivesRestartAndAllowsRetryAfterHelper(t *testing.T) {
	root := t.TempDir()
	updateRoot := filepath.Join(root, updateSubdir)
	if err := os.Mkdir(updateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(updateRoot, ".active")
	if err := os.WriteFile(lockPath, []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Database.DSN = filepath.Join(root, "data", "panel.db")
	app := &App{cfg: cfg}
	router := gin.New()
	router.GET("/backup", app.downloadBackup)
	router.POST("/restore", app.restoreBackup)
	router.POST("/update", app.selfUpdate)
	router.POST("/cloud/sync", app.syncOneDriveBackup)
	router.POST("/cloud/:id/restore", app.restoreOneDriveBackup)
	router.POST("/cloud/auth", app.startOneDriveAuth)
	for _, path := range []string{"/backup", "/restore", "/update", "/cloud/sync", "/cloud/one/restore", "/cloud/auth"} {
		method := http.MethodPost
		if path == "/backup" {
			method = http.MethodGet
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != http.StatusConflict {
			t.Fatalf("new process accepted maintenance %s while helper is active: %d", path, rec.Code)
		}
	}
	// The scheduler uses the same gate, without an HTTP request.
	if app.tryMaintenanceLock() {
		app.selfUpdating.Unlock()
		t.Fatal("background maintenance bypassed the detached update")
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if !app.tryMaintenanceLock() {
		t.Fatal("maintenance stayed locked after helper finished")
	}
	app.selfUpdating.Unlock()

	// Preserve the existing recovery policy for abandoned old update markers.
	if err := os.WriteFile(lockPath, []byte("abandoned"), 0o600); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-panelUpdateLockLifetime - time.Minute)
	if err := os.Chtimes(lockPath, expired, expired); err != nil {
		t.Fatal(err)
	}
	if !app.tryMaintenanceLock() {
		t.Fatal("expired marker prevented a recovery attempt")
	}
	app.selfUpdating.Unlock()
}

func TestDetachedUpdateReleasesOriginalProcessLockOnlyAfterHelperFinishes(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".active")
	if err := os.WriteFile(lockPath, []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	app.selfUpdating.Lock()
	finished := make(chan struct{})
	go func() {
		app.releaseMaintenanceAfterDetachedUpdate(lockPath)
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("parent maintenance gate released before helper completion")
	case <-time.After(300 * time.Millisecond):
	}
	if app.selfUpdating.TryLock() {
		app.selfUpdating.Unlock()
		t.Fatal("another maintenance operation could enter during the swap")
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("surviving parent process did not release maintenance after failed helper")
	}
	if !app.selfUpdating.TryLock() {
		t.Fatal("retry could not acquire the original process maintenance gate")
	}
	app.selfUpdating.Unlock()
}

func TestSwapScriptEarlyFailurePartialSnapshotAndSuccessOutcomes(t *testing.T) {
	realCP, err := exec.LookPath("cp")
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"before-stop", "partial-snapshot", "success"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			stage := filepath.Join(root, "stage")
			liveBin, stagedBin := filepath.Join(root, "panel"), filepath.Join(stage, "panel.new")
			webDir, agentsDir := filepath.Join(root, "web"), filepath.Join(root, "agents")
			stagedWeb, stagedAgents := filepath.Join(stage, "web.new"), filepath.Join(stage, "agents.new")
			dbPath := filepath.Join(root, "data", "panel.db")
			rollbackDB, lockPath := filepath.Join(stage, "database.rollback"), filepath.Join(root, ".active")
			fakeBin := filepath.Join(root, "fake-bin")
			for _, dir := range []string{stage, webDir, agentsDir, stagedWeb, stagedAgents, filepath.Dir(dbPath), fakeBin} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for path, content := range map[string]string{
				liveBin: "old-binary", stagedBin: "new-binary", dbPath: "old-database", dbPath + "-wal": "old-wal", lockPath: "active",
				filepath.Join(webDir, "asset"): "old-web", filepath.Join(agentsDir, "agent"): "old-agent",
				filepath.Join(stagedWeb, "asset"): "new-web", filepath.Join(stagedAgents, "agent"): "new-agent",
			} {
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cpLog, serviceLog := filepath.Join(root, "copies"), filepath.Join(root, "service-actions")
			failDestination := ""
			if outcome == "before-stop" {
				failDestination = filepath.Join(stage, "rollback", "panel")
			} else if outcome == "partial-snapshot" {
				failDestination = rollbackDB + "-wal"
			}
			writeExecutable(t, filepath.Join(fakeBin, "cp"), "#!/bin/sh\n"+
				"for destination; do :; done\nprintf '%s\\n' \"$destination\" >> "+shellQuote(cpLog)+"\n"+
				"[ \"$destination\" != "+shellQuote(failDestination)+" ] || exit 1\nexec "+shellQuote(realCP)+" \"$@\"\n")
			writeExecutable(t, filepath.Join(fakeBin, "systemctl"), "#!/bin/sh\n"+
				"printf '%s\\n' \"$1\" >> "+shellQuote(serviceLog)+"\nexit 0\n")
			for _, command := range []string{"sleep", "curl", "wget"} {
				writeExecutable(t, filepath.Join(fakeBin, command), "#!/bin/sh\nexit 0\n")
			}
			script := buildSwapScript(liveBin, stagedBin, stagedWeb, stagedAgents, stage, webDir, agentsDir,
				"http://127.0.0.1/ready", lockPath, dbPath, rollbackDB)
			cmd := exec.Command("/bin/sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+fakeBin+":/usr/local/bin:/usr/bin:/bin")
			output, runErr := cmd.CombinedOutput()
			if (runErr == nil) != (outcome == "success") {
				t.Fatalf("unexpected helper outcome: %v: %s", runErr, output)
			}
			operation, err := readPanelUpdateOperation(filepath.Join(root, "result.json"))
			wantStatus := map[string]string{"before-stop": "failed", "partial-snapshot": "rolled_back", "success": "succeeded"}[outcome]
			if err != nil || operation.Status != wantStatus || operation.ID != "stage" {
				t.Fatalf("helper result=%+v error=%v, want %s", operation, err, wantStatus)
			}
			if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
				t.Fatalf("helper left active lock behind: %v", err)
			}
			if outcome == "before-stop" {
				if _, err := os.Stat(serviceLog); !os.IsNotExist(err) {
					t.Fatal("failed binary backup should leave the running service untouched")
				}
			}
			if outcome == "partial-snapshot" {
				copies, err := os.ReadFile(cpLog)
				if err != nil || strings.Contains(string(copies), dbPath+".rollback-new") {
					t.Fatal("partial database snapshot was used for rollback")
				}
			}
			wantBinary := "old-binary"
			if outcome == "success" {
				wantBinary = "new-binary"
			}
			for path, want := range map[string]string{liveBin: wantBinary, dbPath: "old-database", dbPath + "-wal": "old-wal"} {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("unexpected live file after %s: path=%s err=%v", outcome, path, err)
				}
			}
		})
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
		JWTSecret: testLiveJWTSecret,
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
	if got := contents["jwt_secret"]; strings.TrimSpace(got) != testLiveJWTSecret {
		t.Errorf("jwt_secret = %q, want the active secret", got)
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

func TestDownloadBackupRejectsWeakJWTSecret(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	a := &App{
		cfg: config.PanelConfig{
			JWTSecret: "weak-secret",
			Database:  config.DatabaseConfig{Driver: "sqlite", DSN: dbPath},
		},
		db: testDBAt(t, dbPath), version: "v1.0.0",
	}
	r := gin.New()
	r.GET("/backup", a.downloadBackup)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/backup", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "拒绝生成不完整备份") {
		t.Fatalf("unexpected error: %s", w.Body.String())
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

func createUpdateArchive(t *testing.T, write func(*testing.T, *tar.Writer)) string {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	write(t, tw)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(path, raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeUpdateTarEntry(t *testing.T, tw *tar.Writer, hdr *tar.Header, body []byte) {
	t.Helper()
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 {
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}
