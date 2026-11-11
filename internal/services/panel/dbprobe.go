package panel

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Database compatibility preflight for version switches.
//
// A newer panel can migrate the database past what an older binary understands.
// Without a preflight the swap installs the older binary, the binary refuses to
// start ("schema version N is newer than or unknown to this panel binary"), and
// the operator only sees a panel that will not come back. Running the target
// binary against the live database *before* the swap turns that into a clear,
// non-destructive rejection: nothing is stopped or replaced.
//
// The probe opens the database read-only and only inspects the migration
// ledger, so it never writes, never migrates, and is safe to run while the
// current panel is still serving traffic.

// CheckDatabaseCompatibility opens the configured database read-only and
// reports whether this binary could run against it. It returns a human-readable
// reason when it could not.
func CheckDatabaseCompatibility(driver, dsn string) error {
	db, err := openProbeDatabase(databaseProbeConfig{Driver: driver, DSN: dsn})
	if err != nil {
		return fmt.Errorf("无法打开数据库：%w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("无法打开数据库：%w", err)
	}
	defer sqlDB.Close()

	// Reuse the exact ledger validation used at startup so a probe can never
	// disagree with the real boot path.
	_, _, _, err = validateSchemaMigrationHistory(db, applicationMigrations, false)
	if err != nil {
		return err
	}
	return nil
}

// databaseProbeConfig is the subset of panel configuration the probe needs.
type databaseProbeConfig struct {
	Driver string
	DSN    string
}

func openProbeDatabase(cfg databaseProbeConfig) (*gorm.DB, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Driver)) {
	case "", "sqlite":
		path := sqliteFilePath(cfg.DSN)
		if path == "" {
			return nil, fmt.Errorf("数据库路径为空或为内存数据库")
		}
		return openSQLiteReadOnly(path)
	case "mysql", "postgres":
		// Network databases are only read by the check itself; an operator-managed
		// database cannot be opened in a portable read-only mode, so the probe
		// deliberately skips them rather than mutate anything.
		return nil, fmt.Errorf("网络数据库请在升级前单独确认目标版本兼容性")
	default:
		return nil, fmt.Errorf("不支持的数据库驱动 %q", cfg.Driver)
	}
}
