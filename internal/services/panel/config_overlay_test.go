package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func importedConfigMode(t *testing.T, parsed *singbox.ParsedConfig, raw []byte) string {
	t.Helper()
	db := testDB(t)
	srv := model.Server{Name: "mode-test", AgentToken: "mode-test"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	if err := app.applyImport(&srv, parsed, raw); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&srv, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	got, err := app.orch.BuildServerConfig(&srv)
	if err != nil || !equivalentJSON(got, raw) {
		t.Fatalf("import changed original: %s, %v", got, err)
	}
	return srv.EffectiveConfigMode()
}

func TestImportedConfigEditsPreserveUnknownFieldsAndEntries(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "overlay", AgentToken: "overlay"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{
"custom":{"large":9007199254740993},
"inbounds":[{"type":"vless","tag":"in","listen":"::","listen_port":443,"users":[{"name":"custom-user","uuid":"11111111-2222-3333-4444-555555555555","future_user_option":true}],"tls":{"enabled":true,"server_name":"old.example","certificate_path":"/cert","key_path":"/key","future_tls":{"keep":true}}},{"type":"future-inbound","tag":"unknown","listen_port":9000,"data":[1,2]}],
"outbounds":[{"type":"direct","tag":"direct","future_direct":true},{"type":"future-outbound","tag":"custom-exit","setting":true}],
"route":{"rules":[{"action":"sniff"},{"action":"resolve","strategy":"prefer_ipv4"},{"port":[443],"domain":["example.com"],"outbound":"direct","override_port":8443,"override_address":"127.0.0.1"},{"action":"future-rule","unknown":true}],"final":"custom-exit","custom_route":true},
"services":[{"type":"future-service","tag":"custom","data":true}],"http_clients":[{"tag":"manual","custom":true}],"experimental":{"custom":true}}`)
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	if err := app.applyImport(&srv, parsed, raw); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&srv, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if srv.EffectiveConfigMode() != model.ConfigModeManaged {
		t.Fatal("import locked panel")
	}
	generated, err := app.orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	if !equivalentJSON(generated, raw) {
		t.Fatalf("no-op import changed config: %s", generated)
	}
	var inbound model.Inbound
	if err := db.Where("server_id = ?", srv.ID).First(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	var settings singbox.InboundSettings
	if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	settings.TLS.ServerName = "new.example"
	encoded, _ := json.Marshal(settings)
	if err := db.Model(&inbound).Updates(map[string]any{"tag": "renamed", "listen_port": 8444, "settings": model.JSONText(encoded)}).Error; err != nil {
		t.Fatal(err)
	}
	var rules []model.RouteRule
	if err := db.Where("server_id = ?", srv.ID).Order("sort").Find(&rules).Error; err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("editable rules = %d", len(rules))
	}
	var match singbox.RuleInput
	_ = json.Unmarshal(rules[2].Match, &match)
	match.Port = []int{80}
	encoded, _ = json.Marshal(match)
	if err := db.Model(&rules[2]).Updates(map[string]any{"sort": -10, "match": model.JSONText(encoded)}).Error; err != nil {
		t.Fatal(err)
	}
	generated, err = app.orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`9007199254740993`, `"future_tls"`, `"future_user_option"`, `"future-inbound"`, `"future-outbound"`, `"future-service"`, `"custom_route"`, `"prefer_ipv4"`, `"future-rule"`, `"override_port": 8443`, `"override_address": "127.0.0.1"`, `"listen_port": 8444`, `"tag": "renamed"`, `"server_name": "new.example"`} {
		if !strings.Contains(string(generated), part) {
			t.Errorf("missing %s in %s", part, generated)
		}
	}
	var root struct {
		Route struct {
			Rules []map[string]json.RawMessage `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(generated, &root); err != nil {
		t.Fatal(err)
	}
	if len(root.Route.Rules) != 4 || string(root.Route.Rules[0]["override_port"]) != "8443" || !equivalentJSON(root.Route.Rules[0]["port"], []byte(`[80]`)) {
		t.Fatalf("reorder mixed rule fields: %s", generated)
	}
	if err := db.Delete(&rules[2]).Error; err != nil {
		t.Fatal(err)
	}
	generated, err = app.orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(generated), "override_port") || !strings.Contains(string(generated), "prefer_ipv4") || !strings.Contains(string(generated), "future-rule") {
		t.Fatalf("deletion lost unknown rule or restored deleted fields: %s", generated)
	}
}

func TestMigrateRawConfigOverlayRetainsSourceAndUnlocks(t *testing.T) {
	db := testDB(t)
	raw := model.JSONText(`{"route":{"rules":[{"action":"resolve","strategy":"future"}]},"custom":true}`)
	srv := model.Server{Name: "raw-upgrade", AgentToken: "raw-upgrade", ConfigMode: model.ConfigModeRaw, RawConfig: raw}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.Transaction(migrateRawConfigOverlays); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(&srv, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if srv.EffectiveConfigMode() != model.ConfigModeManaged || len(srv.ConfigOverlay) == 0 {
		t.Fatalf("not migrated: %+v", srv)
	}
	got, err := NewOrchestrator(db, nil).BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("source changed: %s", got)
	}
}

func TestMigrateRawConfigOverlaySkipsNullAndBlankRawConfig(t *testing.T) {
	db := testDB(t)
	sNull := model.Server{Name: "raw-null", AgentToken: "raw-null", ConfigMode: model.ConfigModeRaw, RawConfig: model.JSONText("null")}
	sBlank := model.Server{Name: "raw-blank", AgentToken: "raw-blank", ConfigMode: model.ConfigModeRaw, RawConfig: model.JSONText("   ")}
	if err := db.Create(&sNull).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&sBlank).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(migrateRawConfigOverlays); err != nil {
		t.Fatalf("migrateRawConfigOverlays failed on null/blank RawConfig: %v", err)
	}
}

func TestConfigOverlayAPIEditAndCustomReferenceProtection(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "api-overlay", AgentToken: "api-overlay"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"inbounds":[{"type":"socks","tag":"main","listen":"::","listen_port":1080}],"outbounds":[{"type":"socks","tag":"exit","server":"proxy.example","server_port":1080}],"route":{"rules":[{"action":"future","inbound":["main"],"outbound":"exit","rule_set":["custom-set"]}],"rule_set":[{"type":"local","tag":"custom-set","format":"source","path":"/rules.json","unknown":true}]},"future":true}`)
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, hub: NewHub(db), orch: NewOrchestrator(db, NewHub(db))}
	if err := app.applyImport(&srv, parsed, raw); err != nil {
		t.Fatal(err)
	}
	var inbound model.Inbound
	db.Where("server_id = ?", srv.ID).First(&inbound)
	var outbound model.Outbound
	db.Where("server_id = ?", srv.ID).First(&outbound)
	router := gin.New()
	router.PUT("/servers/:id/inbounds/:inboundID", app.updateInbound)
	router.DELETE("/servers/:id/outbounds/:outboundID", app.deleteOutbound)
	path := fmt.Sprintf("/servers/%d/inbounds/%d", srv.ID, inbound.ID)
	response := serveJSONRequest(t, router, http.MethodPut, path, `{"listen_port":1081}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"apply_state":"staged"`) {
		t.Fatalf("edit rejected: %d %s", response.Code, response.Body)
	}
	response = serveJSONRequest(t, router, http.MethodPut, path, `{"tag":"renamed"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("dangling custom inbound reference permitted: %d %s", response.Code, response.Body)
	}
	response = serveJSONRequest(t, router, http.MethodDelete, fmt.Sprintf("/servers/%d/outbounds/%d", srv.ID, outbound.ID), "")
	if response.Code != http.StatusConflict {
		t.Fatalf("dangling custom outbound reference permitted: %d %s", response.Code, response.Body)
	}
	if err := db.First(&srv, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := syncServerRuleSets(db, srv.ID); err != nil {
		t.Fatal(err)
	}
	got, err := app.orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"listen_port": 1081`) || !strings.Contains(string(got), `"unknown": true`) || !strings.Contains(string(got), `"action": "future"`) {
		t.Fatalf("API edit lost custom data: %s", got)
	}
	// A rejected raw-file apply must restore the previous overlay as well.
	oldOverlay := append([]byte(nil), srv.ConfigOverlay...)
	if _, err := app.orch.applyRawConfigUnlocked(context.Background(), srv.ID, []byte(`{"new":true}`)); err == nil {
		t.Fatal("offline raw apply unexpectedly succeeded")
	}
	if err := db.First(&srv, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if string(srv.ConfigOverlay) != string(oldOverlay) || srv.EffectiveConfigMode() != model.ConfigModeManaged {
		t.Fatal("rejected raw edit changed overlay")
	}
}

func TestMigrateManagedResidualUsesCurrentRowsInsteadOfStaleRaw(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "legacy-managed", AgentToken: "legacy-managed", ConfigResidual: model.JSONText(`{"inbounds":{"main":{"custom":true}}}`), RawConfig: model.JSONText(`{"inbounds":[{"type":"socks","tag":"main","listen_port":9999}]}`)}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{ServerID: srv.ID, Tag: "main", Type: model.InboundSocks, ListenPort: 1080, Enabled: true, Settings: model.JSONText(`{"single_user":true}`)}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	orch := NewOrchestrator(db, NewHub(db))
	before, err := orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(migrateManagedConfigOverlays); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&srv, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	got, err := orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	if !equivalentJSON(before, got) {
		t.Fatalf("upgrade used stale raw configuration: %s", got)
	}
	if err := db.Model(&inbound).Update("tag", "renamed").Error; err != nil {
		t.Fatal(err)
	}
	got, err = orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"custom": true`) || !strings.Contains(string(got), `"tag": "renamed"`) {
		t.Fatalf("legacy residual lost on rename: %s", got)
	}
}

func TestConfigOverlayRejectsHiddenPrivateOutboundScopeChanges(t *testing.T) {
	f := newForwardFixture(t)
	raw, err := f.app.orch.BuildServerConfig(&f.server)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	root["route"].(map[string]any)["rules"] = []any{map[string]any{"action": "route", "auth_user": []string{proxyTrafficName(&f.user)}, "inbound": []string{f.inbound.Tag}, "outbound": "direct", "invert": true}}
	raw, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.applyImport(&f.server, parsed, raw); err != nil {
		t.Fatal(err)
	}
	// Keep the original managed-user mode while changing a formerly shared
	// route to a private exit. Form-only validation cannot see its invert flag.
	if err := f.db.Model(&f.inbound).Update("settings", f.inbound.Settings).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.RouteRule{}).Where("server_id = ?", f.server.ID).Update("outbound", f.outbound.Tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&f.server, f.server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.orch.BuildServerConfig(&f.server); err == nil || !strings.Contains(err.Error(), "取反") {
		t.Fatalf("merged private scope was not checked: %v", err)
	}
}

func TestSwitchToManagedConfigDoesNotDeadlockWithUserAccessMuUnderSingleConnSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "single-conn.db")
	db := testDBAt(t, dbPath)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	raw := model.JSONText(`{"inbounds":[{"type":"vless","tag":"in-vless","listen":"::","listen_port":443}],"outbounds":[{"type":"direct","tag":"direct"}]}`)
	srv := model.Server{Name: "lock-order", AgentToken: "lock-order", ConfigMode: model.ConfigModeRaw, RawConfig: raw}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	orch := NewOrchestrator(db, hub)
	app := &App{db: db, hub: hub, orch: orch}
	orch.app = app

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			_ = orch.SwitchToManagedConfig(ctx, srv.ID)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			app.userAccessMu.Lock()
			_ = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var count int64
				return tx.Model(&model.User{}).Count(&count).Error
			})
			app.userAccessMu.Unlock()
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("SwitchToManagedConfig deadlocked with userAccessMu under MaxOpenConns(1)")
	}
}
