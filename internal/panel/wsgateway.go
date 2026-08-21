package panel

import (
	"net/http"
	"sync"
	"time"

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

const (
	wsHandshakeWindow = time.Minute
	wsHandshakeBurst  = 60
	wsHandshakeBlock  = 30 * time.Second
)

type wsHandshakeEntry struct {
	attempts    []time.Time
	blockedTill time.Time
}

type wsHandshakeLimiter struct {
	mu      sync.Mutex
	entries map[string]*wsHandshakeEntry
}

func newWSHandshakeLimiter() *wsHandshakeLimiter {
	return &wsHandshakeLimiter{entries: make(map[string]*wsHandshakeEntry)}
}

func (l *wsHandshakeLimiter) allow(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[key]
	if entry == nil {
		entry = &wsHandshakeEntry{}
		l.entries[key] = entry
	}
	if wait := entry.blockedTill.Sub(now); wait > 0 {
		return wait
	}
	cutoff := now.Add(-wsHandshakeWindow)
	kept := entry.attempts[:0]
	for _, attempt := range entry.attempts {
		if attempt.After(cutoff) {
			kept = append(kept, attempt)
		}
	}
	entry.attempts = kept
	if len(entry.attempts) >= wsHandshakeBurst {
		entry.blockedTill = now.Add(wsHandshakeBlock)
		return wsHandshakeBlock
	}
	entry.attempts = append(entry.attempts, now)
	if len(l.entries) > 4096 {
		for candidate, candidateEntry := range l.entries {
			if candidate != key && candidateEntry.blockedTill.Before(now) &&
				(len(candidateEntry.attempts) == 0 || candidateEntry.attempts[len(candidateEntry.attempts)-1].Before(cutoff)) {
				delete(l.entries, candidate)
			}
		}
	}
	return 0
}

// handleAgentWS authenticates an agent by its server token and runs the
// connection through the hub until it disconnects.
func (a *App) handleAgentWS(c *gin.Context) {
	if wait := a.allowWSHandshake(clientIP(c), time.Now()); wait > 0 {
		c.Header("Retry-After", "30")
		c.AbortWithStatus(http.StatusTooManyRequests)
		return
	}
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
