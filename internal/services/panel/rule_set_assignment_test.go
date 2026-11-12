package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestDeletingLastRuleReleasesRuleSet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{Name: "node-a", AgentToken: "node-a-token", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &server)
	ruleSet := model.RuleSet{Tag: "geosite-cn", Type: "remote", Format: "binary", URL: "https://example.com/cn.srs"}
	mustCreate(t, db, &ruleSet)
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db)), serverOperations: newKeyedMutex[uint]()}
	router := gin.New()
	router.POST("/servers/:id/rules", app.createRule)
	router.DELETE("/servers/:id/rules/:ruleID", app.deleteRule)
	router.GET("/rulesets", app.listSharedRuleSets)

	body := `{"outbound":"block","match":{"action":"reject","rule_set":["geosite-cn"]}}`
	created := serveJSONRequest(t, router, http.MethodPost, fmt.Sprintf("/servers/%d/rules", server.ID), body)
	if created.Code != http.StatusOK {
		t.Fatalf("create rule: %d %s", created.Code, created.Body)
	}
	var rule model.RouteRule
	if err := db.Where("server_id = ?", server.ID).First(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if countServerRuleSets(t, db, server.ID) != 1 {
		t.Fatal("rule-set was not attached")
	}

	deleted := serveJSONRequest(t, router, http.MethodDelete, fmt.Sprintf("/servers/%d/rules/%d", server.ID, rule.ID), "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete rule: %d %s", deleted.Code, deleted.Body)
	}
	if got := countServerRuleSets(t, db, server.ID); got != 0 {
		t.Fatalf("rule-set assignment remained after last rule was deleted: %d", got)
	}
	raw, err := app.orch.BuildServerConfig(&server)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Route struct {
			RuleSet []singbox.RuleSetInput `json:"rule_set"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Route.RuleSet) != 0 {
		t.Fatalf("unreferenced rule-set remained in generated config: %s", raw)
	}
	list := serveJSONRequest(t, router, http.MethodGet, "/rulesets", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list rule-sets: %d %s", list.Code, list.Body)
	}
	var listed struct {
		RuleSets []struct {
			ServerIDs []uint `json:"server_ids"`
		} `json:"rulesets"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.RuleSets) != 1 || len(listed.RuleSets[0].ServerIDs) != 0 {
		t.Fatalf("rule-set list still reports a server reference: %s", list.Body)
	}
}

func TestDeletingOneRuleKeepsSharedRuleSetForRemainingReference(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{Name: "node-b", AgentToken: "node-b-token", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &server)
	ruleSet := model.RuleSet{Tag: "geosite-cn", Type: "remote", Format: "binary", URL: "https://example.com/cn.srs"}
	mustCreate(t, db, &ruleSet)
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db)), serverOperations: newKeyedMutex[uint]()}
	router := gin.New()
	router.POST("/servers/:id/rules", app.createRule)
	router.DELETE("/servers/:id/rules/:ruleID", app.deleteRule)
	firstBody := `{"outbound":"block","match":{"action":"reject","rule_set":["geosite-cn"],"domain":["a.example"]}}`
	secondBody := `{"outbound":"block","match":{"action":"reject","rule_set":["geosite-cn"],"domain":["b.example"]}}`
	for _, body := range []string{firstBody, secondBody} {
		if res := serveJSONRequest(t, router, http.MethodPost, fmt.Sprintf("/servers/%d/rules", server.ID), body); res.Code != http.StatusOK {
			t.Fatalf("create rule: %d %s", res.Code, res.Body)
		}
	}
	var first model.RouteRule
	if err := db.Where("server_id = ? AND match LIKE ?", server.ID, "%a.example%").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	if res := serveJSONRequest(t, router, http.MethodDelete, fmt.Sprintf("/servers/%d/rules/%d", server.ID, first.ID), ""); res.Code != http.StatusOK {
		t.Fatalf("delete rule: %d %s", res.Code, res.Body)
	}
	if got := countServerRuleSets(t, db, server.ID); got != 1 {
		t.Fatalf("shared rule-set assignment was dropped while another rule still references it: %d", got)
	}
}

func TestRuleSetListReportsOnlyReferencedServers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	referenced := model.Server{Name: "node-a", AgentToken: "referenced-token", ConfigMode: model.ConfigModeManaged}
	stale := model.Server{Name: "node-b", AgentToken: "stale-token", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &referenced)
	mustCreate(t, db, &stale)
	ruleSet := model.RuleSet{Tag: "geosite-cn", Type: "remote", Format: "binary", URL: "https://example.com/cn.srs"}
	mustCreate(t, db, &ruleSet)
	mustCreate(t, db, &model.ServerRuleSet{ServerID: referenced.ID, RuleSetID: ruleSet.ID})
	mustCreate(t, db, &model.ServerRuleSet{ServerID: stale.ID, RuleSetID: ruleSet.ID})
	mustCreate(t, db, &model.RouteRule{
		ServerID: referenced.ID, Outbound: "block",
		Match: model.JSONText(`{"action":"reject","rule_set":["geosite-cn"]}`),
	})
	app := &App{db: db}
	router := gin.New()
	router.GET("/rulesets", app.listSharedRuleSets)
	response := serveJSONRequest(t, router, http.MethodGet, "/rulesets", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list rule-sets: %d %s", response.Code, response.Body)
	}
	var listed struct {
		RuleSets []struct {
			ServerIDs []uint `json:"server_ids"`
		} `json:"rulesets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.RuleSets) != 1 || len(listed.RuleSets[0].ServerIDs) != 1 || listed.RuleSets[0].ServerIDs[0] != referenced.ID {
		t.Fatalf("stale assignment was reported as a real reference: %s", response.Body)
	}
}

func TestPruneUnreferencedRuleSetAssignmentsMigration(t *testing.T) {
	db := testDB(t)
	referenced := model.Server{Name: "node-a", AgentToken: "migration-referenced", ConfigMode: model.ConfigModeManaged}
	stale := model.Server{Name: "node-b", AgentToken: "migration-stale", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &referenced)
	mustCreate(t, db, &stale)
	ruleSet := model.RuleSet{Tag: "geosite-cn", Type: "remote", Format: "binary", URL: "https://example.com/cn.srs"}
	mustCreate(t, db, &ruleSet)
	for _, server := range []model.Server{referenced, stale} {
		mustCreate(t, db, &model.ServerRuleSet{ServerID: server.ID, RuleSetID: ruleSet.ID})
	}
	mustCreate(t, db, &model.RouteRule{
		ServerID: referenced.ID, Outbound: "block",
		Match: model.JSONText(`{"action":"reject","rule_set":["geosite-cn"]}`),
	})
	if err := pruneUnreferencedRuleSetAssignments(db); err != nil {
		t.Fatal(err)
	}
	if got := countServerRuleSets(t, db, referenced.ID); got != 1 {
		t.Fatalf("referenced assignment was pruned: %d", got)
	}
	if got := countServerRuleSets(t, db, stale.ID); got != 0 {
		t.Fatalf("unreferenced assignment survived migration: %d", got)
	}
}

func countServerRuleSets(t *testing.T, db *gorm.DB, serverID uint) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&model.ServerRuleSet{}).Where("server_id = ?", serverID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}
