package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/protocol"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRawConfigIsTheReconnectSourceOfTruth(t *testing.T) {
	db := testDB(t)
	raw := []byte(`{"dns":{"servers":[{"type":"local","tag":"dns"}]},"inbounds":[],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`)
	srv := model.Server{Name: "raw", AgentToken: "t", ConfigMode: model.ConfigModeRaw, RawConfig: model.JSONText(raw)}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	orch := NewOrchestrator(db, NewHub(db))
	got, err := orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("raw config changed:\n%s", got)
	}
}

func TestImportSyncPreservesInboundGrantsByTag(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "node", AgentToken: "sync", Address: "node.example.com"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	keep := model.Inbound{ServerID: srv.ID, Tag: "Snell", Type: model.InboundSnell, ListenPort: 10001, Enabled: true}
	removed := model.Inbound{ServerID: srv.ID, Tag: "old", Type: model.InboundSnell, ListenPort: 10002, Enabled: true}
	if err := db.Create(&keep).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&removed).Error; err != nil {
		t.Fatal(err)
	}
	user := model.User{
		Email: "sync-user", Password: "unused", Role: model.RoleUser, Enabled: true,
		ServerIDs: []uint{srv.ID}, InboundIDs: []uint{keep.ID}, SubToken: "sync-user-token",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	raw := []byte(`{
  "inbounds": [
    {"type":"snell","tag":"Snell","listen":"::","listen_port":38376,"psk":"one","version":5},
    {"type":"snell","tag":"Snell2","listen":"::","listen_port":38378,"psk":"two","version":5}
  ],
  "outbounds": [{"type":"direct","tag":"direct"}],
  "route": {"final":"direct"}
}`)
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	if err := app.applyImport(&srv, parsed, raw); err != nil {
		t.Fatal(err)
	}

	var inbounds []model.Inbound
	if err := db.Where("server_id = ?", srv.ID).Order("tag").Find(&inbounds).Error; err != nil {
		t.Fatal(err)
	}
	if len(inbounds) != 2 {
		t.Fatalf("inbounds = %d; want 2", len(inbounds))
	}
	byTag := map[string]model.Inbound{}
	for _, inbound := range inbounds {
		byTag[inbound.Tag] = inbound
	}
	if got := byTag["Snell"]; got.ID != keep.ID || got.ListenPort != 38376 {
		t.Fatalf("existing inbound not updated in place: %+v", got)
	}
	if got := byTag["Snell2"]; got.ID == 0 || got.ID == keep.ID {
		t.Fatalf("new inbound missing: %+v", got)
	}
	if _, ok := byTag["old"]; ok {
		t.Fatal("removed inbound still exists")
	}

	if err := db.First(&srv, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if srv.EffectiveConfigMode() != model.ConfigModeRaw || string(srv.RawConfig) != string(raw) || !srv.ConfigInitialized {
		t.Fatalf("raw source not persisted: mode=%q initialized=%v raw=%s", srv.ConfigMode, srv.ConfigInitialized, srv.RawConfig)
	}
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(user.InboundIDs) != 1 || user.InboundIDs[0] != keep.ID {
		t.Fatalf("user grant changed: %v", user.InboundIDs)
	}
	if nodes := app.userNodeDetails(&user); len(nodes) != 1 || nodes[0].Name != "node - Snell" {
		t.Fatalf("authorized nodes after sync = %+v", nodes)
	}
}

func TestImportAutomaticallySelectsSafeConfigMode(t *testing.T) {
	base, err := singbox.BuildServerConfig(singbox.ServerConfigInput{
		Inbounds: []singbox.InboundInput{{
			Tag: "Snell", Type: "snell", ListenPort: 38376,
			Settings: singbox.InboundSettings{SingleUser: true, SnellVersion: 5, SnellPSK: "secret"},
		}},
		Final: "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	withDNS := func() []byte {
		var root map[string]any
		if err := json.Unmarshal(base, &root); err != nil {
			t.Fatal(err)
		}
		root["dns"] = map[string]any{"servers": []any{map[string]any{"type": "local", "tag": "local"}}}
		raw, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	withSupportedSniff := func() []byte {
		var root map[string]any
		if err := json.Unmarshal(base, &root); err != nil {
			t.Fatal(err)
		}
		root["route"] = map[string]any{
			"rules": []any{
				map[string]any{"action": "sniff"},
				map[string]any{"action": "route", "domain_suffix": []any{"example.com"}, "outbound": "direct"},
			},
			"final": "direct",
		}
		raw, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	for _, tc := range []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "lossless managed config", raw: base, want: model.ConfigModeManaged},
		{name: "supported sniff stays managed", raw: withSupportedSniff(), want: model.ConfigModeManaged},
		{name: "unmodelled DNS stays raw", raw: withDNS(), want: model.ConfigModeRaw},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			srv := model.Server{Name: tc.name, AgentToken: tc.name}
			if err := db.Create(&srv).Error; err != nil {
				t.Fatal(err)
			}
			parsed, err := singbox.ParseServerConfig(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
			if err := app.applyImport(&srv, parsed, tc.raw); err != nil {
				t.Fatal(err)
			}
			if err := db.First(&srv, srv.ID).Error; err != nil {
				t.Fatal(err)
			}
			if srv.EffectiveConfigMode() != tc.want {
				t.Fatalf("config mode = %q; want %q", srv.EffectiveConfigMode(), tc.want)
			}
		})
	}
}

func TestBuildServerConfigReturnsDatabaseReadError(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "managed", AgentToken: "db-error", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOrchestrator(db, NewHub(db)).BuildServerConfig(&srv); err == nil || !strings.Contains(err.Error(), "load inbounds") {
		t.Fatalf("BuildServerConfig error = %v", err)
	}
}

func TestBuildServerConfigRejectsCorruptStoredJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  any
		want string
	}{
		{name: "outbound", row: &model.Outbound{Tag: "bad-out", Type: "vless", Settings: model.JSONText(`{"server":`)}, want: "outbound \"bad-out\" settings"},
		{name: "route rule", row: &model.RouteRule{Outbound: "direct", Enabled: true, Match: model.JSONText(`{"inbound":`)}, want: "route rule"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			srv := model.Server{Name: "managed", AgentToken: "json-" + tc.name, ConfigMode: model.ConfigModeManaged}
			if err := db.Create(&srv).Error; err != nil {
				t.Fatal(err)
			}
			switch row := tc.row.(type) {
			case *model.Outbound:
				row.ServerID = srv.ID
			case *model.RouteRule:
				row.ServerID = srv.ID
			}
			if err := db.Create(tc.row).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := NewOrchestrator(db, NewHub(db)).BuildServerConfig(&srv); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("BuildServerConfig error = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestApplyDesiredConfigReportsPendingAndFailed(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "managed", AgentToken: "apply-state", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	orch := NewOrchestrator(db, NewHub(db))
	if got := orch.ApplyDesiredConfig(context.Background(), srv.ID); got.ApplyState != ConfigPending || got.ApplyError != "" {
		t.Fatalf("offline result = %+v", got)
	}
	if err := db.First(&srv, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	managed, err := orch.serverIsManaged(srv.ID)
	if err != nil || !srv.ConfigInitialized || !managed {
		t.Fatalf("desired config marker: initialized=%v managed=%v err=%v", srv.ConfigInitialized, managed, err)
	}
	bad := model.Outbound{ServerID: srv.ID, Tag: "bad", Type: "vless", Settings: model.JSONText(`{"server":`)}
	if err := db.Create(&bad).Error; err != nil {
		t.Fatal(err)
	}
	if got := orch.ApplyDesiredConfig(context.Background(), srv.ID); got.ApplyState != ConfigFailed || !strings.Contains(got.ApplyError, "outbound") {
		t.Fatalf("invalid config result = %+v", got)
	}
}

func TestNormalizeNodeAddress(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "Node.Example.COM.", want: "node.example.com"},
		{in: "192.0.2.10", want: "192.0.2.10"},
		{in: "[2001:db8::1]", want: "2001:db8::1"},
		{in: "2001:db8::2", want: "2001:db8::2"},
		{in: "https://node.example.com", wantErr: true},
		{in: "node.example.com:443", wantErr: true},
		{in: "node.example.com/path", wantErr: true},
		{in: "node.example.com?q=1", wantErr: true},
		{in: "user@node.example.com", wantErr: true},
		{in: " node.example.com", wantErr: true},
		{in: "999.1.1.1", wantErr: true},
	} {
		got, err := normalizeNodeAddress(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizeNodeAddress(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("normalizeNodeAddress(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestHubOldConnectionCannotMarkReplacementOffline(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "node", AgentToken: "hub-race", Online: true}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	oldConn := &agentConn{serverID: srv.ID}
	newConn := &agentConn{serverID: srv.ID}
	hub.conns[srv.ID] = newConn
	hub.unregister(oldConn)
	if err := db.First(&srv, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !srv.Online || hub.conns[srv.ID] != newConn {
		t.Fatalf("replacement connection was disturbed: online=%v current=%p", srv.Online, hub.conns[srv.ID])
	}
	hub.unregister(newConn)
	if err := db.First(&srv, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if srv.Online || hub.IsOnline(srv.ID) {
		t.Fatalf("current connection unregister did not mark offline: online=%v", srv.Online)
	}
}

func TestValidateRuleRejectsDanglingReferences(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "managed", AgentToken: "t", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{db: db}
	match, _ := json.Marshal(map[string]any{"inbound": []string{"missing"}})
	if err := a.validateRule(db, srv.ID, match, "missing-outbound"); err == nil {
		t.Fatal("dangling inbound/outbound references were accepted")
	}
	if err := a.validateInboundIdentity(db, srv.ID, 0, "in", 443); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Inbound{ServerID: srv.ID, Tag: "in", Type: model.InboundVLESS, ListenPort: 443}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.validateInboundIdentity(db, srv.ID, 0, "other", 443); err == nil {
		t.Fatal("duplicate listen port was accepted")
	}
}

func TestJWTIsRevokedByTokenVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	u := model.User{Email: "admin", Password: "hash", Role: model.RoleAdmin, Enabled: true, SubToken: "sub"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	auth := NewAuth("0123456789abcdef0123456789abcdef", db)
	token, err := auth.Issue(&u, sessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(auth.Middleware())
	r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := func() int {
		req := httptest.NewRequest(http.MethodGet, "/ok", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if got := request(); got != http.StatusNoContent {
		t.Fatalf("fresh token status = %d", got)
	}
	if err := db.Model(&u).Update("token_version", 1).Error; err != nil {
		t.Fatal(err)
	}
	if got := request(); got != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d", got)
	}
}

func TestPasswordChangeReturnsAReplacementToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	hash, err := hashPassword("old-password")
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "user", Password: hash, Role: model.RoleUser, Enabled: true, SubToken: "sub"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	auth := NewAuth("0123456789abcdef0123456789abcdef", db)
	oldToken, err := auth.Issue(&u, sessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, auth: auth}
	r := gin.New()
	r.POST("/change", auth.Middleware(), a.handleChangePassword)
	body := bytes.NewBufferString(`{"old_password":"old-password","new_password":"new-password"}`)
	req := httptest.NewRequest(http.MethodPost, "/change", body)
	req.Header.Set("Authorization", "Bearer "+oldToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("change password status = %d, body = %s", w.Code, w.Body.String())
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Token == "" {
		t.Fatalf("replacement token missing: err=%v body=%s", err, w.Body.String())
	}

	protected := gin.New()
	protected.Use(auth.Middleware())
	protected.GET("/ok", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	requestWith := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/ok", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, req)
		return w.Code
	}
	if got := requestWith(oldToken); got != http.StatusUnauthorized {
		t.Fatalf("old token status = %d", got)
	}
	if got := requestWith(response.Token); got != http.StatusNoContent {
		t.Fatalf("replacement token status = %d", got)
	}
}

func TestSwitchToManagedRollsBackWhenAgentIsOffline(t *testing.T) {
	db := testDB(t)
	raw := model.JSONText(`{"inbounds":[],"outbounds":[{"type":"direct","tag":"direct"}]}`)
	srv := model.Server{Name: "raw", AgentToken: "offline", ConfigMode: model.ConfigModeRaw, RawConfig: raw}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	orch := NewOrchestrator(db, NewHub(db))
	if err := orch.SwitchToManagedConfig(context.Background(), srv.ID); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("switch error = %v; want ErrAgentOffline", err)
	}
	var got model.Server
	if err := db.First(&got, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.EffectiveConfigMode() != model.ConfigModeRaw || string(got.RawConfig) != string(raw) {
		t.Fatalf("mode was not rolled back: mode=%q raw=%s", got.ConfigMode, got.RawConfig)
	}
}

func TestInstallCommandQuotesEveryArgument(t *testing.T) {
	baseURL := "https://panel.example.com/it's"
	token := "tok'en"
	command := installCommand(baseURL, token)
	for _, value := range []string{baseURL + "/api/agent/install.sh", baseURL, token} {
		if !strings.Contains(command, shellQuote(value)) {
			t.Fatalf("command does not quote %q: %s", value, command)
		}
	}
	if got := shellQuote("a'b"); got != `'a'"'"'b'` {
		t.Fatalf("shellQuote = %q", got)
	}
}

func TestAgentInstallerVerifiesAndCanRollbackBinary(t *testing.T) {
	for _, required := range []string{
		"/api/agent/checksum?arch=$GOARCH",
		"Agent SHA256 mismatch",
		`"$TMP" --version`,
		"singbox-panel-agent.prev",
		"/run/singbox-panel-agent.ready",
	} {
		if !strings.Contains(agentInstallScript, required) {
			t.Fatalf("Agent installer missing %q", required)
		}
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(agentInstallScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Agent installer shell syntax: %v: %s", err, out)
	}
}

func TestFileChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(path, []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := fileChecksum(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "d4f0bc5a29de06b510f9aa428f1eedba926012b591fef7a518e776a7c9bd1824" {
		t.Fatalf("checksum = %q", got)
	}
}

func TestDeleteServerCleansSerializedUserAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	remove := model.Server{Name: "remove", AgentToken: "remove-token"}
	keep := model.Server{Name: "keep", AgentToken: "keep-token"}
	if err := db.Create(&remove).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&keep).Error; err != nil {
		t.Fatal(err)
	}
	removeInbound := model.Inbound{ServerID: remove.ID, Tag: "remove-in", Type: model.InboundVLESS, ListenPort: 443}
	keepInbound := model.Inbound{ServerID: keep.ID, Tag: "keep-in", Type: model.InboundVLESS, ListenPort: 8443}
	for _, inbound := range []*model.Inbound{&removeInbound, &keepInbound} {
		if err := db.Create(inbound).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.Outbound{ServerID: remove.ID, Tag: "landing", Type: "vless"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RouteRule{ServerID: remove.ID, Outbound: "landing"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RuleSet{ServerID: remove.ID, Tag: "geo", URL: "https://example.com/geo.srs"}).Error; err != nil {
		t.Fatal(err)
	}
	u := model.User{
		Email: "user", Password: "hash", Role: model.RoleUser, Enabled: true, SubToken: "sub",
		ServerIDs: []uint{remove.ID, keep.ID}, InboundIDs: []uint{removeInbound.ID, keepInbound.ID},
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}

	a := &App{db: db}
	r := gin.New()
	r.DELETE("/servers/:id", a.deleteServer)
	req := httptest.NewRequest(http.MethodDelete, "/servers/"+strconv.FormatUint(uint64(remove.ID), 10), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", w.Code, w.Body.String())
	}
	if err := db.First(&u, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(u.ServerIDs) != 1 || u.ServerIDs[0] != keep.ID || len(u.InboundIDs) != 1 || u.InboundIDs[0] != keepInbound.ID {
		t.Fatalf("user access not cleaned: servers=%v inbounds=%v", u.ServerIDs, u.InboundIDs)
	}
	for _, modelValue := range []any{&model.Inbound{}, &model.Outbound{}, &model.RouteRule{}, &model.RuleSet{}} {
		var count int64
		if err := db.Model(modelValue).Where("server_id = ?", remove.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%T rows remaining = %d", modelValue, count)
		}
	}
	var serverCount int64
	if err := db.Model(&model.Server{}).Where("id = ?", remove.ID).Count(&serverCount).Error; err != nil || serverCount != 0 {
		t.Fatalf("server remaining = %d, err=%v", serverCount, err)
	}
}

func TestUninstallAgentRequiresOnlineAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	srv := model.Server{Name: "offline", AgentToken: "offline-token"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}

	a := &App{db: db, hub: NewHub(db)}
	r := gin.New()
	r.POST("/servers/:id/uninstall-agent", a.uninstallAgent)
	req := httptest.NewRequest(http.MethodPost,
		"/servers/"+strconv.FormatUint(uint64(srv.ID), 10)+"/uninstall-agent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("uninstall status = %d, body = %s", w.Code, w.Body.String())
	}
	var count int64
	if err := db.Model(&model.Server{}).Where("id = ?", srv.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("panel node should be preserved: count=%d err=%v", count, err)
	}
}

func TestPersistServerStatusRefreshesMetricsWithoutBlankingLegacyHostInfo(t *testing.T) {
	db := testDB(t)
	srv := model.Server{
		Name: "node", AgentToken: "status-token", Hostname: "old-host", OS: "old-os",
		Arch: "amd64", Kernel: "old-kernel", PublicIP: "2001:db8::1",
		Load1: 1.5, MemUsed: 10, MemTotal: 20,
	}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}

	persistServerStatus(db, srv.ID, protocol.StatusData{
		SingboxInstalled: true,
		SingboxVersion:   "1.12.0",
	})
	var legacy model.Server
	if err := db.First(&legacy, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.Hostname != "old-host" || legacy.OS != "old-os" || legacy.Load1 != 1.5 {
		t.Fatalf("legacy status blanked host data: %+v", legacy)
	}

	persistServerStatus(db, srv.ID, protocol.StatusData{
		AgentVersion:     "agent-test",
		SingboxInstalled: true,
		SingboxVersion:   "1.13.0",
		ServiceActive:    true,
		Uptime:           1234,
		Hostname:         "new-host",
		OS:               "Debian 13",
		Arch:             "arm64",
		Kernel:           "6.12",
		PublicIP:         "203.0.113.8",
		Load1:            0.25,
		MemUsed:          30,
		MemTotal:         40,
	})
	var refreshed model.Server
	if err := db.First(&refreshed, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !refreshed.Online || refreshed.Hostname != "new-host" || refreshed.OS != "Debian 13" ||
		refreshed.Arch != "arm64" || refreshed.Kernel != "6.12" || refreshed.Load1 != 0.25 ||
		refreshed.MemUsed != 30 || refreshed.MemTotal != 40 || refreshed.PublicIP != "203.0.113.8" ||
		refreshed.SingboxVersion != "1.13.0" || refreshed.AgentVersion != "agent-test" ||
		!refreshed.SingboxActive || refreshed.Uptime != 1234 {
		t.Fatalf("status was not refreshed: %+v", refreshed)
	}
}

func TestRegisterPrefersReportedIPv4OverObservedIPv6(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "node", AgentToken: "register-token", PublicIP: "2001:db8::2"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}

	hub := NewHub(db)
	hub.onRegister(srv.ID, protocol.RegisterEvt{PublicIP: "198.51.100.9"})
	var got model.Server
	if err := db.First(&got, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.PublicIP != "198.51.100.9" {
		t.Fatalf("public IP = %q; want reported IPv4", got.PublicIP)
	}
}

func TestSupportedInboundTypesMatchManagedUI(t *testing.T) {
	want := map[string]bool{
		"shadowsocks": true,
		"snell":       true,
		"vless":       true,
		"anytls":      true,
		"vmess":       true,
		"tuic":        true,
		"trojan":      true,
		"hysteria2":   true,
		"socks":       true,
	}
	if len(supportedInboundTypes) != len(want) {
		t.Fatalf("supported inbound count = %d, want %d: %v", len(supportedInboundTypes), len(want), supportedInboundTypes)
	}
	for typ := range want {
		if !supportedInboundTypes[typ] {
			t.Errorf("managed inbound %q is missing", typ)
		}
	}
	for _, removed := range []string{"naive", "hysteria", "shadowtls"} {
		if supportedInboundTypes[removed] {
			t.Errorf("removed inbound %q is still creatable", removed)
		}
	}
}

func TestBlankInboundTagGetsProtocolPrefix(t *testing.T) {
	got := inboundTag("socks", "  ")
	if !strings.HasPrefix(got, "socks-") || len(got) != len("socks-")+6 {
		t.Fatalf("generated SOCKS tag = %q", got)
	}
	if got := inboundTag("socks", " custom "); got != "custom" {
		t.Fatalf("explicit tag = %q", got)
	}
}

func TestManagedConfigKeepsSocksOutboundCredentials(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "managed", AgentToken: "socks-outbound", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	settings := model.JSONText(`{"server":"proxy.example.com","server_port":1080,"username":"alice","password":"secret","settings":{}}`)
	if err := db.Create(&model.Outbound{ServerID: srv.ID, Tag: "socks-out", Type: "socks", Settings: settings}).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := NewOrchestrator(db, NewHub(db)).BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	var socks map[string]any
	for _, outbound := range cfg.Outbounds {
		if outbound["tag"] == "socks-out" {
			socks = outbound
			break
		}
	}
	if socks["username"] != "alice" || socks["password"] != "secret" {
		t.Fatalf("generated SOCKS outbound = %v", socks)
	}
}

func TestRuleSetRequestSupportsRemoteAndLocal(t *testing.T) {
	remote := ruleSetReq{Tag: " remote ", Type: "remote", Format: "binary", URL: " https://example.com/rules.srs ", UpdateInterval: "1d"}
	if err := normalizeRuleSetReq(&remote); err != nil {
		t.Fatal(err)
	}
	if remote.Tag != "remote" || remote.URL != "https://example.com/rules.srs" || remote.Path != "" {
		t.Fatalf("normalized remote rule-set = %+v", remote)
	}
	local := ruleSetReq{Tag: "local", Type: "local", Format: "source", Path: " /etc/sing-box/local.json ", URL: "stale"}
	if err := normalizeRuleSetReq(&local); err != nil {
		t.Fatal(err)
	}
	if local.Path != "/etc/sing-box/local.json" || local.URL != "" || local.UpdateInterval != "" {
		t.Fatalf("normalized local rule-set = %+v", local)
	}
	bad := ruleSetReq{Tag: "bad", Type: "local"}
	if err := normalizeRuleSetReq(&bad); err == nil {
		t.Fatal("local rule-set without path was accepted")
	}
}

func TestSocksSubscriptionsKeepOptionalAuthentication(t *testing.T) {
	settings := singbox.InboundSettings{SingleUser: true, Username: "alice", Password: "secret"}
	n := node{name: "socks", server: "proxy.example.com", port: 1080, typ: "socks", settings: settings}
	proxy := clashProxy(n, map[string]int{})
	if proxy == nil || proxy["type"] != "socks5" || proxy["username"] != "alice" || proxy["password"] != "secret" {
		t.Fatalf("Clash SOCKS proxy = %v", proxy)
	}
	line := surgeProxy(n, "socks")
	if !strings.Contains(line, "socks5") || !strings.Contains(line, "username=alice") || !strings.Contains(line, "password=secret") {
		t.Fatalf("Surge SOCKS proxy = %s", line)
	}
}

func TestNodeFormatItemsIncludeConnectionDetails(t *testing.T) {
	n := node{
		name: "node-socks", server: "192.0.2.10", port: 1080, typ: "socks",
		settings: singbox.InboundSettings{SingleUser: true, Username: "alice", Password: "secret"},
	}
	items := buildNodeFormatItems([]node{n})
	if len(items) != 1 {
		t.Fatalf("node format item count = %d", len(items))
	}
	item := items[0]
	if item.Server != "192.0.2.10" || item.Port != 1080 {
		t.Fatalf("connection details = %s:%d", item.Server, item.Port)
	}
	if item.Params["用户名"] != "alice" || item.Params["密码"] != "secret" {
		t.Fatalf("SOCKS params = %v", item.Params)
	}
}

func TestSelfSignedTLSForcesSkipVerificationInClashAndSurge(t *testing.T) {
	tls := singbox.TLSSettings{SelfSigned: true, ServerName: "example.com"}
	settings := singbox.InboundSettings{
		SingleUser: true,
		UUID:       "bf000d23-0752-40b4-affe-68f7707a9661",
		Password:   "secret",
		TLS:        tls,
	}
	clashTypes := []string{"vless", "vmess", "trojan", "hysteria2", "tuic", "anytls"}
	for _, typ := range clashTypes {
		t.Run("clash-"+typ, func(t *testing.T) {
			proxy := clashProxy(node{name: typ, server: "1.2.3.4", port: 443, typ: typ, settings: settings}, map[string]int{})
			if proxy == nil || proxy["skip-cert-verify"] != true {
				t.Fatalf("Clash %s skip-cert-verify = %v", typ, proxy)
			}
		})
	}

	for _, typ := range []string{"vmess", "trojan", "hysteria2", "tuic", "anytls"} {
		t.Run("surge-"+typ, func(t *testing.T) {
			line := surgeProxy(node{name: typ, server: "1.2.3.4", port: 443, typ: typ, settings: settings}, typ)
			if !strings.Contains(line, "skip-cert-verify=true") {
				t.Fatalf("Surge %s missing skip-cert-verify: %s", typ, line)
			}
		})
	}
}

func TestTrustedTLSDoesNotSkipVerificationInClashAndSurge(t *testing.T) {
	settings := singbox.InboundSettings{
		SingleUser: true,
		Password:   "secret",
		TLS: singbox.TLSSettings{
			Enabled:    true,
			ServerName: "example.com",
		},
	}
	n := node{name: "trojan", server: "example.com", port: 443, typ: "trojan", settings: settings}

	proxy := clashProxy(n, map[string]int{})
	if _, exists := proxy["skip-cert-verify"]; exists {
		t.Fatalf("trusted TLS unexpectedly skips verification in Clash: %v", proxy)
	}

	line := surgeProxy(n, "trojan")
	if strings.Contains(line, "skip-cert-verify=true") {
		t.Fatalf("trusted TLS unexpectedly skips verification in Surge: %s", line)
	}
}

func TestFrontendServesBundledClientLogos(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	logos := filepath.Join(dir, "logos")
	if err := os.MkdirAll(logos, 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("png-logo")
	if err := os.WriteFile(filepath.Join(logos, "clashmeta.png"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("spa-index"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SINGBOX_PANEL_WEB_DIR", dir)

	// 走一遍真实的 config 加载：二进制安装就是靠这个环境变量把 web/dist
	// 指到 /opt 下面的，直接构造 App{} 会跳过这条链路。
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	(&App{cfg: cfg}).mountFrontend(r)
	req := httptest.NewRequest(http.MethodGet, "/logos/clashmeta.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("logo status = %d, body = %q", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("logo response = %q, want PNG bytes", w.Body.Bytes())
	}
}

func TestFrontendIndexDisablesCaching(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("spa-index"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	(&App{cfg: config.PanelConfig{WebDir: dir}}).mountFrontend(r)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/users", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("index status = %d, body = %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q; want no-store", got)
	}
}
