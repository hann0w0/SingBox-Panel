package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestUserForwardAPIOnlyAcceptsOwnPrivateOutbound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{Name: "entry", Address: "entry.example", AgentToken: "entry", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &server)
	user := model.User{Email: "alice", Role: model.RoleUser, Enabled: true, ForwardingEnabled: true, ServerIDs: []uint{server.ID}, SubToken: "alice", ProxyToken: "alice-proxy"}
	mustCreate(t, db, &user)
	other := model.User{Email: "bob", Role: model.RoleUser, Enabled: true, ServerIDs: []uint{server.ID}, SubToken: "bob", ProxyToken: "bob-proxy"}
	mustCreate(t, db, &other)
	inbound := model.Inbound{ServerID: server.ID, Tag: "multi-in", Type: model.InboundVLESS, ListenPort: 443, Enabled: true, Settings: model.JSONText(`{"multi_user":true}`)}
	mustCreate(t, db, &inbound)
	outbound := model.Outbound{ServerID: 0, OwnerUserID: user.ID, Tag: "alice-home", Type: "vless", Settings: model.JSONText(`{"server":"home.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`)}
	mustCreate(t, db, &outbound)
	foreign := model.Outbound{ServerID: 0, OwnerUserID: other.ID, Tag: "bob-home", Type: "vless", Settings: model.JSONText(`{"server":"bob.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`)}
	mustCreate(t, db, &foreign)
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	router := gin.New()
	router.POST("/user/forwards", func(c *gin.Context) { c.Set("uid", user.ID) }, app.createUserForward)
	router.GET("/user/forwards", func(c *gin.Context) { c.Set("uid", user.ID) }, app.listUserForwards)
	create := func(body string) *httptest.ResponseRecorder {
		return serveJSONRequest(t, router, http.MethodPost, "/user/forwards", body)
	}
	response := create(fmt.Sprintf(`{"inbound_server_id":%d,"inbound_tag":"multi-in","outbound_id":%d}`, server.ID, outbound.ID))
	if response.Code != http.StatusOK {
		t.Fatalf("create own private forward: %d %s", response.Code, response.Body)
	}
	response = create(fmt.Sprintf(`{"inbound_server_id":%d,"inbound_tag":"multi-in","outbound_id":%d}`, server.ID, foreign.ID))
	if response.Code != http.StatusNotFound {
		t.Fatalf("foreign private outbound accepted: %d %s", response.Code, response.Body)
	}
	response = create(fmt.Sprintf(`{"inbound_server_id":%d,"inbound_tag":"multi-in","outbound_id":%d}`, server.ID, outbound.ID))
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate forward accepted: %d %s", response.Code, response.Body)
	}
	var forwards []model.UserForward
	if err := db.Where("user_id = ?", user.ID).Find(&forwards).Error; err != nil {
		t.Fatal(err)
	}
	if len(forwards) != 1 {
		t.Fatalf("forwards = %d, want one", len(forwards))
	}
	var rules []model.RouteRule
	if err := db.Where("server_id = ? AND outbound = ?", server.ID, outbound.Tag).Find(&rules).Error; err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("route rules = %d, want one", len(rules))
	}
	var match struct {
		AuthUser []string `json:"auth_user"`
	}
	if err := json.Unmarshal(rules[0].Match, &match); err != nil {
		t.Fatal(err)
	}
	if len(match.AuthUser) != 1 || match.AuthUser[0] != forwardTrafficName(&user, forwards[0].ID) {
		t.Fatalf("auth_user = %v", match.AuthUser)
	}
	nodes, err := app.userNodeDetails(&user)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("derived subscription nodes = %d, want original + forward", len(nodes))
	}
	if nodes[0].Params["UUID"] == nodes[1].Params["UUID"] {
		t.Fatal("derived node reused the original node credentials")
	}
	if !strings.Contains(nodes[1].Name, "→") {
		t.Fatalf("derived node name = %q, want inbound → outbound label", nodes[1].Name)
	}
	if got := nodes[0].Params["UUID"]; got == "" || got == nodes[1].Params["UUID"] {
		t.Fatalf("derived node did not receive its own credential: %q vs %q", got, nodes[1].Params["UUID"])
	}

	listed := serveJSONRequest(t, router, http.MethodGet, "/user/forwards", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list forwards: %d %s", listed.Code, listed.Body)
	}
	var listedForwards struct {
		Forwards []UserForward `json:"forwards"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listedForwards); err != nil {
		t.Fatal(err)
	}
	if len(listedForwards.Forwards) != 1 || listedForwards.Forwards[0].OutboundID != outbound.ID || listedForwards.Forwards[0].ServerID != server.ID {
		t.Fatalf("forwards = %+v", listedForwards.Forwards)
	}

	raw, err := app.orch.BuildServerConfig(&server)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []struct {
			Users []struct {
				Name     string `json:"name"`
				UUID     string `json:"uuid"`
				Password string `json:"password"`
			} `json:"users"`
		} `json:"inbounds"`
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
	if len(config.Inbounds) != 1 || len(config.Inbounds[0].Users) != 3 {
		t.Fatalf("inbound users = %+v, want both accounts and the forward identity", config.Inbounds)
	}
	names := make([]string, 0, len(config.Inbounds[0].Users))
	for _, entry := range config.Inbounds[0].Users {
		names = append(names, entry.Name)
	}
	if !containsString(names, proxyTrafficName(&user)) || !containsString(names, forwardTrafficName(&user, forwards[0].ID)) {
		t.Fatalf("inbound identities = %v", names)
	}
	original, forwardIdentity := "", ""
	for _, entry := range config.Inbounds[0].Users {
		switch entry.Name {
		case proxyTrafficName(&user):
			original = entry.UUID
		case forwardTrafficName(&user, forwards[0].ID):
			forwardIdentity = entry.UUID
		}
	}
	if original == "" || forwardIdentity == "" || original == forwardIdentity {
		t.Fatalf("original and forward identities are not distinct: %+v", config.Inbounds[0].Users)
	}
	found := false
	for _, rule := range config.Route.Rules {
		if len(rule.AuthUser) == 1 && rule.AuthUser[0] == forwardTrafficName(&user, forwards[0].ID) && rule.Outbound == outbound.Tag {
			found = true
		}
	}
	if !found {
		t.Fatalf("forward route missing: %s", raw)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestUserForwardRequiresExplicitPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{Name: "entry", Address: "entry.example", AgentToken: "entry", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &server)
	user := model.User{Email: "alice", Role: model.RoleUser, Enabled: true, ServerIDs: []uint{server.ID}, SubToken: "alice", ProxyToken: "alice-proxy"}
	mustCreate(t, db, &user)
	inbound := model.Inbound{ServerID: server.ID, Tag: "multi-in", Type: model.InboundVLESS, ListenPort: 443, Enabled: true, Settings: model.JSONText(`{"multi_user":true}`)}
	mustCreate(t, db, &inbound)
	outbound := model.Outbound{ServerID: 0, OwnerUserID: user.ID, Tag: "alice-home", Type: "vless", Settings: model.JSONText(`{"server":"home.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`)}
	mustCreate(t, db, &outbound)
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	router := gin.New()
	router.POST("/user/forwards", func(c *gin.Context) { c.Set("uid", user.ID) }, app.createUserForward)
	router.GET("/user/forwards", func(c *gin.Context) { c.Set("uid", user.ID) }, app.listUserForwards)
	body := fmt.Sprintf(`{"inbound_server_id":%d,"inbound_tag":"multi-in","outbound_id":%d}`, server.ID, outbound.ID)

	response := serveJSONRequest(t, router, http.MethodPost, "/user/forwards", body)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forward without permission = %d, want 403: %s", response.Code, response.Body)
	}
	list := serveJSONRequest(t, router, http.MethodGet, "/user/forwards", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"forwards":[]`) {
		t.Fatalf("list without permission = %d %s", list.Code, list.Body)
	}

	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("forwarding_enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	response = serveJSONRequest(t, router, http.MethodPost, "/user/forwards", body)
	if response.Code != http.StatusOK {
		t.Fatalf("forward with permission = %d, want 200: %s", response.Code, response.Body)
	}

	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("forwarding_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	revoked := user
	revoked.ForwardingEnabled = false
	nodes, err := app.userNodeDetails(&revoked)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("derived nodes after permission revoked = %d, want original only", len(nodes))
	}
}

func TestRevokingForwardingPermissionRemovesDerivedState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{Name: "entry", Address: "entry.example", AgentToken: "entry", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &server)
	user := model.User{Email: "alice", Role: model.RoleUser, Enabled: true, ForwardingEnabled: true, ServerIDs: []uint{server.ID}, SubToken: "alice", ProxyToken: "alice-proxy"}
	mustCreate(t, db, &user)
	inbound := model.Inbound{ServerID: server.ID, Tag: "multi-in", Type: model.InboundVLESS, ListenPort: 443, Enabled: true, Settings: model.JSONText(`{"multi_user":true}`)}
	mustCreate(t, db, &inbound)
	outbound := model.Outbound{ServerID: 0, OwnerUserID: user.ID, Tag: "alice-home", Type: "vless", Settings: model.JSONText(`{"server":"home.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`)}
	mustCreate(t, db, &outbound)
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	router := gin.New()
	router.POST("/user/forwards", func(c *gin.Context) { c.Set("uid", user.ID) }, app.createUserForward)
	router.PUT("/admin/users/:id", app.updateUser)
	body := fmt.Sprintf(`{"inbound_server_id":%d,"inbound_tag":"multi-in","outbound_id":%d}`, server.ID, outbound.ID)
	if response := serveJSONRequest(t, router, http.MethodPost, "/user/forwards", body); response.Code != http.StatusOK {
		t.Fatalf("create forward: %d %s", response.Code, response.Body)
	}
	var forward model.UserForward
	if err := db.Where("user_id = ?", user.ID).First(&forward).Error; err != nil {
		t.Fatal(err)
	}
	var rule model.RouteRule
	if err := db.Where("server_id = ? AND outbound = ?", server.ID, outbound.Tag).First(&rule).Error; err != nil {
		t.Fatal(err)
	}
	mustCreate(t, db, &model.UserNodeOrder{UserID: user.ID, NodeType: userNodeTypeForward, NodeID: forward.ID, SortOrder: 0})

	response := serveJSONRequest(t, router, http.MethodPut, fmt.Sprintf("/admin/users/%d", user.ID), `{"forwarding_enabled":false}`)
	if response.Code != http.StatusOK {
		t.Fatalf("revoke permission: %d %s", response.Code, response.Body)
	}
	var forwards int64
	if err := db.Model(&model.UserForward{}).Where("user_id = ?", user.ID).Count(&forwards).Error; err != nil {
		t.Fatal(err)
	}
	if forwards != 0 {
		t.Fatalf("forward rows after revoke = %d, want 0", forwards)
	}
	var rules int64
	if err := db.Model(&model.RouteRule{}).Where("id = ?", rule.ID).Count(&rules).Error; err != nil {
		t.Fatal(err)
	}
	if rules != 0 {
		t.Fatalf("route rules after revoke = %d, want 0", rules)
	}
	var orders int64
	if err := db.Model(&model.UserNodeOrder{}).Where("user_id = ? AND node_type = ? AND node_id = ?", user.ID, userNodeTypeForward, forward.ID).Count(&orders).Error; err != nil {
		t.Fatal(err)
	}
	if orders != 0 {
		t.Fatalf("node orders after revoke = %d, want 0", orders)
	}
}
