package panel

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/model"
)

// schemaMigration is one ordered, atomic application-schema change. Migration
// functions may use GORM's cross-database Migrator for DDL, but unlike the old
// unconditional AutoMigrate call they only run once and their version is
// persisted.
type schemaMigration struct {
	version uint
	name    string
	up      func(*gorm.DB) error
}

var applicationMigrations = []schemaMigration{
	{
		version: 1,
		name:    "baseline schema",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(model.AllModels()...)
		},
	},
	{
		version: 2,
		name:    "node traffic accounting",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Server{})
		},
	},
	{
		version: 3,
		name:    "multi-user credential seeds",
		up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&model.User{}); err != nil {
				return err
			}
			var users []model.User
			if err := tx.Where("proxy_token = '' OR proxy_token IS NULL").Find(&users).Error; err != nil {
				return err
			}
			for i := range users {
				if err := tx.Model(&model.User{}).Where("id = ?", users[i].ID).
					Update("proxy_token", randHex(32)).Error; err != nil {
					return err
				}
			}
			// Preserve every pre-feature inbound as single-credential. Multi-user
			// is opt-in after upgrade and never silently changes live credentials.
			return migrateSingleUserInbounds(tx)
		},
	},
	{
		version: 4,
		name:    "traffic records",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Server{}, &model.TrafficRecord{})
		},
	},
	{
		version: 5,
		name:    "traffic connection counters",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Server{}, &model.TrafficRecord{})
		},
	},
	{
		version: 6,
		name:    "traffic rate samples",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.TrafficRecord{})
		},
	},
	{
		version: 7,
		name:    "custom subscription nodes",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.CustomNode{})
		},
	},
	{
		version: 8,
		name:    "custom node multi-user + structured nodes",
		up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&model.CustomNode{}); err != nil {
				return err
			}
			// v7 stored a single user_id; carry it over to the new user_ids array.
			var nodes []model.CustomNode
			if err := tx.Find(&nodes).Error; err != nil {
				return err
			}
			for i := range nodes {
				old := struct{ UserID *uint }{}
				if err := tx.Model(&model.CustomNode{}).Where("id = ?", nodes[i].ID).Select("user_id").Scan(&old).Error; err != nil {
					return err
				}
				if old.UserID != nil {
					ids := []uint{*old.UserID}
					if err := tx.Model(&model.CustomNode{}).Where("id = ?", nodes[i].ID).Update("user_ids", ids).Error; err != nil {
						return err
					}
				}
			}
			return nil
		},
	},
	{
		version: 9,
		name:    "explicit custom node audience",
		up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&model.CustomNode{}); err != nil {
				return err
			}
			var nodes []model.CustomNode
			if err := tx.Find(&nodes).Error; err != nil {
				return err
			}
			for i := range nodes {
				if err := tx.Model(&model.CustomNode{}).Where("id = ?", nodes[i].ID).Updates(map[string]any{
					"all_users":         len(nodes[i].UserIDs) == 0,
					"excluded_user_ids": []uint{},
				}).Error; err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 10,
		name:    "custom node groups",
		up: func(tx *gorm.DB) error {
			// AutoMigrate adds the new group column (and its index) to the
			// existing custom_nodes table; existing rows keep an empty group.
			return tx.AutoMigrate(&model.CustomNode{})
		},
	},
	{
		version: 11,
		name:    "saved custom node subscriptions",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.CustomNodeSubscription{}, &model.CustomNode{})
		},
	},
	{
		version: 12,
		name:    "subscription node name rewrite rules",
		up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&model.CustomNodeSubscription{}, &model.CustomNode{}); err != nil {
				return err
			}
			// Existing managed nodes predate SourceName. Their current display
			// name is the only trustworthy source value available at migration
			// time; seed it once so later rule edits are deterministic.
			return tx.Model(&model.CustomNode{}).
				Where("subscription_id IS NOT NULL AND (source_name = '' OR source_name IS NULL)").
				UpdateColumn("source_name", gorm.Expr("name")).Error
		},
	},
	{
		version: 13,
		name:    "subscription protocol filtering",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.CustomNode{})
		},
	},
}

// runSchemaMigrations applies every pending migration in order. If any
// migration fails the transaction is rolled back and InitDB returns the error,
// so the panel never starts on a partially-upgraded schema.
func runSchemaMigrations(db *gorm.DB, cfg config.DatabaseConfig) error {
	migrations, err := validateMigrations(applicationMigrations)
	if err != nil {
		return err
	}

	applied, hasVersionTable, err := validateSchemaMigrationHistory(db, migrations)
	if err != nil {
		return err
	}

	pending := make([]schemaMigration, 0, len(migrations))
	for _, migration := range migrations {
		if !applied[migration.version] {
			pending = append(pending, migration)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	// A consistent SQLite snapshot is taken before the first schema write. A
	// backup failure is fatal: continuing would remove the promised recovery
	// point from the upgrade path.
	if strings.EqualFold(cfg.Driver, "sqlite") || cfg.Driver == "" {
		backup, err := backupSQLiteBeforeMigration(db, cfg.DSN, pending[len(pending)-1].version)
		if err != nil {
			return fmt.Errorf("backup sqlite before migration: %w", err)
		}
		if backup != "" {
			log.Printf("database backup created: %s", backup)
		}
	}

	if !hasVersionTable {
		if err := db.Migrator().CreateTable(&model.SchemaMigration{}); err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}
	}

	for _, migration := range pending {
		dirty := model.SchemaMigration{Version: migration.version, Name: migration.name, Dirty: true}
		if err := db.Create(&dirty).Error; err != nil {
			return fmt.Errorf("mark schema migration %d dirty: %w", migration.version, err)
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := migration.up(tx); err != nil {
				return err
			}
			return tx.Model(&model.SchemaMigration{}).Where("version = ?", migration.version).Updates(map[string]any{
				"dirty":      false,
				"applied_at": time.Now(),
			}).Error
		})
		if err != nil {
			return fmt.Errorf("schema migration %d (%s): %w", migration.version, migration.name, err)
		}
		log.Printf("database schema migrated to version %d (%s)", migration.version, migration.name)
	}
	return nil
}

// validateSchemaMigrationHistory reads and validates the migration ledger
// without applying anything. Keeping this check separate lets the restore path
// reject a dirty, future, or internally inconsistent database before it can
// replace the live SQLite file.
func validateSchemaMigrationHistory(db *gorm.DB, migrations []schemaMigration) (map[uint]bool, bool, error) {
	hasVersionTable := db.Migrator().HasTable(&model.SchemaMigration{})
	applied := map[uint]bool{}
	if hasVersionTable {
		var rows []model.SchemaMigration
		if err := db.Order("version").Find(&rows).Error; err != nil {
			return nil, true, fmt.Errorf("read schema version: %w", err)
		}
		for _, row := range rows {
			if row.Dirty {
				return nil, true, fmt.Errorf("database schema migration %d (%s) is marked dirty; restore the pre-migration backup before starting", row.Version, row.Name)
			}
			applied[row.Version] = true
		}
	}

	known := make(map[uint]bool, len(migrations))
	for _, migration := range migrations {
		known[migration.version] = true
	}
	for version := range applied {
		if !known[version] {
			return nil, hasVersionTable, fmt.Errorf("database schema version %d is newer than or unknown to this panel binary", version)
		}
	}

	missingEarlier := false
	for _, migration := range migrations {
		if !applied[migration.version] {
			missingEarlier = true
			continue
		}
		if missingEarlier {
			return nil, hasVersionTable, fmt.Errorf("database schema history is not an ordered prefix: version %d is applied after a missing migration", migration.version)
		}
	}
	return applied, hasVersionTable, nil
}

func validateMigrations(source []schemaMigration) ([]schemaMigration, error) {
	migrations := append([]schemaMigration(nil), source...)
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	for i, migration := range migrations {
		if migration.version == 0 || strings.TrimSpace(migration.name) == "" || migration.up == nil {
			return nil, fmt.Errorf("invalid schema migration at index %d", i)
		}
		if i > 0 && migrations[i-1].version == migration.version {
			return nil, fmt.Errorf("duplicate schema migration version %d", migration.version)
		}
	}
	return migrations, nil
}

func sqliteFilePath(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" || dsn == ":memory:" || strings.Contains(dsn, "mode=memory") {
		return ""
	}
	dsn = strings.TrimPrefix(dsn, "file:")
	if idx := strings.IndexByte(dsn, '?'); idx >= 0 {
		dsn = dsn[:idx]
	}
	return dsn
}

func backupSQLiteBeforeMigration(db *gorm.DB, dsn string, targetVersion uint) (string, error) {
	databaseFile := sqliteFilePath(dsn)
	if databaseFile == "" {
		return "", nil
	}
	// Opening a brand-new database in WAL mode creates a small, non-empty file
	// before migrations run. Back up only databases that already contain user or
	// application tables; otherwise every first boot would produce a meaningless
	// "upgrade" backup.
	var tableCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tableCount).Error; err != nil {
		return "", err
	}
	if tableCount == 0 {
		return "", nil
	}
	info, err := os.Stat(databaseFile)
	if os.IsNotExist(err) || (err == nil && info.Size() == 0) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	backupDir := databaseFile + ".backups"
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("schema-v%d-%s.db", targetVersion, time.Now().UTC().Format("20060102T150405.000000000Z"))
	backupFile := filepath.Join(backupDir, name)
	// VACUUM INTO uses SQLite's own snapshot mechanism, so the backup is valid
	// even when the database uses WAL mode. SQLite does not accept a bind
	// parameter for the filename; quote single quotes according to SQL rules.
	quoted := strings.ReplaceAll(backupFile, "'", "''")
	if err := db.Exec("VACUUM INTO '" + quoted + "'").Error; err != nil {
		_ = os.Remove(backupFile)
		return "", err
	}
	if err := pruneSQLiteBackups(backupDir, 5); err != nil {
		return "", err
	}
	return backupFile, nil
}

func pruneSQLiteBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type backupEntry struct {
		name string
		info os.FileInfo
	}
	backups := make([]backupEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		backups = append(backups, backupEntry{name: entry.Name(), info: info})
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].info.ModTime().After(backups[j].info.ModTime())
	})
	if keep < 0 {
		keep = 0
	}
	if len(backups) <= keep {
		return nil
	}
	for _, backup := range backups[keep:] {
		if err := os.Remove(filepath.Join(dir, backup.name)); err != nil {
			return err
		}
	}
	return nil
}
