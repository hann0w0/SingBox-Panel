package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

// installSingbox triggers an official sing-box install on the agent.
func (a *App) installSingbox(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
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
	if req.Version != "" && (req.Method == protocol.MethodAPT || req.Method == protocol.MethodDNF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "指定 sing-box 版本请使用官方脚本安装方式；APT/DNF 仅支持软件源最新版本"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Minute)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdInstallSingbox, req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "output": res.Output})
		return
	}
	// Invalidate the release cache so the UI immediately reflects the new
	// version instead of comparing against a stale "latest" until cache expiry.
	invalidateSingboxReleaseCache()
	var installInfo protocol.InstallSingboxData
	if len(res.Data) > 0 {
		_ = json.Unmarshal(res.Data, &installInfo)
	}
	if ver := strings.TrimSpace(installInfo.SingboxVersion); ver != "" {
		if err := a.db.Model(&model.Server{}).Where("id = ?", id).Updates(map[string]any{
			"singbox_installed": true,
			"singbox_version":   ver,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "更新节点 Sing-box 版本失败"})
			return
		}
	}
	// Once installed, push the panel's config ONLY if this server has protocols
	// configured in the panel — never overwrite an existing config with an empty one.
	a.orch.PushConfigIfManaged(id)
	c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
}

// uninstallSingbox removes the remote sing-box package, binary and host service
// definition while preserving its configuration, Agent and panel-side node data.
func (a *App) uninstallSingbox(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute+30*time.Second)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdUninstallSingbox, nil)
	if err != nil {
		status := http.StatusBadGateway
		message := err.Error()
		switch {
		case errors.Is(err, ErrAgentOffline):
			status = http.StatusConflict
			message = "节点离线，无法卸载 Sing-box"
		case strings.Contains(err.Error(), "unknown command"):
			status = http.StatusBadRequest
			message = "该节点的 Agent 版本过旧，请先点击「安装 / 升级 Agent」完成自动升级后再卸载 Sing-box"
		}
		c.JSON(status, gin.H{"error": message, "output": res.Output})
		return
	}
	if err := a.db.Model(&model.Server{}).Where("id = ?", id).Updates(map[string]any{
		"singbox_installed": false,
		"singbox_version":   "",
		"singbox_active":    false,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "output": res.Output})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
}

// uninstallAgent removes the Agent binary, configuration and host service
// definition from the controlled VPS while keeping the panel's node record and
// install command.
func (a *App) uninstallAgent(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
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
	a.invalidateAgentInstallCodes(id)
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
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
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
// Version direction is intentionally ignored: an operator-triggered sync must
// also recover from release-number resets. The Agent validates the downloaded
// binary and compares its SHA-256 before staging any replacement.
func (a *App) updateAgent(c *gin.Context) {
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()
	res, err := a.sendAgentUpdateCommand(ctx, id)
	if err != nil {
		// An agent predating self-update cannot upgrade itself — tell the admin
		// to run the install command once; from then on this button works.
		if strings.Contains(err.Error(), "unknown command") {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "该节点的 Agent 版本过旧，不支持一键升级。请在这台服务器上重跑一次安装命令（升级后本按钮即可使用）：\n" +
					a.newAgentInstallCommand(srv.ID),
			})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "output": res.Output})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"updated": agentUpdatePerformed(res.Output),
		"output":  res.Output,
	})
}

func agentUpdatePerformed(output string) bool {
	return !strings.HasPrefix(strings.TrimSpace(output), "Agent 已是最新版本")
}

func (a *App) sendAgentUpdateCommand(ctx context.Context, serverID uint) (protocol.CommandResultEvt, error) {
	if a.hub == nil {
		return protocol.CommandResultEvt{}, ErrAgentOffline
	}
	connection := a.hub.connection(serverID)
	if connection == nil {
		return protocol.CommandResultEvt{}, ErrAgentOffline
	}
	return a.hub.sendCommandToConnection(ctx, connection, protocol.CmdUpdateAgent, nil)
}

type agentUpdateResult struct {
	ServerID   uint   `json:"server_id"`
	ServerName string `json:"server_name"`
	Success    bool   `json:"success"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (a *App) updateAllAgents(c *gin.Context) {
	if a.hub == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent hub unavailable"})
		return
	}
	serverIDs := a.hub.OnlineServerIDs()
	sort.Slice(serverIDs, func(i, j int) bool { return serverIDs[i] < serverIDs[j] })
	if len(serverIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"ok": true, "message": "当前没有在线的服务器", "count": 0,
			"requested": 0, "succeeded": 0, "failed": 0, "results": []agentUpdateResult{},
		})
		return
	}

	var servers []model.Server
	if err := a.db.Where("id IN ?", serverIDs).Find(&servers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	serversByID := make(map[uint]model.Server, len(servers))
	for i := range servers {
		serversByID[servers[i].ID] = servers[i]
	}
	results := make([]agentUpdateResult, len(serverIDs))
	jobs := make(chan int, len(serverIDs))
	for i := range serverIDs {
		jobs <- i
	}
	close(jobs)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()
	workers := 4
	if len(serverIDs) < workers {
		workers = len(serverIDs)
	}
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for index := range jobs {
				serverID := serverIDs[index]
				server := serversByID[serverID]
				result := agentUpdateResult{ServerID: serverID, ServerName: server.Name}
				if result.ServerName == "" {
					result.Error = "服务器记录不存在"
					results[index] = result
					continue
				}
				unlockOperation := a.lockServerOperation(serverID)
				res, err := a.sendAgentUpdateCommand(ctx, serverID)
				unlockOperation()
				result.Output = trimRunes(res.Output, 2000)
				if err != nil {
					result.Error = trimRunes(strings.TrimSpace(err.Error()), 500)
					if result.Error == "" {
						result.Error = "Agent 更新失败"
					}
				} else {
					result.Success = true
				}
				results[index] = result
			}
		}()
	}
	wg.Wait()

	succeeded := 0
	for i := range results {
		if results[i].Success {
			succeeded++
		}
	}
	failed := len(results) - succeeded
	message := fmt.Sprintf("Agent 同步完成：%d 台成功，%d 台失败", succeeded, failed)

	c.JSON(http.StatusOK, gin.H{
		"ok":        failed == 0,
		"message":   message,
		"count":     len(results),
		"requested": len(results),
		"succeeded": succeeded,
		"failed":    failed,
		"results":   results,
	})
}

func (a *App) serverStatus(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	connection := a.hub.connection(id)
	if connection == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": ErrAgentOffline.Error()})
		return
	}
	res, err := a.hub.sendCommandToConnection(ctx, connection, protocol.CmdGetStatus, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var status protocol.StatusData
	if err := json.Unmarshal(res.Data, &status); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解析节点状态失败"})
		return
	}
	a.hub.persistServerStatus(connection, status)
	c.JSON(http.StatusOK, gin.H{"status": status})
}

func (h *Hub) persistServerStatus(connection *agentConn, status protocol.StatusData) {
	if connection == nil {
		return
	}
	unlock := h.state.lock(connection.serverID)
	defer unlock()
	if !h.isCurrentConnection(connection.serverID, connection) {
		return
	}
	changed := h.updatePublicAddresses(connection.serverID, status.PublicIP, status.PublicIPv4, status.PublicIPv6)
	persistServerStatus(h.db, connection.serverID, status)
	if changed && h.AfterAddressChange != nil {
		h.AfterAddressChange(connection.serverID)
	}
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
