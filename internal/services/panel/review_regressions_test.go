package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestInboundTLSModeSwitchPreservesOnlyCompatibleSecrets(t *testing.T) {
	old := singbox.InboundSettings{TLS: singbox.TLSSettings{
		Enabled: true, SelfSigned: true, Certificate: "existing-certificate", Key: "existing-key",
	}}
	for _, tc := range []struct {
		name string
		tls  singbox.TLSSettings
		keep bool
	}{
		{"self-signed", singbox.TLSSettings{Enabled: true, SelfSigned: true}, true},
		{"reality", singbox.TLSSettings{Enabled: true, Reality: singbox.RealitySettings{Enabled: true}}, false},
		{"acme", singbox.TLSSettings{Enabled: true, ACMEDomain: "example.com"}, false},
		{"paths", singbox.TLSSettings{Enabled: true, CertificatePath: "/etc/cert.pem", KeyPath: "/etc/key.pem"}, false},
		{"disabled", singbox.TLSSettings{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := singbox.InboundSettings{TLS: tc.tls}
			preserveInboundSecrets(&old, &next)
			if err := fillInboundSecrets("vless", &next); err != nil {
				t.Fatal(err)
			}
			if err := next.Validate("vless"); err != nil {
				t.Fatalf("TLS mode switch rejected: %v", err)
			}
			if tc.keep {
				if next.TLS.Certificate != old.TLS.Certificate || next.TLS.Key != old.TLS.Key {
					t.Fatal("editing self-signed settings changed its certificate")
				}
			} else if next.TLS.Certificate != "" || next.TLS.Key != "" {
				t.Fatal("old inline certificate leaked into another TLS mode")
			}
		})
	}
	old.TLS.SelfSigned = false
	next := singbox.InboundSettings{TLS: singbox.TLSSettings{Enabled: true}}
	preserveInboundSecrets(&old, &next)
	if next.TLS.Certificate != old.TLS.Certificate || next.TLS.Key != old.TLS.Key {
		t.Fatal("omitting inline PEM during an ordinary edit must preserve it")
	}

	old.TLS = singbox.TLSSettings{Enabled: true, Reality: singbox.RealitySettings{
		Enabled: true, PrivateKey: "existing-private", PublicKey: "existing-public", ShortID: []string{"01234567"},
	}}
	next.TLS = singbox.TLSSettings{Enabled: true, Reality: singbox.RealitySettings{Enabled: true}}
	preserveInboundSecrets(&old, &next)
	if !reflect.DeepEqual(next.TLS.Reality, old.TLS.Reality) {
		t.Fatal("ordinary REALITY edit changed its keys")
	}
}

func TestRuleSetDownloadDetourIntegrityAcrossOutboundEdits(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "node", AgentToken: "detour-fixture"}
	other := model.Server{Name: "other", AgentToken: "detour-other"}
	for _, row := range []*model.Server{&server, &other} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	outbound := model.Outbound{ServerID: server.ID, Tag: "landing", Type: "socks", Settings: model.JSONText(`{"server":"example.com","server_port":1080}`)}
	if err := db.Create(&outbound).Error; err != nil {
		t.Fatal(err)
	}
	ruleSet := model.RuleSet{ServerID: server.ID, Tag: "geo", Type: "remote", Format: "binary", URL: "https://example.com/geo.srs", DownloadDetour: "landing"}
	otherSet := model.RuleSet{ServerID: other.ID, Tag: "geo", Type: "remote", Format: "binary", URL: ruleSet.URL, DownloadDetour: "landing"}
	for _, row := range []*model.RuleSet{&ruleSet, &otherSet} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	router := gin.New()
	router.PUT("/servers/:id/outbounds/:outboundID", app.updateOutbound)
	router.DELETE("/servers/:id/outbounds/:outboundID", app.deleteOutbound)
	router.POST("/servers/:id/rulesets", app.createRuleSet)
	router.PUT("/servers/:id/rulesets/:rulesetID", app.updateRuleSet)
	request := func(method, path, body string, status int) {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, rec.Code, status, rec.Body.String())
		}
	}
	path := fmt.Sprintf("/servers/%d/outbounds/%d", server.ID, outbound.ID)
	request(http.MethodDelete, path, "", http.StatusConflict)
	request(http.MethodPut, path, `{"tag":"renamed"}`, http.StatusOK)
	for _, row := range []*model.RuleSet{&ruleSet, &otherSet} {
		if err := db.First(row, row.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	if ruleSet.DownloadDetour != "renamed" || otherSet.DownloadDetour != "landing" {
		t.Fatal("outbound rename did not update only its own server's rule-set reference")
	}
	request(http.MethodDelete, path, "", http.StatusConflict)
	for _, detour := range []string{"missing", "block", "sniff"} {
		body := fmt.Sprintf(`{"tag":"test","url":"https://example.com/geo.srs","download_detour":%q}`, detour)
		request(http.MethodPost, fmt.Sprintf("/servers/%d/rulesets", server.ID), body, http.StatusBadRequest)
		request(http.MethodPut, fmt.Sprintf("/servers/%d/rulesets/%d", server.ID, ruleSet.ID), body, http.StatusBadRequest)
	}
	request(http.MethodPut, fmt.Sprintf("/servers/%d/rulesets/%d", server.ID, ruleSet.ID),
		`{"tag":"geo","url":"https://example.com/geo.srs","download_detour":"direct"}`, http.StatusOK)
	request(http.MethodDelete, path, "", http.StatusOK)
}

func TestSubscriptionsAvoidGeneratedAndReservedNameCollisions(t *testing.T) {
	nodes := make([]node, 0)
	for _, name := range []string{"HK", "HK", "HK-2", "HK", "HK-2-2", "auto", "proxy", "direct"} {
		nodes = append(nodes, node{name: name, typ: "socks", server: "example.com", port: 1080})
	}
	for _, render := range []func([]node) ([]map[string]any, []string){clashProxies, shadowrocketProxies} {
		proxies, _ := render(nodes)
		seen := map[string]bool{}
		for _, proxy := range proxies {
			name := proxy["name"].(string)
			if seen[name] {
				t.Fatalf("subscription repeats proxy name %q", name)
			}
			seen[name] = true
		}
		if len(proxies) != len(nodes) {
			t.Fatal("name collision caused a node to be omitted")
		}
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	(&App{}).writeSingbox(ctx, nodes)
	var config struct {
		Outbounds []struct {
			Tag       string   `json:"tag"`
			Outbounds []string `json:"outbounds"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, outbound := range config.Outbounds {
		if seen[outbound.Tag] {
			t.Fatalf("sing-box subscription repeats tag %q", outbound.Tag)
		}
		seen[outbound.Tag] = true
	}
	if len(config.Outbounds) != len(nodes)+3 {
		t.Fatal("expected every node plus the three built-in outbounds")
	}
	for _, outbound := range config.Outbounds {
		for _, ref := range outbound.Outbounds {
			if !seen[ref] || ref == outbound.Tag {
				t.Fatalf("group %q has invalid member %q", outbound.Tag, ref)
			}
		}
	}
}

func TestImportPrunesDeletedInboundGrantsWithoutBroadeningAccess(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "node", AgentToken: "import-grants"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	removed := model.Inbound{ServerID: server.ID, Tag: "removed", Type: model.InboundVLESS, ListenPort: 1080}
	kept := model.Inbound{ServerID: server.ID, Tag: "kept", Type: model.InboundVLESS, ListenPort: 1081}
	for _, row := range []*model.Inbound{&removed, &kept} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	users := []model.User{
		{Email: "only-removed", SubToken: "only-removed", ServerIDs: []uint{server.ID}, InboundIDs: []uint{removed.ID}},
		{Email: "both", SubToken: "both", ServerIDs: []uint{server.ID}, InboundIDs: []uint{removed.ID, kept.ID}},
		{Email: "whole-server", SubToken: "whole-server", ServerIDs: []uint{server.ID}},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	raw := []byte(`{"inbounds":[{"type":"vless","tag":"kept","listen_port":1081,"users":[{"uuid":"11111111-2222-4333-8444-555555555555"}]}],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`)
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.applyImport(&server, parsed, raw); err != nil {
		t.Fatal(err)
	}
	for i := range users {
		if err := db.First(&users[i], users[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := app.validateUserNodeIDs(&users[i]); err != nil {
			t.Fatalf("import left invalid user grants: %v", err)
		}
	}
	if len(users[0].ServerIDs) != 0 || len(users[0].InboundIDs) != 0 || users[0].HasInbound(server.ID, kept.ID) {
		t.Fatal("deleting the final explicit inbound expanded access to the whole server")
	}
	if !reflect.DeepEqual(users[1].InboundIDs, []uint{kept.ID}) || !users[1].HasInbound(server.ID, kept.ID) {
		t.Fatal("import lost access to the retained inbound")
	}
	if len(users[2].InboundIDs) != 0 || !reflect.DeepEqual(users[2].ServerIDs, []uint{server.ID}) {
		t.Fatal("import changed an existing server-wide assignment")
	}
}

func TestTrafficAccountingKeepsBaselineAcrossUnavailableSamples(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "node", AgentToken: "traffic-baseline"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	if err := recordServerTraffic(db, server.ID, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	for _, step := range []struct {
		total     uint64
		available bool
		want      uint64
	}{
		{100, true, 0}, {200, true, 100}, {0, false, 100}, {500, true, 400},
		{0, false, 400}, {50, true, 450}, {80, true, 480},
	} {
		var snapshot *protocol.TrafficSnapshot
		if step.available {
			snapshot = &protocol.TrafficSnapshot{SampledAt: now.Unix(), UploadTotal: step.total, DownloadTotal: step.total * 2}
		}
		if err := recordServerTraffic(db, server.ID, snapshot); err != nil {
			t.Fatal(err)
		}
		if err := db.First(&server, server.ID).Error; err != nil {
			t.Fatal(err)
		}
		if server.TrafficUpload != step.want || server.TrafficDownload != step.want*2 || server.TrafficAvailable != step.available {
			t.Fatalf("total=%d available=%t: accumulated upload=%d download=%d", step.total, step.available, server.TrafficUpload, server.TrafficDownload)
		}
	}
	var bucket model.TrafficRecord
	if err := db.Where("server_id = ? AND inbound_id = 0", server.ID).First(&bucket).Error; err != nil {
		t.Fatal(err)
	}
	if bucket.Upload != 480 || bucket.Download != 960 {
		t.Fatalf("stored history lost traffic: upload=%d download=%d", bucket.Upload, bucket.Download)
	}
}

func TestLegacyCustomNodeAudienceMigrationsStoreJSONArray(t *testing.T) {
	db := testDB(t)
	if err := db.Exec("ALTER TABLE custom_nodes ADD COLUMN user_id integer").Error; err != nil {
		t.Fatal(err)
	}
	global := model.CustomNode{Name: "global"}
	scoped := model.CustomNode{Name: "scoped"}
	for _, row := range []*model.CustomNode{&global, &scoped} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&scoped).UpdateColumn("user_id", 7).Error; err != nil {
		t.Fatal(err)
	}
	for _, migration := range applicationMigrations {
		if migration.version == 8 || migration.version == 9 {
			if err := migration.up(db); err != nil {
				t.Fatalf("migration %d: %v", migration.version, err)
			}
		}
	}
	for _, row := range []*model.CustomNode{&global, &scoped} {
		if err := db.First(row, row.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	if !global.AllUsers || scoped.AllUsers || !reflect.DeepEqual(scoped.UserIDs, []uint{7}) {
		t.Fatal("migration changed the legacy audience")
	}
	var stored struct {
		UserIDs         string
		ExcludedUserIDs string
	}
	if err := db.Table("custom_nodes").Select("user_ids", "excluded_user_ids").Where("id = ?", scoped.ID).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.UserIDs != "[7]" || stored.ExcludedUserIDs != "[]" {
		t.Fatalf("audience columns are not JSON arrays: %+v", stored)
	}
}
