package panel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/protocol"
)

// handleTrafficSSE opens a Server-Sent Events stream that pushes real-time
// traffic, progress, and log events for a specific server.
// The browser receives real-time events via SSE (fetch + ReadableStream);
// EventSource is not used because it cannot send custom Authorization headers.
func (a *App) handleTrafficSSE(c *gin.Context) {
	serverID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var srv model.Server
	if err := a.db.Select("id").First(&srv, serverID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // disable Nginx/Caddy buffering

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	sub, backlog := a.hub.live.subscribe(serverID)
	defer a.hub.live.unsubscribe(serverID, sub)

	// Send initial backlog so the chart isn't empty on connect.
	for _, evt := range backlog {
		_, _ = io.WriteString(c.Writer, evt.SSE())
	}
	flusher.Flush()

	// Heartbeat comment every 15s to keep the connection alive through proxies.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sub.ch:
			if !ok {
				return
			}
			_, _ = io.WriteString(c.Writer, evt.SSE())
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = io.WriteString(c.Writer, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// streamLogs starts/stops continuous log streaming from a node.
func (a *App) streamLogs(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req protocol.StreamLogsCmd
	if !bindJSON(c, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdStreamLogs, req)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, ErrAgentOffline) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
}
