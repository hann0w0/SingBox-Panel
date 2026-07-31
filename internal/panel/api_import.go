package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"gorm.io/gorm"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/protocol"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

type importReq struct {
	// Config, when non-empty, is parsed instead of reading the agent's file.
	Config string `json:"config"`
	// DryRun only reports what would be imported, changing nothing.
	DryRun bool `json:"dry_run"`
}

// importSummary is what the UI shows before/after an import.
type importSummary struct {
	Inbounds  []importedInbound `json:"inbounds"`
	Outbounds []importedItem    `json:"outbounds"`
	Rules     []importedRule    `json:"rules"`
	RuleSets  []importedItem    `json:"rulesets"`
	Final     string            `json:"final"`
	Skipped   []string          `json:"skipped"`
}

type importedInbound struct {
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	ListenPort int    `json:"listen_port"`
	SingleUser bool   `json:"single_user"`
	Users      int    `json:"users"`
}

type importedItem struct {
	Tag  string `json:"tag"`
	Type string `json:"type"`
	Info string `json:"info"`
}

type importedRule struct {
	Inbound  []string `json:"inbound"`
	Outbound string   `json:"outbound"`
	Info     string   `json:"info"`
}

// fetchRemoteConfigRaw asks the agent for the server's current config.json.
func (a *App) fetchRemoteConfigRaw(ctx context.Context, serverID uint) (string, error) {
	res, err := a.hub.SendCommand(ctx, serverID, protocol.CmdGetConfig, nil)
	if err != nil {
		return "", err
	}
	var data protocol.ConfigData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		return "", fmt.Errorf("解析 Agent 返回失败: %w", err)
	}
	if !data.Exists {
		return "", fmt.Errorf("该服务器上没有 %s", data.Path)
	}
	return data.Raw, nil
}

// importRemoteConfig adopts an existing sing-box config into the panel: it
// parses the server's config.json (or a pasted one) and replaces this server's
// panel-side inbounds/outbounds/rules with what the file actually contains.
func (a *App) importRemoteConfig(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var srv model.Server
	if err := a.db.First(&srv, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	var req importReq
	if !bindJSON(c, &req) {
		return
	}

	raw := req.Config
	if raw == "" {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		var err error
		if raw, err = a.fetchRemoteConfigRaw(ctx, id); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
	}

	parsed, err := singbox.ParseServerConfig([]byte(raw))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sum := buildImportSummary(parsed)
	if req.DryRun {
		c.JSON(http.StatusOK, gin.H{"summary": sum, "dry_run": true})
		return
	}
	// Replace this server's panel-side routing model with the parsed one. The
	// server's own config file is untouched — importing only teaches the panel
	// what is already running there.
	if err := a.applyImport(&srv, parsed, []byte(raw)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "summary": sum})
}

func buildImportSummary(p *singbox.ParsedConfig) importSummary {
	sum := importSummary{Final: p.Final, Skipped: p.Skipped}
	for _, in := range p.Inbounds {
		sum.Inbounds = append(sum.Inbounds, importedInbound{
			Tag: in.Tag, Type: in.Type, ListenPort: in.ListenPort,
			SingleUser: in.Settings.SingleUser, Users: in.UserCount,
		})
	}
	for _, ob := range p.Outbounds {
		sum.Outbounds = append(sum.Outbounds, importedItem{
			Tag: ob.Tag, Type: ob.Type, Info: fmt.Sprintf("%s:%d", ob.Server, ob.ServerPort),
		})
	}
	for _, r := range p.Rules {
		info := ""
		if n := len(r.Match.DomainSuffix) + len(r.Match.Domain); n > 0 {
			info = fmt.Sprintf("域名×%d", n)
		}
		if n := len(r.Match.IPCIDR); n > 0 {
			info += fmt.Sprintf(" IP×%d", n)
		}
		sum.Rules = append(sum.Rules, importedRule{Inbound: r.Match.Inbound, Outbound: r.Outbound, Info: info})
	}
	for _, rs := range p.RuleSets {
		sum.RuleSets = append(sum.RuleSets, importedItem{Tag: rs.Tag, Type: rs.Format, Info: rs.URL})
	}
	return sum
}

// applyImport writes the parsed view of a raw config into the DB. Existing
// rows with the same tag keep their IDs so per-inbound user grants do not break
// whenever an administrator imports or edits config.json. A lossless round
// trip is promoted to managed mode automatically; otherwise raw JSON remains
// the source of truth so fields the panel cannot model are never discarded.
func (a *App) applyImport(srv *model.Server, p *singbox.ParsedConfig, raw []byte) error {
	// Import changes both the structured rows and the raw source-of-truth mode.
	// Serialize it with reconnect pushes, raw edits and explicit mode switches so
	// no concurrent operation can observe or apply a half-transitioned state.
	lock := a.orch.serverLock(srv.ID)
	lock.Lock()
	defer lock.Unlock()
	return a.applyImportUnlocked(srv, p, raw)
}

// applyImportUnlocked performs the database transaction while the caller owns
// the server configuration lock.
func (a *App) applyImportUnlocked(srv *model.Server, p *singbox.ParsedConfig, raw []byte) error {
	mode := importedConfigMode(p, raw)
	return a.db.Transaction(func(tx *gorm.DB) error {
		if err := syncImportedInbounds(tx, srv.ID, p.Inbounds); err != nil {
			return err
		}
		if err := syncImportedOutbounds(tx, srv.ID, p.Outbounds); err != nil {
			return err
		}
		if err := replaceImportedRules(tx, srv.ID, p.Rules); err != nil {
			return err
		}
		if err := syncImportedRuleSets(tx, srv.ID, p.RuleSets); err != nil {
			return err
		}
		return tx.Model(&model.Server{}).Where("id = ?", srv.ID).Updates(map[string]any{
			"final_outbound":     p.Final,
			"config_mode":        mode,
			"raw_config":         model.JSONText(append([]byte(nil), raw...)),
			"config_initialized": true,
		}).Error
	})
}

func importedConfigMode(p *singbox.ParsedConfig, raw []byte) string {
	generated, err := buildManagedConfigFromImport(p)
	if err != nil || !equivalentJSON(raw, generated) {
		return model.ConfigModeRaw
	}
	return model.ConfigModeManaged
}

func buildManagedConfigFromImport(p *singbox.ParsedConfig) ([]byte, error) {
	inbounds := make([]singbox.InboundInput, 0, len(p.Inbounds))
	for _, inbound := range p.Inbounds {
		settings := inbound.Settings
		// Managed mode intentionally emits one credential per inbound. A manual
		// multi-user config therefore cannot pass the lossless comparison.
		settings.SingleUser = true
		inbounds = append(inbounds, singbox.InboundInput{
			Tag: inbound.Tag, Type: inbound.Type, ListenPort: inbound.ListenPort, Settings: settings,
		})
	}
	outbounds := make([]singbox.OutboundInput, 0, len(p.Outbounds))
	for _, outbound := range p.Outbounds {
		outbounds = append(outbounds, singbox.OutboundInput{
			Tag: outbound.Tag, Type: outbound.Type, Server: outbound.Server, ServerPort: outbound.ServerPort,
			Username: outbound.Username, UUID: outbound.UUID, Password: outbound.Password, Settings: outbound.Settings,
		})
	}
	rules := make([]singbox.RuleInput, 0, len(p.Rules))
	for _, parsedRule := range p.Rules {
		rule := parsedRule.Match
		rule.Outbound = parsedRule.Outbound
		rules = append(rules, rule)
	}
	return singbox.BuildServerConfig(singbox.ServerConfigInput{
		Inbounds: inbounds, Outbounds: outbounds, Rules: rules,
		RuleSets: p.RuleSets, Final: p.Final,
	})
}

func equivalentJSON(left, right []byte) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	removeInternalTrafficAPI(leftValue)
	removeInternalTrafficAPI(rightValue)
	leftCanonical, err := json.Marshal(leftValue)
	if err != nil {
		return false
	}
	rightCanonical, err := json.Marshal(rightValue)
	return err == nil && string(leftCanonical) == string(rightCanonical)
}

// removeInternalTrafficAPI ignores only the exact loopback endpoint the panel
// injects into managed configs. Any other experimental field remains part of
// the lossless comparison and therefore keeps the source in raw mode.
func removeInternalTrafficAPI(value any) {
	root, ok := value.(map[string]any)
	if !ok {
		return
	}
	experimental, ok := root["experimental"].(map[string]any)
	if !ok {
		return
	}
	clashAPI, ok := experimental["clash_api"].(map[string]any)
	if !ok || len(clashAPI) != 1 || clashAPI["external_controller"] != protocol.LocalTrafficAddress {
		return
	}
	delete(experimental, "clash_api")
	if len(experimental) == 0 {
		delete(root, "experimental")
	}
}

func deleteRowsExcept(tx *gorm.DB, serverID uint, keepIDs []uint, row any) error {
	query := tx.Where("server_id = ?", serverID)
	if len(keepIDs) > 0 {
		query = query.Where("id NOT IN ?", keepIDs)
	}
	return query.Delete(row).Error
}

func syncImportedInbounds(tx *gorm.DB, serverID uint, parsed []singbox.ParsedInbound) error {
	var existing []model.Inbound
	if err := tx.Where("server_id = ?", serverID).Find(&existing).Error; err != nil {
		return err
	}
	byTag := make(map[string]*model.Inbound, len(existing))
	for i := range existing {
		byTag[existing[i].Tag] = &existing[i]
	}
	keepIDs := make([]uint, 0, len(parsed))
	for _, in := range parsed {
		settings, err := json.Marshal(in.Settings)
		if err != nil {
			return err
		}
		row := byTag[in.Tag]
		if row == nil {
			row = &model.Inbound{ServerID: serverID, Tag: in.Tag}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
			byTag[in.Tag] = row
		}
		if err := tx.Model(row).Updates(map[string]any{
			"type":        model.InboundType(in.Type),
			"listen_port": in.ListenPort,
			"enabled":     true,
			"settings":    model.JSONText(settings),
			"remark":      "同步自服务器配置",
		}).Error; err != nil {
			return err
		}
		keepIDs = append(keepIDs, row.ID)
	}
	return deleteRowsExcept(tx, serverID, keepIDs, &model.Inbound{})
}

func syncImportedOutbounds(tx *gorm.DB, serverID uint, parsed []singbox.ParsedOutbound) error {
	var existing []model.Outbound
	if err := tx.Where("server_id = ?", serverID).Find(&existing).Error; err != nil {
		return err
	}
	byTag := make(map[string]*model.Outbound, len(existing))
	for i := range existing {
		byTag[existing[i].Tag] = &existing[i]
	}
	keepIDs := make([]uint, 0, len(parsed))
	for i, ob := range parsed {
		blob, err := json.Marshal(map[string]any{
			"server": ob.Server, "server_port": ob.ServerPort,
			"username": ob.Username, "uuid": ob.UUID, "password": ob.Password,
			"settings": ob.Settings,
		})
		if err != nil {
			return err
		}
		row := byTag[ob.Tag]
		if row == nil {
			row = &model.Outbound{ServerID: serverID, Tag: ob.Tag}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
			byTag[ob.Tag] = row
		}
		if err := tx.Model(row).Updates(map[string]any{
			"type": ob.Type, "settings": model.JSONText(blob), "sort": i,
			"remark": "同步自服务器配置",
		}).Error; err != nil {
			return err
		}
		keepIDs = append(keepIDs, row.ID)
	}
	return deleteRowsExcept(tx, serverID, keepIDs, &model.Outbound{})
}

func replaceImportedRules(tx *gorm.DB, serverID uint, parsed []singbox.ParsedRule) error {
	if err := tx.Where("server_id = ?", serverID).Delete(&model.RouteRule{}).Error; err != nil {
		return err
	}
	for i, rule := range parsed {
		match, err := json.Marshal(rule.Match)
		if err != nil {
			return err
		}
		row := &model.RouteRule{
			ServerID: serverID, Sort: i, Match: model.JSONText(match),
			Outbound: rule.Outbound, Enabled: true, Remark: "同步自服务器配置",
		}
		if err := tx.Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}

func syncImportedRuleSets(tx *gorm.DB, serverID uint, parsed []singbox.RuleSetInput) error {
	var existing []model.RuleSet
	if err := tx.Where("server_id = ?", serverID).Find(&existing).Error; err != nil {
		return err
	}
	byTag := make(map[string]*model.RuleSet, len(existing))
	for i := range existing {
		byTag[existing[i].Tag] = &existing[i]
	}
	keepIDs := make([]uint, 0, len(parsed))
	for _, rs := range parsed {
		format := rs.Format
		if format != "source" {
			format = "binary"
		}
		typ := rs.Type
		if typ != "local" {
			typ = "remote"
		}
		row := byTag[rs.Tag]
		if row == nil {
			row = &model.RuleSet{ServerID: serverID, Tag: rs.Tag}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
			byTag[rs.Tag] = row
		}
		if err := tx.Model(row).Updates(map[string]any{
			"type": typ, "format": format, "url": rs.URL, "path": rs.Path,
			"download_detour": rs.DownloadDetour, "update_interval": rs.UpdateInterval,
		}).Error; err != nil {
			return err
		}
		keepIDs = append(keepIDs, row.ID)
	}
	return deleteRowsExcept(tx, serverID, keepIDs, &model.RuleSet{})
}
