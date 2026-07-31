package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/protocol"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

// ---------- overview ----------

// nodeBrief is the per-node row shown on the overview.
type nodeBrief struct {
	ID        uint    `json:"id"`
	Name      string  `json:"name"`
	Region    string  `json:"region"`
	Online    bool    `json:"online"`
	Version   string  `json:"singbox_version"`
	Installed bool    `json:"singbox_installed"`
	Load1     float64 `json:"load1"`
	MemUsed   uint64  `json:"mem_used"`
	MemTotal  uint64  `json:"mem_total"`
	Inbounds  int     `json:"inbounds"`
	Outbounds int     `json:"outbounds"`
	Rules     int     `json:"rules"`
}

func (a *App) adminOverview(c *gin.Context) {
	var users, inbounds, outbounds, rules int64
	a.db.Model(&model.User{}).Where("role = ?", model.RoleUser).Count(&users)
	a.db.Model(&model.Inbound{}).Count(&inbounds)
	a.db.Model(&model.Outbound{}).Count(&outbounds)
	a.db.Model(&model.RouteRule{}).Count(&rules)

	var usersEnabled, usersExpiring int64
	a.db.Model(&model.User{}).Where("role = ? AND enabled = ?", model.RoleUser, true).Count(&usersEnabled)
	weekAhead := time.Now().Add(7 * 24 * time.Hour)
	a.db.Model(&model.User{}).
		Where("role = ? AND enabled = ? AND expire_at IS NOT NULL AND expire_at <= ?",
			model.RoleUser, true, weekAhead).
		Count(&usersExpiring)

	// Per-node rows, with each node's own object counts.
	var servers []model.Server
	a.db.Order("id").Find(&servers)
	applyServerOrder(a.db, servers)
	nodes := make([]nodeBrief, 0, len(servers))
	for i := range servers {
		srv := &servers[i]
		var ib, ob, rl int64
		a.db.Model(&model.Inbound{}).Where("server_id = ?", srv.ID).Count(&ib)
		a.db.Model(&model.Outbound{}).Where("server_id = ?", srv.ID).Count(&ob)
		a.db.Model(&model.RouteRule{}).Where("server_id = ?", srv.ID).Count(&rl)
		nodes = append(nodes, nodeBrief{
			ID: srv.ID, Name: srv.Name, Region: srv.Region,
			Online:  a.hub.IsOnline(srv.ID),
			Version: srv.SingboxVersion, Installed: srv.SingboxInstalled,
			Load1: srv.Load1, MemUsed: srv.MemUsed, MemTotal: srv.MemTotal,
			Inbounds: int(ib), Outbounds: int(ob), Rules: int(rl),
		})
	}

	memUsed, memTotal, memPct := hostMem()

	c.JSON(http.StatusOK, gin.H{
		"servers_total":   len(servers),
		"servers_online":  len(a.hub.OnlineServerIDs()),
		"users_total":     users,
		"users_enabled":   usersEnabled,
		"users_expiring":  usersExpiring,
		"inbounds_total":  inbounds,
		"outbounds_total": outbounds,
		"rules_total":     rules,
		"cpu_percent":     a.host.CPUPercent(),
		"mem_used":        memUsed,
		"mem_total":       memTotal,
		"mem_percent":     memPct,
		"uptime_seconds":  int64(time.Since(a.startedAt).Seconds()),
		"nodes":           nodes,
	})
}

// serverLogs returns the tail of a node's sing-box service log.
func (a *App) serverLogs(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	lines, _ := strconv.Atoi(c.DefaultQuery("lines", "200"))
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdGetLogs, protocol.GetLogsCmd{Lines: lines})
	if err != nil {
		if strings.Contains(err.Error(), "unknown command") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "该节点的 Agent 版本过旧，请先点「升级 Agent」"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var data protocol.LogsData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解析日志失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"text": data.Text})
}

// ---------- servers ----------

type serverReq struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Region  string `json:"region"`
	Remark  string `json:"remark"`
}

const LatestAgentVersion = "v1.0.0"

func (a *App) listServers(c *gin.Context) {
	var servers []model.Server
	// Inbounds are preloaded so the admin UI can offer per-protocol access.
	a.db.Preload("Inbounds").Order("id").Find(&servers)
	applyServerOrder(a.db, servers)
	releases := getLatestSingboxReleases()
	for i := range servers {
		servers[i].Online = a.hub.IsOnline(servers[i].ID)
		if servers[i].SingboxInstalled && servers[i].SingboxVersion != "" {
			hasUp, latest := checkSingboxUpdate(servers[i].SingboxVersion, releases)
			servers[i].SingboxHasUpdate = hasUp
			servers[i].SingboxLatestVersion = latest
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"servers":              servers,
		"latest_agent_version": LatestAgentVersion,
	})
}

func (a *App) createServer(c *gin.Context) {
	var req serverReq
	if !bindJSON(c, &req) {
		return
	}
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
		return
	}
	address, err := normalizeNodeAddress(req.Address)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "节点连接地址格式无效: " + err.Error()})
		return
	}
	srv := &model.Server{
		Name:       req.Name,
		Address:    address,
		Region:     req.Region,
		Remark:     req.Remark,
		AgentToken: randHex(24),
		ConfigMode: model.ConfigModeManaged,
	}
	if err := a.db.Create(srv).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"server":          srv,
		"install_command": installCommand(a.baseURL(), srv.AgentToken),
		"public_url":      a.baseURL(),
	})
}

func (a *App) getServer(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var srv model.Server
	if err := a.db.Preload("Inbounds").First(&srv, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	srv.Online = a.hub.IsOnline(srv.ID)
	if srv.SingboxInstalled && srv.SingboxVersion != "" {
		releases := getLatestSingboxReleases()
		hasUp, latest := checkSingboxUpdate(srv.SingboxVersion, releases)
		srv.SingboxHasUpdate = hasUp
		srv.SingboxLatestVersion = latest
	}
	c.JSON(http.StatusOK, gin.H{
		"server":          srv,
		"install_command": installCommand(a.baseURL(), srv.AgentToken),
		"public_url":      a.baseURL(),
	})
}

func (a *App) updateServer(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req serverReq
	if !bindJSON(c, &req) {
		return
	}
	address, err := normalizeNodeAddress(req.Address)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "节点连接地址格式无效: " + err.Error()})
		return
	}
	updates := map[string]any{
		"name":    req.Name,
		"address": address,
		"region":  req.Region,
		"remark":  req.Remark,
	}
	if err := a.db.Model(&model.Server{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Name, address, region and remark are panel metadata. None belongs in the
	// sing-box config, so editing a node must not restart its service.
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (a *App) deleteServer(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}

	var srv model.Server
	if err := a.db.First(&srv, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	// Panel-only deletion: never send an Agent command here. In particular,
	// this endpoint must not touch the VPS sing-box config, binary or service.
	tx := a.db.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": tx.Error.Error()})
		return
	}
	var inboundIDs []uint
	if err := tx.Model(&model.Inbound{}).Where("server_id = ?", id).Pluck("id", &inboundIDs).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, row := range []any{&model.Inbound{}, &model.Outbound{}, &model.RouteRule{}, &model.RuleSet{}} {
		if err := tx.Where("server_id = ?", id).Delete(row).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	var users []model.User
	if err := tx.Find(&users).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	inboundSet := map[uint]bool{}
	for _, inboundID := range inboundIDs {
		inboundSet[inboundID] = true
	}
	for i := range users {
		servers := users[i].ServerIDs[:0]
		for _, serverID := range users[i].ServerIDs {
			if serverID != id {
				servers = append(servers, serverID)
			}
		}
		inbounds := users[i].InboundIDs[:0]
		for _, inboundID := range users[i].InboundIDs {
			if !inboundSet[inboundID] {
				inbounds = append(inbounds, inboundID)
			}
		}
		users[i].ServerIDs = servers
		users[i].InboundIDs = inbounds
		if err := tx.Select("ServerIDs", "InboundIDs").Save(&users[i]).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Delete(&model.Server{}, id).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if a.hub != nil {
		a.hub.Disconnect(id)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// installSingbox triggers an official sing-box install on the agent.
func (a *App) installSingbox(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req protocol.InstallSingboxCmd
	if !bindJSON(c, &req) {
		return
	}
	if req.Channel == "" {
		req.Channel = protocol.ChannelStable
	}
	if req.Method == "" {
		req.Method = protocol.MethodScript
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Minute)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdInstallSingbox, req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "output": res.Output})
		return
	}
	// Once installed, push the panel's config ONLY if this server has protocols
	// configured in the panel — never overwrite an existing config with an empty one.
	a.orch.PushConfigIfManaged(id)
	c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
}

// uninstallAgent removes the Agent binary, configuration and systemd unit from
// the controlled VPS while keeping the panel's node record and install command.
func (a *App) uninstallAgent(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Minute)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdUninstallAgent, nil)
	if err != nil {
		status := http.StatusBadGateway
		message := err.Error()
		switch {
		case errors.Is(err, ErrAgentOffline):
			status = http.StatusConflict
			message = "节点离线，无法卸载被控端 Agent"
		case strings.Contains(err.Error(), "unknown command"):
			status = http.StatusBadRequest
			message = "该节点的 Agent 版本过旧，请先点击「安装 / 升级 Agent」完成自动升级后再卸载"
		}
		c.JSON(status, gin.H{"error": message, "output": res.Output})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
}

type serviceReq struct {
	Action string `json:"action"`
}

func (a *App) serviceAction(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req serviceReq
	if !bindJSON(c, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdServiceAction, protocol.ServiceActionCmd{Action: req.Action})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "output": res.Output})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
}

// updateAgent tells the agent to replace itself with the build this panel
// currently serves and restart. The agent acks before restarting, so a
// dropped connection right after is expected and not an error.
func (a *App) updateAgent(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdUpdateAgent, nil)
	if err != nil {
		// An agent predating self-update cannot upgrade itself — tell the admin
		// to run the install command once; from then on this button works.
		if strings.Contains(err.Error(), "unknown command") {
			var srv model.Server
			a.db.First(&srv, id)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "该节点的 Agent 版本过旧，不支持一键升级。请在这台服务器上重跑一次安装命令（升级后本按钮即可使用）：\n" +
					installCommand(a.baseURL(), srv.AgentToken),
			})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "output": res.Output})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
}

func (a *App) updateAllAgents(c *gin.Context) {
	var servers []model.Server
	if err := a.db.Where("online = ?", true).Find(&servers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(servers) == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": true, "message": "当前没有在线的服务器", "count": 0})
		return
	}

	count := 0
	for i := range servers {
		srv := &servers[i]
		go func(serverID uint) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			_, _ = a.hub.SendCommand(ctx, serverID, protocol.CmdUpdateAgent, nil)
		}(srv.ID)
		count++
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"message": fmt.Sprintf("已成功向 %d 台在线服务器下发 Agent 更新指令", count),
		"count":   count,
	})
}

func (a *App) serverStatus(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdGetStatus, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var status protocol.StatusData
	if err := json.Unmarshal(res.Data, &status); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解析节点状态失败"})
		return
	}
	persistServerStatus(a.db, id, status)
	c.JSON(http.StatusOK, gin.H{"status": status})
}

// persistServerStatus stores a status snapshot explicitly requested by the
// admin UI. Older Agents do not include host metrics in get_status, so only
// replace those fields when the extended snapshot is actually present.
func persistServerStatus(db *gorm.DB, serverID uint, status protocol.StatusData) {
	now := time.Now()
	updates := map[string]any{
		"online":            true,
		"last_seen":         &now,
		"singbox_installed": status.SingboxInstalled,
		"singbox_version":   status.SingboxVersion,
		"singbox_active":    status.ServiceActive,
		"uptime":            status.Uptime,
	}
	if status.AgentVersion != "" {
		updates["agent_version"] = status.AgentVersion
	}
	if status.Hostname != "" {
		updates["hostname"] = status.Hostname
		updates["os"] = status.OS
		updates["arch"] = status.Arch
		updates["kernel"] = status.Kernel
		updates["load1"] = status.Load1
		updates["mem_used"] = status.MemUsed
		updates["mem_total"] = status.MemTotal
	}
	if ip, ok := normalizedIPv4(status.PublicIP); ok {
		updates["public_ip"] = ip
	}
	db.Model(&model.Server{}).Where("id = ?", serverID).Updates(updates)
}

// remoteConfig reads the sing-box config currently on the host (for viewing an
// existing/hand-made installation).
func (a *App) remoteConfig(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdGetConfig, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", res.Data)
}

type rawConfigReq struct {
	Config string `json:"config"`
}

// applyRawConfig pushes an admin-edited raw config.json to the server. The agent
// validates it with `sing-box check` before applying and rolls back on failure,
// so an invalid edit never takes down the running service.
func (a *App) applyRawConfig(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req rawConfigReq
	if !bindJSON(c, &req) {
		return
	}
	raw := []byte(req.Config)
	if !json.Valid(raw) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "配置不是合法 JSON"})
		return
	}
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	// Keep applying the file and refreshing its structured panel view under one
	// server lock. A concurrent edit can therefore never apply config B and then
	// have the slower request overwrite the database view with config A.
	lock := a.orch.serverLock(id)
	lock.Lock()
	defer lock.Unlock()
	res, err := a.orch.applyRawConfigUnlocked(ctx, id, raw)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "output": res.Output})
		return
	}
	if err := a.applyImportUnlocked(&model.Server{ID: id}, parsed, raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  "配置已下发，但同步面板视图失败: " + err.Error(),
			"output": res.Output,
		})
		return
	}
	var synced model.Server
	if err := a.db.Select("config_mode").First(&synced, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取同步结果失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "output": res.Output, "summary": buildImportSummary(parsed),
		"config_mode": synced.ConfigMode,
	})
}

type configModeReq struct {
	Mode string `json:"mode"`
}

// setConfigMode explicitly converts a raw/imported node back to structured
// panel management. The raw snapshot is retained for recovery, but reconnects
// will use the generated model only after this operation succeeds.
func (a *App) setConfigMode(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req configModeReq
	if !bindJSON(c, &req) {
		return
	}
	if req.Mode != model.ConfigModeManaged {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只能显式切换到 managed 模式；raw 模式由导入或原始配置编辑自动启用"})
		return
	}
	var srv model.Server
	if err := a.db.Select("id").First(&srv, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	if err := a.orch.SwitchToManagedConfig(ctx, id); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "切换失败，已保留原始配置模式: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "config_mode": model.ConfigModeManaged})
}

// ---------- inbounds ----------

type inboundReq struct {
	Type       string          `json:"type"`
	Tag        string          `json:"tag"`
	ListenPort int             `json:"listen_port"`
	Settings   json.RawMessage `json:"settings"`
	Remark     string          `json:"remark"`
	Enabled    *bool           `json:"enabled"`
}

func (a *App) listInbounds(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var inbounds []model.Inbound
	a.db.Where("server_id = ?", id).Order("id").Find(&inbounds)
	c.JSON(http.StatusOK, gin.H{"inbounds": inbounds})
}

func inboundTag(typ, tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return typ + "-" + randHex(3)
	}
	return tag
}

func (a *App) createInbound(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if !a.requireManagedMode(c, id) {
		return
	}
	var req inboundReq
	if !bindJSON(c, &req) {
		return
	}
	if !supportedInboundTypes[req.Type] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported inbound type: " + req.Type})
		return
	}
	if req.ListenPort <= 0 || req.ListenPort > 65535 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid listen_port"})
		return
	}

	var st singbox.InboundSettings
	if len(req.Settings) > 0 {
		if err := json.Unmarshal(req.Settings, &st); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid settings: " + err.Error()})
			return
		}
	}
	if err := fillInboundSecrets(req.Type, &st); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Reject bad settings here: an unusable inbound row would otherwise make
	// EVERY later config push for this server fail, silently.
	if err := st.Validate(req.Type); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	raw, _ := json.Marshal(st)

	tag := inboundTag(req.Type, req.Tag)
	if err := a.validateInboundIdentity(a.db, id, 0, tag, req.ListenPort); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ib := &model.Inbound{
		ServerID:   id,
		Type:       model.InboundType(req.Type),
		Tag:        tag,
		ListenPort: req.ListenPort,
		Settings:   model.JSONText(raw),
		Remark:     req.Remark,
		Enabled:    enabled,
	}
	if err := a.db.Create(ib).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, id, gin.H{"inbound": ib})
}

func (a *App) updateInbound(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if !a.requireManagedMode(c, sid) {
		return
	}
	iid, ok := uintParam(c, "inboundID")
	if !ok {
		return
	}
	var ib model.Inbound
	if err := a.db.Where("id = ? AND server_id = ?", iid, sid).First(&ib).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "inbound not found"})
		return
	}
	var req inboundReq
	if !bindJSON(c, &req) {
		return
	}
	nextTag := ib.Tag
	if req.Tag != "" {
		nextTag = strings.TrimSpace(req.Tag)
	}
	nextPort := ib.ListenPort
	if req.ListenPort != 0 {
		nextPort = req.ListenPort
	}
	if err := a.validateInboundIdentity(a.db, sid, ib.ID, nextTag, nextPort); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	oldTag := ib.Tag
	ib.Tag = nextTag
	ib.ListenPort = nextPort
	ib.Remark = req.Remark
	if req.Enabled != nil {
		ib.Enabled = *req.Enabled
	}
	if len(req.Settings) > 0 {
		var oldSettings singbox.InboundSettings
		if len(ib.Settings) > 0 {
			_ = json.Unmarshal(ib.Settings, &oldSettings)
		}
		var st singbox.InboundSettings
		if err := json.Unmarshal(req.Settings, &st); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid settings: " + err.Error()})
			return
		}
		preserveInboundSecrets(&oldSettings, &st)
		if string(ib.Type) == "socks" && st.UseMultiUser(string(ib.Type)) && !oldSettings.UseMultiUser(string(ib.Type)) {
			// The previous shared SOCKS login must not become the hidden fallback
			// credential after switching modes, or revoked clients could reconnect
			// whenever the active user list becomes empty.
			st.Username = ""
			st.Password = ""
		}
		// Preserve previously-generated secrets when the new payload omits them.
		if err := fillInboundSecrets(string(ib.Type), &st); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if err := st.Validate(string(ib.Type)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		raw, _ := json.Marshal(st)
		ib.Settings = model.JSONText(raw)
	}
	tx := a.db.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": tx.Error.Error()})
		return
	}
	if oldTag != ib.Tag {
		if err := renameInboundRuleRefs(tx, sid, oldTag, ib.Tag); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Save(&ib).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, sid, gin.H{"inbound": ib})
}

func (a *App) deleteInbound(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	iid, ok := uintParam(c, "inboundID")
	if !ok {
		return
	}
	if !a.requireManagedMode(c, sid) {
		return
	}
	var ib model.Inbound
	if err := a.db.Where("id = ? AND server_id = ?", iid, sid).First(&ib).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "inbound not found"})
		return
	}
	referenced, err := a.inboundReferenced(a.db, sid, ib.Tag)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if referenced {
		c.JSON(http.StatusConflict, gin.H{"error": "该入站仍被分流规则引用，请先修改或删除相关规则"})
		return
	}
	if err := a.db.Delete(&ib).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, sid, gin.H{"ok": true})
}
