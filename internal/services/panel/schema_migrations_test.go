package panel

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
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
	if db.Migrator().HasTable("audit_events") {
		t.Fatal("fresh database still contains the removed administrator event table")
	}
	for _, column := range []string{"agent_token_pending", "agent_token_revoked"} {
		if db.Migrator().HasColumn("servers", column) {
			t.Fatalf("fresh database contains removed column %s", column)
		}
	}
	if _, err := os.Stat(databaseFile + ".backups"); !os.IsNotExist(err) {
		t.Fatalf("fresh database should not create an upgrade backup: %v", err)
	}
}

func TestRemoveAdministratorEventRecordsMigrationDropsLegacyTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "remove-events.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE audit_events (id INTEGER PRIMARY KEY, route TEXT NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SchemaMigration{Version: 17, Name: "legacy combined schema", AppliedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	var migration schemaMigration
	for _, candidate := range applicationMigrations {
		if candidate.version == 18 {
			migration = candidate
			break
		}
	}
	if migration.version != 18 {
		t.Fatal("v18 migration not found")
	}
	if err := migration.up(db); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable("audit_events") {
		t.Fatal("legacy administrator event table was not dropped")
	}
	var v17 model.SchemaMigration
	if err := db.First(&v17, "version = ?", 17).Error; err != nil {
		t.Fatal(err)
	}
	if v17.Name != "legacy server credential schema" {
		t.Fatalf("v17 migration name = %q", v17.Name)
	}
}

func TestInitDBUpgradesVersion17DatabaseWithoutAdministratorEventTable(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "version17.db")
	db, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE audit_events (id INTEGER PRIMARY KEY, route TEXT NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	for _, migration := range applicationMigrations {
		if migration.version >= 18 {
			break
		}
		name := migration.name
		if migration.version == 17 {
			name = "legacy combined schema"
		}
		if err := db.Create(&model.SchemaMigration{Version: migration.version, Name: name, AppliedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}

	cfg := config.Default()
	cfg.Database.DSN = databaseFile
	cfg.Admin.Email = "admin"
	cfg.Admin.Password = "test-password"
	upgraded, err := InitDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Migrator().HasTable("audit_events") {
		t.Fatal("version 17 database retained the removed administrator event table")
	}
	var latest model.SchemaMigration
	if err := upgraded.Order("version DESC").First(&latest).Error; err != nil {
		t.Fatal(err)
	}
	if latest.Version != applicationMigrations[len(applicationMigrations)-1].version || latest.Dirty {
		t.Fatalf("latest migration = %+v", latest)
	}
}

func TestStableAgentCredentialMigrationPreservesExistingToken(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "existing", AgentToken: "old-agent-token"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	var migration schemaMigration
	for _, candidate := range applicationMigrations {
		if candidate.version == 19 {
			migration = candidate
			break
		}
	}
	if migration.version != 19 {
		t.Fatal("v19 migration not found")
	}
	if err := migration.up(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&server, server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if server.AgentToken != "old-agent-token" {
		t.Fatalf("stable token was not preserved: %+v", server)
	}
}

func TestStableAgentCredentialMigrationInitializesMissingToken(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "missing"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	var migration schemaMigration
	for _, candidate := range applicationMigrations {
		if candidate.version == 19 {
			migration = candidate
			break
		}
	}
	if migration.version != 19 {
		t.Fatal("v19 migration not found")
	}
	if err := migration.up(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&server, server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(server.AgentToken) != 48 {
		t.Fatalf("missing stable token was not initialized: %+v", server)
	}
}

func TestRemoveLegacyAgentCredentialStateMigrationDropsColumns(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "legacy", AgentToken: "stable-agent-token"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`ALTER TABLE servers ADD COLUMN agent_token_pending text`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`ALTER TABLE servers ADD COLUMN agent_token_revoked numeric NOT NULL DEFAULT 0`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE INDEX idx_servers_agent_token_pending ON servers(agent_token_pending)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE INDEX idx_servers_agent_token_revoked ON servers(agent_token_revoked)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE servers SET agent_token_pending = 'obsolete', agent_token_revoked = 1 WHERE id = ?`, server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SchemaMigration{Version: 17, Name: "legacy combined schema", AppliedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	var migration schemaMigration
	for _, candidate := range applicationMigrations {
		if candidate.version == 20 {
			migration = candidate
			break
		}
	}
	if migration.version != 20 {
		t.Fatal("v20 migration not found")
	}
	if err := migration.up(db); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"agent_token_pending", "agent_token_revoked"} {
		if db.Migrator().HasColumn("servers", column) {
			t.Fatalf("legacy column %s was not dropped", column)
		}
	}
	if !db.Migrator().HasIndex(&model.Server{}, "idx_servers_agent_token") {
		t.Fatal("stable Agent token index was lost while dropping legacy columns")
	}
	if err := db.First(&server, server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if server.AgentToken != "stable-agent-token" {
		t.Fatalf("stable token changed to %q", server.AgentToken)
	}
	var v17 model.SchemaMigration
	if err := db.First(&v17, "version = ?", 17).Error; err != nil {
		t.Fatal(err)
	}
	if v17.Name != "legacy server credential schema" {
		t.Fatalf("v17 migration name = %q", v17.Name)
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

func TestExplicitCustomNodeAudienceMigrationPreservesLegacyMeaning(t *testing.T) {
	db := testDB(t)
	all := model.CustomNode{Name: "legacy-all", Link: "socks5://127.0.0.1:1080", UserIDs: []uint{}}
	scoped := model.CustomNode{Name: "legacy-scoped", Link: "socks5://127.0.0.1:1081", UserIDs: []uint{3, 7}}
	if err := db.Create(&all).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&scoped).Error; err != nil {
		t.Fatal(err)
	}
	var migration schemaMigration
	for _, m := range applicationMigrations {
		if m.version == 9 {
			migration = m
			break
		}
	}
	if migration.version != 9 {
		t.Fatalf("v9 migration not found")
	}
	if err := migration.up(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&all, all.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&scoped, scoped.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !all.AllUsers || len(all.ExcludedUserIDs) != 0 {
		t.Fatalf("legacy global node migrated incorrectly: %+v", all)
	}
	if scoped.AllUsers || len(scoped.UserIDs) != 2 || len(scoped.ExcludedUserIDs) != 0 {
		t.Fatalf("legacy scoped node migrated incorrectly: %+v", scoped)
	}
}

func TestNameRewriteMigrationBackfillsManagedSourceNames(t *testing.T) {
	db := testDB(t)
	source := model.CustomNodeSubscription{Name: "订阅", URL: "https://example.com/sub"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	node := model.CustomNode{Name: "旧名称", SubscriptionID: &source.ID, SubscriptionKey: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	var migration schemaMigration
	for _, candidate := range applicationMigrations {
		if candidate.version == 12 {
			migration = candidate
			break
		}
	}
	if migration.version != 12 {
		t.Fatal("v12 migration not found")
	}
	if err := migration.up(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&node, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if node.SourceName != node.Name {
		t.Fatalf("source name = %q; want %q", node.SourceName, node.Name)
	}
}

func TestProtocolFilterMigrationAddsHiddenNodeState(t *testing.T) {
	db := testDB(t)
	var migration schemaMigration
	for _, candidate := range applicationMigrations {
		if candidate.version == 13 {
			migration = candidate
			break
		}
	}
	if migration.version != 13 {
		t.Fatal("v13 migration not found")
	}
	if err := migration.up(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(&model.CustomNode{}, "HiddenBySubscriptionRule") {
		t.Fatal("hidden subscription rule column was not added")
	}
}

func TestUserNodeOrderMigrationAddsOrderTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "order-migration.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable(&model.UserNodeOrder{}) {
		t.Fatal("user node order table unexpectedly exists before migration")
	}
	var migration schemaMigration
	for _, candidate := range applicationMigrations {
		if candidate.version == 14 {
			migration = candidate
			break
		}
	}
	if migration.version != 14 {
		t.Fatal("v14 migration not found")
	}
	if err := migration.up(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable(&model.UserNodeOrder{}) {
		t.Fatal("user node order table was not added")
	}
}

func TestV8MigrationWithoutLegacyUserIDColumn(t *testing.T) {
	db := testDB(t)
	if db.Migrator().HasColumn(&model.CustomNode{}, "user_id") {
		t.Fatal("current custom_nodes unexpectedly has legacy user_id")
	}
	if err := db.Create(&model.CustomNode{Name: "current", Link: "socks5://127.0.0.1:1080"}).Error; err != nil {
		t.Fatal(err)
	}
	var migration schemaMigration
	for _, candidate := range applicationMigrations {
		if candidate.version == 8 {
			migration = candidate
			break
		}
	}
	if err := migration.up(db); err != nil {
		t.Fatalf("v8 migration without user_id failed: %v", err)
	}
}

func TestMigrationReplaySafetyIsExplicit(t *testing.T) {
	known := make(map[uint]bool, len(applicationMigrations))
	for _, migration := range applicationMigrations {
		known[migration.version] = true
		if _, ok := migrationReplaySafe[migration.version]; !ok {
			t.Fatalf("migration %d (%s) records no replay-safety decision", migration.version, migration.name)
		}
	}
	for version := range migrationReplaySafe {
		if !known[version] {
			t.Fatalf("replay-safety table lists unknown migration %d", version)
		}
	}
}

// A dirty ledger on a database that commits DDL implicitly must not be a dead
// end: the interrupted migration is cleared and replayed. The driver name picks
// the policy while the connection stays SQLite, so no MySQL/Postgres server is
// needed to exercise the decision.
func TestImplicitCommitDatabaseReplaysInterruptedMigration(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "replay.db")
	db, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SchemaMigration{Version: 1, Name: "interrupted", Dirty: true}).Error; err != nil {
		t.Fatal(err)
	}
	cfg := config.DatabaseConfig{Driver: "mysql", DSN: databaseFile}
	if err := runSchemaMigrations(db, cfg); err != nil {
		t.Fatalf("interrupted migration was not replayed: %v", err)
	}
	var rows []model.SchemaMigration
	if err := db.Order("version").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(applicationMigrations) {
		t.Fatalf("expected %d applied migrations, got %d", len(applicationMigrations), len(rows))
	}
	if latest := applicationMigrations[len(applicationMigrations)-1].version; rows[len(rows)-1].Version != latest {
		t.Fatalf("latest applied version = %d, want %d", rows[len(rows)-1].Version, latest)
	}
	for _, row := range rows {
		if row.Dirty {
			t.Fatalf("migration %d is still dirty after replay", row.Version)
		}
	}
}

func TestImplicitCommitDatabaseKeepsUnsafeInterruptedMigrationFatal(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "unsafe.db")
	db, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	// Version 9 rewrites every node's audience, so a retry would discard later
	// administrator edits and must stay fatal.
	if err := db.Create(&model.SchemaMigration{Version: 9, Name: "audience", Dirty: true}).Error; err != nil {
		t.Fatal(err)
	}
	cfg := config.DatabaseConfig{Driver: "postgres", DSN: databaseFile}
	if err := runSchemaMigrations(db, cfg); err == nil {
		t.Fatal("a migration whose replay discards data must not be retried automatically")
	}
}

// SQLite keeps its strict contract: the pre-migration snapshot is the recovery
// point, so an interrupted migration stops startup instead of being replayed.
func TestFileDatabaseDoesNotReplayInterruptedMigration(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "strict.db")
	db, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SchemaMigration{Version: 1, Name: "interrupted", Dirty: true}).Error; err != nil {
		t.Fatal(err)
	}
	cfg := config.DatabaseConfig{Driver: "sqlite", DSN: databaseFile}
	if err := runSchemaMigrations(db, cfg); err == nil {
		t.Fatal("a file database must not silently replay an interrupted migration")
	}
}

func TestInterruptedMigrationFollowedByAppliedMigrationIsRejected(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "inconsistent.db")
	db, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	rows := []model.SchemaMigration{
		{Version: 1, Name: "interrupted", Dirty: true},
		{Version: 2, Name: "applied", AppliedAt: time.Now()},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	cfg := config.DatabaseConfig{Driver: "mysql", DSN: databaseFile}
	if err := runSchemaMigrations(db, cfg); err == nil {
		t.Fatal("an interrupted migration followed by an applied one is inconsistent and must stop startup")
	}
}

func TestMultipleInterruptedMigrationsAreRejected(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "two-dirty.db")
	db, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	rows := []model.SchemaMigration{
		{Version: 1, Name: "one", Dirty: true},
		{Version: 2, Name: "two", Dirty: true},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	cfg := config.DatabaseConfig{Driver: "mysql", DSN: databaseFile}
	if err := runSchemaMigrations(db, cfg); err == nil {
		t.Fatal("two interrupted migrations must stop startup")
	}
}
