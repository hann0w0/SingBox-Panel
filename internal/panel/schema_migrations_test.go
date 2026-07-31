package panel

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/model"
)

func TestInitDBRecordsSchemaVersion(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "panel.db")
	cfg := config.Default()
	cfg.Database.DSN = databaseFile
	cfg.Admin.Email = "admin"
	cfg.Admin.Password = "test-password"

	db, err := InitDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var rows []model.SchemaMigration
	if err := db.Order("version").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(applicationMigrations) || rows[len(rows)-1].Version != applicationMigrations[len(applicationMigrations)-1].version {
		t.Fatalf("unexpected schema versions: %+v", rows)
	}
	if _, err := os.Stat(databaseFile + ".backups"); !os.IsNotExist(err) {
		t.Fatalf("fresh database should not create an upgrade backup: %v", err)
	}
}

func TestLegacySQLiteIsBackedUpBeforeMigration(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Create(&model.Setting{Key: "legacy", Value: "kept"}).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := legacy.DB(); err == nil {
		_ = sqlDB.Close()
	}

	cfg := config.Default()
	cfg.Database.DSN = databaseFile
	cfg.Admin.Email = "admin"
	cfg.Admin.Password = "test-password"
	if _, err := InitDB(cfg); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(databaseFile + ".backups")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one backup, got %d", len(entries))
	}
	backup, err := gorm.Open(sqlite.Open(filepath.Join(databaseFile+".backups", entries[0].Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var setting model.Setting
	if err := backup.First(&setting, "key = ?", "legacy").Error; err != nil || setting.Value != "kept" {
		t.Fatalf("backup is not restorable: setting=%+v err=%v", setting, err)
	}
}

func TestValidateMigrationsRejectsDuplicateVersion(t *testing.T) {
	noop := func(*gorm.DB) error { return nil }
	_, err := validateMigrations([]schemaMigration{
		{version: 1, name: "one", up: noop},
		{version: 1, name: "duplicate", up: noop},
	})
	if err == nil {
		t.Fatal("expected duplicate migration version error")
	}
}

func TestDirtySchemaStopsStartup(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "dirty.db")
	db, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SchemaMigration{Version: 1, Name: "failed", Dirty: true}).Error; err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Database.DSN = databaseFile
	cfg.Admin.Password = "test-password"
	if _, err := InitDB(cfg); err == nil {
		t.Fatal("dirty schema must stop panel startup")
	}
}

func TestUnknownNewerSchemaStopsStartup(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "newer.db")
	db, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	unknown := applicationMigrations[len(applicationMigrations)-1].version + 1
	if err := db.Create(&model.SchemaMigration{Version: unknown, Name: "future", AppliedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Database.DSN = databaseFile
	cfg.Admin.Password = "test-password"
	if _, err := InitDB(cfg); err == nil {
		t.Fatal("a database newer than the binary must stop panel startup")
	}
}

func TestNonPrefixSchemaHistoryStopsStartup(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "gap.db")
	db, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint{1, 3} {
		if err := db.Create(&model.SchemaMigration{Version: version, Name: "gap", AppliedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Database.DSN = databaseFile
	cfg.Admin.Password = "test-password"
	if _, err := InitDB(cfg); err == nil {
		t.Fatal("a non-prefix schema history must stop panel startup")
	}
}
