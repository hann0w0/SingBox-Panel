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
		delete(a.logStreamActive, serverID)
	}
	return lease, true
}

func (a *App) setLogStreamActiveLocked(serverID uint, active bool) {
	if !active {
		delete(a.logStreamActive, serverID)
		return
	}
	if a.logStreamActive == nil {
		a.logStreamActive = make(map[uint]bool)
	}
	a.logStreamActive[serverID] = true
}

func (a *App) clearLogStreamActive(serverID uint) {
	a.logSessionsMu.Lock()
	delete(a.logStreamActive, serverID)
	a.logSessionsMu.Unlock()
}

// expireLogSession removes a browser lease that stopped renewing (tab crash,
// network loss, mobile OS suspension). When it was the final viewer, stop the
// Agent's single native log follower as well.
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

func (a *App) stopLogSessions() {
	a.logSessionsMu.Lock()
	defer a.logSessionsMu.Unlock()
	for serverID, sessions := range a.logSessions {
		for sessionID, lease := range sessions {
			if lease.Timer != nil {
				lease.Timer.Stop()
			}
			delete(sessions, sessionID)
		}
		delete(a.logSessions, serverID)
	}
	clear(a.logStreamActive)
}

const sseWriteDeadline = 10 * time.Second

func drainLiveEvents(ch <-chan liveEvent, first liveEvent, maxBatch int) ([]liveEvent, bool) {
	batch := make([]liveEvent, 1, min(maxBatch, 16))
	batch[0] = first
	for len(batch) < maxBatch {
		select {
		case evt, ok := <-ch:
			if !ok {
				return batch, false
			}
			batch = append(batch, evt)
		default:
			return batch, true
		}
	}
	return batch, true
}

// handleTrafficSSE opens a Server-Sent Events stream that pushes real-time
// traffic, progress, and log events for a specific server.
// The browser receives real-time events via SSE (fetch + ReadableStream);
// EventSource is not used because it cannot send custom Authorization headers.
func (a *App) handleTrafficSSE(c *gin.Context) {
	value, _ := c.Get("session_claims")
	claims, _ := value.(*Claims)
	ctx := c.Request.Context()
	if !a.auth.sessionActive(ctx, claims) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session revoked"})
		return
	}
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
	rc := http.NewResponseController(c.Writer)
	_ = rc.SetReadDeadline(time.Time{})

	sub, backlog := a.hub.live.subscribe(serverID)
	defer a.hub.live.unsubscribe(serverID, sub)
	logUsers := logUserResolver{db: a.db, serverID: serverID}

	_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
	if len(backlog) > 0 {
		if !a.auth.sessionActive(ctx, claims) {
			return
		}
		// Send initial backlog so the chart isn't empty on connect.
		for _, evt := range backlog {
			if _, err := io.WriteString(c.Writer, logUsers.event(ctx, evt).SSE()); err != nil {
				return
			}
		}
	}
	flusher.Flush()

	// Heartbeat comment every 15s to keep the connection alive through proxies.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	expires := time.NewTimer(time.Until(claims.ExpiresAt.Time))
	defer expires.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expires.C:
			return
		case evt, ok := <-sub.ch:
			if !ok {
				return
			}
			batch, open := drainLiveEvents(sub.ch, evt, 64)
			if !a.auth.sessionActive(ctx, claims) {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
			for _, item := range batch {
				if _, err := io.WriteString(c.Writer, logUsers.event(ctx, item).SSE()); err != nil {
					return
				}
			}
			flusher.Flush()
			if !open {
				return
			}
		case <-heartbeat.C:
			if !a.auth.sessionActive(ctx, claims) {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
			if _, err := io.WriteString(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleServersLiveSSE opens a Server-Sent Events stream that pushes real-time
// server_status updates (rates, load, memory, online state) for all servers.
func (a *App) handleServersLiveSSE(c *gin.Context) {
	value, _ := c.Get("session_claims")
	claims, _ := value.(*Claims)
	ctx := c.Request.Context()
	if !a.auth.sessionActive(ctx, claims) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session revoked"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}
	rc := http.NewResponseController(c.Writer)
	_ = rc.SetReadDeadline(time.Time{})

	sub := a.hub.live.subscribeAll()
	defer a.hub.live.unsubscribeAll(sub)

	_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
	if _, err := io.WriteString(c.Writer, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	expires := time.NewTimer(time.Until(claims.ExpiresAt.Time))
	defer expires.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expires.C:
			return
		case evt, ok := <-sub.ch:
			if !ok {
				return
			}
			batch, open := drainLiveEvents(sub.ch, evt, 64)
			if !a.auth.sessionActive(ctx, claims) {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
			for _, item := range batch {
				if _, err := io.WriteString(c.Writer, item.SSE()); err != nil {
					return
				}
			}
			flusher.Flush()
			if !open {
				return
			}
		case <-heartbeat.C:
			if !a.auth.sessionActive(ctx, claims) {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
			if _, err := io.WriteString(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
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

	// The Agent owns one native log follower, while several browser tabs may be
	// watching it. Serialize transitions for this server only; never hold the
	// global state lock while waiting on a network round trip to the Agent.
	unlockServer := a.logSessionLocks.lock(id)
	defer unlockServer()

	var removedLease logSessionLease
	a.logSessionsMu.Lock()
	sessions := a.logSessions[id]
	streamRunning := a.logStreamActive[id] && a.hub != nil && a.hub.IsOnline(id)
	if req.Enable {
		if _, exists := sessions[req.SessionID]; exists {
			if streamRunning {
				a.setLogSessionLocked(id, req.SessionID, req.Lines)
				a.logSessionsMu.Unlock()
				c.JSON(http.StatusOK, gin.H{"ok": true, "output": "log session lease renewed"})
				return
			}
		} else {
			if len(sessions) >= 64 {
				a.logSessionsMu.Unlock()
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many log sessions"})
				return
			}
			if len(sessions) > 0 && streamRunning {
				a.setLogSessionLocked(id, req.SessionID, req.Lines)
				a.logSessionsMu.Unlock()
				c.JSON(http.StatusOK, gin.H{"ok": true, "output": "joined existing log stream"})
				return
			}
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
			a.clearLogStreamActive(id)
			// The command may have reached the Agent just before the request was
			// cancelled. Stop any orphaned stream without reusing the cancelled
			// browser context; a later explicit enable will start it again.
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, _ = a.hub.SendCommand(stopCtx, id, protocol.CmdStreamLogs, protocol.StreamLogsCmd{Enable: false, SessionID: req.SessionID})
			stopCancel()
		} else if !errors.Is(err, ErrAgentOffline) {
			// Keep the final browser reference when an online Agent rejected the
			// stop so a retry can stop a stream that may still be running. When the
			// Agent is offline its native follower is already gone, so do not
			// resurrect a 2-minute zombie lease.
			a.logSessionsMu.Lock()
			a.setLogSessionLocked(id, req.SessionID, removedLease.Lines)
			a.logSessionsMu.Unlock()
		} else {
			a.clearLogStreamActive(id)
		}
		status := http.StatusBadGateway
		if errors.Is(err, ErrAgentOffline) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	a.logSessionsMu.Lock()
	if req.Enable {
		a.setLogSessionLocked(id, req.SessionID, req.Lines)
		a.setLogStreamActiveLocked(id, true)
	} else if len(a.logSessions[id]) == 0 {
		a.setLogStreamActiveLocked(id, false)
	}
	a.logSessionsMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output})
}

// resumeLogStream restarts the Agent-owned journal stream after an Agent
// reconnect when at least one browser session is still active.
func (a *App) resumeLogStream(serverID uint) {
	unlockServer := a.logSessionLocks.lock(serverID)
	defer unlockServer()

	a.logSessionsMu.Lock()
	a.setLogStreamActiveLocked(serverID, false)
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
	}); err != nil {
		if !errors.Is(err, ErrAgentOffline) {
			log.Printf("resume log stream for server %d: %v", serverID, err)
		}
		return
	}
	a.logSessionsMu.Lock()
	if len(a.logSessions[serverID]) > 0 {
		a.setLogStreamActiveLocked(serverID, true)
	}
	a.logSessionsMu.Unlock()
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
