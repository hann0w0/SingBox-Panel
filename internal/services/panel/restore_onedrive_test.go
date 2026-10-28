package panel

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestRestoreReencryptsOneDriveSettingsForRuntimeKey(t *testing.T) {
	for _, tc := range []struct {
		name        string
		archived    oneDriveSettings
		current     oneDriveSettings
		badArchive  bool
		missingKey  bool
		wantWarning bool
	}{
		{name: "archive authorization", archived: oneDriveSettings{RefreshToken: "archive-token", Passphrase: "archive-backup-passphrase", AutoSync: true}},
		{name: "archive passphrase only", archived: oneDriveSettings{Passphrase: "archive-backup-passphrase"}},
		{name: "current authorization wins", archived: oneDriveSettings{RefreshToken: "archive-token", Passphrase: "archive-backup-passphrase"}, current: oneDriveSettings{RefreshToken: "current-token", Passphrase: "current-backup-passphrase", AutoSync: true}},
		{name: "missing archived key", archived: oneDriveSettings{RefreshToken: "archive-token", Passphrase: "archive-backup-passphrase"}, missingKey: true, wantWarning: true},
		{name: "corrupt cloud settings", badArchive: true, wantWarning: true},
		{name: "current authorization replaces corrupt archive", badArchive: true, current: oneDriveSettings{RefreshToken: "current-token", Passphrase: "current-backup-passphrase", AutoSync: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("JWT_SECRET", testLiveJWTSecret)
			t.Setenv("SINGBOX_PANEL_JWT_SECRET", "")
			dbPath := filepath.Join(t.TempDir(), "singbox-panel.db")
			cfg := config.PanelConfig{JWTSecret: testLiveJWTSecret, Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}}
			a := &App{cfg: cfg, db: testDBAt(t, dbPath), version: "v1.0.8"}
			if err := a.saveOneDriveSettings(tc.current); err != nil {
				t.Fatal(err)
			}
			importedKey := testRestoredJWTSecret
			if tc.missingKey {
				importedKey = ""
			}
			archive := makeBackupArchive(t, importedKey, func(db *gorm.DB) {
				source := &App{cfg: config.PanelConfig{JWTSecret: testRestoredJWTSecret}, db: db}
				if err := source.saveOneDriveSettings(tc.archived); err != nil {
					t.Fatal(err)
				}
				if tc.badArchive {
					if err := db.Save(&model.Setting{Key: oneDriveSettingKey, Value: "{invalid"}).Error; err != nil {
						t.Fatal(err)
					}
				}
			})
			response := uploadRestore(t, a, archive)
			if response.Code != http.StatusOK {
				t.Fatalf("restore status %d: %s", response.Code, response.Body.String())
			}
			restored := &App{cfg: cfg, db: testDBAt(t, dbPath)}
			got, err := restored.loadOneDriveSettings()
			if err != nil {
				t.Fatalf("restored cloud settings are unreadable: %v", err)
			}
			want := tc.archived
			if tc.current.RefreshToken != "" {
				want = tc.current
			} else if tc.wantWarning {
				want = defaultOneDriveSettings()
			}
			if got.RefreshToken != want.RefreshToken || got.Passphrase != want.Passphrase || got.AutoSync != want.AutoSync {
				t.Fatal("restored cloud settings do not match the selected authorization")
			}
			if hasWarning := strings.Contains(response.Header().Get("X-Restore-Warning"), "OneDrive"); hasWarning != tc.wantWarning {
				t.Fatalf("cloud recovery warning=%t, want %t", hasWarning, tc.wantWarning)
			}
			var row model.Setting
			if err := restored.db.First(&row, "key = ?", oneDriveSettingKey).Error; err != nil {
				t.Fatal(err)
			}
			var stored oneDriveStoredSettings
			if err := json.Unmarshal([]byte(row.Value), &stored); err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{stored.EncryptedRefresh, stored.EncryptedPassphrase} {
				if value != "" && !strings.HasPrefix(value, "v1:") {
					t.Fatal("restored cloud secret was not encrypted")
				}
			}
			if err := restored.mutateOneDriveSettings(func(settings *oneDriveSettings) error {
				settings.RefreshToken = "new-device-code-token"
				return nil
			}); err != nil {
				t.Fatalf("subsequent authorization cannot be saved: %v", err)
			}
		})
	}
}
