package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestHubTrafficFutureBucketPublishesLiveAndSendsClockSyncAck(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "fast-clock", AgentToken: "fast-clock-token"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	ac := &agentConn{
		serverID: srv.ID,
		send:     make(chan protocol.Envelope, 4),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
		pending:  make(map[string]chan protocol.CommandResultEvt),
	}
	hub.mu.Lock()
	hub.conns[srv.ID] = ac
	hub.mu.Unlock()

	sub, _ := hub.live.subscribe(srv.ID)
	defer hub.live.unsubscribe(srv.ID, sub)

	futureBucket := time.Now().UTC().Add(15 * time.Minute).Truncate(time.Minute)
	env, err := protocol.NewEnvelope(protocol.EvtTraffic, "", protocol.TrafficEvt{
		Traffic: &protocol.TrafficSnapshot{
			AccountingVersion: 2,
			UploadRate:        456,
			DownloadRate:      789,
			SampledAt:         time.Now().Unix(),
			Report: &protocol.TrafficReport{
				Session:  "7795c26e-d04c-4f36-aa76-fc891179f361",
				Sequence: 1,
				Buckets: []protocol.TrafficTimeBucket{{
					Time:     futureBucket.Unix(),
					Upload:   1024,
					Download: 2048,
				}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	before := time.Now().UTC().Unix()
	hub.handleEvent(ac, env)
	after := time.Now().UTC().Unix()

	select {
	case ackEnv := <-ac.send:
		if ackEnv.Type != protocol.EvtTrafficAck {
			t.Fatalf("ack type = %q, want %q", ackEnv.Type, protocol.EvtTrafficAck)
		}
		var ack protocol.TrafficAck
		if err := ackEnv.Decode(&ack); err != nil {
			t.Fatal(err)
		}
		if ack.Session != "7795c26e-d04c-4f36-aa76-fc891179f361" || ack.Sequence != 0 {
			t.Fatalf("unexpected clock-sync ack: %+v", ack)
		}
		if ack.ServerTime < before || ack.ServerTime > after {
			t.Fatalf("ack ServerTime = %d, want between %d and %d", ack.ServerTime, before, after)
		}
	default:
		t.Fatal("missing clock-sync traffic_ack for future bucket")
	}

	var sawTraffic, sawStatus bool
	for range 2 {
		select {
		case evt := <-sub.ch:
			if evt.Kind == "traffic" {
				sawTraffic = true
			} else if evt.Kind == "server_status" {
				st := evt.Data.(serverLiveStatus)
				if st.TrafficUploadRate != nil && *st.TrafficUploadRate == 456 &&
					st.TrafficDownloadRate != nil && *st.TrafficDownloadRate == 789 {
					sawStatus = true
				}
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for live events on future-clock traffic report")
		}
	}
	if !sawTraffic || !sawStatus {
		t.Fatalf("live events not published: sawTraffic=%v sawStatus=%v", sawTraffic, sawStatus)
	}

	var cursorCount int64
	if err := db.Model(&model.TrafficReportCursor{}).Count(&cursorCount).Error; err != nil {
		t.Fatal(err)
	}
	if cursorCount != 0 {
		t.Fatalf("future bucket unexpectedly persisted cursor rows=%d", cursorCount)
	}
}

func TestHubTrafficValidAckIncludesServerTime(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	hub := NewHub(f.db)
	ac := &agentConn{
		serverID: f.server.ID,
		send:     make(chan protocol.Envelope, 4),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
		pending:  make(map[string]chan protocol.CommandResultEvt),
	}
	hub.mu.Lock()
	hub.conns[f.server.ID] = ac
	hub.mu.Unlock()

	bucketAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Minute)
	env, err := protocol.NewEnvelope(protocol.EvtTraffic, "", protocol.TrafficEvt{
		Traffic: &protocol.TrafficSnapshot{
			AccountingVersion: 2,
			SampledAt:         bucketAt.Unix(),
			Report: &protocol.TrafficReport{
				Session:  "8895c26e-d04c-4f36-aa76-fc891179f362",
				Sequence: 1,
				Buckets: []protocol.TrafficTimeBucket{{
					Time:   bucketAt.Unix(),
					Upload: 512,
					Ports:  []protocol.PortTrafficSnapshot{{Inbound: f.multi.Tag, Upload: 512}},
					Users:  []protocol.UserTrafficSnapshot{{Inbound: f.multi.Tag, User: f.alice.Email, Upload: 512}},
				}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hub.handleEvent(ac, env)

	ackEnv := <-ac.send
	var ack protocol.TrafficAck
	if err := ackEnv.Decode(&ack); err != nil {
		t.Fatal(err)
	}
	if ack.Sequence != 1 || ack.ServerTime <= 0 {
		t.Fatalf("valid ack = %+v, want seq=1 and server_time>0", ack)
	}
	if usage := userRows(t, f.db)[f.alice.ID]; len(usage) == 0 || usage[0] != 512 {
		t.Fatalf("per-user rows not saved without nested savepoint: %+v", usage)
	}
}

func TestPruneTrafficRecordsCleansExpiredCursors(t *testing.T) {
	db := testDB(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	stale := model.TrafficReportCursor{
		ServerID: 1, Session: "stale-session", Sequence: 5, UpdatedAt: now.Add(-15 * 24 * time.Hour),
	}
	recent := model.TrafficReportCursor{
		ServerID: 1, Session: "recent-session", Sequence: 9, UpdatedAt: now.Add(-2 * 24 * time.Hour),
	}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&recent).Error; err != nil {
		t.Fatal(err)
	}
	// Ensure explicit old UpdatedAt survives GORM autoUpdateTime hook.
	if err := db.Model(&model.TrafficReportCursor{}).Where("session = ?", stale.Session).
		UpdateColumn("updated_at", stale.UpdatedAt).Error; err != nil {
		t.Fatal(err)
	}
	if err := pruneTrafficRecords(db, now); err != nil {
		t.Fatal(err)
	}
	var remaining []model.TrafficReportCursor
	if err := db.Find(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].Session != recent.Session {
		t.Fatalf("remaining cursors = %+v, want only %q", remaining, recent.Session)
	}
}

func TestForwardUserDisplayNameSanitizesControlCharsAndBrackets(t *testing.T) {
	db := testDB(t)
	user := model.User{
		Email:      "evil\n[admin]",
		Role:       model.RoleUser,
		Enabled:    true,
		ProxyToken: "proxy-tok",
		SubToken:   "sub-tok",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := forwardTrafficName(&user, 7)
	resolver := &logUserResolver{db: db, serverID: 1}
	got := resolver.forwardUserDisplayName(context.Background(), token)
	if strings.ContainsAny(got, "\r\n[]") {
		t.Fatalf("unsanitized forward display name: %q", got)
	}
	if !strings.Contains(got, `\u005badmin\u005d`) || !strings.HasSuffix(got, " → 转发") {
		t.Fatalf("unexpected forward display name: %q", got)
	}
}

func TestStopLogSessionsAndHubClose(t *testing.T) {
	db := testDB(t)
	hub := NewHub(db)
	ac := &agentConn{
		serverID: 99,
		send:     make(chan protocol.Envelope, 1),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
		pending:  make(map[string]chan protocol.CommandResultEvt),
	}
	hub.mu.Lock()
	hub.conns[99] = ac
	hub.mu.Unlock()
	hub.Close()
	select {
	case <-ac.done:
	default:
		t.Fatal("Hub.Close did not close agent connection done channel")
	}

	a := &App{
		logSessions: map[uint]map[string]logSessionLease{
			1: {
				"sess-1": {Timer: time.NewTimer(time.Hour)},
			},
		},
	}
	a.stopLogSessions()
	a.logSessionsMu.Lock()
	remaining := len(a.logSessions)
	a.logSessionsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("stopLogSessions left %d servers in map", remaining)
	}
}

func TestHandleTrafficSSEBatchesEventsAndStopsOnRevocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	user := model.User{Email: "sse-admin", Role: model.RoleAdmin, Enabled: true, SubToken: "sse-sub", TokenVersion: 1}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	srv := model.Server{Name: "sse-node", AgentToken: "sse-token"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{
		db:   db,
		cfg:  config.PanelConfig{},
		auth: NewAuth("0123456789abcdef0123456789abcdef", db),
		hub:  NewHub(db),
	}
	tok, err := a.auth.Issue(&user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.GET("/servers/:id/traffic/live", a.auth.Middleware(), a.handleTrafficSSE)
	ts := httptest.NewServer(router)
	defer ts.Close()

	// Seed multiple queued traffic points before connecting or via liveHub after connect.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/servers/1/traffic/live", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	// Publish one backlog item first so the SSE handler enters streaming cleanly.
	a.hub.live.publishTraffic(srv.ID, &protocol.TrafficSnapshot{UploadRate: 11, DownloadRate: 22})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d", resp.StatusCode)
	}
	// Revoke token and push event to trigger immediate stream close on batch auth check.
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("token_version", 2).Error; err != nil {
		t.Fatal(err)
	}
	a.hub.live.publishTraffic(srv.ID, &protocol.TrafficSnapshot{UploadRate: 33, DownloadRate: 44})

	buf := make([]byte, 4096)
	var body strings.Builder
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			body.Write(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
	if !strings.Contains(body.String(), `"kind":"traffic"`) || !strings.Contains(body.String(), `"upload_rate":11`) {
		t.Fatalf("expected initial backlog traffic event in SSE stream: %q", body.String())
	}
	if strings.Contains(body.String(), `"upload_rate":33`) {
		t.Fatalf("revoked session still received post-revocation event: %q", body.String())
	}
}

func TestAgentInstallScriptHardensOpenRCCgroupAndCmdlineCheck(t *testing.T) {
	if !strings.Contains(agentInstallScript, `rc_cgroup_cleanup="NO"`) {
		t.Fatal("agent install script OpenRC unit missing rc_cgroup_cleanup=NO")
	}
	if !strings.Contains(agentInstallScript, `/proc/$UPGRADE_PID/cmdline`) {
		t.Fatal("agent install script missing /proc cmdline guard before killing upgrade PID")
	}
}
