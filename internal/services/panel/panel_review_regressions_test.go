package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestConfigOverlayPreservesActiveForwardRulesWhenAbsentFromImport(t *testing.T) {
	fixture := newForwardFixture(t)
	forward := fixture.create(t)
	// Start from the server's real multi-user config so the inbound stays multi-user,
	// then strip the derived forward rule and private outbound from the raw JSON.
	full, err := fixture.app.orch.BuildServerConfig(&fixture.server)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(full, &doc); err != nil {
		t.Fatal(err)
	}
	doc["outbounds"] = []any{map[string]any{"type": "direct", "tag": "direct"}}
	doc["route"] = map[string]any{
		"rules": []any{map[string]any{"outbound": "direct"}},
		"final": "direct",
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.applyImport(&fixture.server, parsed, raw); err != nil {
		t.Fatalf("applyImport failed: %v", err)
	}
	var srv model.Server
	if err := fixture.db.First(&srv, fixture.server.ID).Error; err != nil {
		t.Fatal(err)
	}
	rendered, err := fixture.app.orch.buildServerConfig(&srv, nil)
	if err != nil {
		t.Fatalf("buildServerConfig after overlay import failed: %v", err)
	}
	var cfg struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
		Route struct {
			Rules []struct {
				AuthUser []string `json:"auth_user"`
				Outbound string   `json:"outbound"`
			} `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(rendered, &cfg); err != nil {
		t.Fatalf("unmarshal rendered config: %v", err)
	}
	foundOutbound := false
	for _, ob := range cfg.Outbounds {
		if ob.Tag == fixture.outbound.Tag {
			foundOutbound = true
		}
	}
	if !foundOutbound {
		t.Fatalf("active forward private outbound %q was dropped by overlay: %s", fixture.outbound.Tag, rendered)
	}
	wantAuth := forwardTrafficName(&fixture.user, forward.ID)
	foundRule := false
	for _, r := range cfg.Route.Rules {
		if len(r.AuthUser) == 1 && r.AuthUser[0] == wantAuth && r.Outbound == fixture.outbound.Tag {
			foundRule = true
		}
	}
	if !foundRule {
		t.Fatalf("active forward rule %q -> %q was dropped by overlay: %s", wantAuth, fixture.outbound.Tag, rendered)
	}
}

func TestValidateImportedPrivateOutboundsRejectsUnmanagedTagCollision(t *testing.T) {
	fixture := newForwardFixture(t)
	raw := []byte(fmt.Sprintf(`{
		"outbounds":[
			{"type":"direct","tag":"direct"},
			{"type":"selector","tag":%q,"outbounds":["direct"]}
		]
	}`, fixture.outbound.Tag))
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateImportedPrivateOutbounds(fixture.db, parsed.Outbounds, raw); err == nil {
		t.Fatalf("expected unmanaged selector outbound colliding with private tag %q to be rejected", fixture.outbound.Tag)
	}
}

func TestUserTrafficSeriesStepSecondsAndSingleInvalidRangeResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	user := model.User{Email: "alice", Role: model.RoleUser, Enabled: true, SubToken: "tok", ProxyToken: "ptok"}
	mustCreate(t, db, &user)
	cfg := config.PanelConfig{Timezone: "Asia/Shanghai"}
	app := &App{cfg: cfg, db: db}
	router := gin.New()
	router.GET("/admin/users/:id/traffic", app.userTraffic)

	rec := serveJSONRequest(t, router, http.MethodGet, fmt.Sprintf("/admin/users/%d/traffic?range=bad", user.ID), "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid range status = %d, want 400", rec.Code)
	}
	var errPayload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &errPayload); err != nil {
		t.Fatalf("invalid range body is not single valid JSON (%v): %q", err, rec.Body.String())
	}

	okRec := serveJSONRequest(t, router, http.MethodGet, fmt.Sprintf("/admin/users/%d/traffic?range=7d", user.ID), "")
	if okRec.Code != http.StatusOK {
		t.Fatalf("7d range status = %d, want 200: %s", okRec.Code, okRec.Body.String())
	}
	var series userTrafficSeries
	if err := json.Unmarshal(okRec.Body.Bytes(), &series); err != nil {
		t.Fatalf("decode 7d series: %v", err)
	}
	if series.StepSeconds != 86400 {
		t.Fatalf("7d StepSeconds = %d, want 86400", series.StepSeconds)
	}
}

func TestDeleteSharedRuleSetSafeWithoutOrchestratorAndBlocksReferenced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	srv := model.Server{Name: "s1", Address: "1.2.3.4", AgentToken: "t1", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &srv)
	rs := model.RuleSet{Tag: "geoip-cn", Type: "remote", URL: "https://example.com/cn.srs", Format: "binary"}
	mustCreate(t, db, &rs)
	mustCreate(t, db, &model.ServerRuleSet{ServerID: srv.ID, RuleSetID: rs.ID})
	rule := model.RouteRule{ServerID: srv.ID, Outbound: "direct", Match: model.JSONText(`{"rule_set":["geoip-cn"]}`), Enabled: true}
	mustCreate(t, db, &rule)

	app := &App{db: db, orch: nil}
	router := gin.New()
	router.DELETE("/admin/rulesets/:rulesetID", app.deleteSharedRuleSet)

	rec := serveJSONRequest(t, router, http.MethodDelete, fmt.Sprintf("/admin/rulesets/%d", rs.ID), "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("referenced ruleset delete status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if err := db.Delete(&rule).Error; err != nil {
		t.Fatal(err)
	}
	rec = serveJSONRequest(t, router, http.MethodDelete, fmt.Sprintf("/admin/rulesets/%d", rs.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unreferenced ruleset delete with nil orch status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestCustomNodeUpdateBlocksParamsOnSubscriptionManagedAndPushesConsumers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	hub := NewHub(db)
	orch := NewOrchestrator(db, hub)
	app := &App{db: db, hub: hub, orch: orch}
	orch.app = app
	t.Cleanup(func() { waitForSubscriptionResetPush(t, orch) })

	srv := model.Server{Name: "s1", Address: "1.2.3.4", AgentToken: "tok", ConfigMode: model.ConfigModeManaged, ConfigInitialized: true}
	mustCreate(t, db, &srv)
	sub := model.CustomNodeSubscription{Name: "sub1", URL: "https://example.com/sub"}
	mustCreate(t, db, &sub)
	subID := sub.ID
	managedNode := model.CustomNode{
		Name: "managed-vless", Protocol: "vless", Address: "old.example", Port: 443,
		Params: model.JSONText(`{"uuid":"11111111-2222-4333-8444-555555555555"}`),
		SubscriptionID: &subID, SubscriptionKey: "k1",
	}
	mustCreate(t, db, &managedNode)

	router := gin.New()
	router.PUT("/admin/custom-nodes/:id", app.updateCustomNode)

	// Attempting to mutate Params on a subscription-managed custom node must return 409.
	rec := serveJSONRequest(t, router, http.MethodPut, fmt.Sprintf("/admin/custom-nodes/%d", managedNode.ID), `{
		"name":"managed-vless","protocol":"vless","address":"old.example","port":443,
		"params":{"uuid":"22222222-2222-4333-8444-555555555555"}
	}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("mutating params on subscription-managed node = %d, want 409: %s", rec.Code, rec.Body.String())
	}

	// Updating a manual custom node referenced by an outbound pushes to the referencing server.
	manualNode := model.CustomNode{
		Name: "manual-ss", Protocol: "shadowsocks", Address: "manual.example", Port: 8388,
		Params: model.JSONText(`{"method":"aes-128-gcm","password":"secret"}`),
	}
	mustCreate(t, db, &manualNode)
	mustCreate(t, db, &model.Outbound{
		ServerID: srv.ID, Tag: "ext-ss", Type: "shadowsocks",
		SourceKind: model.OutboundSourceCustomNode, SourceID: manualNode.ID,
		Settings: model.JSONText(`{"server":"manual.example","server_port":8388,"password":"secret","settings":{"method":"aes-128-gcm"}}`),
	})
	conn := hub.register(srv.ID, "", nil)
	defer conn.close()

	rec = serveJSONRequest(t, router, http.MethodPut, fmt.Sprintf("/admin/custom-nodes/%d", manualNode.ID), `{
		"name":"manual-ss","protocol":"shadowsocks","address":"updated.example","port":8388,
		"params":{"method":"aes-128-gcm","password":"secret"}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update manual node = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	select {
	case env := <-conn.send:
		if env.Type != protocol.CmdApplyConfig || !strings.Contains(string(env.Payload), "updated.example") {
			t.Fatalf("unexpected push after manual node edit: type=%s payload=%s", env.Type, env.Payload)
		}
		conn.mu.Lock()
		ch := conn.pending[env.ID]
		conn.mu.Unlock()
		ch <- protocol.CommandResultEvt{ID: env.ID, OK: true}
	case <-time.After(3 * time.Second):
		t.Fatal("editing manual custom node did not push updated config to referencing server")
	}
	waitForSubscriptionResetPush(t, orch)

	// Syncing a subscription whose node is referenced by an outbound also pushes to referencing server.
	mustCreate(t, db, &model.Outbound{
		ServerID: srv.ID, Tag: "ext-sub", Type: "vless",
		SourceKind: model.OutboundSourceCustomNode, SourceID: managedNode.ID,
		Settings: model.JSONText(`{"server":"old.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`),
	})
	mgr := newCustomNodeSubscriptionManager(db)
	mgr.onNodesChanged = app.pushCustomNodeConsumers
	mgr.fetch = func(context.Context, *url.URL) ([]byte, error) {
		return []byte("vless://11111111-2222-4333-8444-555555555555@synced.example:443#managed-vless\n"), nil
	}
	if _, err := mgr.sync(context.Background(), sub.ID); err != nil {
		t.Fatalf("subscription sync failed: %v", err)
	}
	select {
	case env := <-conn.send:
		if env.Type != protocol.CmdApplyConfig || !strings.Contains(string(env.Payload), "synced.example") {
			t.Fatalf("unexpected push after subscription sync: type=%s payload=%s", env.Type, env.Payload)
		}
		conn.mu.Lock()
		ch := conn.pending[env.ID]
		conn.mu.Unlock()
		ch <- protocol.CommandResultEvt{ID: env.ID, OK: true}
	case <-time.After(3 * time.Second):
		t.Fatal("syncing custom node subscription did not push updated config to referencing server")
	}
	waitForSubscriptionResetPush(t, orch)
}

func TestListUserForwardsReflectsDisabledAndSingleUserInbound(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.create(t)
	router := gin.New()
	router.GET("/user/forwards", func(c *gin.Context) { c.Set("uid", fixture.user.ID) }, fixture.app.listUserForwards)

	// Active multi-user inbound -> Enabled: true
	rec := serveJSONRequest(t, router, http.MethodGet, "/user/forwards", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("active forward list = %d %s", rec.Code, rec.Body.String())
	}

	// Disable inbound -> Enabled: false
	if err := fixture.db.Model(&model.Inbound{}).Where("id = ?", fixture.inbound.ID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	rec = serveJSONRequest(t, router, http.MethodGet, "/user/forwards", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("disabled inbound forward list = %d %s", rec.Code, rec.Body.String())
	}

	// Re-enable inbound but switch to single-user -> Enabled: false
	if err := fixture.db.Model(&model.Inbound{}).Where("id = ?", fixture.inbound.ID).Updates(map[string]any{
		"enabled":  true,
		"settings": model.JSONText(`{"multi_user":false}`),
	}).Error; err != nil {
		t.Fatal(err)
	}
	rec = serveJSONRequest(t, router, http.MethodGet, "/user/forwards", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("single-user inbound forward list = %d %s", rec.Code, rec.Body.String())
	}
}
