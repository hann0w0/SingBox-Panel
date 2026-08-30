package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestStopLogStreamFailureKeepsRetryableSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		serverID = uint(7)
		session  = "0123456789abcdef"
	)
	hub := NewHub(testDB(t))
	conn := &agentConn{
		serverID: serverID,
		send:     make(chan protocol.Envelope, 1),
		done:     make(chan struct{}),
		pending:  make(map[string]chan protocol.CommandResultEvt),
	}
	hub.conns[serverID] = conn
	go func() {
		env := <-conn.send
		conn.mu.Lock()
		result := conn.pending[env.ID]
		conn.mu.Unlock()
		result <- protocol.CommandResultEvt{ID: env.ID, OK: false, Error: "stop failed"}
	}()

	a := &App{
		hub:         hub,
		logSessions: map[uint]map[string]logSessionLease{serverID: {session: {Lines: 200, ExpiresAt: time.Now().Add(time.Minute)}}},
	}
	router := gin.New()
	router.POST("/servers/:id/stream-logs", a.streamLogs)
	req := httptest.NewRequest(http.MethodPost, "/servers/7/stream-logs", strings.NewReader(`{"enable":false,"lines":200,"session_id":"0123456789abcdef"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	a.logSessionsMu.Lock()
	lease, exists := a.logSessions[serverID][session]
	a.logSessionsMu.Unlock()
	if !exists || lease.Lines != 200 {
		t.Fatalf("failed stop lost the retryable session: exists=%v lines=%d", exists, lease.Lines)
	}
	if lease.Timer != nil {
		lease.Timer.Stop()
	}
}

func TestStopUnknownLogSessionDoesNotAllocateState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &App{}
	router := gin.New()
	router.POST("/servers/:id/stream-logs", a.streamLogs)
	req := httptest.NewRequest(http.MethodPost, "/servers/9/stream-logs", strings.NewReader(`{"enable":false,"session_id":"fedcba9876543210"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(a.logSessions) != 0 {
		t.Fatalf("unknown stop allocated log session state: %#v", a.logSessions)
	}
	if size := a.logSessionLocks.size(); size != 0 {
		t.Fatalf("per-server log lock leaked: size=%d", size)
	}
}

func TestExpiredLogSessionIsRemoved(t *testing.T) {
	const (
		serverID = uint(12)
		session  = "expired-session-001"
	)
	deadline := time.Now().Add(-time.Second)
	a := &App{
		hub: NewHub(testDB(t)),
		logSessions: map[uint]map[string]logSessionLease{
			serverID: {session: {Lines: 500, ExpiresAt: deadline}},
		},
	}
	a.expireLogSession(serverID, session, deadline)
	a.logSessionsMu.Lock()
	remaining := len(a.logSessions)
	a.logSessionsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("expired session was retained: %#v", a.logSessions)
	}
	if size := a.logSessionLocks.size(); size != 0 {
		t.Fatalf("per-server log lock leaked: size=%d", size)
	}
}
