package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestAuthUserRouteRendersForOnePrivateOutbound(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "entry", Address: "entry.example", AgentToken: "entry-token", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &server)
	user := model.User{Email: "alice", Role: model.RoleUser, Enabled: true, SubToken: "alice-sub", ProxyToken: "alice-proxy", ServerIDs: []uint{server.ID}}
	mustCreate(t, db, &user)
	other := model.User{Email: "bob", Role: model.RoleUser, Enabled: true, SubToken: "bob-sub", ProxyToken: "bob-proxy", ServerIDs: []uint{server.ID}}
	mustCreate(t, db, &other)
	outbound := model.Outbound{
		ServerID: 0, OwnerUserID: user.ID, Tag: "alice-hk-home", Type: "vless",
		Settings: model.JSONText(`{"server":"hk-home.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555","settings":{"tls":{"enabled":true,"server_name":"hk-home.example"}}}`),
	}
	mustCreate(t, db, &outbound)
	mustCreate(t, db, &model.RouteRule{
		ServerID: server.ID, Outbound: outbound.Tag, Enabled: true,
		Match: model.JSONText(`{"action":"route","auth_user":["` + proxyTrafficName(&user) + `"]}`),
	})
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	raw, err := app.orch.BuildServerConfig(&server)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Outbounds []map[string]any `json:"outbounds"`
		Route     struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	var match map[string]any
	for _, rule := range config.Route.Rules {
		if _, ok := rule["auth_user"]; ok {
			match = rule
			break
		}
	}
	if match == nil {
		t.Fatalf("auth_user rule missing from generated config: %s", raw)
	}
	authUsers, _ := match["auth_user"].([]any)
	if len(authUsers) != 1 || authUsers[0] != proxyTrafficName(&user) {
		t.Fatalf("auth_user = %v, want %q: %s", match["auth_user"], proxyTrafficName(&user), raw)
	}
	if match["outbound"] != outbound.Tag {
		t.Fatalf("rule target = %v, want %q", match["outbound"], outbound.Tag)
	}
	if len(config.Outbounds) == 0 {
		t.Fatalf("private outbound missing from generated config: %s", raw)
	}
}

func TestPrivateOutboundIsHiddenFromPublicOutboundList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{Name: "entry", AgentToken: "entry-token", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &server)
	user := model.User{Email: "alice", Role: model.RoleUser, Enabled: true, SubToken: "alice-sub", ProxyToken: "alice-proxy"}
	mustCreate(t, db, &user)
	mustCreate(t, db, &model.Outbound{ServerID: server.ID, Tag: "shared", Type: "vless", Settings: model.JSONText(`{"server":"shared.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`)})
	mustCreate(t, db, &model.Outbound{ServerID: 0, OwnerUserID: user.ID, Tag: "private", Type: "vless", Settings: model.JSONText(`{"server":"private.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`)})
	app := &App{db: db}
	router := gin.New()
	router.GET("/servers/:id/outbounds", app.listOutbounds)
	response := serveJSONRequest(t, router, http.MethodGet, fmt.Sprintf("/servers/%d/outbounds", server.ID), "")
	if response.Code != http.StatusOK {
		t.Fatalf("list outbounds: %d %s", response.Code, response.Body)
	}
	var listed struct {
		Outbounds []model.Outbound `json:"outbounds"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	for _, row := range listed.Outbounds {
		if row.Tag == "private" {
			t.Fatalf("private outbound exposed in public list: %+v", listed.Outbounds)
		}
	}
}

func TestPrivateOutboundIsIncludedOnlyOnReferencingServers(t *testing.T) {
	db := testDB(t)
	entryA := model.Server{Name: "entry-a", AgentToken: "a", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &entryA)
	entryB := model.Server{Name: "entry-b", AgentToken: "b", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &entryB)
	user := model.User{Email: "alice", Role: model.RoleUser, Enabled: true, SubToken: "alice", ProxyToken: "alice-proxy", ServerIDs: []uint{entryA.ID, entryB.ID}}
	mustCreate(t, db, &user)
	outbound := model.Outbound{
		ServerID: 0, OwnerUserID: user.ID, Tag: "alice-hk-home", Type: "vless",
		Settings: model.JSONText(`{"server":"hk-home.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`),
	}
	mustCreate(t, db, &outbound)
	mustCreate(t, db, &model.RouteRule{
		ServerID: entryA.ID, Outbound: outbound.Tag, Enabled: true,
		Match: model.JSONText(`{"action":"route","auth_user":["` + proxyTrafficName(&user) + `"]}`),
	})
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}

	containsTag := func(server *model.Server, tag string) bool {
		t.Helper()
		raw, err := app.orch.BuildServerConfig(server)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Outbounds []map[string]any `json:"outbounds"`
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		for _, row := range config.Outbounds {
			if row["tag"] == tag {
				return true
			}
		}
		return false
	}
	if !containsTag(&entryA, outbound.Tag) {
		t.Fatalf("referencing server is missing private outbound: %s", outbound.Tag)
	}
	if containsTag(&entryB, outbound.Tag) {
		t.Fatalf("unrelated server received private outbound: %s", outbound.Tag)
	}
}

func TestAdminUserOutboundListDoesNotExposeConnectionDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	user := model.User{Email: "alice", Role: model.RoleUser, Enabled: true, SubToken: "alice", ProxyToken: "alice-proxy"}
	mustCreate(t, db, &user)
	mustCreate(t, db, &model.Outbound{
		ServerID: 0, OwnerUserID: user.ID, Tag: "alice-home", Type: "vless",
		Remark:   "Alice 家宽出口",
		Settings: model.JSONText(`{"server":"home.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`),
	})
	app := &App{db: db}
	router := gin.New()
	router.GET("/users/:id/outbounds", app.listAdminUserOutbounds)
	response := serveJSONRequest(t, router, http.MethodGet, fmt.Sprintf("/users/%d/outbounds", user.ID), "")
	if response.Code != http.StatusOK {
		t.Fatalf("list admin outbounds: %d %s", response.Code, response.Body)
	}
	for _, secret := range []string{"alice-home", "Alice 家宽出口", "vless", "home.example", "11111111-2222-4333-8444-555555555555"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("admin outbound response leaked %q: %s", secret, response.Body)
		}
	}
	var listed struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Outbounds) != 1 || len(listed.Outbounds[0]) != 1 || listed.Outbounds[0]["id"] == nil {
		t.Fatalf("admin outbound list = %+v, want opaque id only", listed.Outbounds)
	}
}
