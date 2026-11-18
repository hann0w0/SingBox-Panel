package panel

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
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

type perServerCount struct {
	ServerID uint  `gorm:"column:server_id"`
	Count    int64 `gorm:"column:count"`
}

func groupedServerCounts(db *gorm.DB, modelValue any) (map[uint]int64, error) {
	var rows []perServerCount
	if err := db.Model(modelValue).
		Select("server_id, COUNT(*) AS count").
		Group("server_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[uint]int64, len(rows))
	for _, row := range rows {
		counts[row.ServerID] = row.Count
	}
	return counts, nil
}

func (a *App) adminOverview(c *gin.Context) {
	var users, inbounds, outbounds, rules int64
	if err := a.db.Model(&model.User{}).Where("role = ?", model.RoleUser).Count(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := a.db.Model(&model.Inbound{}).Count(&inbounds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := a.db.Model(&model.Outbound{}).Count(&outbounds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := a.db.Model(&model.RouteRule{}).Count(&rules).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var usersEnabled, usersExpiring int64
	if err := a.db.Model(&model.User{}).Where("role = ? AND enabled = ?", model.RoleUser, true).Count(&usersEnabled).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	weekAhead := time.Now().Add(7 * 24 * time.Hour)
	if err := a.db.Model(&model.User{}).
		Where("role = ? AND enabled = ? AND expire_at IS NOT NULL AND expire_at <= ?",
			model.RoleUser, true, weekAhead).
		Count(&usersExpiring).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Per-node rows, with each node's own object counts.
	var servers []model.Server
	if err := a.db.Order("id").Find(&servers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	applyServerOrder(a.db, servers)
	inboundCounts, err := groupedServerCounts(a.db, &model.Inbound{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	outboundCounts, err := groupedServerCounts(a.db, &model.Outbound{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ruleCounts, err := groupedServerCounts(a.db, &model.RouteRule{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	nodes := make([]nodeBrief, 0, len(servers))
	for i := range servers {
		srv := &servers[i]
		nodes = append(nodes, nodeBrief{
			ID: srv.ID, Name: srv.Name, Region: srv.Region,
			Online:  a.hub.IsOnline(srv.ID),
			Version: srv.SingboxVersion, Installed: srv.SingboxInstalled,
			Load1: srv.Load1, MemUsed: srv.MemUsed, MemTotal: srv.MemTotal,
			Inbounds: int(inboundCounts[srv.ID]), Outbounds: int(outboundCounts[srv.ID]), Rules: int(ruleCounts[srv.ID]),
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
	users := logUserResolver{db: a.db, serverID: id}
	c.JSON(http.StatusOK, gin.H{"text": users.resolve(ctx, data.Text)})
}

// ---------- servers ----------

type serverReq struct {
	Name            string `json:"name"`
	Address         string `json:"address"`
	Region          string `json:"region"`
	Remark          string `json:"remark"`
	TrafficResetDay *int   `json:"traffic_reset_day"`
}

type serverUpdateReq struct {
	Name            *string `json:"name"`
	Address         *string `json:"address"`
	Region          *string `json:"region"`
	Remark          *string `json:"remark"`
	TrafficResetDay *int    `json:"traffic_reset_day"`
}

// latestAgentVersion is the agent build the panel currently serves. It reads
// the VERSION file placed beside the served agent binaries, so upgrading the
// panel alone no longer wrongly flags every node as having an agent update.
func (a *App) latestAgentVersion() string {
	if a.cfg.AgentsDir != "" {
		verPath := filepath.Join(a.cfg.AgentsDir, "VERSION")
		if data, err := os.ReadFile(verPath); err == nil {
			if v := strings.TrimSpace(string(data)); v != "" {
				return v
			}
		}
	}
	// A missing VERSION file means the panel cannot safely identify the Agent
	// binary it would serve. Never report an old hard-coded version here: that
	// makes a panel upgrade look like an Agent downgrade. The install/upgrade
	// endpoint will still fail clearly if the actual binary is missing.
	return "unknown"
}

func (a *App) listServers(c *gin.Context) {
	var servers []model.Server
	// Inbounds are preloaded so the admin UI can offer per-protocol access.
	if err := a.db.Preload("Inbounds").Order("id").Find(&servers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	applyServerOrder(a.db, servers)
	releases := getLatestSingboxReleases()
	latestAgentVersion := a.latestAgentVersion()
	for i := range servers {
		setServerAddressAvailability(&servers[i])
		servers[i].Online = a.hub.IsOnline(servers[i].ID)
		if !servers[i].Online {
			servers[i].TrafficUploadRate, servers[i].TrafficDownloadRate = 0, 0
		}
		if agentHasUpdate(servers[i].AgentVersion, latestAgentVersion) {
			servers[i].AgentHasUpdate = true
			servers[i].AgentLatestVersion = latestAgentVersion
		}
		if servers[i].SingboxInstalled && servers[i].SingboxVersion != "" {
			hasUp, latest := checkSingboxUpdate(servers[i].SingboxVersion, releases)
			servers[i].SingboxHasUpdate = hasUp
			servers[i].SingboxLatestVersion = latest
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"servers":              servers,
		"latest_agent_version": latestAgentVersion,
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
	resetDay := 1
	if req.TrafficResetDay != nil {
		resetDay = *req.TrafficResetDay
	}
	if !validTrafficResetDay(resetDay) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "流量重置日必须是 1–31 日"})
		return
	}
	address, err := normalizeNodeAddress(req.Address)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "节点连接地址格式无效: " + err.Error()})
		return
	}
	srv := &model.Server{
		Name:            req.Name,
		Address:         address,
		Region:          req.Region,
		Remark:          req.Remark,
		AgentToken:      randHex(24),
		ConfigMode:      model.ConfigModeManaged,
		TrafficResetDay: resetDay,
	}
	if err := a.db.Create(srv).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"server":          srv,
		"install_command": a.newAgentInstallCommand(srv.ID),
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
	setServerAddressAvailability(&srv)
	if srv.SingboxInstalled && srv.SingboxVersion != "" {
		releases := getLatestSingboxReleases()
		hasUp, latest := checkSingboxUpdate(srv.SingboxVersion, releases)
		srv.SingboxHasUpdate = hasUp
		srv.SingboxLatestVersion = latest
	}
	c.JSON(http.StatusOK, gin.H{
		"server":          srv,
		"install_command": a.newAgentInstallCommand(srv.ID),
		"public_url":      a.baseURL(),
	})
}

func (a *App) updateServer(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
	var req serverUpdateReq
	if !bindJSON(c, &req) {
		return
	}
	updates := map[string]any{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
			return
		}
		updates["name"] = name
	}
	if req.Address != nil {
		address, err := normalizeNodeAddress(*req.Address)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "节点连接地址格式无效: " + err.Error()})
			return
		}
		updates["address"] = address
	}
	if req.Region != nil {
		updates["region"] = *req.Region
	}
	if req.Remark != nil {
		updates["remark"] = *req.Remark
	}
	if req.TrafficResetDay != nil {
		if !validTrafficResetDay(*req.TrafficResetDay) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "流量重置日必须是 1–31 日"})
			return
		}
		updates["traffic_reset_day"] = *req.TrafficResetDay
	}
	if err := a.db.Model(&model.Server{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if req.Address != nil && a.orch != nil {
		a.reconcileNodeConsumers(id)
	}
	// Server metadata and traffic cycles do not belong in the
	// sing-box config, so editing a node must not restart its service.
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (a *App) deleteServer(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
	unlockConfig := func() {}
	if a.orch != nil {
		unlockConfig = a.orch.lockServer(id)
	}
	defer unlockConfig()
	a.userAccessMu.Lock()
	defer a.userAccessMu.Unlock()

	var srv model.Server
	if err := a.db.First(&srv, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	if a.hub != nil {
		disconnectCtx, cancelDisconnect := context.WithTimeout(context.Background(), 10*time.Second)
		disconnectErr := a.hub.DisconnectAndWait(disconnectCtx, id)
		cancelDisconnect()
		if disconnectErr != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "节点连接仍在处理数据，请稍后重试删除"})
			return
		}
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
	var forwards []model.UserForward
	if err := tx.Where("server_id = ?", id).Find(&forwards).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := deleteForwardState(tx, forwards); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, row := range []any{&model.Inbound{}, &model.Outbound{}, &model.RouteRule{}, &model.ServerRuleSet{}} {
		if err := tx.Where("server_id = ?", id).Delete(row).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	for _, row := range []any{&model.InboundTrafficIdentity{}, &model.UserTrafficIdentity{}, &model.TrafficRecord{}, &model.TrafficHourly{}, &model.TrafficUserHourly{}, &model.TrafficReportCursor{}} {
		if err := tx.Where("server_id = ?", id).Delete(row).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := deleteUserNodeOrderRefs(tx, userNodeTypeManaged, inboundIDs); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := removeServerUserAssignments(tx, id, inboundIDs); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
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
		a.hub.traffic.dropServer(id)
		for _, row := range []any{&model.InboundTrafficIdentity{}, &model.UserTrafficIdentity{}, &model.TrafficRecord{}, &model.TrafficHourly{}, &model.TrafficUserHourly{}, &model.TrafficReportCursor{}} {
			if err := a.db.Where("server_id = ?", id).Delete(row).Error; err != nil {
				log.Printf("delete server %d: final traffic cleanup failed: %v", id, err)
			}
		}
		if a.hub.live != nil {
			a.hub.live.clearServer(id)
		}
	}
	unlockLogs := a.logSessionLocks.lock(id)
	a.logSessionsMu.Lock()
	for _, lease := range a.logSessions[id] {
		if lease.Timer != nil {
			lease.Timer.Stop()
		}
	}
	delete(a.logSessions, id)
	a.logSessionsMu.Unlock()
	unlockLogs()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
