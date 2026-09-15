package panel

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// normalizeLegacySOCKSSettings converts the removed panel-managed multi-user
// mode to a persistent shared login. Explicit single-user/no-auth settings are
// untouched; a legacy multi-user listener must never become an open proxy.
func normalizeLegacySOCKSSettings(s *singbox.InboundSettings) {
	if !s.MultiUser {
		return
	}
	if s.Username == "" || s.Username == "__singbox_panel_disabled__" {
		s.Username = "singbox"
	}
	if s.Password == "" {
		s.Password = randHex(32)
	}
	s.MultiUser = false
	s.SingleUser = true
}

func migrateSOCKSSingleUserInbounds(tx *gorm.DB) error {
	var servers []model.Server
	if err := tx.Select("id", "config_mode").Find(&servers).Error; err != nil {
		return err
	}
	rawServers := make(map[uint]bool)
	for _, server := range servers {
		if server.ConfigMode == model.ConfigModeRaw {
			rawServers[server.ID] = true
		}
	}
	var inbounds []model.Inbound
	if err := tx.Where("type = ?", model.InboundSocks).Find(&inbounds).Error; err != nil {
		return err
	}
	for _, inbound := range inbounds {
		if rawServers[inbound.ServerID] || len(inbound.Settings) == 0 {
			continue
		}
		var settings singbox.InboundSettings
		if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
			return fmt.Errorf("migrate SOCKS inbound %d: %w", inbound.ID, err)
		}
		if !settings.MultiUser {
			continue
		}
		normalizeLegacySOCKSSettings(&settings)
		// Keep unrecognized settings intact during a one-time data migration.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(inbound.Settings, &fields); err != nil {
			return err
		}
		delete(fields, "multi_user")
		fields["single_user"] = json.RawMessage(`true`)
		fields["username"], _ = json.Marshal(settings.Username)
		fields["password"], _ = json.Marshal(settings.Password)
		encoded, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.Inbound{}).Where("id = ?", inbound.ID).
			Update("settings", model.JSONText(encoded)).Error; err != nil {
			return err
		}
	}
	return nil
}
