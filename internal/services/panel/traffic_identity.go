package panel

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

const (
	proxyTrafficNamePrefix   = "__singbox_panel_user__:"
	forwardTrafficNamePrefix = "__singbox_panel_forward__:"
)

// proxyTrafficName is metadata, not a client credential. The seed hash also
// prevents a restored/recreated account with the same numeric ID from claiming
// a previous account's delayed reports.
func proxyTrafficName(user *model.User) string {
	seed := user.ProxyToken
	if seed == "" {
		seed = user.SubToken
	}
	sum := sha256.Sum256([]byte("traffic:v1:" + seed))
	return fmt.Sprintf("%s%d:%x", proxyTrafficNamePrefix, user.ID, sum[:16])
}

// forwardTrafficName identifies the dedicated credential of one forwarded
// node. It belongs to the same account as proxyTrafficName but is never shared
// with the original node, so selecting node A and node A→B produces different
// sing-box users and independent routing.
func forwardTrafficName(user *model.User, forwardID uint) string {
	seed := user.ProxyToken
	if seed == "" {
		seed = user.SubToken
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("forward-traffic:v1:%s:%d", seed, forwardID)))
	return fmt.Sprintf("%s%d:%d:%x", forwardTrafficNamePrefix, user.ID, forwardID, sum[:16])
}

func reservedTrafficUsername(name string) bool {
	name = normalizeUsername(name)
	return strings.HasPrefix(name, proxyTrafficNamePrefix) || strings.HasPrefix(name, forwardTrafficNamePrefix)
}

// rememberProxyTrafficIdentities is called before publishing a managed config.
// A name is registered for a particular server/inbound and never reassigned.
func rememberProxyTrafficIdentities(db *gorm.DB, rows []model.UserTrafficIdentity) error {
	if len(rows) == 0 {
		return nil
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 200).Error; err != nil {
		return err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	var saved []model.UserTrafficIdentity
	if err := db.Where("server_id = ? AND inbound_id = ? AND name IN ?", rows[0].ServerID, rows[0].InboundID, names).Find(&saved).Error; err != nil {
		return err
	}
	byName := make(map[string]model.UserTrafficIdentity, len(saved))
	for _, row := range saved {
		byName[row.Name] = row
	}
	for _, row := range rows {
		current, ok := byName[row.Name]
		if !ok || current.UserID != row.UserID || current.UserKey != row.UserKey {
			return fmt.Errorf("proxy traffic identity already belongs to another account")
		}
	}
	return nil
}

// migrateUserTrafficIdentities snapshots legacy names once, before accounts can
// be renamed or reused. There is no expiry: an offline Agent can retain deltas
// for longer than a short rename grace period, including across panel restarts.
func migrateUserTrafficIdentities(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.UserTrafficIdentity{}); err != nil {
		return err
	}
	var users []model.User
	if err := tx.Find(&users).Error; err != nil {
		return err
	}
	var inbounds []model.Inbound
	if err := tx.Find(&inbounds).Error; err != nil {
		return err
	}
	for _, inbound := range inbounds {
		var settings singbox.InboundSettings
		if len(inbound.Settings) > 0 {
			if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
				return fmt.Errorf("read inbound %d traffic identity settings: %w", inbound.ID, err)
			}
		}
		if !settings.UseMultiUser(string(inbound.Type)) {
			continue
		}
		var rows []model.UserTrafficIdentity
		for i := range users {
			user := &users[i]
			if !user.HasInbound(inbound.ServerID, inbound.ID) {
				continue
			}
			rows = append(rows, model.UserTrafficIdentity{
				ServerID: inbound.ServerID, InboundID: inbound.ID,
				Name:   normalizeUsername(proxyIdentity(user, inbound.ID).Name),
				UserID: user.ID, UserKey: proxyTrafficName(user),
			})
		}
		// A retry never changes the owner of an already recorded legacy name.
		if len(rows) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 200).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
