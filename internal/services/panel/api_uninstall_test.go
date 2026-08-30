package panel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func connectedTestAgent(hub *Hub, serverID uint, expected protocol.MessageType, t *testing.T) {
	t.Helper()
	conn := &agentConn{
		serverID: serverID,
		send:     make(chan protocol.Envelope, 1),
		done:     make(chan struct{}),
		pending:  make(map[string]chan protocol.CommandResultEvt),
	}
	hub.conns[serverID] = conn
	go func() {
		envelope := <-conn.send
		if envelope.Type != expected {
			t.Errorf("command = %s, want %s", envelope.Type, expected)
		}
		conn.mu.Lock()
		result := conn.pending[envelope.ID]
		conn.mu.Unlock()
		result <- protocol.CommandResultEvt{ID: envelope.ID, OK: true, Output: "done"}
	}()
}

func TestUninstallSingboxKeepsAgentCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{
		Name: "node", AgentToken: "agent-token", Online: true,
		SingboxInstalled: true, SingboxVersion: "1.14.0", SingboxActive: true,
	}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	connectedTestAgent(hub, server.ID, protocol.CmdUninstallSingbox, t)
	a := &App{db: db, hub: hub, serverOperations: newKeyedMutex[uint]()}
	router := gin.New()
	router.POST("/servers/:id/uninstall-singbox", a.uninstallSingbox)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/servers/1/uninstall-singbox", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := db.First(&server, server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if server.SingboxInstalled || server.SingboxActive || server.SingboxVersion != "" || server.AgentToken != "agent-token" {
		t.Fatalf("unexpected server state: %+v", server)
	}
}

func TestUninstallAgentKeepsStableCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{
		Name: "node", AgentToken: "old-agent-token", SingboxInstalled: true,
	}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	connectedTestAgent(hub, server.ID, protocol.CmdUninstallAgent, t)
	a := &App{
		db: db, hub: hub, serverOperations: newKeyedMutex[uint](),
		agentInstallCodes: map[string]agentInstallCode{},
	}
	router := gin.New()
	router.POST("/servers/:id/uninstall-agent", a.uninstallAgent)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/servers/1/uninstall-agent", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := db.First(&server, server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if server.AgentToken != "old-agent-token" || !server.SingboxInstalled {
		t.Fatalf("unexpected server state: %+v", server)
	}
}
