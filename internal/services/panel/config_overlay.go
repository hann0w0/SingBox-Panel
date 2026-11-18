package panel

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// initializeConfigOverlay runs inside the import transaction after structured
// rows have been synchronized. A failed snapshot rolls back the entire import.
func initializeConfigOverlay(tx *gorm.DB, serverID uint, parsed *singbox.ParsedConfig, raw []byte) error {
	var srv model.Server
	if err := tx.First(&srv, serverID).Error; err != nil {
		return err
	}
	seed := singbox.ConfigOverlay{Version: 1, Original: raw, Bindings: map[string][]string{}}
	var root struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	seed.Bindings["outbounds"] = make([]string, len(root.Outbounds))
	seed.Baseline.Input.StatsController, seed.Baseline.Input.StatsSecret, seed.Baseline.Input.StatsAPI = importedStatsSettings(raw)
	srv.ConfigOverlay, _ = json.Marshal(seed)
	srv.ConfigResidual = nil
	srv.ConfigMode = model.ConfigModeManaged
	var projection singbox.ConfigProjection
	orch := NewOrchestrator(tx, nil)
	if _, err := orch.buildServerConfig(&srv, &projection); err != nil {
		return fmt.Errorf("capture panel baseline: %w", err)
	}
	var rules []model.RouteRule
	if err := tx.Where("server_id = ?", serverID).Order("sort, id").Find(&rules).Error; err != nil {
		return err
	}
	if len(rules) != len(parsed.Rules) {
		return fmt.Errorf("imported rule identities do not match parsed rules")
	}
	sources := make(map[int]string, len(rules))
	for i, rule := range rules {
		sources[parsed.Rules[i].SourceIndex] = configRuleIdentity(rule)
	}
	overlay, err := singbox.NewConfigOverlay(raw, projection, sources)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(overlay)
	if err != nil {
		return err
	}
	// Validate bindings now, never defer a malformed snapshot to the next push.
	if _, err := singbox.ApplyConfigOverlay(encoded, projection); err != nil {
		return err
	}
	return tx.Model(&model.Server{}).Where("id = ?", serverID).Update("config_overlay", model.JSONText(encoded)).Error
}

func configRuleIdentity(rule model.RouteRule) string {
	var match singbox.RuleInput
	if json.Unmarshal(rule.Match, &match) == nil && len(match.AuthUser) == 1 && strings.HasPrefix(match.AuthUser[0], forwardTrafficNamePrefix) {
		parts := strings.Split(strings.TrimPrefix(match.AuthUser[0], forwardTrafficNamePrefix), ":")
		if len(parts) == 3 {
			return "forward:" + parts[0] + ":" + parts[1]
		}
	}
	return fmt.Sprint(rule.ID)
}

// Existing raw nodes were never editable. Re-importing their persisted source
// at upgrade time can therefore establish the baseline without losing edits.
func migrateRawConfigOverlays(tx *gorm.DB) error {
	var servers []model.Server
	if err := tx.Where("config_mode = ? AND (config_overlay IS NULL OR config_overlay = '' OR config_overlay = 'null')", model.ConfigModeRaw).Find(&servers).Error; err != nil {
		return err
	}
	for _, srv := range servers {
		if len(srv.RawConfig) == 0 {
			continue
		}
		parsed, err := singbox.ParseServerConfig(srv.RawConfig)
		if err != nil {
			return fmt.Errorf("server %d config: %w", srv.ID, err)
		}
		app := &App{db: tx, orch: NewOrchestrator(tx, nil)}
		if err := app.applyImportUnlocked(&srv, parsed, srv.RawConfig); err != nil {
			return fmt.Errorf("server %d config: %w", srv.ID, err)
		}
	}
	return nil
}

// Already-managed nodes may have edits newer than RawConfig. Capture their
// current desired configuration without re-importing the stale raw snapshot.
func migrateManagedConfigOverlays(tx *gorm.DB) error {
	var servers []model.Server
	if err := tx.Where("config_mode <> ? AND config_residual IS NOT NULL AND config_residual <> 'null' AND config_residual <> '' AND (config_overlay IS NULL OR config_overlay = 'null' OR config_overlay = '')", model.ConfigModeRaw).Find(&servers).Error; err != nil {
		return err
	}
	orch := NewOrchestrator(tx, nil)
	for _, srv := range servers {
		var projection singbox.ConfigProjection
		raw, err := orch.buildServerConfig(&srv, &projection)
		if err != nil {
			return fmt.Errorf("server %d config: %w", srv.ID, err)
		}
		overlay, err := singbox.NewRenderedConfigOverlay(raw, projection)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(overlay)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.Server{}).Where("id = ?", srv.ID).Updates(map[string]any{"config_overlay": model.JSONText(encoded), "config_residual": model.JSONText(nil)}).Error; err != nil {
			return err
		}
	}
	return nil
}

func preservedConfigReferences(db *gorm.DB, serverID uint, key string) (map[string]bool, error) {
	var srv model.Server
	if err := db.Select("id", "config_overlay").First(&srv, serverID).Error; err != nil {
		return nil, err
	}
	if !singbox.HasConfigOverlay(srv.ConfigOverlay) {
		return nil, nil
	}
	active := make(map[string][]string)
	var inbounds []model.Inbound
	if err := db.Select("id").Where("server_id = ? AND enabled = ?", serverID, true).Find(&inbounds).Error; err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		active["inbounds"] = append(active["inbounds"], fmt.Sprint(inbound.ID))
	}
	var outbounds []model.Outbound
	if err := db.Select("id").Where("server_id = ? OR owner_user_id <> 0", serverID).Find(&outbounds).Error; err != nil {
		return nil, err
	}
	for _, outbound := range outbounds {
		active["outbounds"] = append(active["outbounds"], fmt.Sprint(outbound.ID))
	}
	var rules []model.RouteRule
	if err := db.Where("server_id = ? AND enabled = ?", serverID, true).Find(&rules).Error; err != nil {
		return nil, err
	}
	for _, rule := range rules {
		active["rules"] = append(active["rules"], configRuleIdentity(rule))
	}
	var ruleSets []model.ServerRuleSet
	if err := db.Where("server_id = ?", serverID).Find(&ruleSets).Error; err != nil {
		return nil, err
	}
	for _, ruleSet := range ruleSets {
		active["rule_set"] = append(active["rule_set"], fmt.Sprint(ruleSet.RuleSetID))
	}
	values, err := singbox.PreservedConfigReferences(srv.ConfigOverlay, key, active)
	return stringSet(values), err
}

func unmanagedConfigEntries(db *gorm.DB, serverID uint, section string) ([]json.RawMessage, error) {
	var srv model.Server
	if err := db.Select("id", "config_overlay").First(&srv, serverID).Error; err != nil {
		return nil, err
	}
	return singbox.UnmanagedConfigEntries(srv.ConfigOverlay, section)
}

func preservedReferencesAnywhere(db *gorm.DB, key, tag string) (bool, error) {
	var servers []model.Server
	if err := db.Select("id", "config_overlay").Find(&servers).Error; err != nil {
		return false, err
	}
	for _, srv := range servers {
		values, err := preservedConfigReferences(db, srv.ID, key)
		if err != nil {
			return false, err
		}
		for value := range values {
			if value == tag {
				return true, nil
			}
		}
	}
	return false, nil
}
