package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

type forwardFixture struct {
	app      *App
	db       *gorm.DB
	router   *gin.Engine
	server   model.Server
	user     model.User
	inbound  model.Inbound
	outbound model.Outbound
}

func newForwardFixture(t *testing.T) *forwardFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	fixture := &forwardFixture{db: db}
	fixture.server = model.Server{Name: "entry", Address: "entry.example", AgentToken: "entry", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &fixture.server)
	fixture.user = model.User{Email: "alice", Role: model.RoleUser, Enabled: true, ForwardingEnabled: true, ServerIDs: []uint{fixture.server.ID}, SubToken: "alice-sub", ProxyToken: "alice-seed"}
	mustCreate(t, db, &fixture.user)
	fixture.inbound = model.Inbound{ServerID: fixture.server.ID, Tag: "entry-in", Type: model.InboundVLESS, ListenPort: 443, Enabled: true, Settings: model.JSONText(`{"multi_user":true}`)}
	mustCreate(t, db, &fixture.inbound)
	fixture.outbound = model.Outbound{OwnerUserID: fixture.user.ID, Tag: "alice-home", Type: "vless", Settings: model.JSONText(`{"server":"home.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`)}
	mustCreate(t, db, &fixture.outbound)
	fixture.app = &App{db: db, orch: NewOrchestrator(db, NewHub(db)), serverOperations: newKeyedMutex[uint]()}
	t.Cleanup(func() { waitForSubscriptionResetPush(t, fixture.app.orch) })
	fixture.router = gin.New()
	fixture.router.Use(func(context *gin.Context) { context.Set("uid", fixture.user.ID) })
	fixture.router.POST("/forwards", fixture.app.createUserForward)
	fixture.router.DELETE("/forwards/:forwardID", fixture.app.deleteUserForward)
	fixture.router.POST("/reset", fixture.app.handleResetSub)
	fixture.router.DELETE("/servers/:id", fixture.app.deleteServer)
	return fixture
}

func (fixture *forwardFixture) create(t *testing.T) model.UserForward {
	t.Helper()
	body := fmt.Sprintf(`{"inbound_server_id":%d,"inbound_tag":%q,"outbound_id":%d}`, fixture.server.ID, fixture.inbound.Tag, fixture.outbound.ID)
	response := serveJSONRequest(t, fixture.router, http.MethodPost, "/forwards", body)
	if response.Code != http.StatusOK {
		t.Fatalf("create forward: %d %s", response.Code, response.Body)
	}
	var forward model.UserForward
	if err := fixture.db.Where("user_id = ?", fixture.user.ID).First(&forward).Error; err != nil {
		t.Fatal(err)
	}
	return forward
}

func TestSubscriptionResetRotatesForwardAndAccountRoutes(t *testing.T) {
	fixture := newForwardFixture(t)
	forward := fixture.create(t)
	oldName := forwardTrafficName(&fixture.user, forward.ID)
	other := model.User{Email: "bob", SubToken: "bob", ProxyToken: "bob-seed"}
	mustCreate(t, fixture.db, &other)
	manual := model.RouteRule{ServerID: fixture.server.ID, Outbound: "direct", Enabled: true, Match: model.JSONText(`{"auth_user":["` + proxyTrafficName(&fixture.user) + `","` + proxyTrafficName(&other) + `"],"domain_suffix":["example.org"]}`)}
	mustCreate(t, fixture.db, &manual)
	response := serveJSONRequest(t, fixture.router, http.MethodPost, "/reset", "")
	if response.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", response.Code, response.Body)
	}
	if err := fixture.db.First(&fixture.user, fixture.user.ID).Error; err != nil {
		t.Fatal(err)
	}
	var rules []model.RouteRule
	fixture.db.Where("server_id = ?", fixture.server.ID).Order("id").Find(&rules)
	var derivedMatch, manualMatch singbox.RuleInput
	if len(rules) != 2 || json.Unmarshal(rules[0].Match, &derivedMatch) != nil || json.Unmarshal(rules[1].Match, &manualMatch) != nil {
		t.Fatalf("rules = %+v", rules)
	}
	if derivedMatch.AuthUser[0] == oldName || derivedMatch.AuthUser[0] != forwardTrafficName(&fixture.user, forward.ID) {
		t.Fatalf("stale forward identity: %+v", derivedMatch)
	}
	if manualMatch.AuthUser[0] != proxyTrafficName(&fixture.user) || manualMatch.AuthUser[1] != proxyTrafficName(&other) || len(manualMatch.DomainSuffix) != 1 {
		t.Fatalf("account route rotation changed other matches: %+v", manualMatch)
	}
	response = serveJSONRequest(t, fixture.router, http.MethodDelete, fmt.Sprintf("/forwards/%d", forward.ID), "")
	if response.Code != http.StatusOK {
		t.Fatalf("delete after reset: %d %s", response.Code, response.Body)
	}
	var remaining int64
	fixture.db.Model(&model.RouteRule{}).Where("outbound = ?", fixture.outbound.Tag).Count(&remaining)
	if remaining != 0 {
		t.Fatalf("forward route survived deletion: %d", remaining)
	}
}

func TestForwardCreationRollsBackWhenRouteWriteFails(t *testing.T) {
	fixture := newForwardFixture(t)
	if err := fixture.db.Callback().Create().Before("gorm:create").Register("reject_forward_rule", func(tx *gorm.DB) {
		if tx.Statement.Table == "route_rules" {
			tx.AddError(errors.New("route write failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"inbound_tag":%q,"outbound_id":%d}`, fixture.inbound.Tag, fixture.outbound.ID)
	response := serveJSONRequest(t, fixture.router, http.MethodPost, "/forwards", body)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body)
	}
	var count int64
	fixture.db.Model(&model.UserForward{}).Count(&count)
	if count != 0 {
		t.Fatalf("partially created forwards: %d", count)
	}
}

func TestForwardDeletionRollsBackAndCleansStaleIdentity(t *testing.T) {
	fixture := newForwardFixture(t)
	forward := fixture.create(t)
	old := fixture.user
	old.ProxyToken = "stale-seed"
	staleMatch, _ := json.Marshal(singbox.RuleInput{AuthUser: []string{forwardTrafficName(&old, forward.ID)}, Inbound: []string{fixture.inbound.Tag}})
	fixture.db.Model(&model.RouteRule{}).Where("server_id = ?", fixture.server.ID).Update("match", model.JSONText(staleMatch))
	mustCreate(t, fixture.db, &model.UserNodeOrder{UserID: fixture.user.ID, NodeType: userNodeTypeForward, NodeID: forward.ID})
	if err := fixture.db.Callback().Delete().Before("gorm:delete").Register("reject_forward_rule_delete", func(tx *gorm.DB) {
		if tx.Statement.Table == "route_rules" {
			tx.AddError(errors.New("rule delete failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/forwards/%d", forward.ID)
	response := serveJSONRequest(t, fixture.router, http.MethodDelete, path, "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body)
	}
	for _, row := range []any{&model.UserForward{}, &model.RouteRule{}, &model.UserNodeOrder{}} {
		var count int64
		fixture.db.Model(row).Count(&count)
		if count != 1 {
			t.Fatalf("failed deletion changed %T: %d", row, count)
		}
	}
	fixture.db.Callback().Delete().Remove("reject_forward_rule_delete")
	response = serveJSONRequest(t, fixture.router, http.MethodDelete, path, "")
	if response.Code != http.StatusOK {
		t.Fatalf("retry delete status=%d body=%s", response.Code, response.Body)
	}
	for _, row := range []any{&model.UserForward{}, &model.RouteRule{}, &model.UserNodeOrder{}} {
		var count int64
		fixture.db.Model(row).Count(&count)
		if count != 0 {
			t.Fatalf("deletion left %T: %d", row, count)
		}
	}
}

func TestServerDeletionCleansDerivedForwards(t *testing.T) {
	fixture := newForwardFixture(t)
	forward := fixture.create(t)
	mustCreate(t, fixture.db, &model.UserNodeOrder{UserID: fixture.user.ID, NodeType: userNodeTypeForward, NodeID: forward.ID})
	response := serveJSONRequest(t, fixture.router, http.MethodDelete, fmt.Sprintf("/servers/%d", fixture.server.ID), "")
	if response.Code != http.StatusOK {
		t.Fatalf("delete server: %d %s", response.Code, response.Body)
	}
	for _, row := range []any{&model.UserForward{}, &model.RouteRule{}, &model.UserNodeOrder{}} {
		var count int64
		fixture.db.Model(row).Count(&count)
		if count != 0 {
			t.Fatalf("server deletion left %T: %d", row, count)
		}
	}
}

func TestRawServersCannotOfferOrCreateForwards(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.db.Model(&fixture.server).Updates(map[string]any{"config_mode": model.ConfigModeRaw, "raw_config": model.JSONText(`{"inbounds":[]}`)})
	fixture.router.GET("/inbounds", fixture.app.listUserInbounds)
	response := serveJSONRequest(t, fixture.router, http.MethodGet, "/inbounds", "")
	var listed struct {
		Inbounds []UserInboundOption `json:"inbounds"`
	}
	if json.Unmarshal(response.Body.Bytes(), &listed) != nil || len(listed.Inbounds) != 0 {
		t.Fatalf("raw inbound offered: %s", response.Body)
	}
	body := fmt.Sprintf(`{"inbound_tag":%q,"outbound_id":%d}`, fixture.inbound.Tag, fixture.outbound.ID)
	response = serveJSONRequest(t, fixture.router, http.MethodPost, "/forwards", body)
	if response.Code != http.StatusConflict {
		t.Fatalf("raw forward accepted: %d %s", response.Code, response.Body)
	}
}

func TestForwardRoutesRecoverStaleNamesAndPrecedeGeneralRules(t *testing.T) {
	fixture := newForwardFixture(t)
	forward := fixture.create(t)
	fixture.db.Where("server_id = ?", fixture.server.ID).Delete(&model.RouteRule{})
	old := fixture.user
	old.ProxyToken = "previous-seed"
	match, _ := json.Marshal(singbox.RuleInput{AuthUser: []string{forwardTrafficName(&old, forward.ID)}, Inbound: []string{fixture.inbound.Tag}})
	mustCreate(t, fixture.db, &model.RouteRule{ServerID: fixture.server.ID, Outbound: fixture.outbound.Tag, Match: model.JSONText(match), Enabled: true})
	mustCreate(t, fixture.db, &model.RouteRule{ServerID: fixture.server.ID, Outbound: "direct", Match: model.JSONText(`{"inbound":["entry-in"]}`), Sort: -100, Enabled: true})
	raw, err := fixture.app.orch.BuildServerConfig(&fixture.server)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Route struct {
			Rules []struct {
				AuthUser []string `json:"auth_user"`
				Outbound string   `json:"outbound"`
			} `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, rule := range config.Route.Rules {
		if rule.Outbound == "direct" && !matched {
			t.Fatal("general route precedes derived forward")
		}
		if rule.Outbound == fixture.outbound.Tag {
			if matched || len(rule.AuthUser) != 1 || rule.AuthUser[0] != forwardTrafficName(&fixture.user, forward.ID) {
				t.Fatalf("duplicate or stale forward: %+v", rule)
			}
			matched = true
		}
	}
	if !matched {
		t.Fatal("derived forward not rebuilt")
	}
}
