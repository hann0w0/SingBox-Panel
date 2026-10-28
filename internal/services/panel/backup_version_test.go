package panel

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/config"
)

func TestComparePanelVersionsDoesNotOrderEdgeHashes(t *testing.T) {
	for _, tc := range []struct {
		left, right string
		want        int
	}{
		{"v1.0.8-edge.e171f19e0f3c", "v1.0.8-edge.a38152651ae5", 0},
		{"v1.0.8-edge.a38152651ae5", "v1.0.8-edge.e171f19e0f3c", 0},
		{"v1.0.8", "v1.0.8-edge.a38152651ae5", 0},
		{"v1.0.8-edge.a38152651ae5", "v1.0.8", 0},
		{"v1.0.9-edge.a38152651ae5", "v1.0.8-edge.e171f19e0f3c", 1},
		{"v1.0.7-edge.e171f19e0f3c", "v1.0.8-edge.a38152651ae5", -1},
		{"v1.0.8-rc.1", "v1.0.8", -1},
		{"v1.0.8", "v1.0.8-rc.1", 1},
		{"v1.0.8", "v1.0.8-edge.not-a-commit", 1},
	} {
		t.Run(tc.left+"/"+tc.right, func(t *testing.T) {
			got, err := comparePanelVersions(tc.left, tc.right)
			if err != nil || got != tc.want {
				t.Fatalf("comparison = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestRestoreOlderMainBuildBackup(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("SINGBOX_PANEL_JWT_SECRET", "")
	oldPath, secret, err := extractBackup(bytes.NewReader(makeBackupArchive(t, testLiveJWTSecret, nil)), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldApp := &App{
		cfg: config.PanelConfig{JWTSecret: secret, Database: config.DatabaseConfig{Driver: "sqlite", DSN: oldPath}},
		db:  testDBAt(t, oldPath), version: "v1.0.8-edge.e171f19e0f3c",
	}
	archivePath, _, cleanup, err := oldApp.createBackupArchive()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(t.TempDir(), "singbox-panel.db")
	newApp := &App{
		cfg: config.PanelConfig{JWTSecret: secret, Database: config.DatabaseConfig{Driver: "sqlite", DSN: newPath}},
		db:  testDBAt(t, newPath), version: "v1.0.8-edge.a38152651ae5",
	}
	w := uploadRestore(t, newApp, archive)
	if w.Code != http.StatusOK {
		t.Fatalf("parent build backup rejected by child: %d %s", w.Code, w.Body.String())
	}

	staged, _, metadata, present, err := extractBackupWithMetadata(bytes.NewReader(archive), t.TempDir())
	if err != nil || !present {
		t.Fatalf("backup metadata missing: %v", err)
	}
	metadata.SchemaVersion = currentSchemaVersion() + 1
	if err := validateBackupMetadata(metadata, staged, newApp.version); err == nil {
		t.Fatal("edge compatibility bypassed the schema version guard")
	}
}
