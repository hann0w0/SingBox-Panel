package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/model"
)

func TestOneDriveRefreshTokenIsEncryptedInSettings(t *testing.T) {
	a := &App{
		cfg: config.PanelConfig{JWTSecret: "panel-jwt-secret"},
		db:  testDB(t),
	}
	want := "refresh-token-with-sensitive-data"
	if err := a.saveOneDriveSettings(oneDriveSettings{
		RefreshToken: want,
		AutoSync:     true,
	}); err != nil {
		t.Fatal(err)
	}

	var row model.Setting
	if err := a.db.First(&row, "key = ?", oneDriveSettingKey).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.Value, want) {
		t.Fatalf("refresh token stored in plaintext: %s", row.Value)
	}

	got, err := a.loadOneDriveSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != want || got.ClientID != oneDriveOAuthClientID || !got.AutoSync {
		t.Fatalf("loaded OneDrive settings = %#v", got)
	}
}

func TestOneDriveSecretCannotBeOpenedWithAnotherJWTSecret(t *testing.T) {
	a := &App{cfg: config.PanelConfig{JWTSecret: "first-secret"}}
	sealed, err := a.encryptOneDriveSecret("refresh-token")
	if err != nil {
		t.Fatal(err)
	}

	b := &App{cfg: config.PanelConfig{JWTSecret: "different-secret"}}
	if _, err := b.decryptOneDriveSecret(sealed); err == nil {
		t.Fatal("different JWT secret decrypted the OneDrive token")
	}
}

func TestOneDriveDeviceCodeConnectStoresRefreshToken(t *testing.T) {
	oldOAuthURL := oneDriveOAuthBaseURL
	defer func() { oneDriveOAuthBaseURL = oldOAuthURL }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/devicecode":
			_ = r.ParseForm()
			if r.Form.Get("client_id") != oneDriveOAuthClientID || r.Form.Get("scope") != "offline_access Files.ReadWrite" {
				t.Fatalf("device code form = %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(oneDriveDeviceCodeResponse{
				DeviceCode:      "device-code",
				UserCode:        "ABCD-EFGH",
				VerificationURI: "https://microsoft.com/devicelogin",
				ExpiresIn:       900,
				Interval:        1,
			})
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("device_code") != "device-code" {
				t.Fatalf("token form = %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(oneDriveTokenResponse{
				AccessToken:  "access-token",
				RefreshToken: "refresh-from-microsoft",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oneDriveOAuthBaseURL = server.URL

	a := &App{
		cfg:             config.PanelConfig{JWTSecret: "panel-secret"},
		db:              testDB(t),
		oneDrivePending: make(map[string]oneDriveDeviceSession),
	}
	router := testMaintenanceRouter(a)
	start := httptest.NewRequest(http.MethodPost, "/onedrive/auth/start", nil)
	startRecorder := httptest.NewRecorder()
	router.ServeHTTP(startRecorder, start)
	if startRecorder.Code != http.StatusOK {
		t.Fatalf("start status = %d body = %s", startRecorder.Code, startRecorder.Body.String())
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(startRecorder.Body.Bytes(), &started); err != nil || started.SessionID == "" {
		t.Fatalf("start response = %s, err = %v", startRecorder.Body.String(), err)
	}

	poll := httptest.NewRequest(http.MethodPost, "/onedrive/auth/"+url.PathEscape(started.SessionID)+"/poll", nil)
	pollRecorder := httptest.NewRecorder()
	router.ServeHTTP(pollRecorder, poll)
	if pollRecorder.Code != http.StatusOK {
		t.Fatalf("poll status = %d body = %s", pollRecorder.Code, pollRecorder.Body.String())
	}
	settings, err := a.loadOneDriveSettings()
	if err != nil || settings.RefreshToken != "refresh-from-microsoft" || !settings.AutoSync || settings.ClientID != oneDriveOAuthClientID {
		t.Fatalf("settings = %#v, err = %v", settings, err)
	}
}

func TestOneDriveUploadSessionReceivesBackupBytes(t *testing.T) {
	oldOAuthURL, oldGraphURL := oneDriveOAuthBaseURL, oneDriveGraphBaseURL
	defer func() {
		oneDriveOAuthBaseURL = oldOAuthURL
		oneDriveGraphBaseURL = oldGraphURL
	}()

	want := []byte("data-only-backup-archive")
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_ = json.NewEncoder(w).Encode(oneDriveTokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token"})
		case r.URL.Path == "/me/drive/root:/singbox-panel-backups:":
			_ = json.NewEncoder(w).Encode(oneDriveFile{ID: "folder-id", Name: oneDriveBackupFolder, Folder: &struct{}{}})
		case r.URL.Path == "/me/drive/items/folder-id:/singbox-panel-backup.tar.gz:/createUploadSession":
			var payload struct {
				Item map[string]string `json:"item"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Item["@microsoft.graph.conflictBehavior"] != "replace" || payload.Item["name"] != oneDriveBackupName {
				t.Fatalf("upload session payload = %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"uploadUrl": serverURL(r) + "/upload"})
		case r.URL.Path == "/upload":
			if got := r.Header.Get("Content-Range"); got != "bytes 0-23/24" {
				t.Fatalf("Content-Range = %q", got)
			}
			uploaded, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"backup-id"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oneDriveOAuthBaseURL = server.URL
	oneDriveGraphBaseURL = server.URL

	a := &App{cfg: config.PanelConfig{JWTSecret: "panel-secret"}, db: testDB(t)}
	if err := a.saveOneDriveSettings(oneDriveSettings{
		RefreshToken: "refresh-token",
	}); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "singbox-panel-backup-test.tar.gz")
	if err := os.WriteFile(archivePath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.uploadOneDriveBackup(archivePath); err != nil {
		t.Fatal(err)
	}
	if string(uploaded) != string(want) {
		t.Fatalf("uploaded = %q, want %q", uploaded, want)
	}
}

func TestOneDriveSyncPreservesRotatedRefreshToken(t *testing.T) {
	oldOAuthURL, oldGraphURL := oneDriveOAuthBaseURL, oneDriveGraphBaseURL
	defer func() {
		oneDriveOAuthBaseURL = oldOAuthURL
		oneDriveGraphBaseURL = oldGraphURL
	}()

	rotatedTokens := []string{"refresh-token-2", "refresh-token-3"}
	tokenRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			refresh := rotatedTokens[len(rotatedTokens)-1]
			if tokenRequests < len(rotatedTokens) {
				refresh = rotatedTokens[tokenRequests]
			}
			tokenRequests++
			_ = json.NewEncoder(w).Encode(oneDriveTokenResponse{AccessToken: "access-token", RefreshToken: refresh})
		case r.URL.Path == "/me/drive/root:/singbox-panel-backups:":
			_ = json.NewEncoder(w).Encode(oneDriveFile{ID: "folder-id", Name: oneDriveBackupFolder, Folder: &struct{}{}})
		case r.URL.Path == "/me/drive/items/folder-id:/singbox-panel-backup.tar.gz:/createUploadSession":
			_ = json.NewEncoder(w).Encode(map[string]string{"uploadUrl": serverURL(r) + "/upload"})
		case r.URL.Path == "/upload":
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"backup-id"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oneDriveOAuthBaseURL = server.URL
	oneDriveGraphBaseURL = server.URL

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	a := &App{
		cfg: config.PanelConfig{
			JWTSecret: "panel-secret",
			Database:  config.DatabaseConfig{Driver: "sqlite", DSN: dbPath},
		},
		db: testDBAt(t, dbPath),
	}
	if err := a.saveOneDriveSettings(oneDriveSettings{RefreshToken: "refresh-token-1", AutoSync: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.syncOneDriveBackupLocked(); err != nil {
		t.Fatal(err)
	}
	settings, err := a.loadOneDriveSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.RefreshToken != "refresh-token-3" {
		t.Fatalf("refresh token = %q, want latest rotated token", settings.RefreshToken)
	}
	if settings.LastSyncAt == nil || settings.LastAttemptAt == nil || settings.FailedAttempts != 0 {
		t.Fatalf("sync metadata = %#v", settings)
	}
}

func TestOneDriveBackupListOnlyReturnsFixedBackup(t *testing.T) {
	oldOAuthURL, oldGraphURL := oneDriveOAuthBaseURL, oneDriveGraphBaseURL
	defer func() {
		oneDriveOAuthBaseURL = oldOAuthURL
		oneDriveGraphBaseURL = oldGraphURL
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(oneDriveTokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token"})
		case "/me/drive/root:/singbox-panel-backups:":
			_ = json.NewEncoder(w).Encode(oneDriveFile{ID: "folder-id", Name: oneDriveBackupFolder, Folder: &struct{}{}})
		case "/me/drive/items/folder-id/children":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
				{"id": "backup-id", "name": oneDriveBackupName, "file": map[string]any{}},
				{"id": "other-id", "name": "personal-file.txt", "file": map[string]any{}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oneDriveOAuthBaseURL = server.URL
	oneDriveGraphBaseURL = server.URL

	a := &App{cfg: config.PanelConfig{JWTSecret: "panel-secret"}, db: testDB(t)}
	if err := a.saveOneDriveSettings(oneDriveSettings{RefreshToken: "refresh-token"}); err != nil {
		t.Fatal(err)
	}
	files, err := a.listOneDriveBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ID != "backup-id" {
		t.Fatalf("files = %#v", files)
	}
	if _, err := a.oneDriveBackupByID("other-id"); !errors.Is(err, errOneDriveBackupNotFound) {
		t.Fatalf("unexpected lookup error: %v", err)
	}
}

func TestOneDriveStatusReturnsEmptyArrayWhenFolderHasNoBackup(t *testing.T) {
	oldOAuthURL, oldGraphURL := oneDriveOAuthBaseURL, oneDriveGraphBaseURL
	defer func() {
		oneDriveOAuthBaseURL = oldOAuthURL
		oneDriveGraphBaseURL = oldGraphURL
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(oneDriveTokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token"})
		case "/me/drive/root:/singbox-panel-backups:":
			_ = json.NewEncoder(w).Encode(oneDriveFile{ID: "folder-id", Name: oneDriveBackupFolder, Folder: &struct{}{}})
		case "/me/drive/items/folder-id/children":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []oneDriveFile{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oneDriveOAuthBaseURL = server.URL
	oneDriveGraphBaseURL = server.URL

	a := &App{cfg: config.PanelConfig{JWTSecret: "panel-secret"}, db: testDB(t)}
	if err := a.saveOneDriveSettings(oneDriveSettings{RefreshToken: "refresh-token", AutoSync: true}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/onedrive", a.oneDriveStatus)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/onedrive", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Files         json.RawMessage `json:"files"`
		IntervalHours int             `json:"interval_hours"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload.Files) != "[]" {
		t.Fatalf("files JSON = %s", payload.Files)
	}
	if payload.IntervalHours != 1 {
		t.Fatalf("interval_hours = %d, want 1", payload.IntervalHours)
	}
}

func TestOneDriveDownloadStreamsExpectedLength(t *testing.T) {
	oldOAuthURL, oldGraphURL := oneDriveOAuthBaseURL, oneDriveGraphBaseURL
	defer func() {
		oneDriveOAuthBaseURL = oldOAuthURL
		oneDriveGraphBaseURL = oldGraphURL
	}()

	want := []byte("complete-backup-archive")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(oneDriveTokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token"})
		case "/me/drive/root:/singbox-panel-backups:":
			_ = json.NewEncoder(w).Encode(oneDriveFile{ID: "folder-id", Name: oneDriveBackupFolder, Folder: &struct{}{}})
		case "/me/drive/items/folder-id/children":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{{
				"id": "backup-id", "name": oneDriveBackupName, "size": len(want), "file": map[string]any{},
			}}})
		case "/me/drive/items/backup-id/content":
			w.Header().Set("Content-Length", strconv.Itoa(len(want)))
			_, _ = w.Write(want)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oneDriveOAuthBaseURL = server.URL
	oneDriveGraphBaseURL = server.URL

	a := &App{cfg: config.PanelConfig{JWTSecret: "panel-secret"}, db: testDB(t)}
	if err := a.saveOneDriveSettings(oneDriveSettings{RefreshToken: "refresh-token"}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/onedrive/backups/:id", a.downloadOneDriveBackup)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/onedrive/backups/backup-id", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
		t.Fatalf("Content-Length = %q", recorder.Header().Get("Content-Length"))
	}
	if !bytes.Equal(recorder.Body.Bytes(), want) {
		t.Fatalf("download = %q, want %q", recorder.Body.Bytes(), want)
	}
}

func TestOneDriveRestoreDownloadsDirectlyAndPreservesAuthorization(t *testing.T) {
	oldOAuthURL, oldGraphURL := oneDriveOAuthBaseURL, oneDriveGraphBaseURL
	defer func() {
		oneDriveOAuthBaseURL = oldOAuthURL
		oneDriveGraphBaseURL = oldGraphURL
	}()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	live := testDBAt(t, dbPath)
	if err := live.Create(&model.Server{Name: "before-restore", AgentToken: "old-agent"}).Error; err != nil {
		t.Fatal(err)
	}
	archive := makeBackupArchive(t, "imported-jwt-secret", func(db *gorm.DB) {
		if err := db.Create(&model.Server{Name: "after-restore", AgentToken: "new-agent"}).Error; err != nil {
			t.Fatal(err)
		}
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(oneDriveTokenResponse{AccessToken: "access-token", RefreshToken: "rotated-refresh"})
		case "/me/drive/root:/singbox-panel-backups:":
			_ = json.NewEncoder(w).Encode(oneDriveFile{ID: "folder-id", Name: oneDriveBackupFolder, Folder: &struct{}{}})
		case "/me/drive/items/folder-id/children":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{{
				"id": "backup-id", "name": oneDriveBackupName, "size": len(archive), "file": map[string]any{},
			}}})
		case "/me/drive/items/backup-id/content":
			w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oneDriveOAuthBaseURL = server.URL
	oneDriveGraphBaseURL = server.URL

	a := &App{
		cfg: config.PanelConfig{JWTSecret: "live-jwt-secret", Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}},
		db:  live,
	}
	if err := a.saveOneDriveSettings(oneDriveSettings{RefreshToken: "current-refresh", AutoSync: true}); err != nil {
		t.Fatal(err)
	}

	origRestart := scheduleRestartFn
	scheduleRestartFn = func() {}
	t.Cleanup(func() { scheduleRestartFn = origRestart })
	router := gin.New()
	router.POST("/onedrive/backups/:id/restore", a.restoreOneDriveBackup)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/onedrive/backups/backup-id/restore", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	restored := testDBAt(t, dbPath)
	var servers []model.Server
	if err := restored.Order("id").Find(&servers).Error; err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("restored servers = %#v, want only the archived server", servers)
	}
	restoredServer := servers[0]
	if restoredServer.Name != "after-restore" {
		t.Fatalf("restored server name = %q", restoredServer.Name)
	}
	if restoredServer.AgentToken != "new-agent" {
		t.Fatalf("restored server token = %q", restoredServer.AgentToken)
	}

	restoredApp := &App{cfg: config.PanelConfig{JWTSecret: "imported-jwt-secret"}, db: restored}
	settings, err := restoredApp.loadOneDriveSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.RefreshToken != "rotated-refresh" || !settings.AutoSync {
		t.Fatalf("restored OneDrive settings = %#v", settings)
	}
}

func TestOneDriveExpiredDeviceSessionIsRemoved(t *testing.T) {
	a := &App{oneDrivePending: map[string]oneDriveDeviceSession{
		"expired": {ExpiresAt: time.Now().Add(-time.Minute)},
	}}
	router := gin.New()
	router.POST("/onedrive/auth/:sessionID/poll", a.pollOneDriveAuth)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/onedrive/auth/expired/poll", nil))
	if recorder.Code != http.StatusGone {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if len(a.oneDrivePending) != 0 {
		t.Fatalf("expired sessions were not removed: %#v", a.oneDrivePending)
	}
}

func TestOneDriveRetryDelayUsesBoundedBackoff(t *testing.T) {
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{failures: 0, want: 0},
		{failures: 1, want: 15 * time.Minute},
		{failures: 2, want: 30 * time.Minute},
		{failures: 3, want: time.Hour},
		{failures: 10, want: 6 * time.Hour},
	}
	for _, tc := range tests {
		if got := oneDriveRetryDelay(tc.failures); got != tc.want {
			t.Fatalf("oneDriveRetryDelay(%d) = %s, want %s", tc.failures, got, tc.want)
		}
	}
}

func testMaintenanceRouter(a *App) http.Handler {
	router := gin.New()
	router.POST("/onedrive/auth/start", a.startOneDriveAuth)
	router.POST("/onedrive/auth/:sessionID/poll", a.pollOneDriveAuth)
	return router
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
