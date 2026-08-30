package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestStaleAgentHandshakeCannotRegisterAfterCredentialReset(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "node", AgentToken: "old-agent-token"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, hub: NewHub(db), serverOperations: newKeyedMutex[uint]()}

	unlockOperation := a.lockServerOperation(server.ID)
	started := make(chan struct{})
	registered := make(chan *agentConn, 1)
	go func() {
		close(started)
		registered <- a.registerAuthenticatedAgent(server.ID, "old-agent-token", "", nil)
	}()
	<-started
	if err := db.Model(&model.Server{}).Where("id = ?", server.ID).
		Update("agent_token", "new-agent-token").Error; err != nil {
		unlockOperation()
		t.Fatal(err)
	}
	unlockOperation()

	select {
	case connection := <-registered:
		if connection != nil || a.hub.IsOnline(server.ID) {
			t.Fatal("stale Agent handshake registered after credential reset")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale Agent handshake remained blocked after credential reset")
	}
}

func TestUnknownAgentTokenCannotAuthenticate(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "node", AgentToken: "active-agent-token"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, hub: NewHub(db), serverOperations: newKeyedMutex[uint]()}
	if connection := a.registerAuthenticatedAgent(server.ID, "different-agent-token", "", nil); connection != nil {
		t.Fatal("unknown Agent token was accepted")
	}
	if a.hub.IsOnline(server.ID) {
		t.Fatal("unknown Agent token registered an online connection")
	}
}

func TestInstallCodeExchangeCannotReturnTokenAfterCredentialResetInvalidatesCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{Name: "node", AgentToken: "old-agent-token"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{
		db:                db,
		agentInstallCodes: map[string]agentInstallCode{"pending-code": {ServerID: server.ID, Expires: time.Now().Add(time.Minute)}},
		serverOperations:  newKeyedMutex[uint](),
	}

	unlockOperation := a.lockServerOperation(server.ID)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/agent/register", strings.NewReader("code=pending-code"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = req
		a.exchangeAgentInstallCode(ctx)
		done <- recorder
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.serverOperations.mu.Lock()
		entry := a.serverOperations.items[server.ID]
		waiters := 0
		if entry != nil {
			waiters = entry.refs
		}
		a.serverOperations.mu.Unlock()
		if waiters >= 2 {
			break
		}
		if time.Now().After(deadline) {
			unlockOperation()
			t.Fatal("install-code exchange did not reach the server operation lock")
		}
		time.Sleep(time.Millisecond)
	}

	a.invalidateAgentInstallCodes(server.ID)
	if err := db.Model(&model.Server{}).Where("id = ?", server.ID).Update("agent_token", "new-agent-token").Error; err != nil {
		unlockOperation()
		t.Fatal(err)
	}
	unlockOperation()

	select {
	case recorder := <-done:
		if recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), "old-agent-token") {
			t.Fatalf("stale install-code exchange status=%d body=%q", recorder.Code, recorder.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("install-code exchange remained blocked after credential reset")
	}
}

func TestUpdateAllAgentsReturnsPerServerFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	servers := []model.Server{
		{Name: "alpha", AgentToken: "alpha-token"},
		{Name: "beta", AgentToken: "beta-token"},
	}
	if err := db.Create(&servers).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	a := &App{
		db:               db,
		hub:              hub,
		serverOperations: newKeyedMutex[uint](),
	}
	for i := range servers {
		conn := &agentConn{
			serverID: servers[i].ID,
			remoteIP: "198.51.100.20",
			send:     make(chan protocol.Envelope, 1),
			done:     make(chan struct{}),
			pending:  make(map[string]chan protocol.CommandResultEvt),
		}
		hub.conns[servers[i].ID] = conn
		ok := i == 0
		go func(ac *agentConn, success bool) {
			envelope := <-ac.send
			if envelope.Type != protocol.CmdUpdateAgent {
				t.Errorf("command = %s, want %s", envelope.Type, protocol.CmdUpdateAgent)
			}
			ac.mu.Lock()
			result := ac.pending[envelope.ID]
			ac.mu.Unlock()
			if success {
				result <- protocol.CommandResultEvt{ID: envelope.ID, OK: true, Output: "updated"}
			} else {
				result <- protocol.CommandResultEvt{ID: envelope.ID, OK: false, Error: "checksum mismatch"}
			}
		}(conn, ok)
	}

	router := gin.New()
	router.POST("/servers/update-all-agents", a.updateAllAgents)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/servers/update-all-agents", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		OK        bool                `json:"ok"`
		Requested int                 `json:"requested"`
		Succeeded int                 `json:"succeeded"`
		Failed    int                 `json:"failed"`
		Results   []agentUpdateResult `json:"results"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Requested != 2 || response.Succeeded != 1 || response.Failed != 1 || len(response.Results) != 2 {
		t.Fatalf("unexpected response: %+v", response)
	}
	if !response.Results[0].Success || response.Results[1].Success || response.Results[1].Error != "checksum mismatch" {
		t.Fatalf("unexpected per-server results: %+v", response.Results)
	}
}

func TestAgentUpdatePerformedDistinguishesNoOp(t *testing.T) {
	if agentUpdatePerformed("Agent 已是最新版本（v1.1.1）") {
		t.Fatal("an identical Agent binary was reported as restarted")
	}
	if !agentUpdatePerformed("新 Agent 已校验（v1.1.1，123 字节），将重启并在连接失败时自动回滚") {
		t.Fatal("a staged Agent replacement was reported as a no-op")
	}
}

func TestUpdateAgentSynchronizesNewerLabelToServedBuild(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{Name: "newer-agent", AgentToken: "token", AgentVersion: "v1.1.1"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	conn := &agentConn{
		serverID: server.ID,
		remoteIP: "198.51.100.30",
		send:     make(chan protocol.Envelope, 1),
		done:     make(chan struct{}),
		pending:  make(map[string]chan protocol.CommandResultEvt),
	}
	hub.conns[server.ID] = conn
	a := &App{
		db:               db,
		hub:              hub,
		serverOperations: newKeyedMutex[uint](),
	}
	go func() {
		updateEnvelope := <-conn.send
		if updateEnvelope.Type != protocol.CmdUpdateAgent {
			t.Errorf("command = %s, want %s", updateEnvelope.Type, protocol.CmdUpdateAgent)
		}
		conn.mu.Lock()
		result := conn.pending[updateEnvelope.ID]
		conn.mu.Unlock()
		result <- protocol.CommandResultEvt{
			ID: updateEnvelope.ID, OK: true,
			Output: "新 Agent 已校验（v1.1.0，123 字节），将重启并在连接失败时自动回滚",
		}
	}()
	router := gin.New()
	router.POST("/servers/:id/update-agent", a.updateAgent)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/servers/1/update-agent", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"updated":true`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
