package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func resetSubscriptionForTest(t *testing.T, app *App, userID uint) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/reset-sub", nil)
	c.Set("uid", userID)
	app.handleResetSub(c)
	return rec
}

func waitForSubscriptionResetPush(t *testing.T, orch *Orchestrator) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		orch.asyncMu.Lock()
		idle := len(orch.asyncRunning) == 0
		orch.asyncMu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("subscription reset config push did not finish")
}

func TestSubscriptionResetRotatesOnlyCurrentUserCredentialsAndPushesConfig(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	if err := f.db.Model(&f.server).Update("address", "node.example.com").Error; err != nil {
		t.Fatal(err)
	}
	for i, typ := range []string{"vmess", "trojan", "hysteria2", "tuic", "anytls", "shadowsocks"} {
		settings, err := json.Marshal(singbox.InboundSettings{
			MultiUser: true, Method: "2022-blake3-aes-128-gcm", SSServerPSK: "dGVzdC1zZXJ2ZXIta2V5IQ==",
			TLS: singbox.TLSSettings{Enabled: true, CertificatePath: "/cert.pem", KeyPath: "/key.pem"},
		})
		if err != nil {
			t.Fatal(err)
		}
		mustCreate(t, f.db, &model.Inbound{
			ServerID: f.server.ID, Tag: typ, Type: model.InboundType(typ),
			ListenPort: 10000 + i, Enabled: true, Settings: settings,
		})
	}
	mustCreate(t, f.db, &model.CustomNode{
		Name: "external", Protocol: "vless", Address: "external.example.com", Port: 443,
		Enabled: true, AllUsers: true, Params: model.JSONText(`{"uuid":"external-uuid"}`),
	})
	hub := NewHub(f.db)
	orch := NewOrchestrator(f.db, hub)
	cfg := config.Default()
	cfg.BaseURL = "https://panel.example.com"
	app := &App{db: f.db, cfg: cfg, orch: orch, auth: NewAuth("0123456789abcdef0123456789abcdef", f.db)}
	before, err := app.gatherNodesWithError(&f.alice)
	if err != nil {
		t.Fatal(err)
	}
	otherBefore, err := app.gatherNodesWithError(&f.namedUser)
	if err != nil {
		t.Fatal(err)
	}
	// Record the old traffic identity before it is rotated.
	if _, err := orch.BuildServerConfig(&f.server); err != nil {
		t.Fatal(err)
	}
	conn := hub.register(f.server.ID, "", nil)
	defer conn.close()
	token, err := app.auth.Issue(&f.alice, sessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/api/user/reset-sub", app.auth.Middleware(), app.handleResetSub)
	router.GET("/api/user/nodes", app.auth.Middleware(), app.handleUserNodes)
	router.GET("/sub/:token", app.handleSubscription)
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	old := f.alice
	rec := request(http.MethodPost, "/api/user/reset-sub?user_id="+jsonNumber(f.namedUser.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	if err := f.db.First(&f.alice, f.alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if f.alice.SubToken == old.SubToken || f.alice.ProxyToken == old.ProxyToken || len(f.alice.ProxyToken) != 64 {
		t.Fatal("subscription and proxy seeds were not both rotated")
	}
	if f.alice.Password != old.Password || f.alice.TokenVersion != old.TokenVersion || !reflect.DeepEqual(f.alice.ServerIDs, old.ServerIDs) {
		t.Fatal("reset changed the login password, session version or grants")
	}
	if strings.Contains(rec.Body.String(), f.alice.ProxyToken) || strings.Contains(rec.Body.String(), "proxy_token") ||
		!strings.Contains(rec.Body.String(), app.subURL(f.alice.SubToken)) {
		t.Fatal("reset response leaked the proxy seed or omitted the new subscription URL")
	}
	var other model.User
	if err := f.db.First(&other, f.namedUser.ID).Error; err != nil {
		t.Fatal(err)
	}
	otherAfter, err := app.gatherNodesWithError(&other)
	if err != nil || !reflect.DeepEqual(otherBefore, otherAfter) || other.SubToken != f.namedUser.SubToken || other.ProxyToken != f.namedUser.ProxyToken {
		t.Fatal("reset changed another user's subscription or node credentials")
	}
	after, err := app.gatherNodesWithError(&f.alice)
	if err != nil || len(after) != len(before) || len(after) != 9 {
		t.Fatalf("nodes after reset: %d, err=%v", len(after), err)
	}
	for i, n := range after {
		if n.settings.UseMultiUser(n.typ) {
			if n.user.UUID == before[i].user.UUID || n.user.Password == before[i].user.Password {
				t.Fatalf("%s credentials were not rotated", n.tag)
			}
		} else if !reflect.DeepEqual(n, before[i]) {
			t.Fatalf("shared or external node %q changed", n.tag)
		}
	}
	select {
	case envelope := <-conn.send:
		var command protocol.ApplyConfigCmd
		if envelope.Type != protocol.CmdApplyConfig || json.Unmarshal(envelope.Payload, &command) != nil {
			t.Fatal("reset did not send a valid config apply command")
		}
		want, err := orch.BuildServerConfig(&f.server)
		var compact bytes.Buffer
		if err != nil || json.Compact(&compact, want) != nil || !bytes.Equal(command.Config, compact.Bytes()) {
			t.Fatalf("pushed config differs from the rotated desired config: %v", err)
		}
		if bytes.Contains(command.Config, []byte(proxyIdentity(&old, f.multi.ID).UUID)) ||
			!bytes.Contains(command.Config, []byte(proxyIdentity(&f.alice, f.multi.ID).UUID)) ||
			!bytes.Contains(command.Config, []byte(proxyIdentity(&other, f.multi.ID).UUID)) {
			t.Fatal("pushed config retained the old UUID or lost a current user's UUID")
		}
		conn.mu.Lock()
		result := conn.pending[envelope.ID]
		conn.mu.Unlock()
		result <- protocol.CommandResultEvt{ID: envelope.ID, OK: true}
	case <-time.After(3 * time.Second):
		t.Fatal("reset did not schedule a push to the assigned server")
	}
	waitForSubscriptionResetPush(t, orch)
	if got := request(http.MethodGet, "/sub/"+old.SubToken); got.Code != http.StatusNotFound {
		t.Fatalf("old URL is still usable: %d", got.Code)
	}
	if got := request(http.MethodGet, "/sub/"+f.alice.SubToken+"?target=singbox"); got.Code != http.StatusOK ||
		!strings.Contains(got.Body.String(), proxyIdentity(&f.alice, f.multi.ID).UUID) {
		t.Fatalf("new subscription is missing rotated credentials: %d", got.Code)
	}
	if got := request(http.MethodGet, "/api/user/nodes"); got.Code != http.StatusOK ||
		!strings.Contains(got.Body.String(), proxyIdentity(&f.alice, f.multi.ID).UUID) {
		t.Fatalf("original login session cannot read refreshed node details: %d", got.Code)
	}
}

func TestSubscriptionResetPreservesDelayedTrafficAndHistoricalLogOwnership(t *testing.T) {
	f := newLogUserFixture(t)
	app := &App{db: f.db}
	oldNames := []string{f.alice.Email, proxyTrafficName(&f.alice)}
	stale := model.User{ID: f.alice.ID, ProxyToken: "deleted-account-seed"}
	staleName := proxyTrafficName(&stale)
	mustCreate(t, f.db, &model.UserTrafficIdentity{
		ServerID: f.server.ID, InboundID: f.multi.ID, Name: staleName,
		UserID: f.alice.ID, UserKey: staleName,
	})
	for range 2 {
		if rec := resetSubscriptionForTest(t, app, f.alice.ID); rec.Code != http.StatusOK {
			t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
		}
		if err := f.db.First(&f.alice, f.alice.ID).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := proxyUsersForInbound(f.db, &f.multi, singbox.InboundSettings{MultiUser: true}); err != nil {
			t.Fatal(err)
		}
		oldNames = append(oldNames, proxyTrafficName(&f.alice))
	}
	var snapshots []protocol.UserTrafficSnapshot
	for _, name := range oldNames {
		snapshots = append(snapshots, protocol.UserTrafficSnapshot{Inbound: f.multi.Tag, User: name, Upload: 1, Download: 2})
	}
	snapshots = append(snapshots, protocol.UserTrafficSnapshot{Inbound: f.multi.Tag, User: staleName, Upload: 100})
	f.report(t, newTrafficWriter(f.db), snapshots)
	if got := userRows(t, f.db)[f.alice.ID]; got != [2]uint64{4, 8} {
		t.Fatalf("traffic was lost or inherited from a previous account: %v", got)
	}
	r := logUserResolver{db: f.db, serverID: f.server.ID}
	for _, name := range oldNames[1:] {
		if got := r.resolve(context.Background(), userLogLine(name)); got != userLogLine(f.alice.Email) {
			t.Fatal("historical log name no longer resolves after credential rotation")
		}
	}
	if got := r.resolve(context.Background(), userLogLine(staleName)); got != userLogLine(staleName) {
		t.Fatal("credential reset relabelled a deleted account's logs")
	}
}

func TestSubscriptionResetRollsBackBothSeedsOnFailureAndRejectsMissingUser(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	app := &App{db: f.db}
	if err := f.db.Callback().Update().Before("gorm:update").Register("test:reject_identity_rotation", func(tx *gorm.DB) {
		if tx.Statement.Table == "user_traffic_identities" {
			tx.AddError(errors.New("identity update failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if rec := resetSubscriptionForTest(t, app, f.alice.ID); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed reset returned %d", rec.Code)
	}
	var stored model.User
	if err := f.db.First(&stored, f.alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.SubToken != f.alice.SubToken || stored.ProxyToken != f.alice.ProxyToken {
		t.Fatal("failed reset left a partially rotated account")
	}
	if rec := resetSubscriptionForTest(t, app, f.alice.ID+100); rec.Code != http.StatusNotFound {
		t.Fatalf("missing user reset returned %d", rec.Code)
	}
}

func TestSubscriptionResetOfflineConfigConvergesAfterReconnect(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	hub := NewHub(f.db)
	orch := NewOrchestrator(f.db, hub)
	app := &App{db: f.db, orch: orch}
	oldUUID := proxyIdentity(&f.alice, f.multi.ID).UUID
	if rec := resetSubscriptionForTest(t, app, f.alice.ID); rec.Code != http.StatusOK {
		t.Fatalf("offline reset: %d", rec.Code)
	}
	waitForSubscriptionResetPush(t, orch)
	if err := f.db.First(&f.alice, f.alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	conn := hub.register(f.server.ID, "", nil)
	defer conn.close()
	// The reconnect hook schedules this same reconciliation of saved state.
	orch.PushConfigIfManaged(f.server.ID)
	select {
	case envelope := <-conn.send:
		if envelope.Type != protocol.CmdApplyConfig || bytes.Contains(envelope.Payload, []byte(oldUUID)) ||
			!bytes.Contains(envelope.Payload, []byte(proxyIdentity(&f.alice, f.multi.ID).UUID)) {
			t.Fatal("reconnect did not provision the saved rotated credentials")
		}
		conn.mu.Lock()
		result := conn.pending[envelope.ID]
		conn.mu.Unlock()
		result <- protocol.CommandResultEvt{ID: envelope.ID, OK: true}
	case <-time.After(3 * time.Second):
		t.Fatal("reconnect did not schedule config reconciliation")
	}
	waitForSubscriptionResetPush(t, orch)
}

func TestSubscriptionResetPushesEveryAssignedServerButNoUnassignedServer(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	second := model.Server{Name: "second", AgentToken: "second-token", ConfigMode: model.ConfigModeManaged}
	foreign := model.Server{Name: "foreign", AgentToken: "foreign-token", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, f.db, &second)
	mustCreate(t, f.db, &foreign)
	inbound := model.Inbound{
		ServerID: second.ID, Tag: "second-vless", Type: model.InboundVLESS, ListenPort: 443,
		Enabled: true, Settings: model.JSONText(`{"multi_user":true}`),
	}
	mustCreate(t, f.db, &inbound)
	f.alice.ServerIDs = []uint{f.server.ID, second.ID}
	f.alice.InboundIDs = []uint{f.multi.ID, inbound.ID}
	if err := f.db.Save(&f.alice).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(f.db)
	orch := NewOrchestrator(f.db, hub)
	firstConn := hub.register(f.server.ID, "", nil)
	secondConn := hub.register(second.ID, "", nil)
	foreignConn := hub.register(foreign.ID, "", nil)
	defer firstConn.close()
	defer secondConn.close()
	defer foreignConn.close()
	if rec := resetSubscriptionForTest(t, &App{db: f.db, orch: orch}, f.alice.ID); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d", rec.Code)
	}
	if err := f.db.First(&f.alice, f.alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	for i, conn := range []*agentConn{firstConn, secondConn} {
		inboundID := []uint{f.multi.ID, inbound.ID}[i]
		select {
		case envelope := <-conn.send:
			if envelope.Type != protocol.CmdApplyConfig || !bytes.Contains(envelope.Payload, []byte(proxyIdentity(&f.alice, inboundID).UUID)) {
				t.Fatalf("assigned server %d did not receive the new UUID", conn.serverID)
			}
			conn.mu.Lock()
			result := conn.pending[envelope.ID]
			conn.mu.Unlock()
			result <- protocol.CommandResultEvt{ID: envelope.ID, OK: true}
		case <-time.After(3 * time.Second):
			t.Fatalf("assigned server %d was not pushed", conn.serverID)
		}
	}
	waitForSubscriptionResetPush(t, orch)
	if len(foreignConn.send) != 0 {
		t.Fatal("reset pushed an unassigned server")
	}
}
