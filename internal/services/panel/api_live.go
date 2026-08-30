package panel

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

const logSessionTTL = 2 * time.Minute

type logSessionLease struct {
	Lines     int
	ExpiresAt time.Time
	Timer     *time.Timer
}

func (a *App) setLogSessionLocked(serverID uint, sessionID string, lines int) {
	if a.logSessions == nil {
		a.logSessions = make(map[uint]map[string]logSessionLease)
	}
	sessions := a.logSessions[serverID]
	if sessions == nil {
		sessions = make(map[string]logSessionLease)
		a.logSessions[serverID] = sessions
	}
	previous, existed := sessions[sessionID]
	if existed && previous.Timer != nil {
		previous.Timer.Stop()
	}
	if lines <= 0 {
		if existed && previous.Lines > 0 {
			lines = previous.Lines
		} else {
			lines = 200
		}
	}
	deadline := time.Now().Add(logSessionTTL)
	lease := logSessionLease{Lines: lines, ExpiresAt: deadline}
	lease.Timer = time.AfterFunc(logSessionTTL, func() {
		a.expireLogSession(serverID, sessionID, deadline)
	})
	sessions[sessionID] = lease
}

func (a *App) deleteLogSessionLocked(serverID uint, sessionID string) (logSessionLease, bool) {
	sessions := a.logSessions[serverID]
	if sessions == nil {
		return logSessionLease{}, false
	}
	lease, ok := sessions[sessionID]
	if !ok {
		return logSessionLease{}, false
	}
	if lease.Timer != nil {
		lease.Timer.Stop()
	}
	delete(sessions, sessionID)
	if len(sessions) == 0 {
		delete(a.logSessions, serverID)
	}
	return lease, true
}

// expireLogSession removes a browser lease that stopped renewing (tab crash,
// network loss, mobile OS suspension). When it was the final viewer, stop the
// Agent's single journalctl follower as well.
func (a *App) expireLogSession(serverID uint, sessionID string, deadline time.Time) {
	unlockServer := a.logSessionLocks.lock(serverID)
	defer unlockServer()

	a.logSessionsMu.Lock()
	sessions := a.logSessions[serverID]
	lease, exists := sessions[sessionID]
	if !exists || !lease.ExpiresAt.Equal(deadline) || time.Now().Before(lease.ExpiresAt) {
		a.logSessionsMu.Unlock()
		return
	}
	_, _ = a.deleteLogSessionLocked(serverID, sessionID)
	lastSession := len(a.logSessions[serverID]) == 0
	a.logSessionsMu.Unlock()
	if !lastSession {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := a.hub.SendCommand(ctx, serverID, protocol.CmdStreamLogs, protocol.StreamLogsCmd{
		Enable: false, SessionID: sessionID,
	}); err != nil && !errors.Is(err, ErrAgentOffline) {
		log.Printf("expire log stream for server %d: %v", serverID, err)
	}
}

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
	if !validLogSessionID(req.SessionID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log session"})
		return
	}

	// The Agent owns one journalctl process, while several browser tabs may be
	// watching it. Serialize transitions for this server only; never hold the
	// global state lock while waiting on a network round trip to the Agent.
	unlockServer := a.logSessionLocks.lock(id)
	defer unlockServer()

	var removedLease logSessionLease
	a.logSessionsMu.Lock()
	sessions := a.logSessions[id]
	if req.Enable {
		if _, exists := sessions[req.SessionID]; exists {
			a.setLogSessionLocked(id, req.SessionID, req.Lines)
			a.logSessionsMu.Unlock()
			c.JSON(http.StatusOK, gin.H{"ok": true, "output": "log session lease renewed"})
			return
		}
		if len(sessions) >= 64 {
			a.logSessionsMu.Unlock()
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many log sessions"})
			return
		}
		if len(sessions) > 0 {
			a.setLogSessionLocked(id, req.SessionID, req.Lines)
			a.logSessionsMu.Unlock()
			c.JSON(http.StatusOK, gin.H{"ok": true, "output": "joined existing log stream"})
			return
		}
	} else {
		if sessions == nil {
			a.logSessionsMu.Unlock()
			c.JSON(http.StatusOK, gin.H{"ok": true, "output": "log session already stopped"})
			return
		}
		if _, exists := sessions[req.SessionID]; !exists {
			a.logSessionsMu.Unlock()
			c.JSON(http.StatusOK, gin.H{"ok": true, "output": "log session already stopped"})
			return
		}
		removedLease, _ = a.deleteLogSessionLocked(id, req.SessionID)
		if len(sessions) > 0 {
			a.logSessionsMu.Unlock()
			c.JSON(http.StatusOK, gin.H{"ok": true, "output": "other log sessions remain active"})
			return
		}
	}
	a.logSessionsMu.Unlock()

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, id, protocol.CmdStreamLogs, req)
	if err != nil {
		if req.Enable {
			// The command may have reached the Agent just before the request was
			// cancelled. Stop any orphaned stream without reusing the cancelled
			// browser context; a later explicit enable will start it again.
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, _ = a.hub.SendCommand(stopCtx, id, protocol.CmdStreamLogs, protocol.StreamLogsCmd{Enable: false, SessionID: req.SessionID})
			stopCancel()
		} else {
			// Keep the final browser reference when the Agent did not acknowledge
			// the stop. A retry can then stop a stream that may still be running;
			// dropping the reference here would turn it into an unmanageable orphan.
			a.logSessionsMu.Lock()
			a.setLogSessionLocked(id, req.SessionID, removedLease.Lines)
			a.logSessionsMu.Unlock()
		}
		status := http.StatusBadGateway
		if errors.Is(err, ErrAgentOffline) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	if req.Enable {
		a.logSessionsMu.Lock()
		a.setLogSessionLocked(id, req.SessionID, req.Lines)
		a.logSessionsMu.Unlock()
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
}

// resumeLogStream restarts the Agent-owned journal stream after an Agent
// reconnect when at least one browser session is still active.
func (a *App) resumeLogStream(serverID uint) {
	unlockServer := a.logSessionLocks.lock(serverID)
	defer unlockServer()

	a.logSessionsMu.Lock()
	sessions := a.logSessions[serverID]
	sessionID := ""
	lines := 0
	for id, lease := range sessions {
		if time.Now().After(lease.ExpiresAt) {
			_, _ = a.deleteLogSessionLocked(serverID, id)
			continue
		}
		if sessionID == "" {
			sessionID = id
		}
		if lease.Lines > lines {
			lines = lease.Lines
		}
	}
	a.logSessionsMu.Unlock()
	if sessionID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := a.hub.SendCommand(ctx, serverID, protocol.CmdStreamLogs, protocol.StreamLogsCmd{
		Enable: true, Lines: lines, SessionID: sessionID,
	}); err != nil && !errors.Is(err, ErrAgentOffline) {
		log.Printf("resume log stream for server %d: %v", serverID, err)
	}
}

func validLogSessionID(value string) bool {
	if len(value) < 16 || len(value) > 80 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
