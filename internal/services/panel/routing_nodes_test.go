package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestRoutingNodeTags(t *testing.T) {
	for _, tag := range []string{"", "direct", "node-0", "node-01", "node--1", "node-1x"} {
		if _, err := routingNodeID(tag); err == nil {
			t.Errorf("accepted %q", tag)
		}
	}
	id, err := routingNodeID(routingNodeTag(42))
	if err != nil || id != 42 {
		t.Fatalf("round trip: %d, %v", id, err)
	}
}

func TestBuildServerConfigLoadsOnlyReferencedGlobalResources(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "router", FinalOutbound: "direct"}
	mustCreate(t, db, &srv)
	proxy := model.CustomNode{Name: "private proxy", Protocol: "http", Address: "proxy.example", Port: 443, Params: model.JSONText(`{"tls":"tls","username":"user","password":"pass","http_path":"/relay","http_headers":{"X-Test":"kept"}}`), Enabled: true, AllUsers: false}
	mustCreate(t, db, &proxy)
	download := model.CustomNode{Name: "download", Protocol: "socks", Address: "download.example", Port: 1080, Params: model.JSONText(`{"username":"dl","password":"secret"}`), Enabled: true}
	mustCreate(t, db, &download)
	unrelated := model.CustomNode{Name: "unused", Protocol: "socks", Address: "unused.example", Port: 1080, Enabled: true}
	mustCreate(t, db, &unrelated)
	mustCreate(t, db, &model.Outbound{ServerID: srv.ID, Tag: "legacy", Type: "socks", Settings: model.JSONText(`{}`)})
	mustCreate(t, db, &model.RuleSet{Tag: "used", Type: "remote", URL: "https://rules.example/used.srs", DownloadDetour: routingNodeTag(download.ID)})
	mustCreate(t, db, &model.RuleSet{Tag: "unused", Type: "remote", URL: "https://rules.example/unused.srs", DownloadDetour: routingNodeTag(unrelated.ID)})
	mustCreate(t, db, &model.RouteRule{ServerID: srv.ID, Enabled: true, Match: model.JSONText(`{"rule_set":["used"]}`), Outbound: routingNodeTag(proxy.ID)})
	raw, err := NewOrchestrator(db, nil).BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Outbounds   []map[string]any `json:"outbounds"`
		HTTPClients []map[string]any `json:"http_clients"`
		Route       struct {
			RuleSet []map[string]any `json:"rule_set"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Outbounds) != 3 {
		t.Fatalf("outbounds = %s", raw)
	}
	if len(config.Route.RuleSet) != 1 || config.Route.RuleSet[0]["tag"] != "used" {
		t.Fatalf("rule sets = %s", raw)
	}
	var gotProxy map[string]any
	for _, ob := range config.Outbounds {
		if ob["tag"] == routingNodeTag(proxy.ID) {
			gotProxy = ob
		}
	}
	if gotProxy["path"] != "/relay" || gotProxy["username"] != "user" || gotProxy["tls"] == nil {
		t.Fatalf("protocol options lost: %#v", gotProxy)
	}
	if !strings.Contains(string(raw), `"detour": "`+routingNodeTag(download.ID)+`"`) {
		t.Fatalf("download detour missing: %s", raw)
	}
}

func TestBuildServerConfigRejectsUnavailableRoutingNodes(t *testing.T) {
	for _, state := range []string{"missing", "disabled", "filtered"} {
		t.Run(state, func(t *testing.T) {
			db := testDB(t)
			node := model.CustomNode{Name: "proxy", Protocol: "socks", Address: "example.com", Port: 1080, Enabled: true}
			mustCreate(t, db, &node)
			switch state {
			case "missing":
				db.Delete(&node)
			case "disabled":
				db.Model(&node).Update("enabled", false)
			case "filtered":
				db.Model(&node).Update("hidden_by_subscription_rule", true)
			}
			srv := model.Server{FinalOutbound: routingNodeTag(node.ID)}
			mustCreate(t, db, &srv)
			if _, err := NewOrchestrator(db, nil).BuildServerConfig(&srv); err == nil {
				t.Fatal("unavailable final silently accepted")
			}
		})
	}
}

func TestRoutingNodeLifecycleRejectsDeleteAndDisable(t *testing.T) {
	db := testDB(t)
	node := model.CustomNode{Name: "used", Protocol: "socks", Address: "example.com", Port: 1080, Enabled: true}
	mustCreate(t, db, &node)
	mustCreate(t, db, &model.Server{FinalOutbound: routingNodeTag(node.ID)})
	app := &App{db: db}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.DELETE("/nodes/:id", app.deleteCustomNode)
	router.PUT("/nodes/:id", app.updateCustomNode)
	for _, method := range []string{http.MethodDelete, http.MethodPut} {
		req := httptest.NewRequest(method, "/nodes/"+strconv.Itoa(int(node.ID)), bytes.NewBufferString(`{"protocol":"socks","address":"example.com","port":1080,"enabled":false}`))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusConflict {
			t.Fatalf("%s: %d %s", method, response.Code, response.Body.String())
		}
	}
	var saved model.CustomNode
	if err := db.First(&saved, node.ID).Error; err != nil || !saved.Enabled {
		t.Fatalf("referenced node changed: %#v %v", saved, err)
	}
}

func TestRoutingNodeAffectedServersFollowsSharedRuleSet(t *testing.T) {
	db := testDB(t)
	a, b, c := model.Server{AgentToken: "a"}, model.Server{AgentToken: "b"}, model.Server{AgentToken: "c"}
	mustCreate(t, db, &a)
	mustCreate(t, db, &b)
	mustCreate(t, db, &c)
	tag := routingNodeTag(37)
	db.Model(&a).Update("final_outbound", tag)
	mustCreate(t, db, &model.RuleSet{Tag: "shared", Type: "remote", DownloadDetour: tag})
	mustCreate(t, db, &model.RouteRule{ServerID: b.ID, Enabled: true, Match: model.JSONText(`{"rule_set":["shared"]}`), Outbound: "direct"})
	mustCreate(t, db, &model.RouteRule{ServerID: c.ID, Enabled: true, Match: model.JSONText(`{}`), Outbound: "direct"})
	ids, err := routingNodeAffectedServers(db, []uint{37})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []uint{a.ID, b.ID}) {
		t.Fatalf("affected %v", ids)
	}
}

func TestSubscriptionSyncPreservesReferencedNodesAndNotifiesChanges(t *testing.T) {
	db := testDB(t)
	source := model.CustomNodeSubscription{Name: "upstream", URL: "https://example.com/sub", Enabled: true}
	mustCreate(t, db, &source)
	manager := newCustomNodeSubscriptionManager(db)
	payload := "trojan://old@proxy.example:443#keep\ntrojan://old@stale.example:443#stale"
	manager.fetch = func(context.Context, *url.URL) ([]byte, error) { return []byte(payload), nil }
	if _, err := manager.sync(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	var keep, stale model.CustomNode
	db.Where("name = ?", "keep").First(&keep)
	db.Where("name = ?", "stale").First(&stale)
	mustCreate(t, db, &model.Server{FinalOutbound: routingNodeTag(stale.ID)})
	var changed []uint
	manager.onChanged = func(ids []uint) { changed = append(changed, ids...) }
	payload = "trojan://new@proxy.example:443#keep"
	result, err := manager.sync(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Retained != 1 || result.Deleted != 0 {
		t.Fatalf("sync result %#v", result)
	}
	if err := db.First(&stale, stale.ID).Error; err != nil {
		t.Fatal("referenced upstream removal deleted node", err)
	}
	if !reflect.DeepEqual(changed, []uint{keep.ID}) {
		t.Fatalf("changed IDs = %v", changed)
	}
	db.First(&source, source.ID)
	if source.LastSyncStatus != model.SubscriptionSyncPartial || source.LastError == "" {
		t.Fatalf("retention not reported %#v", source)
	}
}

func TestRoutingNodeProtectsAllReferenceKinds(t *testing.T) {
	for _, kind := range []string{"route", "download", "final"} {
		t.Run(kind, func(t *testing.T) {
			db := testDB(t)
			tag := routingNodeTag(73)
			switch kind {
			case "route":
				mustCreate(t, db, &model.RouteRule{ServerID: 1, Enabled: false, Outbound: tag})
			case "download":
				mustCreate(t, db, &model.RuleSet{Tag: "download", DownloadDetour: tag})
			case "final":
				mustCreate(t, db, &model.Server{FinalOutbound: tag})
			}
			if err := rejectReferencedRoutingNodes(db, []uint{73}); err != errRoutingNodeInUse {
				t.Fatalf("reference not protected: %v", err)
			}
		})
	}
}

func TestSharedRoutingResourcesDoNotInitializeServer(t *testing.T) {
	db := testDB(t)
	srv := model.Server{}
	mustCreate(t, db, &srv)
	mustCreate(t, db, &model.RuleSet{ServerID: srv.ID, Tag: "shared"})
	mustCreate(t, db, &model.Outbound{ServerID: srv.ID, Tag: "legacy", Type: "direct"})
	managed, err := NewOrchestrator(db, nil).serverIsManaged(srv.ID)
	if err != nil || managed {
		t.Fatalf("shared resources initialized server: %v %v", managed, err)
	}
}
