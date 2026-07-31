package panel

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/singpanel/singpanel/internal/model"
)

func bindJSON(c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
		return false
	}
	return true
}

func uintParam(c *gin.Context, name string) (uint, bool) {
	v, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + name})
		return 0, false
	}
	return uint(v), true
}

func (a *App) applyConfigAndRespond(c *gin.Context, serverID uint, payload gin.H) {
	result := a.orch.ApplyDesiredConfig(c.Request.Context(), serverID)
	if payload == nil {
		payload = gin.H{}
	}
	payload["apply_state"] = result.ApplyState
	if result.ApplyError != "" {
		payload["apply_error"] = result.ApplyError
	}
	c.JSON(http.StatusOK, payload)
}

var supportedInboundTypes = map[string]bool{
	string(model.InboundVLESS):       true,
	string(model.InboundVMess):       true,
	string(model.InboundTrojan):      true,
	string(model.InboundShadowsocks): true,
	string(model.InboundHysteria2):   true,
	string(model.InboundTUIC):        true,
	string(model.InboundAnyTLS):      true,
	string(model.InboundSnell):       true,
	string(model.InboundSocks):       true,
}

// unixToTime converts a unix-seconds pointer to a *time.Time (0/nil => nil).
func unixToTime(sec *int64) *time.Time {
	if sec == nil || *sec == 0 {
		return nil
	}
	t := time.Unix(*sec, 0)
	return &t
}
