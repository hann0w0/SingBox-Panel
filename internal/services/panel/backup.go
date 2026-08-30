package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

const (
	backupFormatVersion     = 2
	backupCredentialVersion = 1
)

// backupMetadata is deliberately machine-readable. MANIFEST.txt remains a
// human-readable companion, while this record lets restore reject a newer
// incompatible backup before touching the live database.
type backupMetadata struct {
	FormatVersion     int    `json:"format_version"`
	PanelVersion      string `json:"panel_version"`
	SchemaVersion     uint   `json:"schema_version"`
	CreatedAt         string `json:"created_at"`
	DatabaseDriver    string `json:"database_driver"`
	DatabaseSHA256    string `json:"database_sha256"`
	BaseURL           string `json:"base_url,omitempty"`
	CredentialVersion int    `json:"credential_version"`
	CredentialCount   int    `json:"credential_count"`
	CredentialSHA256  string `json:"credential_sha256"`
}

type backupCredentialRecord struct {
	UserID    uint   `json:"user_id"`
	InboundID uint   `json:"inbound_id"`
	Protocol  string `json:"protocol"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	UUID      string `json:"uuid"`
	Password  string `json:"password"`
}

type credentialFingerprint struct {
	SHA256    string
	Count     int
	Available bool
}

func currentSchemaVersion() uint {
	var version uint
	for _, migration := range applicationMigrations {
		if migration.version > version {
			version = migration.version
		}
	}
	return version
}

func schemaVersionInDB(db *gorm.DB) (uint, error) {
	if !db.Migrator().HasTable(&model.SchemaMigration{}) {
		return 0, nil
	}
	var version uint
	if err := db.Raw("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version).Error; err != nil {
		return 0, err
	}
	return version, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func openSQLiteReadOnly(path string) (*gorm.DB, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath), RawQuery: "mode=ro"}).String()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
	}
	return db, nil
}

func calculateCredentialFingerprint(db *gorm.DB) (credentialFingerprint, error) {
	var users []model.User
	if err := db.Order("id").Find(&users).Error; err != nil {
		// Very old databases predate ProxyToken. The schema migration creates it
		// later, but a pre-migration fingerprint cannot be calculated safely.
		if strings.Contains(strings.ToLower(err.Error()), "proxy_token") {
			return credentialFingerprint{}, nil
		}
		return credentialFingerprint{}, err
	}
	for _, user := range users {
		if strings.TrimSpace(user.ProxyToken) == "" {
			return credentialFingerprint{}, nil
		}
	}

	var inbounds []model.Inbound
	if err := db.Order("id").Find(&inbounds).Error; err != nil {
		return credentialFingerprint{}, err
	}
	records := make([]backupCredentialRecord, 0)
	for i := range inbounds {
		inbound := &inbounds[i]
		var settings singbox.InboundSettings
		if len(inbound.Settings) > 0 {
			if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
				return credentialFingerprint{}, fmt.Errorf("解析入站 %d 配置失败：%w", inbound.ID, err)
			}
		}
		if !settings.UseMultiUser(string(inbound.Type)) {
			continue
		}
		for i := range users {
			user := &users[i]
			if !user.HasInbound(inbound.ServerID, inbound.ID) {
				continue
			}
			proxy := proxyIdentity(user, inbound.ID)
			records = append(records, backupCredentialRecord{
				UserID: user.ID, InboundID: inbound.ID, Protocol: string(inbound.Type),
				Name: proxy.Name, Username: proxy.Username, UUID: proxy.UUID, Password: proxy.Password,
			})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].UserID != records[j].UserID {
			return records[i].UserID < records[j].UserID
		}
		if records[i].InboundID != records[j].InboundID {
			return records[i].InboundID < records[j].InboundID
		}
		return records[i].Protocol < records[j].Protocol
	})
	raw, err := json.Marshal(records)
	if err != nil {
		return credentialFingerprint{}, err
	}
	sum := sha256.Sum256(raw)
	return credentialFingerprint{SHA256: hex.EncodeToString(sum[:]), Count: len(records), Available: true}, nil
}

func comparePanelVersions(left, right string) (int, error) {
	parse := func(value string) ([3]int, string, error) {
		var parts [3]int
		value = strings.TrimPrefix(strings.TrimSpace(value), "v")
		if value == "" {
			return parts, "", fmt.Errorf("版本号为空")
		}
		value = strings.SplitN(value, "+", 2)[0]
		pre := ""
		if i := strings.IndexByte(value, '-'); i >= 0 {
			pre = value[i+1:]
			value = value[:i]
		}
		segments := strings.Split(value, ".")
		if len(segments) != 3 {
			return parts, "", fmt.Errorf("无法识别版本号 %q", value)
		}
		for i := range parts {
			n, err := strconv.Atoi(segments[i])
			if err != nil || n < 0 {
				return parts, "", fmt.Errorf("无法识别版本号 %q", value)
			}
			parts[i] = n
		}
		return parts, pre, nil
	}
	leftParts, leftPre, err := parse(left)
	if err != nil {
		return 0, err
	}
	rightParts, rightPre, err := parse(right)
	if err != nil {
		return 0, err
	}
	for i := range leftParts {
		if leftParts[i] < rightParts[i] {
			return -1, nil
		}
		if leftParts[i] > rightParts[i] {
			return 1, nil
		}
	}
	if leftPre == rightPre {
		return 0, nil
	}
	if leftPre == "" {
		return 1, nil
	}
	if rightPre == "" {
		return -1, nil
	}
	return strings.Compare(leftPre, rightPre), nil
}
