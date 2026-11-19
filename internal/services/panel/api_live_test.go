package panel

import (
	"bufio"
	"context"
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

func TestAggregateTrafficRecordsGroupsInDatabase(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "traffic", AgentToken: "traffic-aggregate-token"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{ServerID: server.ID, Tag: "in", Type: model.InboundVLESS, ListenPort: 443}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Hour)
	rows := []model.TrafficRecord{
		{ServerID: server.ID, Bucket: start.Add(time.Minute), Upload: 10, Download: 20, UploadRate: 3},
		{ServerID: server.ID, Bucket: start.Add(2 * time.Minute), Upload: 30, Download: 40, UploadRate: 9},
		{ServerID: server.ID, InboundID: inbound.ID, Bucket: start.Add(time.Minute), Upload: 5, Download: 7},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	aggregated, err := aggregateTrafficRecords(db, server.ID, start, start.Add(time.Hour), 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregated) != 2 {
		t.Fatalf("aggregated rows = %d, want 2: %+v", len(aggregated), aggregated)
	}
	points, ports := buildAggregatedTrafficPoints(aggregated, start, start.Add(time.Hour), 30*time.Minute)
	if points[0].Upload != 40 || points[0].Download != 60 || points[0].UploadRate != 9 {
		t.Fatalf("total point = %+v", points[0])
	}
	if ports[inbound.ID][0].Upload != 5 || ports[inbound.ID][0].Download != 7 {
		t.Fatalf("inbound point = %+v", ports[inbound.ID][0])
	}
}

func TestHubPublishesServerStatusToGlobalAndPerServerSubscribers(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "edge-1", AgentToken: "live-status-token"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	ac := &agentConn{
		serverID: srv.ID,
		send:     make(chan protocol.Envelope, 4),
		done:     make(chan struct{}),
		pending:  make(map[string]chan protocol.CommandResultEvt),
	}
	hub.conns[srv.ID] = ac

	globalSub := hub.live.subscribeAll()
	defer hub.live.unsubscribeAll(globalSub)
	serverSub, _ := hub.live.subscribe(srv.ID)
	defer hub.live.unsubscribe(srv.ID, serverSub)
	otherSub, _ := hub.live.subscribe(srv.ID + 99)
	defer hub.live.unsubscribe(srv.ID+99, otherSub)

	// 1. Traffic event publishes rates without overwriting load/memory.
	trafficEnv, err := protocol.NewEnvelope(protocol.EvtTraffic, "", protocol.TrafficEvt{
		Traffic: &protocol.TrafficSnapshot{
			AccountingVersion: 2,
			UploadRate:        1234,
			DownloadRate:      5678,
			SampledAt:         time.Now().Unix(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hub.handleEvent(ac, trafficEnv)

	gotGlobal := <-globalSub.ch
	if gotGlobal.Kind != "server_status" {
		t.Fatalf("global kind = %q, want server_status", gotGlobal.Kind)
	}
	status := gotGlobal.Data.(serverLiveStatus)
	if status.ServerID != srv.ID || status.TrafficUploadRate == nil || *status.TrafficUploadRate != 1234 || status.TrafficDownloadRate == nil || *status.TrafficDownloadRate != 5678 {
		t.Fatalf("unexpected traffic status: %+v", status)
	}
	if status.Load1 != nil || status.MemUsed != nil {
		t.Fatalf("traffic status must not carry load/memory fields: %+v", status)
	}

	// Per-server subscriber receives both traffic (for chart) and server_status.
	var sawTraffic, sawStatus bool
	for range 2 {
		evt := <-serverSub.ch
		switch evt.Kind {
		case "traffic":
			sawTraffic = true
		case "server_status":
			sawStatus = true
		}
	}
	if !sawTraffic || !sawStatus {
		t.Fatalf("serverSub sawTraffic=%v sawStatus=%v, want both true", sawTraffic, sawStatus)
	}

	// 2. Heartbeat publishes load, memory, and service state.
	hub.onHeartbeat(ac, protocol.HeartbeatEvt{
		Load1:         0.42,
		MemUsed:       256 << 20,
		MemTotal:      1024 << 20,
		Uptime:        3600,
		SingboxActive: true,
		Traffic: &protocol.TrafficSnapshot{
			AccountingVersion: 2,
			UploadRate:        100,
			DownloadRate:      200,
			SampledAt:         time.Now().Unix(),
		},
	})
	hbEvt := <-globalSub.ch
	hbStatus := hbEvt.Data.(serverLiveStatus)
	if hbStatus.Load1 == nil || *hbStatus.Load1 != 0.42 || hbStatus.MemUsed == nil || *hbStatus.MemUsed != 256<<20 || hbStatus.MemTotal == nil || *hbStatus.MemTotal != 1024<<20 {
		t.Fatalf("unexpected heartbeat status: %+v", hbStatus)
	}
	<-serverSub.ch // drain heartbeat server_status

	// 3. Unregister publishes offline and zero rates.
	hub.unregister(ac)
	offEvt := <-globalSub.ch
	offStatus := offEvt.Data.(serverLiveStatus)
	if offStatus.Online == nil || *offStatus.Online || offStatus.TrafficDownloadRate == nil || *offStatus.TrafficDownloadRate != 0 {
		t.Fatalf("unexpected offline status: %+v", offStatus)
	}

	select {
	case leaked := <-otherSub.ch:
		t.Fatalf("other server subscriber received unrelated event: %+v", leaked)
	default:
	}
}

func TestHandleServersLiveSSEStreamsServerStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	u := model.User{Email: "servers-live-admin", Role: model.RoleAdmin, Enabled: true, SubToken: "servers-live-sub"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, auth: NewAuth("0123456789abcdef0123456789abcdef", db), hub: NewHub(db)}
	router := gin.New()
	router.GET("/api/admin/servers/live", a.auth.Middleware(), AdminOnly(), a.handleServersLiveSSE)
	httpServer := httptest.NewServer(router)
	defer httpServer.Close()

	token, err := a.auth.Issue(&u, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/admin/servers/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	firstLine, err := reader.ReadString('\n')
	if err != nil || firstLine != ": connected\n" {
		t.Fatalf("initial comment = %q, err = %v", firstLine, err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read blank separator: %v", err)
	}

	online := true
	down := uint64(4096)
	a.hub.live.publishServerStatus(serverLiveStatus{
		ServerID:            42,
		Online:              &online,
		TrafficDownloadRate: &down,
	})

	dataLine, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(dataLine, "data: ") {
		t.Fatalf("data line = %q, err = %v", dataLine, err)
	}
	var payload struct {
		Kind string           `json:"kind"`
		Data serverLiveStatus `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(dataLine, "data: ")), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Kind != "server_status" || payload.Data.ServerID != 42 || payload.Data.TrafficDownloadRate == nil || *payload.Data.TrafficDownloadRate != 4096 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}
