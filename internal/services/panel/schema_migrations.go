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
	"github.com/hann0w0/singbox-panel/internal/domain/model"
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
			legacyUserID := tx.Migrator().HasColumn(&model.CustomNode{}, "user_id")
			if err := tx.AutoMigrate(&model.CustomNode{}); err != nil {
				return err
			}
			if !legacyUserID {
				return nil
			}
			// v7 stored a single user_id; carry it over to the new user_ids array.
			var nodes []model.CustomNode
			if err := tx.Find(&nodes).Error; err != nil {
				return err
			}
			for i := range nodes {
				old := struct{ UserID *uint }{}
				if err := tx.Table("custom_nodes").Where("id = ?", nodes[i].ID).Select("user_id").Scan(&old).Error; err != nil {
					return err
				}
				if old.UserID != nil {
					nodes[i].UserIDs = []uint{*old.UserID}
					if err := tx.Model(&nodes[i]).Select("UserIDs").Updates(&nodes[i]).Error; err != nil {
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
				nodes[i].AllUsers = len(nodes[i].UserIDs) == 0
				nodes[i].ExcludedUserIDs = []uint{}
				if err := tx.Model(&nodes[i]).Select("AllUsers", "ExcludedUserIDs").Updates(&nodes[i]).Error; err != nil {
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
	{
		version: 14,
		name:    "per-user node subscription order",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.UserNodeOrder{})
		},
	},
	{
		version: 15,
		name:    "per-user node order compatibility",
		up: func(tx *gorm.DB) error {
			// Version 15 was briefly used by the removed per-user alias feature.
			// Keep the migration number recognized so databases that already
			// applied it remain compatible. The obsolete column is harmless and
			// intentionally left in place instead of performing destructive DDL.
			return nil
		},
	},
	{
		version: 16,
		name:    "normalized account identifiers",
		up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&model.User{}); err != nil {
				return err
			}
			var users []model.User
			if err := tx.Order("id").Find(&users).Error; err != nil {
				return err
			}
			seen := make(map[string]uint, len(users))
			for i := range users {
				display, normalized, err := validateUsername(users[i].Email)
				if err != nil {
					return fmt.Errorf("user %d has an invalid username: %w", users[i].ID, err)
				}
				if prior := seen[normalized]; prior != 0 {
					return fmt.Errorf("users %d and %d have the same case-insensitive username %q", prior, users[i].ID, display)
				}
				seen[normalized] = users[i].ID
				if err := tx.Model(&model.User{}).Where("id = ?", users[i].ID).
					Updates(map[string]any{"email": display, "email_normalized": normalized}).Error; err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 17,
		name:    "legacy server credential schema",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Server{})
		},
	},
	{
		version: 18,
		name:    "remove obsolete administrator event records",
		up: func(tx *gorm.DB) error {
			if tx.Migrator().HasTable("audit_events") {
				if err := tx.Migrator().DropTable("audit_events"); err != nil {
					return err
				}
			}
			// Normalize the historical v17 ledger description while retaining its
			// version so databases that already applied it remain compatible.
			return tx.Model(&model.SchemaMigration{}).Where("version = ?", 17).
				Update("name", "legacy server credential schema").Error
		},
	},
	{
		version: 19,
		name:    "stable agent credential normalization",
		up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&model.Server{}); err != nil {
				return err
			}
			var servers []model.Server
			if err := tx.Select("id", "agent_token").Find(&servers).Error; err != nil {
				return err
			}
			for i := range servers {
				if strings.TrimSpace(servers[i].AgentToken) == "" {
					if err := tx.Model(&model.Server{}).Where("id = ?", servers[i].ID).
						Update("agent_token", randHex(24)).Error; err != nil {
						return err
					}
				}
			}
			return nil
		},
	},
	{
		version: 20,
		name:    "remove legacy agent credential state",
		up: func(tx *gorm.DB) error {
			if tx.Dialector.Name() == "sqlite" {
				if tx.Migrator().HasColumn("servers", "agent_token_pending") {
					if err := tx.Exec(`DROP INDEX IF EXISTS "idx_servers_agent_token_pending"`).Error; err != nil {
						return err
					}
					if err := tx.Exec(`ALTER TABLE "servers" DROP COLUMN "agent_token_pending"`).Error; err != nil {
						return fmt.Errorf("drop servers.agent_token_pending: %w", err)
					}
				}
				if tx.Migrator().HasColumn("servers", "agent_token_revoked") {
					if err := tx.Exec(`DROP INDEX IF EXISTS "idx_servers_agent_token_revoked"`).Error; err != nil {
						return err
					}
					if err := tx.Exec(`ALTER TABLE "servers" DROP COLUMN "agent_token_revoked"`).Error; err != nil {
						return fmt.Errorf("drop servers.agent_token_revoked: %w", err)
					}
				}
			} else {
				for _, column := range []string{"agent_token_pending", "agent_token_revoked"} {
					if tx.Migrator().HasColumn("servers", column) {
						if err := tx.Migrator().DropColumn("servers", column); err != nil {
							return fmt.Errorf("drop servers.%s: %w", column, err)
						}
					}
				}
			}
			return tx.Model(&model.SchemaMigration{}).Where("version = ?", 17).
				Update("name", "legacy server credential schema").Error
		},
	},
	{
		version: 21,
		name:    "single-user SOCKS inbounds",
		up:      migrateSOCKSSingleUserInbounds,
	},
}

// migrationReplaySafe records whether an interrupted attempt of each migration
// may be retried automatically.
//
// SQLite never needs a retry: runSchemaMigrations writes a consistent snapshot
// to "<db>.backups" before the first schema write, so a dirty ledger stays fatal
// and the operator restores that snapshot.
//
// MySQL and Postgres are different: their DDL commits implicitly, so a failed
// migration leaves a partially applied schema that no transaction can roll back
// and there is no snapshot to fall back to. Refusing to start forever would
// leave the deployment with no supported way forward, so a migration whose `up`
// is genuinely idempotent is replayed instead.
//
// Every version must be listed explicitly — TestMigrationReplaySafetyIsExplicit
// fails when a new migration records no decision — so a future migration cannot
// silently inherit either policy.
var migrationReplaySafe = map[uint]bool{
	1:  true,  // AutoMigrate of every model: idempotent DDL
	2:  true,  // AutoMigrate
	3:  true,  // skips inbounds that already carry single-user credentials
	4:  true,  // AutoMigrate
	5:  true,  // AutoMigrate
	6:  true,  // AutoMigrate
	7:  true,  // AutoMigrate
	8:  true,  // HasColumn guard: a retry only runs AutoMigrate
	9:  false, // rewrites every node's audience, discarding later administrator edits
	10: true,  // AutoMigrate
	11: true,  // AutoMigrate
	12: true,  // only fills source_name where it is still empty
	13: true,  // AutoMigrate
	14: true,  // AutoMigrate
	15: true,  // no-op compatibility migration
	16: true,  // recomputes the same normalized identifiers
	17: true,  // AutoMigrate
	18: true,  // guarded DROP TABLE plus a ledger description update
	19: true,  // only fills empty agent tokens
	20: true,  // HasColumn-guarded DROP COLUMN plus a ledger description update
	21: true,  // skips inbounds that are no longer multi-user
}

// runSchemaMigrations applies every pending migration in order. If any
// migration fails the transaction is rolled back and InitDB returns the error,
// so the panel never starts on a partially-upgraded schema.
//
// On a database that commits DDL implicitly a known idempotent migration is
// retried instead of failing forever; see migrationReplaySafe.
func runSchemaMigrations(db *gorm.DB, cfg config.DatabaseConfig) error {
	migrations, err := validateMigrations(applicationMigrations)
	if err != nil {
		return err
	}

	fileDatabase := strings.EqualFold(cfg.Driver, "sqlite") || cfg.Driver == ""
	applied, hasVersionTable, replay, err := validateSchemaMigrationHistory(db, migrations, !fileDatabase)
	if err != nil {
		return err
	}
	if replay != nil {
		log.Printf("retrying interrupted schema migration %d (%s): %s commits DDL implicitly and this migration is idempotent", replay.version, replay.name, cfg.Driver)
		if err := db.Where("version = ?", replay.version).Delete(&model.SchemaMigration{}).Error; err != nil {
			return fmt.Errorf("clear interrupted schema migration %d: %w", replay.version, err)
		}
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
	if fileDatabase {
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
//
// allowReplay returns an interrupted (dirty) migration so the caller can retry
// it. Callers that must never mutate an unverified database — the restore
// preflight — pass false.
func validateSchemaMigrationHistory(db *gorm.DB, migrations []schemaMigration, allowReplay bool) (map[uint]bool, bool, *schemaMigration, error) {
	hasVersionTable := db.Migrator().HasTable(&model.SchemaMigration{})
	applied := map[uint]bool{}
	var interrupted *model.SchemaMigration
	if hasVersionTable {
		var rows []model.SchemaMigration
		if err := db.Order("version").Find(&rows).Error; err != nil {
			return nil, true, nil, fmt.Errorf("read schema version: %w", err)
		}
		for _, row := range rows {
			if row.Dirty {
				if interrupted != nil {
					return nil, true, nil, fmt.Errorf("database schema has interrupted migrations %d and %d; restore the pre-migration backup before starting", interrupted.Version, row.Version)
				}
				candidate := row
				interrupted = &candidate
				// A retry re-runs this version, so it must not count as applied.
				continue
			}
			applied[row.Version] = true
		}
	}

	known := make(map[uint]schemaMigration, len(migrations))
	for _, migration := range migrations {
		known[migration.version] = migration
	}
	for version := range applied {
		if _, ok := known[version]; !ok {
			return nil, hasVersionTable, nil, fmt.Errorf("database schema version %d is newer than or unknown to this panel binary", version)
		}
	}

	var replay *schemaMigration
	if interrupted != nil {
		migration, ok := known[interrupted.Version]
		if !ok {
			return nil, hasVersionTable, nil, fmt.Errorf("interrupted schema migration %d (%s) is newer than or unknown to this panel binary", interrupted.Version, interrupted.Name)
		}
		for version := range applied {
			if version > migration.version {
				return nil, hasVersionTable, nil, fmt.Errorf("interrupted schema migration %d (%s) is followed by applied migration %d; the schema history is inconsistent", migration.version, migration.name, version)
			}
		}
		if !allowReplay || !migrationReplaySafe[migration.version] {
			return nil, hasVersionTable, nil, fmt.Errorf("database schema migration %d (%s) is marked dirty; restore the pre-migration backup before starting", migration.version, migration.name)
		}
		replay = &migration
	}

	missingEarlier := false
	for _, migration := range migrations {
		if !applied[migration.version] {
			missingEarlier = true
			continue
		}
		if missingEarlier {
			return nil, hasVersionTable, nil, fmt.Errorf("database schema history is not an ordered prefix: version %d is applied after a missing migration", migration.version)
		}
	}
	return applied, hasVersionTable, replay, nil
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
		log.Printf("warning: prune old SQLite migration backups: %v", err)
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
