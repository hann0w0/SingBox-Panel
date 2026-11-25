package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestHandleAgentWSDoesNotOverwritePublicIPBeforeRegisterAndExposesCapabilitiesImmediately(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	srv := model.Server{
		Name:       "edge-1",
		AgentToken: "token-node4-ws-race",
		PublicIP:   "198.51.100.10",
		PublicIPv6: "2001:db8::10",
	}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{
		db:               db,
		hub:              NewHub(db),
		serverOperations: newKeyedMutex[uint](),
		wsLimiter:        newWSHandshakeLimiter(),
	}
	addressChanged := make(chan uint, 2)
	a.hub.AfterAddressChange = func(serverID uint) {
		addressChanged <- serverID
	}

	router := gin.New()
	router.GET("/api/agent/ws", a.handleAgentWS)
	httpSrv := httptest.NewServer(router)
	defer httpSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/agent/ws"
	hdr := http.Header{"Authorization": {"Bearer " + srv.AgentToken}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Before EvtRegister arrives, hub must NOT publish an uninitialized connection
	// or overwrite stored public_ip.
	time.Sleep(30 * time.Millisecond)
	if a.hub.IsOnline(srv.ID) {
		t.Fatal("connection registered in Hub before EvtRegister frame arrived")
	}

	newV4 := "198.51.100.20"
	regEnv, err := protocol.NewEnvelope(protocol.EvtRegister, "", protocol.RegisterEvt{
		AgentVersion:                 "1.0.17",
		PublicIPv4:                   &newV4,
		RestrictedOutboundValidation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(regEnv); err != nil {
		t.Fatal(err)
	}

	select {
	case gotID := <-addressChanged:
		if gotID != srv.ID {
			t.Fatalf("AfterAddressChange serverID = %d, want %d", gotID, srv.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AfterAddressChange did not fire after IPv4 change in EvtRegister")
	}

	ac := a.hub.connection(srv.ID)
	if ac == nil || !ac.restrictedOutboundValidation {
		t.Fatalf("expected live connection with restrictedOutboundValidation=true, got %+v", ac)
	}
	var updated model.Server
	if err := db.First(&updated, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.PublicIP != newV4 {
		t.Fatalf("server.PublicIP = %q, want %q", updated.PublicIP, newV4)
	}
}

func TestInstallSingboxPersistsVersionBeforePushingConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	srv := model.Server{
		Name:       "edge-install",
		AgentToken: "tok-install-ver",
		ConfigMode: model.ConfigModeManaged,
	}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Inbound{
		ServerID: srv.ID, Tag: "socks-in", Type: model.InboundSocks, ListenPort: 1080, Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	a := &App{
		db:               db,
		hub:              NewHub(db),
		serverOperations: newKeyedMutex[uint](),
	}
	a.orch = NewOrchestrator(db, a.hub)

	seenVersionCh := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			var env protocol.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			switch env.Type {
			case protocol.CmdInstallSingbox:
				data, _ := json.Marshal(protocol.InstallSingboxData{SingboxVersion: "1.14.4"})
				res, _ := protocol.NewEnvelope(protocol.EvtCommandResult, env.ID, protocol.CommandResultEvt{
					ID: env.ID, OK: true, Output: "installed", Data: data,
				})
				_ = c.WriteJSON(res)
			case protocol.CmdApplyConfig:
				var row model.Server
				_ = db.First(&row, srv.ID).Error
				select {
				case seenVersionCh <- row.SingboxVersion:
				default:
				}
				res, _ := protocol.NewEnvelope(protocol.EvtCommandResult, env.ID, protocol.CommandResultEvt{
					ID: env.ID, OK: true,
				})
				_ = c.WriteJSON(res)
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ac := a.hub.registerWithEvent(srv.ID, "", wsConn, &protocol.RegisterEvt{AgentVersion: "1.0.17"})
	go ac.serve()
	defer func() {
		ac.close()
		<-ac.stopped
	}()

	router := gin.New()
	router.POST("/api/admin/servers/:id/install-singbox", a.installSingbox)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/servers/%d/install-singbox", srv.ID), bytes.NewBufferString(`{"channel":"stable","method":"script"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("installSingbox status = %d body=%s", w.Code, w.Body.String())
	}
	var seenVersionAtPush string
	select {
	case seenVersionAtPush = <-seenVersionCh:
	case <-time.After(3 * time.Second):
		t.Fatal("CmdApplyConfig was not pushed after installSingbox")
	}
	if seenVersionAtPush != "1.14.4" {
		t.Fatalf("singbox_version at PushConfigIfManaged time = %q, want 1.14.4", seenVersionAtPush)
	}
}
