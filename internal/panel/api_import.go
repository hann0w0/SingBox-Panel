package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/singpanel/singpanel/internal/model"
	"github.com/singpanel/singpanel/internal/protocol"
	"github.com/singpanel/singpanel/internal/singbox"
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

// applyImport writes the parsed model into the DB for this server, replacing
// whatever the panel had for it.
func (a *App) applyImport(srv *model.Server, p *singbox.ParsedConfig, raw []byte) error {
	// Import changes both the structured rows and the raw source-of-truth mode.
	// Serialize it with reconnect pushes, raw edits and explicit mode switches so
	// no concurrent operation can observe or apply a half-transitioned state.
	lock := a.orch.serverLock(srv.ID)
	lock.Lock()
	defer lock.Unlock()

	tx := a.db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	for _, m := range []any{&model.Inbound{}, &model.Outbound{}, &model.RouteRule{}, &model.RuleSet{}} {
		if err := tx.Where("server_id = ?", srv.ID).Delete(m).Error; err != nil {
			tx.Rollback()
			return err
		}
	}

	for _, in := range p.Inbounds {
		settings, err := json.Marshal(in.Settings)
		if err != nil {
			tx.Rollback()
			return err
		}
		row := &model.Inbound{
			ServerID: srv.ID, Tag: in.Tag, Type: model.InboundType(in.Type),
			ListenPort: in.ListenPort, Enabled: true, Settings: model.JSONText(settings),
			Remark: "导入自服务器配置",
		}
		if err := tx.Create(row).Error; err != nil {
			tx.Rollback()
			return err
		}
	}

	for i, ob := range p.Outbounds {
		blob, err := json.Marshal(map[string]any{
			"server": ob.Server, "server_port": ob.ServerPort,
			"username": ob.Username, "uuid": ob.UUID, "password": ob.Password,
			"settings": ob.Settings,
		})
		if err != nil {
			tx.Rollback()
			return err
		}
		row := &model.Outbound{
			ServerID: srv.ID, Tag: ob.Tag, Type: ob.Type,
			Settings: model.JSONText(blob), Sort: i, Remark: "导入自服务器配置",
		}
		if err := tx.Create(row).Error; err != nil {
			tx.Rollback()
			return err
		}
	}

	for i, r := range p.Rules {
		match, err := json.Marshal(r.Match)
		if err != nil {
			tx.Rollback()
			return err
		}
		row := &model.RouteRule{
			ServerID: srv.ID, Sort: i, Match: model.JSONText(match),
			Outbound: r.Outbound, Enabled: true, Remark: "导入自服务器配置",
		}
		if err := tx.Create(row).Error; err != nil {
			tx.Rollback()
			return err
		}
	}

	for _, rs := range p.RuleSets {
		format := rs.Format
		if format != "source" {
			format = "binary"
		}
		typ := rs.Type
		if typ != "local" {
			typ = "remote"
		}
		row := &model.RuleSet{
			ServerID: srv.ID, Tag: rs.Tag, Type: typ, URL: rs.URL, Path: rs.Path,
			Format: format, DownloadDetour: rs.DownloadDetour,
			UpdateInterval: rs.UpdateInterval,
		}
		if err := tx.Create(row).Error; err != nil {
			tx.Rollback()
			return err
		}
	}

	if err := tx.Model(&model.Server{}).Where("id = ?", srv.ID).Updates(map[string]any{
		"final_outbound": p.Final,
		"config_mode":    model.ConfigModeRaw,
		"raw_config":     model.JSONText(append([]byte(nil), raw...)),
	}).Error; err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}
