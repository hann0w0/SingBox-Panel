package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestRuleSetSourceMatchAPIAndConfig(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "source match", AgentToken: "source-match-test"}
	mustCreate(t, db, &server)
	mustCreate(t, db, &model.Inbound{ServerID: server.ID, Tag: "dmit-in", Type: model.InboundSocks,
		ListenPort: 1080, Enabled: true, Settings: model.JSONText(`{"username":"test","password":"test"}`)})
	ruleSet := model.RuleSet{Tag: "geoip-cn", Type: "local", Format: "source", Path: "/etc/sing-box/cn.json"}
	mustCreate(t, db, &ruleSet)
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	router := gin.New()
	router.POST("/servers/:id/rules", app.createRule)
	router.PUT("/servers/:id/rules/:ruleID", app.updateRule)
	path := fmt.Sprintf("/servers/%d/rules", server.ID)
	response := serveJSONRequest(t, router, http.MethodPost, path,
		`{"outbound":"block","match":{"action":"reject","inbound":["dmit-in"],"rule_set":["geoip-cn"],"rule_set_ip_cidr_match_source":true}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("create source-IP rejection: %d %s", response.Code, response.Body)
	}
	var stored model.RouteRule
	if err := db.Where("server_id = ?", server.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	// A source match without a rule set must be rejected at the API boundary,
	// leaving the existing CN restriction intact.
	response = serveJSONRequest(t, router, http.MethodPut, fmt.Sprintf("%s/%d", path, stored.ID),
		`{"match":{"action":"reject","inbound":["dmit-in"],"rule_set_ip_cidr_match_source":true}}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "必须选择规则集") {
		t.Fatalf("accepted source matching without a rule set: %d %s", response.Code, response.Body)
	}
	if err := db.Model(&ruleSet).Update("tag", "cn-renamed").Error; err != nil {
		t.Fatal(err)
	}
	if err := renameRuleSetRefs(db, 0, "geoip-cn", "cn-renamed"); err != nil {
		t.Fatal(err)
	}
	raw, err := app.orch.BuildServerConfig(&server)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Route struct {
			Rules []singbox.RuleInput `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	for _, rule := range config.Route.Rules {
		if len(rule.RuleSet) == 0 {
			continue
		}
		if rule.Action != "reject" || !rule.RuleSetIPCIDRMatchSource ||
			len(rule.Inbound) != 1 || rule.Inbound[0] != "dmit-in" || rule.RuleSet[0] != "cn-renamed" {
			t.Fatalf("CN source restriction changed during save/rename/render: %s", raw)
		}
		return
	}
	t.Fatalf("CN source restriction missing from generated config: %s", raw)
}
