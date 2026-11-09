package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
	"gorm.io/gorm"
)

func TestGlobalRoutingMigrationPreservesTargetsAndAudience(t *testing.T) {
	db := testDB(t)
	servers := []model.Server{{Name: "one", AgentToken: "global-one", FinalOutbound: "landing"}, {Name: "two", AgentToken: "global-two", FinalOutbound: "landing"}, {Name: "three", AgentToken: "global-three"}}
	if err := db.Create(&servers).Error; err != nil {
		t.Fatal(err)
	}
	for i, srv := range servers {
		if i < 2 {
			if err := db.Create(&model.Outbound{ServerID: srv.ID, Tag: "landing", Type: "socks", Settings: model.JSONText(fmt.Sprintf(`{"server":"proxy%d.example.com","server_port":1080,"username":"alice","password":"secret","settings":{}}`, i))}).Error; err != nil {
				t.Fatal(err)
			}
		}
		url := "https://example.com/shared.srs"
		if i == 1 {
			url = "https://example.com/different.srs"
		}
		if err := db.Create(&model.RuleSet{ServerID: srv.ID, Tag: "geo", Type: "remote", Format: "binary", URL: url, DownloadDetour: "direct"}).Error; err != nil {
			t.Fatal(err)
		}
		target := "direct"
		if i < 2 {
			target = "landing"
		}
		if err := db.Create(&model.RouteRule{ServerID: srv.ID, Outbound: target, Match: model.JSONText(`{"rule_set":["geo"]}`), Enabled: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := migrateGlobalOutbounds(tx); err != nil {
				return err
			}
			return migrateGlobalRuleSets(tx)
		}); err != nil {
			t.Fatal(err)
		}
	}
	var nodes []model.CustomNode
	db.Order("id").Find(&nodes)
	if len(nodes) != 2 {
		t.Fatalf("nodes=%d", len(nodes))
	}
	for _, n := range nodes {
		if n.AllUsers || len(n.UserIDs) > 0 || !n.Enabled {
			t.Fatalf("migration changed audience or availability: %+v", n)
		}
	}
	var legacy int64
	db.Model(&model.Outbound{}).Count(&legacy)
	if legacy != 0 {
		t.Fatal("legacy outbounds retained")
	}
	var sets []model.RuleSet
	db.Order("id").Find(&sets)
	if len(sets) != 2 {
		t.Fatalf("expected conflicting definition retained and identical merged, got %d", len(sets))
	}
	for _, rs := range sets {
		if rs.ServerID != 0 {
			t.Fatal("server-owned rule set retained")
		}
	}
	for i, srv := range servers {
		db.First(&srv, srv.ID)
		if i < 2 && srv.FinalOutbound != routingNodeTag(nodes[i].ID) {
			t.Fatalf("final not migrated: %s", srv.FinalOutbound)
		}
		raw, err := NewOrchestrator(db, NewHub(db)).BuildServerConfig(&srv)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Outbounds []map[string]any `json:"outbounds"`
			Route     struct {
				RuleSet []map[string]any `json:"rule_set"`
			} `json:"route"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		if len(cfg.Route.RuleSet) != 1 {
			t.Fatalf("unused rule sets leaked: %s", raw)
		}
		if i < 2 {
			found := false
			for _, ob := range cfg.Outbounds {
				if ob["tag"] == srv.FinalOutbound {
					found = true
					if ob["username"] != "alice" || ob["password"] != "secret" {
						t.Fatalf("credentials lost: %s", raw)
					}
				}
			}
			if !found {
				t.Fatalf("target missing: %s", raw)
			}
		}
	}
}

func TestGlobalRuleSetRenameAndDeleteAcrossServers(t *testing.T) {
	db := testDB(t)
	gin.SetMode(gin.TestMode)
	rs := model.RuleSet{Tag: "geo", Type: "remote", Format: "binary", URL: "https://example.com/geo.srs"}
	db.Create(&rs)
	for i := 1; i <= 2; i++ {
		srv := model.Server{Name: fmt.Sprint(i), AgentToken: fmt.Sprint("global", i), ConfigInitialized: true}
		db.Create(&srv)
		db.Create(&model.RouteRule{ServerID: srv.ID, Outbound: "direct", Enabled: i == 1, Match: model.JSONText(`{"rule_set":["geo"]}`)})
	}
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	r := gin.New()
	r.GET("/rulesets", app.listRuleSets)
	r.PUT("/rulesets/:rulesetID", app.updateRuleSet)
	r.DELETE("/rulesets/:rulesetID", app.deleteRuleSet)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	w := call(http.MethodPut, fmt.Sprintf("/rulesets/%d", rs.ID), `{"tag":"renamed","type":"remote","format":"binary","url":"https://example.com/new.srs"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
	var rules []model.RouteRule
	db.Find(&rules)
	for _, rule := range rules {
		var match singbox.RuleInput
		json.Unmarshal(rule.Match, &match)
		if len(match.RuleSet) != 1 || match.RuleSet[0] != "renamed" {
			t.Fatalf("reference not renamed: %s", rule.Match)
		}
	}
	w = call(http.MethodDelete, fmt.Sprintf("/rulesets/%d", rs.ID), "")
	if w.Code != http.StatusConflict {
		t.Fatalf("referenced deletion accepted: %d %s", w.Code, w.Body)
	}
	w = call(http.MethodGet, "/rulesets", "")
	if bytes.Contains(w.Body.Bytes(), []byte("server_id")) {
		t.Fatal("global response exposes owning server")
	}
}

func TestGlobalRoutingMigrationDoesNotCascadeGeneratedTags(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "collision", AgentToken: "collision", FinalOutbound: "alpha"}
	mustCreate(t, db, &srv)
	for i, tag := range []string{"alpha", "node-1"} {
		mustCreate(t, db, &model.Outbound{ServerID: srv.ID, Tag: tag, Type: "socks", Settings: model.JSONText(fmt.Sprintf(`{"server":"proxy%d.example.com","server_port":1080}`, i))})
		mustCreate(t, db, &model.RouteRule{ServerID: srv.ID, Outbound: tag, Match: model.JSONText(`{}`), Enabled: true})
	}
	global := model.RuleSet{Tag: "geo", URL: "https://global.example/geo.srs"}
	mustCreate(t, db, &global)
	firstTag := fmt.Sprintf("geo-server-%d", srv.ID)
	for i, tag := range []string{"geo", firstTag} {
		mustCreate(t, db, &model.RuleSet{ServerID: srv.ID, Tag: tag, URL: fmt.Sprintf("https://server.example/%d.srs", i)})
		mustCreate(t, db, &model.RouteRule{ServerID: srv.ID, Outbound: "direct", Match: model.JSONText(fmt.Sprintf(`{"rule_set":[%q]}`, tag)), Enabled: true})
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := migrateGlobalOutbounds(tx); err != nil {
			return err
		}
		return migrateGlobalRuleSets(tx)
	}); err != nil {
		t.Fatal(err)
	}
	db.First(&srv, srv.ID)
	if srv.FinalOutbound != "node-1" {
		t.Fatalf("final cascaded to wrong node: %s", srv.FinalOutbound)
	}
	var rules []model.RouteRule
	db.Order("id").Find(&rules)
	if rules[0].Outbound != "node-1" || rules[1].Outbound != "node-2" {
		t.Fatalf("node references cascaded: %+v", rules)
	}
	for i, r := range rules[2:] {
		var match singbox.RuleInput
		if err := json.Unmarshal(r.Match, &match); err != nil {
			t.Fatal(err)
		}
		var rs model.RuleSet
		if err := db.Where("server_id=0 AND tag = ?", match.RuleSet[0]).First(&rs).Error; err != nil {
			t.Fatal(err)
		}
		if rs.URL != fmt.Sprintf("https://server.example/%d.srs", i) {
			t.Fatalf("rule set reference cascaded: %s", r.Match)
		}
	}
}

func TestGlobalRoutingImportReusesLinkedAndSubscriptionNodeIdentity(t *testing.T) {
	for _, kind := range []string{"link", "subscription", "http", "hysteria"} {
		t.Run(kind, func(t *testing.T) {
			db := testDB(t)
			node := model.CustomNode{Name: "direct", Protocol: "socks", Address: "proxy.example.com", Port: 1080, Params: model.JSONText(`{"username":"alice","password":"secret"}`), Enabled: true}
			switch kind {
			case "link":
				node.Link = "socks5://alice:secret@proxy.example.com:1080#original"
				node.Protocol = ""
				node.Address = ""
				node.Port = 0
				node.Params = nil
			case "subscription":
				source := model.CustomNodeSubscription{Name: "source", URL: "https://example.com/sub", Enabled: true}
				mustCreate(t, db, &source)
				node.SubscriptionID = &source.ID
				node.SubscriptionKey = "stable"
			case "http":
				node.Protocol = "http"
				node.Port = 8080
			case "hysteria":
				node.Protocol = "hysteria"
				node.Port = 443
				node.Params = model.JSONText(`{"password":"secret","tls":"tls","sni":"example.com","up_mbps":30,"down_mbps":80}`)
			}
			mustCreate(t, db, &node)
			srv := model.Server{Name: "server", AgentToken: "import-reuse", FinalOutbound: routingNodeTag(node.ID), ConfigMode: model.ConfigModeManaged}
			mustCreate(t, db, &srv)
			app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
			raw, err := app.orch.BuildServerConfig(&srv)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := singbox.ParseServerConfig(raw)
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := app.applyImport(&srv, parsed, raw); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.First(&srv, srv.ID).Error; err != nil {
				t.Fatal(err)
			}
			var count int64
			db.Model(&model.CustomNode{}).Count(&count)
			if count != 1 || srv.FinalOutbound != routingNodeTag(node.ID) || srv.EffectiveConfigMode() != model.ConfigModeManaged {
				t.Fatalf("node identity/mode changed on import: count=%d final=%s mode=%s", count, srv.FinalOutbound, srv.ConfigMode)
			}
		})
	}
}

func TestGlobalRoutingMigrationRollsBackInvalidLegacyOutbound(t *testing.T) {
	db := testDB(t)
	srv := model.Server{AgentToken: "rollback", FinalOutbound: "valid"}
	mustCreate(t, db, &srv)
	mustCreate(t, db, &model.Outbound{ServerID: srv.ID, Tag: "valid", Type: "socks", Settings: model.JSONText(`{"server":"example.com","server_port":1080}`)})
	mustCreate(t, db, &model.Outbound{ServerID: srv.ID, Tag: "invalid", Type: "vless", Settings: model.JSONText(`{broken`)})
	if err := db.Transaction(migrateGlobalOutbounds); err == nil {
		t.Fatal("invalid legacy settings accepted")
	}
	var nodes, old int64
	db.Model(&model.CustomNode{}).Count(&nodes)
	db.Model(&model.Outbound{}).Count(&old)
	db.First(&srv, srv.ID)
	if nodes != 0 || old != 2 || srv.FinalOutbound != "valid" {
		t.Fatal("failed migration partially modified routing data")
	}
}
