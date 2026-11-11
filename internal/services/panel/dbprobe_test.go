package panel

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

// A database migrated by a newer panel must be rejected by the preflight, so a
// version rollback is refused before the swap instead of leaving a panel that
// cannot start.
func TestDatabaseProbeRejectsNewerSchema(t *testing.T) {
	cfg := config.Default()
	cfg.Database.DSN = filepath.Join(t.TempDir(), "probe.db")
	cfg.Admin.Email, cfg.Admin.Password = "admin", "test-password"
	db, err := InitDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := CheckDatabaseCompatibility(cfg.Database.Driver, cfg.Database.DSN); err != nil {
		t.Fatalf("current schema must pass the probe: %v", err)
	}

	future := currentSchemaVersion() + 1
	if err := db.Create(&model.SchemaMigration{
		Version: future, Name: "a migration from a newer panel", AppliedAt: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	err = CheckDatabaseCompatibility(cfg.Database.Driver, cfg.Database.DSN)
	if err == nil || !strings.Contains(err.Error(), "newer than or unknown") {
		t.Fatalf("later schema was accepted: %v", err)
	}
	if !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("probe error must name the schema version: %v", err)
	}
}

// The probe is read-only: it must not create tables or migrate anything, so it
// is safe to run while the current panel is still serving.
func TestDatabaseProbeDoesNotModifyTheDatabase(t *testing.T) {
	cfg := config.Default()
	cfg.Database.DSN = filepath.Join(t.TempDir(), "probe-ro.db")
	cfg.Admin.Email, cfg.Admin.Password = "admin", "test-password"
	db, err := InitDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	before, err := schemaVersionInDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckDatabaseCompatibility(cfg.Database.Driver, cfg.Database.DSN); err != nil {
		t.Fatal(err)
	}
	after, err := schemaVersionInDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("probe changed the schema version: %d -> %d", before, after)
	}
}

func TestDatabaseProbeRejectsNonSQLiteDeployments(t *testing.T) {
	err := CheckDatabaseCompatibility("postgres", "postgres://example")
	if err == nil || !strings.Contains(err.Error(), "网络数据库") {
		t.Fatalf("network database probe should be refused explicitly: %v", err)
	}
}
