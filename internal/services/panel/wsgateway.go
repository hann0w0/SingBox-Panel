package panel

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

var agentUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Agents are non-browser clients; Origin checks do not apply.
	CheckOrigin: func(r *http.Request) bool { return true },
}

const (
	wsHandshakeWindow   = time.Minute
	wsHandshakeBurst    = 60
	wsHandshakeBlock    = 30 * time.Second
	maxWSHandshakePeers = 4096
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
	if len(l.entries) > maxWSHandshakePeers {
		for candidate, candidateEntry := range l.entries {
			if candidate != key && candidateEntry.blockedTill.Before(now) &&
				(len(candidateEntry.attempts) == 0 || candidateEntry.attempts[len(candidateEntry.attempts)-1].Before(cutoff)) {
				delete(l.entries, candidate)
			}
		}
		l.evictOldestLocked(key)
	}
	return 0
}

func (l *wsHandshakeLimiter) evictOldestLocked(preserve string) {
	for len(l.entries) > maxWSHandshakePeers {
		oldestKey := ""
		var oldestAt time.Time
		for key, entry := range l.entries {
			if key == preserve && len(l.entries) > 1 {
				continue
			}
			activity := entry.blockedTill
			if n := len(entry.attempts); n > 0 && entry.attempts[n-1].After(activity) {
				activity = entry.attempts[n-1]
			}
			if oldestKey == "" || activity.Before(oldestAt) {
				oldestKey = key
				oldestAt = activity
			}
		}
		if oldestKey == "" {
			return
		}
		delete(l.entries, oldestKey)
	}
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
	ac := a.registerAuthenticatedAgent(srv.ID, token, clientIP(c), conn)
	if ac == nil {
		return
	}
	ac.serve()
}

func (a *App) registerAuthenticatedAgent(serverID uint, token, publicIP string, conn *websocket.Conn) *agentConn {
	// Keep the final credential check and Hub registration in the same
	// per-server critical section used by credential resets. This
	// prevents a handshake validated with an old token from registering after
	// the credential has changed.
	unlockOperation := a.lockServerOperation(serverID)
	defer unlockOperation()

	var srv model.Server
	if err := a.db.Select("id", "agent_token").First(&srv, serverID).Error; err != nil || token != srv.AgentToken {
		if conn != nil {
			_ = conn.Close()
		}
		return nil
	}
	// Record the observed public IP (used for display; share links use the
	// admin-configured Address).
	if publicIP != "" {
		a.db.Model(&model.Server{}).Where("id = ?", serverID).Update("public_ip", publicIP)
	}

	return a.hub.register(serverID, publicIP, conn)
}
