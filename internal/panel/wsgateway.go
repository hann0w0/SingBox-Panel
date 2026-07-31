package panel

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/hann0w0/singbox-panel/internal/model"
)

var agentUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Agents are non-browser clients; Origin checks do not apply.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// handleAgentWS authenticates an agent by its server token and runs the
// connection through the hub until it disconnects.
func (a *App) handleAgentWS(c *gin.Context) {
	token := bearerToken(c)
	if token == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing agent token"})
		return
	}
	var srv model.Server
	if err := a.db.Where("agent_token = ?", token).First(&srv).Error; err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid agent token"})
		return
	}

	conn, err := agentUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return // Upgrade already wrote the error response
	}

	// Record the observed public IP (used for display; share links use the
	// admin-configured Address).
	if ip := c.ClientIP(); ip != "" {
		a.db.Model(&model.Server{}).Where("id = ?", srv.ID).Update("public_ip", ip)
	}

	ac := a.hub.register(srv.ID, conn)
	ac.serve()
}
