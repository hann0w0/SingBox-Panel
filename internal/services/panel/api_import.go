package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
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
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
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
		status := http.StatusInternalServerError
		if errors.Is(err, errInboundTrafficTagReserved) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
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
	unlock := a.orch.lockServer(srv.ID)
	defer unlock()
	return a.applyImportUnlocked(srv, p, raw)
}

// applyImportUnlocked performs the database transaction while the caller owns
// the server configuration lock.
func (a *App) applyImportUnlocked(srv *model.Server, p *singbox.ParsedConfig, raw []byte) error {
	a.userAccessMu.Lock()
	defer a.userAccessMu.Unlock()
	mode := importedConfigMode(p, raw)
	preserveManagedUsers := false
	var current model.Server
	if err := a.db.First(&current, srv.ID).Error; err != nil {
		return err
	}
	current.ConfigMode = model.ConfigModeManaged
	if generated, err := a.orch.BuildServerConfig(&current); err == nil && equivalentJSON(raw, generated) {
		mode = model.ConfigModeManaged
		preserveManagedUsers = true
	}
	return a.db.Transaction(func(tx *gorm.DB) error {
		if err := validateImportedPrivateRouting(tx, srv.ID, p, raw); err != nil {
			return err
		}
		if err := validateImportedInboundTrafficTags(tx, srv.ID, raw); err != nil {
			return err
		}
		inbounds := append([]singbox.ParsedInbound(nil), p.Inbounds...)
		if preserveManagedUsers {
			var previous []model.Inbound
			if err := tx.Where("server_id = ?", srv.ID).Find(&previous).Error; err != nil {
				return err
			}
			for index := range inbounds {
				for _, inbound := range previous {
					var settings singbox.InboundSettings
					if inbound.Tag == inbounds[index].Tag && string(inbound.Type) == inbounds[index].Type && json.Unmarshal(inbound.Settings, &settings) == nil && settings.UseMultiUser(string(inbound.Type)) {
						inbounds[index].Settings.MultiUser = true
						inbounds[index].Settings.SingleUser = false
					}
				}
			}
		}
		if err := syncImportedInbounds(tx, srv.ID, inbounds); err != nil {
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
	// Subscription parsers can understand more client types than the managed
	// server editor. Keep those whole-server imports editable as raw JSON.
	for _, outbound := range p.Outbounds {
		if !supportedOutboundTypes[outbound.Type] {
			return model.ConfigModeRaw
		}
	}
	// The real orchestrator adds panel statistics services even when they were
	// absent in the imported JSON. Do not promote a gate that occupies those
	// reserved ports: the raw configuration may work but managed pushes cannot.
	for _, in := range p.Inbounds {
		if !in.Settings.RealityGuardEnabled() {
			continue
		}
		for _, port := range []int{in.ListenPort, in.Settings.TLS.Reality.Guard.InternalPort} {
			if port == localTrafficPort() || port == addressPort(protocol.LocalStatsAPIAddress) {
				return model.ConfigModeRaw
			}
		}
	}
	generated, err := buildManagedConfigFromImport(p, raw)
	if err != nil {
		return model.ConfigModeRaw
	}
	// Configs written by an older panel carry the narrower loopback guard; treat
	// it as the current one so they stay managed and receive it on next push.
	raw = singbox.UpgradeLegacyLoopbackGuardJSON(raw)
	if equivalentJSON(raw, generated) {
		return model.ConfigModeManaged
	}
	// Only an independently recognised, unambiguous REALITY gate may use
	// these finite normalisations. All other imports retain strict equality.
	normalizedRaw, rawOK := singbox.NormalizeRealityGuardImportJSON(raw)
	normalizedGenerated, generatedOK := singbox.NormalizeRealityGuardImportJSON(generated)
	if rawOK && generatedOK && equivalentJSON(normalizedRaw, normalizedGenerated) {
		return model.ConfigModeManaged
	}
	return model.ConfigModeRaw
}

func buildManagedConfigFromImport(p *singbox.ParsedConfig, raw []byte) ([]byte, error) {
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
		outbounds = append(outbounds, singbox.OutboundInput(outbound))
	}
	rules := make([]singbox.RuleInput, 0, len(p.Rules))
	for _, parsedRule := range p.Rules {
		rule := parsedRule.Match
		rule.Outbound = parsedRule.Outbound
		rules = append(rules, rule)
	}
	statsController, statsSecretValue, statsAPI := importedStatsSettings(raw)
	return singbox.BuildServerConfig(singbox.ServerConfigInput{
		Inbounds: inbounds, Outbounds: outbounds, Rules: rules,
		RuleSets: p.RuleSets, Final: p.Final, StatsController: statsController,
		StatsSecret: statsSecretValue, StatsAPI: statsAPI,
	})
}

// rawConfigSecurityWarnings flags control endpoints without a secret. A
// proxy user can reach the node's loopback interface through the proxy, so an
// unauthenticated Clash API or API service exposes every user's connections
// and lets anyone close them.
func rawConfigSecurityWarnings(raw []byte) []string {
	var root struct {
		Experimental struct {
			ClashAPI struct {
				ExternalController string `json:"external_controller"`
				Secret             string `json:"secret"`
			} `json:"clash_api"`
		} `json:"experimental"`
		Services []struct {
			Type       string `json:"type"`
			Tag        string `json:"tag"`
			Listen     string `json:"listen"`
			ListenPort int    `json:"listen_port"`
			Secret     string `json:"secret"`
		} `json:"services"`
	}
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	warnings := []string{}
	clash := root.Experimental.ClashAPI
	if clash.ExternalController != "" && clash.Secret == "" {
		warnings = append(warnings, fmt.Sprintf("Clash API（%s）未设置 secret：代理用户可经节点访问它，查看他人连接或断开所有连接", clash.ExternalController))
	}
	for _, service := range root.Services {
		if service.Type == "api" && service.Secret == "" {
			warnings = append(warnings, fmt.Sprintf("API 服务 %s（%s）未设置 secret：代理用户可经节点访问它并控制 sing-box",
				service.Tag, net.JoinHostPort(service.Listen, strconv.Itoa(service.ListenPort))))
		}
	}
	return warnings
}

// importedStatsSettings recovers the panel's own stats hooks from an imported
// config so a panel-generated config compares losslessly and stays managed.
// Only the exact shapes the panel emits are recognised.
func importedStatsSettings(raw []byte) (controller, secret, api string) {
	var root struct {
		Experimental struct {
			ClashAPI struct {
				ExternalController string `json:"external_controller"`
				Secret             string `json:"secret"`
			} `json:"clash_api"`
		} `json:"experimental"`
		Services []struct {
			Type       string `json:"type"`
			Tag        string `json:"tag"`
			Listen     string `json:"listen"`
			ListenPort int    `json:"listen_port"`
			Secret     string `json:"secret"`
		} `json:"services"`
	}
	if json.Unmarshal(raw, &root) != nil || root.Experimental.ClashAPI.ExternalController != protocol.LocalTrafficAddress {
		return "", "", ""
	}
	controller = protocol.LocalTrafficAddress
	secret = root.Experimental.ClashAPI.Secret
	if secret != "" && len(root.Services) == 1 {
		service := root.Services[0]
		address := net.JoinHostPort(service.Listen, strconv.Itoa(service.ListenPort))
		if service.Type == "api" && service.Tag == singbox.StatsServiceTag &&
			service.Secret == secret && address == protocol.LocalStatsAPIAddress {
			api = protocol.LocalStatsAPIAddress
		}
	}
	return controller, secret, api
}

func equivalentJSON(left, right []byte) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, err := json.Marshal(leftValue)
	if err != nil {
		return false
	}
	rightCanonical, err := json.Marshal(rightValue)
	return err == nil && string(leftCanonical) == string(rightCanonical)
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
		if err := rememberInboundTrafficTag(tx, &existing[i]); err != nil {
			return err
		}
		byTag[existing[i].Tag] = &existing[i]
	}
	keepIDs := make([]uint, 0, len(parsed))
	for _, in := range parsed {
		row := byTag[in.Tag]
		settings, err := json.Marshal(in.Settings)
		if err != nil {
			return err
		}
		if row == nil {
			row = &model.Inbound{ServerID: serverID, Tag: in.Tag}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
			byTag[in.Tag] = row
		}
		if err := rememberInboundTrafficTag(tx, row); err != nil {
			return err
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
	keep := make(map[uint]bool, len(keepIDs))
	for _, id := range keepIDs {
		keep[id] = true
	}
	removedIDs := make([]uint, 0, len(existing))
	for _, row := range existing {
		if !keep[row.ID] {
			removedIDs = append(removedIDs, row.ID)
		}
	}
	if len(removedIDs) > 0 {
		var forwards []model.UserForward
		if err := tx.Where("server_id = ? AND inbound_id IN ?", serverID, removedIDs).Find(&forwards).Error; err != nil {
			return err
		}
		if err := deleteForwardState(tx, forwards); err != nil {
			return err
		}
	}
	if err := deleteUserNodeOrderRefs(tx, userNodeTypeManaged, removedIDs); err != nil {
		return err
	}
	if err := deleteRowsExcept(tx, serverID, keepIDs, &model.Inbound{}); err != nil {
		return err
	}
	for _, inboundID := range removedIDs {
		if err := removeInboundUserAssignments(tx, inboundID); err != nil {
			return err
		}
	}
	return nil
}

func syncImportedOutbounds(tx *gorm.DB, serverID uint, parsed []singbox.ParsedOutbound) error {
	privateByTag, err := validateImportedPrivateOutbounds(tx, parsed)
	if err != nil {
		return err
	}
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
		if _, exists := privateByTag[ob.Tag]; exists {
			continue
		}
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

func validateImportedPrivateOutbounds(db *gorm.DB, parsed []singbox.ParsedOutbound) (map[string]model.Outbound, error) {
	var private []model.Outbound
	if err := db.Where("owner_user_id <> 0").Find(&private).Error; err != nil {
		return nil, err
	}
	privateByTag := make(map[string]model.Outbound, len(private))
	for _, row := range private {
		privateByTag[row.Tag] = row
	}
	for _, ob := range parsed {
		if private, exists := privateByTag[ob.Tag]; exists {
			var settings outboundStoredSettings
			if err := json.Unmarshal(private.Settings, &settings); err != nil {
				return nil, err
			}
			expected, err := singbox.BuildClientOutbound(singbox.ClientNode{Name: ob.Tag, Type: private.Type, Server: settings.Server, ServerPort: settings.ServerPort, User: singbox.ProxyUser{UUID: settings.UUID, Username: settings.Username, Password: settings.Password}, Settings: settings.Settings})
			if err != nil {
				return nil, err
			}
			actual, err := singbox.BuildClientOutbound(singbox.ClientNode{Name: ob.Tag, Type: ob.Type, Server: ob.Server, ServerPort: ob.ServerPort, User: singbox.ProxyUser{UUID: ob.UUID, Username: ob.Username, Password: ob.Password}, Settings: ob.Settings})
			if err != nil {
				return nil, err
			}
			if !equivalentJSON(expected, actual) {
				return nil, fmt.Errorf("导入出站 %q 与专属出站标签冲突，请先重命名", ob.Tag)
			}
		}
	}
	return privateByTag, nil
}

func validatePrivateRouteScope(db *gorm.DB, serverID uint, outbound model.Outbound, match singbox.RuleInput) error {
	if outbound.OwnerUserID == 0 {
		return nil
	}
	if outbound.ServerID != 0 && outbound.ServerID != serverID {
		return fmt.Errorf("专属出站 %q 绑定在其它入口服务器", outbound.Tag)
	}
	if len(match.AuthUser) != 1 {
		return fmt.Errorf("专属出站 %q 必须限定为所属用户或对应转发的唯一身份", outbound.Tag)
	}
	var owner model.User
	if err := db.Select("id", "email", "proxy_token", "sub_token").First(&owner, outbound.OwnerUserID).Error; err != nil {
		return fmt.Errorf("读取专属出站 %q 所属用户失败: %w", outbound.Tag, err)
	}
	if match.AuthUser[0] == proxyTrafficName(&owner) {
		return nil
	}
	var forwards []model.UserForward
	if err := db.Where("user_id = ? AND outbound_id = ? AND server_id = ?", owner.ID, outbound.ID, serverID).Find(&forwards).Error; err != nil {
		return err
	}
	for _, forward := range forwards {
		if match.AuthUser[0] != forwardTrafficName(&owner, forward.ID) || len(match.Inbound) != 1 {
			continue
		}
		var inbound model.Inbound
		if err := db.Where("id = ? AND server_id = ?", forward.InboundID, serverID).First(&inbound).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		if match.Inbound[0] == inbound.Tag {
			return nil
		}
	}
	return fmt.Errorf("专属出站 %q 只能由所属用户或对应转发的身份规则使用", outbound.Tag)
}

func validateImportedPrivateRouting(db *gorm.DB, serverID uint, p *singbox.ParsedConfig, raw []byte) error {
	var private []model.Outbound
	if err := db.Where("owner_user_id <> 0").Find(&private).Error; err != nil {
		return err
	}
	privateByTag := make(map[string]model.Outbound, len(private))
	for _, outbound := range private {
		privateByTag[outbound.Tag] = outbound
	}
	if _, exists := privateByTag[p.Final]; exists {
		return fmt.Errorf("专属出站 %q 不能作为默认出站", p.Final)
	}
	for _, rule := range p.Rules {
		if outbound, exists := privateByTag[rule.Outbound]; exists {
			if err := validatePrivateRouteScope(db, serverID, outbound, rule.Match); err != nil {
				return err
			}
		}
	}
	for _, ruleSet := range p.RuleSets {
		if _, exists := privateByTag[ruleSet.DownloadDetour]; exists {
			return fmt.Errorf("专属出站 %q 不能用于规则集下载", ruleSet.DownloadDetour)
		}
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	var inspect func(any, bool) error
	inspect = func(value any, inverted bool) error {
		switch item := value.(type) {
		case []any:
			for _, child := range item {
				if err := inspect(child, inverted); err != nil {
					return err
				}
			}
		case map[string]any:
			if invert, exists := item["invert"]; exists && invert != false {
				inverted = true
			}
			for _, key := range []string{"final", "default", "default_outbound", "detour", "download_detour"} {
				if tag, _ := item[key].(string); privateByTag[tag].OwnerUserID != 0 {
					return fmt.Errorf("专属出站 %q 不能用于 %s", tag, key)
				}
			}
			if members, ok := item["outbounds"].([]any); ok {
				for _, member := range members {
					if tag, _ := member.(string); privateByTag[tag].OwnerUserID != 0 {
						return fmt.Errorf("专属出站 %q 不能作为出站组成员", tag)
					}
				}
			}
			if tag, _ := item["outbound"].(string); privateByTag[tag].OwnerUserID != 0 {
				if ruleType, exists := item["type"]; exists && ruleType != "default" {
					return fmt.Errorf("专属出站 %q 只能由 default 类型的身份规则使用", tag)
				}
				if inverted {
					return fmt.Errorf("专属出站 %q 的身份范围不能取反", tag)
				}
				var match singbox.RuleInput
				for key, field := range map[string]*[]string{"auth_user": &match.AuthUser, "inbound": &match.Inbound} {
					if text, ok := item[key].(string); ok {
						*field = []string{text}
					} else if values, exists := item[key]; exists {
						encoded, err := json.Marshal(values)
						if err != nil {
							return err
						}
						if err := json.Unmarshal(encoded, field); err != nil {
							return err
						}
					}
				}
				if err := validatePrivateRouteScope(db, serverID, privateByTag[tag], match); err != nil {
					return err
				}
			}
			for _, child := range item {
				if err := inspect(child, inverted); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return inspect(root, false)
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
	var assigned []model.ServerRuleSet
	if err := tx.Where("server_id = ?", serverID).Find(&assigned).Error; err != nil {
		return err
	}
	assignedByID := make(map[uint]model.ServerRuleSet, len(assigned))
	for _, assignment := range assigned {
		assignedByID[assignment.RuleSetID] = assignment
	}
	var existing []model.RuleSet
	if err := tx.Find(&existing).Error; err != nil {
		return err
	}
	byTag := make(map[string]model.RuleSet, len(existing))
	for _, row := range existing {
		byTag[row.Tag] = row
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
		row, ok := byTag[rs.Tag]
		if !ok {
			row = model.RuleSet{Tag: rs.Tag}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			byTag[rs.Tag] = row
		}
		if err := tx.Model(&model.RuleSet{}).Where("id = ?", row.ID).Updates(map[string]any{
			"type": typ, "format": format, "url": rs.URL, "path": rs.Path,
			"update_interval": rs.UpdateInterval,
		}).Error; err != nil {
			return err
		}
		keepIDs = append(keepIDs, row.ID)
		assignment := assignedByID[row.ID]
		if assignment.ID == 0 {
			assignment = model.ServerRuleSet{ServerID: serverID, RuleSetID: row.ID}
		}
		assignment.DownloadDetour = rs.DownloadDetour
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "server_id"}, {Name: "rule_set_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"download_detour", "updated_at"}),
		}).Create(&assignment).Error; err != nil {
			return err
		}
	}
	if len(keepIDs) == 0 {
		return tx.Where("server_id = ?", serverID).Delete(&model.ServerRuleSet{}).Error
	}
	return tx.Where("server_id = ? AND rule_set_id NOT IN ?", serverID, keepIDs).Delete(&model.ServerRuleSet{}).Error
}
