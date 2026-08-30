package panel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

// InitDB opens the configured database, migrates the schema, and seeds a
// default group and the bootstrap admin.
func InitDB(cfg config.PanelConfig) (*gorm.DB, error) {
	var dial gorm.Dialector
	var sqlitePath string
	switch cfg.Database.Driver {
	case "sqlite", "":
		sqlitePath = sqliteFilePath(cfg.Database.DSN)
		if sqlitePath != "" {
			dir := filepath.Dir(sqlitePath)
			if dir != "" && dir != "." {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return nil, fmt.Errorf("create sqlite dir: %w", err)
				}
			}
		}
		dial = sqlite.Open(sqliteDSN(cfg.Database.DSN))
	case "mysql":
		dial = mysql.Open(cfg.Database.DSN)
	case "postgres":
		dial = postgres.Open(cfg.Database.DSN)
	default:
		return nil, fmt.Errorf("unsupported db driver %q", cfg.Database.Driver)
	}

	db, err := gorm.Open(dial, &gorm.Config{
		Logger:         newDatabaseLogger(log.New(os.Stdout, "\r\n", log.LstdFlags), gormlogger.Warn),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if sqlitePath != "" {
		if err := protectSQLiteFiles(sqlitePath); err != nil {
			return nil, fmt.Errorf("protect sqlite database: %w", err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get database pool: %w", err)
	}
	if strings.EqualFold(cfg.Database.Driver, "sqlite") || cfg.Database.Driver == "" {
		// Subscription reconciliation can run in parallel with HTTP writes. WAL
		// plus a busy timeout prevents transient writer collisions, while a single
		// pooled connection keeps SQLite transaction ordering deterministic.
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		sqlDB.SetConnMaxLifetime(0)
		sqlDB.SetConnMaxIdleTime(0)
	} else {
		sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
		sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
		sqlDB.SetConnMaxLifetime(cfg.Database.ConnMaxLifetime)
		sqlDB.SetConnMaxIdleTime(cfg.Database.ConnMaxIdleTime)
	}
	if err := runSchemaMigrations(db, cfg.Database); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := seed(db, cfg); err != nil {
		return nil, err
	}
	if sqlitePath != "" {
		if err := protectSQLiteFiles(sqlitePath); err != nil {
			return nil, fmt.Errorf("protect sqlite database journals: %w", err)
		}
	}
	return db, nil
}

func protectSQLiteFiles(databaseFile string) error {
	for _, path := range []string{databaseFile, databaseFile + "-wal", databaseFile + "-shm"} {
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func newDatabaseLogger(writer gormlogger.Writer, level gormlogger.LogLevel) gormlogger.Interface {
	base := gormlogger.New(writer, gormlogger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  level,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      true,
		Colorful:                  false,
	})
	return &secureDatabaseLogger{Interface: base}
}

type secureDatabaseLogger struct {
	gormlogger.Interface
}

func (l *secureDatabaseLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return &secureDatabaseLogger{Interface: l.Interface.LogMode(level)}
}

func (l *secureDatabaseLogger) Trace(
	ctx context.Context,
	begin time.Time,
	fc func() (string, int64),
	err error,
) {
	l.Interface.Trace(ctx, begin, func() (string, int64) {
		_, rows := fc()
		return "[SQL redacted]", rows
	}, err)
}

func sqliteDSN(dsn string) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
}

func seed(db *gorm.DB, cfg config.PanelConfig) error {
	// Create the bootstrap admin ONLY when no admin exists yet (first boot on an
	// empty database). After that the admin is managed in the panel: the ADMIN /
	// ADMIN_PASSWORD env vars are not re-applied on restart, so they never create
	// duplicates or override an in-app password change, and can be removed after
	// the first boot.
	var adminCount int64
	db.Model(&model.User{}).Where("role = ?", model.RoleAdmin).Count(&adminCount)
	if adminCount > 0 {
		return nil
	}

	email := strings.TrimSpace(cfg.Admin.Email)
	if email == "" {
		email = "admin"
	}
	email, normalizedEmail, err := validateUsername(email)
	if err != nil {
		return fmt.Errorf("bootstrap administrator username: %w", err)
	}
	pass := cfg.Admin.Password
	if pass == "" {
		pass = randHex(8)
		log.Printf("========================================")
		log.Printf(" bootstrap admin: %s", email)
		log.Printf(" bootstrap password: %s", pass)
		log.Printf("========================================")
	}
	if err := validateNewPassword(pass); err != nil {
		return fmt.Errorf("bootstrap administrator password: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return db.Create(&model.User{
		Email:           email,
		EmailNormalized: &normalizedEmail,
		Password:        string(hash),
		Role:            model.RoleAdmin,
		Enabled:         true,
		SubToken:        randHex(16),
		ProxyToken:      randHex(32),
	}).Error
}

// hashPassword bcrypts a plaintext password.
func hashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(h), err
}

// dummyHash is a valid bcrypt hash of a fixed value. Comparing against it when
// an account does not exist keeps the response time indistinguishable from a
// wrong-password attempt, so login cannot be used to enumerate usernames.
var dummyHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte("singbox-panel-timing-equalizer"), bcrypt.DefaultCost)
	if err != nil {
		return ""
	}
	return string(h)
}()

// checkPassword verifies a plaintext password against a bcrypt hash.
func checkPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// touchServerSeen updates a server's online/last-seen bookkeeping.
func touchServerSeen(db *gorm.DB, serverID uint, online bool) {
	now := time.Now()
	db.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
		"online":    online,
		"last_seen": &now,
	})
}

// ErrNotFound is returned by store helpers when a row is missing.
var ErrNotFound = errors.New("not found")
