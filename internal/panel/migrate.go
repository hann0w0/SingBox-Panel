package panel

import (
	"encoding/json"
	"log"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

// migrateSingleUserInbounds converts inbounds stored back when the panel could
// provision a per-user list into single-credential ones, and fills in any
// credential those rows never had (it used to come from the panel's users).
// Persisting it here — instead of patching at generation time — keeps a node's
// credentials stable across pushes.
func migrateSingleUserInbounds(db *gorm.DB) {
	var rows []model.Inbound
	if err := db.Find(&rows).Error; err != nil {
		return
	}
	migrated := 0
	for i := range rows {
		ib := &rows[i]
		var st singbox.InboundSettings
		if len(ib.Settings) > 0 {
			if err := json.Unmarshal(ib.Settings, &st); err != nil {
				continue
			}
		}
		if st.SingleUser && !credentialMissing(string(ib.Type), st) {
			continue
		}
		st.SingleUser = true
		if err := fillInboundSecrets(string(ib.Type), &st); err != nil {
			log.Printf("migrate inbound %d (%s): %v", ib.ID, ib.Tag, err)
			continue
		}
		blob, err := json.Marshal(st)
		if err != nil {
			continue
		}
		if err := db.Model(&model.Inbound{}).Where("id = ?", ib.ID).
			Update("settings", model.JSONText(blob)).Error; err != nil {
			log.Printf("migrate inbound %d (%s): %v", ib.ID, ib.Tag, err)
			continue
		}
		migrated++
	}
	if migrated > 0 {
		log.Printf("migrated %d inbound(s) to single-credential mode", migrated)
	}
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
