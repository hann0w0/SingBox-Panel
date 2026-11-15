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
