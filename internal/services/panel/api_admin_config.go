package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// remoteConfig reads the sing-box config currently on the host (for viewing an
// existing/hand-made installation).
func (a *App) remoteConfig(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	var srv model.Server
	if err := a.db.First(&srv, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdGetConfig, nil)
	if errors.Is(err, ErrAgentOffline) {
		raw, buildErr := a.orch.buildServerConfigReadOnly(&srv, nil)
		if buildErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": buildErr.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"exists": true, "raw": string(raw), "path": "", "source": "panel"})
		return
	}
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
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
	var req rawConfigReq
	if !bindJSONLimit(c, &req, maxPanelAgentMessageSize) {
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
	unlock := a.orch.lockServer(id)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	if err := validateImportedInboundTrafficTags(a.db, id, raw); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errInboundTrafficTagReserved) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	if _, err := validateImportedPrivateOutbounds(a.db, parsed.Outbounds, raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateImportedPrivateRouting(a.db, id, parsed, raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, err := a.orch.applyRawConfigUnlocked(ctx, id, raw)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "output": res.Output})
		return
	}
	resolved, err := a.applyImportUnlockedWithSummary(&model.Server{ID: id}, parsed, raw)
	if err != nil {
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
	locked = false
	unlock()
	a.reconcileNodeConsumers(id)
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "output": res.Output, "summary": buildImportSummary(parsed, &resolved),
		"config_mode": synced.ConfigMode,
		"warnings":    rawConfigSecurityWarnings(raw),
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
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
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
	a.reconcileNodeConsumers(id)
	c.JSON(http.StatusOK, gin.H{"ok": true, "config_mode": model.ConfigModeManaged})
}
