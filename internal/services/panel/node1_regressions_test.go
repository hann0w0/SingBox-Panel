package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestSwitchToManagedConfigWithImportedOutboundSQLiteNoDeadlock(t *testing.T) {
	db := testDB(t)
	hub := NewHub(db)
	orch := NewOrchestrator(db, hub)
	app := &App{db: db, hub: hub, orch: orch, serverOperations: newKeyedMutex[uint]()}
	orch.app = app

	src := model.Server{Name: "source", Address: "1.2.3.4", AgentToken: "src-tok", ConfigMode: model.ConfigModeManaged, ConfigInitialized: true}
	tgt := model.Server{
		Name: "target", Address: "5.6.7.8", AgentToken: "tgt-tok", ConfigMode: model.ConfigModeRaw,
		RawConfig: model.JSONText(`{"inbounds":[],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`),
	}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&tgt).Error; err != nil {
		t.Fatal(err)
	}
	ib := model.Inbound{
		ServerID: src.ID, Tag: "vless-in", Type: model.InboundVLESS, ListenPort: 443, Enabled: true,
		Settings: model.JSONText(`{"single_user":true,"uuid":"11111111-2222-4333-8444-555555555555"}`),
	}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatal(err)
	}
	ob := model.Outbound{
		ServerID: tgt.ID, Tag: "hop-src", Type: "vless",
		SourceKind: model.OutboundSourceInbound, SourceID: ib.ID,
		Settings: model.JSONText(`{"server":"old.example.com","server_port":8443,"uuid":"old"}`),
	}
	if err := db.Create(&ob).Error; err != nil {
		t.Fatal(err)
	}

	conn := hub.register(tgt.ID, "", nil)
	defer conn.close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case env := <-conn.send:
			conn.mu.Lock()
			ch := conn.pending[env.ID]
			conn.mu.Unlock()
			ch <- protocol.CommandResultEvt{ID: env.ID, OK: true}
		case <-time.After(3 * time.Second):
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := orch.SwitchToManagedConfig(ctx, tgt.ID); err != nil {
		t.Fatalf("SwitchToManagedConfig deadlocked or failed: %v", err)
	}
	<-done
}

func TestDeleteServerDetachesCrossServerOutboundsAndPrunesServerOrder(t *testing.T) {
	db := testDB(t)
	hub := NewHub(db)
	orch := NewOrchestrator(db, hub)
	app := &App{db: db, hub: hub, orch: orch, serverOperations: newKeyedMutex[uint]()}
	orch.app = app

	src := model.Server{Name: "source", Address: "1.2.3.4", AgentToken: "src-tok", ConfigMode: model.ConfigModeManaged}
	tgt := model.Server{Name: "target", Address: "5.6.7.8", AgentToken: "tgt-tok", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&tgt).Error; err != nil {
		t.Fatal(err)
	}
	ib := model.Inbound{
		ServerID: src.ID, Tag: "vless-src", Type: model.InboundVLESS, ListenPort: 443, Enabled: true,
		Settings: model.JSONText(`{"single_user":true,"uuid":"11111111-2222-4333-8444-555555555555"}`),
	}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatal(err)
	}
	ob := model.Outbound{
		ServerID: tgt.ID, Tag: "via-src", Type: "vless",
		SourceKind: model.OutboundSourceInbound, SourceID: ib.ID,
		Settings: model.JSONText(`{"server":"1.2.3.4","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`),
	}
	if err := db.Create(&ob).Error; err != nil {
		t.Fatal(err)
	}
	orderJSON, _ := json.Marshal([]uint{src.ID, tgt.ID})
	if err := db.Save(&model.Setting{Key: serverOrderSettingKey, Value: string(orderJSON)}).Error; err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.DELETE("/servers/:id", app.deleteServer)
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/servers/%d", src.ID), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deleteServer = %d %s", rec.Code, rec.Body.String())
	}

	var storedOb model.Outbound
	if err := db.First(&storedOb, ob.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedOb.SourceKind != "" || storedOb.SourceID != 0 {
		t.Fatalf("cross-server outbound still attached: kind=%q id=%d", storedOb.SourceKind, storedOb.SourceID)
	}
	var orderSetting model.Setting
	if err := db.Where("key = ?", serverOrderSettingKey).First(&orderSetting).Error; err != nil {
		t.Fatal(err)
	}
	var gotOrder []uint
	if err := json.Unmarshal([]byte(orderSetting.Value), &gotOrder); err != nil {
		t.Fatal(err)
	}
	if len(gotOrder) != 1 || gotOrder[0] != tgt.ID {
		t.Fatalf("server_order = %v; want [%d]", gotOrder, tgt.ID)
	}

	// Reusing the freed inbound RowID on a new server must never rebind tgt's outbound.
	other := model.Server{Name: "other", Address: "9.9.9.9", AgentToken: "oth-tok", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	reusedIB := model.Inbound{
		ID: ib.ID, ServerID: other.ID, Tag: "hijacked", Type: model.InboundVLESS, ListenPort: 8443, Enabled: true,
		Settings: model.JSONText(`{"single_user":true,"uuid":"99999999-9999-4999-8999-999999999999"}`),
	}
	if err := db.Create(&reusedIB).Error; err != nil {
		t.Fatal(err)
	}
	cfg, err := orch.BuildServerConfig(&tgt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "9.9.9.9") || !strings.Contains(string(cfg), "1.2.3.4") {
		t.Fatalf("detached outbound rebound to reused RowID:\n%s", cfg)
	}
}

func TestCustomNodeSubscriptionSyncDetachesDeletedNodeOutbounds(t *testing.T) {
	db := testDB(t)
	mgr := newCustomNodeSubscriptionManager(db)
	payload := "vless://11111111-2222-4333-8444-555555555555@1.1.1.1:443#NodeA\nvless://11111111-2222-4333-8444-555555555556@2.2.2.2:443#NodeB\n"
	mgr.fetch = func(context.Context, *url.URL) ([]byte, error) {
		return []byte(payload), nil
	}

	sub := model.CustomNodeSubscription{Name: "sub1", URL: "https://example.com/sub"}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.sync(context.Background(), sub.ID); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	var nodeA model.CustomNode
	if err := db.Where("subscription_id = ? AND source_name = ?", sub.ID, "NodeA").First(&nodeA).Error; err != nil {
		t.Fatal(err)
	}
	srv := model.Server{Name: "edge", Address: "3.3.3.3", AgentToken: "edge-tok", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	ob := model.Outbound{
		ServerID: srv.ID, Tag: "out-a", Type: "vless",
		SourceKind: model.OutboundSourceCustomNode, SourceID: nodeA.ID,
		Settings: model.JSONText(`{"server":"1.1.1.1","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`),
	}
	if err := db.Create(&ob).Error; err != nil {
		t.Fatal(err)
	}

	payload = "vless://11111111-2222-4333-8444-555555555556@2.2.2.2:443#NodeB\n"
	if _, err := mgr.sync(context.Background(), sub.ID); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	var stored model.Outbound
	if err := db.First(&stored, ob.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.SourceKind != "" || stored.SourceID != 0 {
		t.Fatalf("outbound referencing deleted custom node not detached: %+v", stored)
	}
}

func TestStageConfigSetsConfigInitializedAndSettlesUnmanagedSync(t *testing.T) {
	db := testDB(t)
	orch := NewOrchestrator(db, NewHub(db))
	app := &App{db: db, orch: orch}
	srv := model.Server{Name: "fresh", AgentToken: "fresh-tok", ProxySyncRevision: 2, ProxySyncAppliedRevision: 0}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}

	orch.PushConfigIfManaged(srv.ID)
	var settled model.Server
	if err := db.First(&settled, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if settled.ProxySyncAppliedRevision != 2 {
		t.Fatalf("unmanaged ProxySyncAppliedRevision = %d; want 2", settled.ProxySyncAppliedRevision)
	}

	router := gin.New()
	router.POST("/stage/:id", func(c *gin.Context) {
		id, _ := uintParam(c, "id")
		app.stageConfigAndRespond(c, id, gin.H{"ok": true})
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/stage/%d", srv.ID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stageConfigAndRespond = %d", rec.Code)
	}
	managed, err := orch.serverIsManaged(srv.ID)
	if err != nil || !managed {
		t.Fatalf("serverIsManaged = %v, %v; want true", managed, err)
	}
}

func TestDeleteUserCleansSubscriptionFetchesSyncsRuleSetsAndPreservesInvertedRule(t *testing.T) {
	db := testDB(t)
	orch := NewOrchestrator(db, NewHub(db))
	app := &App{db: db, orch: orch, serverOperations: newKeyedMutex[uint]()}
	srv := model.Server{Name: "s1", Address: "1.2.3.4", AgentToken: "s1-tok", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	user := model.User{Email: "alice@example.com", Role: model.RoleUser, Enabled: true, SubToken: "sub-a", ProxyToken: "proxy-a", ServerIDs: []uint{srv.ID}}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	fetch := model.SubscriptionFetch{UserID: user.ID, IP: "1.1.1.1", Client: "clash", Format: "clash", Status: 200}
	if err := db.Create(&fetch).Error; err != nil {
		t.Fatal(err)
	}
	rs := model.RuleSet{Tag: "ads", Type: "remote", Format: "binary", URL: "https://example.com/ads.srs"}
	if err := db.Create(&rs).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ServerRuleSet{ServerID: srv.ID, RuleSetID: rs.ID}).Error; err != nil {
		t.Fatal(err)
	}
	target := proxyTrafficName(&user)
	normalMatch, _ := json.Marshal(singbox.RuleInput{AuthUser: []string{target}, RuleSet: []string{rs.Tag}})
	normalRule := model.RouteRule{ServerID: srv.ID, Enabled: true, Outbound: "direct", Match: normalMatch}
	invertMatch, _ := json.Marshal(singbox.RuleInput{AuthUser: []string{target}, Extra: map[string]any{"invert": true}})
	invertRule := model.RouteRule{ServerID: srv.ID, Enabled: true, Outbound: "direct", Match: invertMatch}
	if err := db.Create(&normalRule).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&invertRule).Error; err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.DELETE("/users/:id", app.deleteUser)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/users/%d", user.ID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("deleteUser = %d %s", rec.Code, rec.Body.String())
	}

	var fetchCount int64
	if err := db.Model(&model.SubscriptionFetch{}).Where("user_id = ?", user.ID).Count(&fetchCount).Error; err != nil {
		t.Fatal(err)
	}
	if fetchCount != 0 {
		t.Fatalf("orphaned SubscriptionFetch rows = %d; want 0", fetchCount)
	}
	var srsCount int64
	if err := db.Model(&model.ServerRuleSet{}).Where("server_id = ?", srv.ID).Count(&srsCount).Error; err != nil {
		t.Fatal(err)
	}
	if srsCount != 0 {
		t.Fatalf("orphaned ServerRuleSet rows = %d; want 0", srsCount)
	}
	var normalCount int64
	if err := db.Model(&model.RouteRule{}).Where("id = ?", normalRule.ID).Count(&normalCount).Error; err != nil {
		t.Fatal(err)
	}
	if normalCount != 0 {
		t.Fatalf("user-only rule survived deletion")
	}
	var kept model.RouteRule
	if err := db.First(&kept, invertRule.ID).Error; err != nil {
		t.Fatalf("inverted rule deleted unexpectedly: %v", err)
	}
	var keptMatch singbox.RuleInput
	if err := json.Unmarshal(kept.Match, &keptMatch); err != nil {
		t.Fatal(err)
	}
	if len(keptMatch.AuthUser) != 0 || keptMatch.Extra["invert"] != nil {
		t.Fatalf("inverted rule not cleared to unscoped match: %+v", keptMatch)
	}
}

func TestOverlayServerPrunesDeletedRuleSetAssignment(t *testing.T) {
	db := testDB(t)
	orch := NewOrchestrator(db, NewHub(db))
	app := &App{db: db, orch: orch, serverOperations: newKeyedMutex[uint]()}

	raw := []byte(`{
  "inbounds": [{"type":"vless","tag":"vless-in","listen":"::","listen_port":443,"users":[{"uuid":"11111111-2222-4333-8444-555555555555"}]}],
  "outbounds": [{"type":"direct","tag":"direct"}],
  "route": {
    "rule_set": [{"type":"remote","tag":"geo-cn","format":"binary","url":"https://example.com/cn.srs"}],
    "rules": [{"rule_set":["geo-cn"],"outbound":"direct"}],
    "final": "direct"
  }
}`)
	srv := model.Server{Name: "ov", Address: "1.2.3.4", AgentToken: "ov-tok"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.applyImport(&srv, parsed, raw); err != nil {
		t.Fatal(err)
	}
	var rule model.RouteRule
	if err := db.Where("server_id = ?", srv.ID).First(&rule).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.DELETE("/servers/:id/rules/:ruleID", app.deleteRule)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/servers/%d/rules/%d", srv.ID, rule.ID), bytes.NewBuffer(nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("deleteRule = %d %s", rec.Code, rec.Body.String())
	}
	var count int64
	if err := db.Model(&model.ServerRuleSet{}).Where("server_id = ?", srv.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("overlay ServerRuleSet not pruned after deleting rule: count=%d", count)
	}
}
