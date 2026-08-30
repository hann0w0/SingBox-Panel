package panel

import (
	"encoding/json"
	"fmt"
	"log"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// migrateSingleUserInbounds converts inbounds stored back when the panel could
// provision a per-user list into single-credential ones, and fills in any
// credential those rows never had (it used to come from the panel's users).
// Persisting it here — instead of patching at generation time — keeps a node's
// credentials stable across pushes.
func migrateSingleUserInbounds(db *gorm.DB) error {
	var rows []model.Inbound
	if err := db.Find(&rows).Error; err != nil {
		return fmt.Errorf("list inbounds for credential-mode migration: %w", err)
	}
	migrated := 0
	for i := range rows {
		ib := &rows[i]
		var st singbox.InboundSettings
		if len(ib.Settings) > 0 {
			if err := json.Unmarshal(ib.Settings, &st); err != nil {
				return fmt.Errorf("decode inbound %d settings: %w", ib.ID, err)
			}
		}
		if st.SingleUser && !credentialMissing(string(ib.Type), st) {
			continue
		}
		st.SingleUser = true
		st.MultiUser = false
		if err := fillInboundSecrets(string(ib.Type), &st); err != nil {
			return fmt.Errorf("generate inbound %d credentials: %w", ib.ID, err)
		}
		blob, err := json.Marshal(st)
		if err != nil {
			return fmt.Errorf("encode inbound %d settings: %w", ib.ID, err)
		}
		if err := db.Model(&model.Inbound{}).Where("id = ?", ib.ID).
			Update("settings", model.JSONText(blob)).Error; err != nil {
			return fmt.Errorf("save inbound %d settings: %w", ib.ID, err)
		}
		migrated++
	}
	if migrated > 0 {
		log.Printf("migrated %d inbound(s) to single-credential mode", migrated)
	}
	return nil
}

// credentialMissing reports whether a single-credential inbound still lacks the
// secret its protocol needs.
func credentialMissing(typ string, s singbox.InboundSettings) bool {
	switch typ {
	case "vless", "vmess":
		return s.UUID == ""
	case "tuic":
		return s.UUID == "" || s.Password == ""
	case "trojan", "hysteria2", "hysteria", "naive", "anytls":
		return s.Password == ""
	case "shadowsocks":
		return s.SSServerPSK == ""
	case "snell":
		return s.SnellPSK == ""
	case "shadowtls":
		if s.ShadowTLSVersion == 2 {
			return s.ShadowTLSPassword == ""
		}
		return s.Password == ""
	}
	return false
}
