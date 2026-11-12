package panel

import (
	"encoding/json"
	"fmt"
	"log"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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

// migrateSharedRuleSets turns per-server rule-set rows into shared definitions
// plus per-server assignments. Rows with identical connection details and tag
// collapse into one definition; download_detour stays on the assignment
// because outbound tags are local to each node.
func migrateSharedRuleSets(tx *gorm.DB) error {
	type legacyRuleSet struct {
		ID             uint
		ServerID       uint
		Tag            string
		Type           string
		Format         string
		URL            string
		Path           string
		DownloadDetour string
		UpdateInterval string
	}
	var rows []legacyRuleSet
	if err := tx.Table("rule_sets").Order("id").Find(&rows).Error; err != nil {
		return err
	}
	if err := tx.AutoMigrate(&model.RuleSet{}, &model.ServerRuleSet{}); err != nil {
		return err
	}
	type definitionKey struct {
		Tag, Type, Format, URL, Path, UpdateInterval string
	}
	definitions := make(map[definitionKey]uint)
	var existing []model.RuleSet
	if err := tx.Order("id").Find(&existing).Error; err != nil {
		return err
	}
	for _, row := range existing {
		definitions[definitionKey{row.Tag, row.Type, row.Format, row.URL, row.Path, row.UpdateInterval}] = row.ID
	}
	for _, row := range rows {
		key := definitionKey{row.Tag, row.Type, row.Format, row.URL, row.Path, row.UpdateInterval}
		ruleSetID := definitions[key]
		if ruleSetID == 0 {
			created := model.RuleSet{
				Tag: row.Tag, Type: row.Type, Format: row.Format,
				URL: row.URL, Path: row.Path, UpdateInterval: row.UpdateInterval,
			}
			if err := tx.Create(&created).Error; err != nil {
				return err
			}
			definitions[key] = created.ID
			ruleSetID = created.ID
		}
		assignment := model.ServerRuleSet{ServerID: row.ServerID, RuleSetID: ruleSetID, DownloadDetour: row.DownloadDetour}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&assignment).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasColumn(&model.RuleSet{}, "server_id") {
		if err := tx.Migrator().DropColumn(&model.RuleSet{}, "server_id"); err != nil {
			return err
		}
	}
	if tx.Migrator().HasColumn(&model.RuleSet{}, "download_detour") {
		if err := tx.Migrator().DropColumn(&model.RuleSet{}, "download_detour"); err != nil {
			return err
		}
	}
	return nil
}
