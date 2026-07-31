package panel

import (
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
	"github.com/hann0w0/singbox-panel/internal/model"
)

// InitDB opens the configured database, migrates the schema, and seeds a
// default group and the bootstrap admin.
func InitDB(cfg config.PanelConfig) (*gorm.DB, error) {
	var dial gorm.Dialector
	switch cfg.Database.Driver {
	case "sqlite", "":
		if dir := filepath.Dir(cfg.Database.DSN); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create sqlite dir: %w", err)
			}
		}
		dial = sqlite.Open(cfg.Database.DSN)
	case "mysql":
		dial = mysql.Open(cfg.Database.DSN)
	case "postgres":
		dial = postgres.Open(cfg.Database.DSN)
	default:
		return nil, fmt.Errorf("unsupported db driver %q", cfg.Database.Driver)
	}

	db, err := gorm.Open(dial, &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := seed(db, cfg); err != nil {
		return nil, err
	}
	migrateSingleUserInbounds(db)
	return db, nil
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
	pass := cfg.Admin.Password
	if pass == "" {
		pass = randHex(8)
		log.Printf("========================================")
		log.Printf(" bootstrap admin: %s", email)
		log.Printf(" bootstrap password: %s", pass)
		log.Printf("========================================")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return db.Create(&model.User{
		Email:    email,
		Password: string(hash),
		Role:     model.RoleAdmin,
		Enabled:  true,
		SubToken: randHex(16),
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
