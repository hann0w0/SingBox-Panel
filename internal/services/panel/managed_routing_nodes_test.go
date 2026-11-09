package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestManagedNodeCandidatesIncludeOwnServersWithoutSecrets(t *testing.T) {
	db := testDB(t)
	source := model.Server{Name: "source", Address: "source.example", AgentToken: "source-secret"}
	mustCreate(t, db, &source)
	target := model.Server{Name: "东京", Address: "target.example", AgentToken: "target-secret"}
	mustCreate(t, db, &target)
	for _, srv := range []model.Server{source, target} {
		mustCreate(t, db, &model.Inbound{ServerID: srv.ID, Tag: "vless-abcdef", Type: model.InboundVLESS, ListenPort: 443, Enabled: true, Settings: model.JSONText(`{"multi_user":true}`)})
	}
	mustCreate(t, db, &model.Inbound{ServerID: target.ID, Tag: "disabled", Type: model.InboundSocks, ListenPort: 1080, Enabled: false})
	custom := model.CustomNode{Name: "外部节点", Protocol: "socks", Address: "external.example", Port: 1080, Enabled: true}
	mustCreate(t, db, &custom)
	router := gin.New()
	router.GET("/nodes", (&App{db: db}).listRoutingNodes)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/nodes?server_id=%d", source.ID), nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		Nodes []struct{ Tag, Name, Type string } `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 2 || result.Nodes[1].Name != "东京" || result.Nodes[1].Tag != "inbound-2" {
		t.Fatalf("candidate list: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "uuid") || strings.Contains(w.Body.String(), "password") {
		t.Fatal("credentials leaked in candidate metadata")
	}
	if err := (&App{db: db}).validateRule(db, source.ID, json.RawMessage(`{}`), "inbound-2"); err != nil {
		t.Fatal(err)
	}
	if err := (&App{db: db}).validateRule(db, target.ID, json.RawMessage(`{}`), "inbound-2"); err == nil {
		t.Fatal("self-route accepted")
	}
}

func TestManagedNodeCredentialsMatchTargetForEachMultiUserProtocol(t *testing.T) {
	for _, typ := range []string{"vless", "vmess", "trojan", "hysteria2", "tuic", "anytls", "shadowsocks"} {
		t.Run(typ, func(t *testing.T) {
			db := testDB(t)
			source := model.Server{Name: "source", Address: "source.example", AgentToken: "source"}
			mustCreate(t, db, &source)
			target := model.Server{Name: "target", Address: "target.example", AgentToken: "target"}
			mustCreate(t, db, &target)
			settings := singbox.InboundSettings{MultiUser: true, Method: "2022-blake3-aes-128-gcm", SSServerPSK: "MDEyMzQ1Njc4OWFiY2RlZg==", TLS: singbox.TLSSettings{Enabled: true, SelfSigned: true, ServerName: "target.example"}}
			if err := fillInboundSecrets(typ, &settings); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(settings)
			ib := model.Inbound{ServerID: target.ID, Tag: "target", Type: model.InboundType(typ), ListenPort: 443, Enabled: true, Settings: model.JSONText(encoded)}
			mustCreate(t, db, &ib)
			orch := NewOrchestrator(db, NewHub(db))
			// No reference means no issued machine identity.
			before, err := orch.BuildServerConfig(&target)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(before), managedRoutingProxyUserName) {
				t.Fatal("unused inbound has a routing credential")
			}
			source.FinalOutbound = managedRoutingNodeTag(ib.ID)
			if err := db.Save(&source).Error; err != nil {
				t.Fatal(err)
			}
			sourceRaw, err := orch.BuildServerConfig(&source)
			if err != nil {
				t.Fatal(err)
			}
			targetRaw, err := orch.BuildServerConfig(&target)
			if err != nil {
				t.Fatal(err)
			}
			var src struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			json.Unmarshal(sourceRaw, &src)
			var dst struct {
				Inbounds []struct {
					Users []map[string]any `json:"users"`
				} `json:"inbounds"`
			}
			json.Unmarshal(targetRaw, &dst)
			var outbound, user map[string]any
			for _, ob := range src.Outbounds {
				if ob["tag"] == source.FinalOutbound {
					outbound = ob
				}
			}
			for _, u := range dst.Inbounds[0].Users {
				if u["name"] == managedRoutingProxyUserName {
					user = u
				}
			}
			if outbound == nil || user == nil {
				t.Fatalf("missing routing connection or identity: source=%s target=%s", sourceRaw, targetRaw)
			}
			for _, field := range []string{"uuid", "password"} {
				if value, ok := user[field]; ok {
					expected := value
					if typ == "shadowsocks" && field == "password" {
						expected = settings.SSServerPSK + ":" + value.(string)
					}
					if outbound[field] != expected {
						t.Fatalf("%s does not match target", field)
					}
				}
			}
			if typ != "shadowsocks" {
				tls, _ := outbound["tls"].(map[string]any)
				if tls["insecure"] != true || tls["server_name"] != "target.example" {
					t.Fatalf("TLS options lost: %v", tls)
				}
			}
			var bindings int64
			db.Model(&model.UserTrafficIdentity{}).Count(&bindings)
			if bindings != 0 {
				t.Fatal("machine identity was assigned to a normal user")
			}
			db.Model(&source).Update("final_outbound", "direct")
			after, err := orch.BuildServerConfig(&target)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(after), managedRoutingProxyUserName) {
				t.Fatal("last reference did not revoke dedicated identity")
			}
		})
	}
}

func TestManagedNodeFixedCredentialsAndReferenceStability(t *testing.T) {
	db := testDB(t)
	target := model.Server{Name: "target", PublicIP: "192.0.2.10", AgentToken: "target"}
	mustCreate(t, db, &target)
	ib := model.Inbound{ServerID: target.ID, Tag: "SOCKS", Type: model.InboundSocks, ListenPort: 1080, Enabled: true, Settings: model.JSONText(`{"single_user":true,"username":"fixed","password":"fixed-secret"}`)}
	mustCreate(t, db, &ib)
	source := model.Server{Name: "source", AgentToken: "source", FinalOutbound: managedRoutingNodeTag(ib.ID)}
	mustCreate(t, db, &source)
	orch := NewOrchestrator(db, NewHub(db))
	raw, err := orch.BuildServerConfig(&source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"username": "fixed"`) || !strings.Contains(string(raw), `"server": "192.0.2.10"`) {
		t.Fatal(string(raw))
	}
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&App{db: db, orch: orch}).applyImport(&source, parsed, raw); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&model.CustomNode{}).Count(&count)
	db.First(&source, source.ID)
	if count != 0 || source.FinalOutbound != managedRoutingNodeTag(ib.ID) {
		t.Fatal("import detached managed node identity")
	}
	db.Model(&ib).Updates(map[string]any{"tag": "renamed", "listen_port": 2080})
	raw, err = orch.BuildServerConfig(&source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"server_port": 2080`) {
		t.Fatal("port update not reflected")
	}
	db.Model(&ib).Update("enabled", false)
	if _, err := orch.BuildServerConfig(&source); err == nil {
		t.Fatal("disabled managed target accepted")
	}
}

func TestManagedNodeDownloadDetourAndDefaultShareTarget(t *testing.T) {
	db := testDB(t)
	target := model.Server{Name: "target", Address: "target.example", AgentToken: "target"}
	mustCreate(t, db, &target)
	ib := model.Inbound{ServerID: target.ID, Tag: "proxy", Type: model.InboundSocks, ListenPort: 1080, Enabled: true}
	mustCreate(t, db, &ib)
	source := model.Server{Name: "source", AgentToken: "source"}
	mustCreate(t, db, &source)
	mustCreate(t, db, &model.RuleSet{Tag: "geo", Type: "remote", URL: "https://example.com/geo.srs", DownloadDetour: managedRoutingNodeTag(ib.ID)})
	mustCreate(t, db, &model.RouteRule{ServerID: source.ID, Enabled: true, Outbound: "direct", Match: model.JSONText(`{"rule_set":["geo"]}`)})
	raw, err := NewOrchestrator(db, NewHub(db)).BuildServerConfig(&source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"tag": "`+managedRoutingNodeTag(ib.ID)+`"`) || !strings.Contains(string(raw), `"detour": "`+managedRoutingNodeTag(ib.ID)+`"`) {
		t.Fatal(string(raw))
	}
}

func TestManagedNodeMachineTrafficIsNotChargedToUsers(t *testing.T) {
	inbounds := []model.Inbound{{ID: 1, Tag: "multi", Settings: model.JSONText(`{"multi_user":true}`), Type: model.InboundVLESS}}
	deltas := multiUserTrafficDeltas(1, inbounds, []protocol.UserTrafficSnapshot{{Inbound: "multi", User: managedRoutingProxyUserName, Upload: 100, Download: 100}}, time.Now())
	if len(deltas) != 0 {
		t.Fatal("machine traffic entered user accounting")
	}
}

func TestManagedNodeOfflineTargetMakesApplyPending(t *testing.T) {
	db := testDB(t)
	target := model.Server{Address: "target.example", AgentToken: "target"}
	mustCreate(t, db, &target)
	ib := model.Inbound{ServerID: target.ID, Tag: "vless", Type: model.InboundVLESS, ListenPort: 443, Enabled: true, Settings: model.JSONText(`{"multi_user":true}`)}
	mustCreate(t, db, &ib)
	source := model.Server{AgentToken: "source", FinalOutbound: managedRoutingNodeTag(ib.ID)}
	mustCreate(t, db, &source)
	result := NewOrchestrator(db, NewHub(db)).ApplyDesiredConfig(context.Background(), source.ID)
	if result.ApplyState != ConfigPending {
		t.Fatalf("apply=%+v", result)
	}
}

func TestManagedNodeApplyInstallsTargetCredentialBeforeSource(t *testing.T) {
	db := testDB(t)
	target := model.Server{Address: "target.example", AgentToken: "target"}
	mustCreate(t, db, &target)
	ib := model.Inbound{ServerID: target.ID, Tag: "vless", Type: model.InboundVLESS, ListenPort: 443, Enabled: true, Settings: model.JSONText(`{"multi_user":true}`)}
	mustCreate(t, db, &ib)
	source := model.Server{AgentToken: "source", FinalOutbound: managedRoutingNodeTag(ib.ID)}
	mustCreate(t, db, &source)
	hub := NewHub(db)
	targetConn := hub.register(target.ID, "", nil)
	defer targetConn.close()
	sourceConn := hub.register(source.ID, "", nil)
	defer sourceConn.close()
	orch := NewOrchestrator(db, hub)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan ConfigApplyResult, 1)
	go func() { done <- orch.ApplyDesiredConfig(ctx, source.ID) }()
	targetConfig := nextSubscriptionConfig(t, targetConn)
	if !strings.Contains(string(targetConfig.Payload), managedRoutingProxyUserName) {
		t.Fatal("target did not receive dedicated credential")
	}
	select {
	case <-sourceConn.send:
		t.Fatal("source applied before target accepted credential")
	default:
	}
	replySubscriptionConfig(targetConn, targetConfig, true)
	sourceConfig := nextSubscriptionConfig(t, sourceConn)
	if !strings.Contains(string(sourceConfig.Payload), managedRoutingNodeTag(ib.ID)) {
		t.Fatal("source did not receive managed target")
	}
	replySubscriptionConfig(sourceConn, sourceConfig, true)
	if result := <-done; result.ApplyState != ConfigApplied {
		t.Fatalf("apply=%+v", result)
	}
}

func TestManagedNodeRuleSetDownloadRejectsSelfReferenceAtBothBoundaries(t *testing.T) {
	db := testDB(t)
	target := model.Server{Name: "target", Address: "target.example", AgentToken: "target"}
	mustCreate(t, db, &target)
	ib := model.Inbound{ServerID: target.ID, Tag: "socks", Type: model.InboundSocks, ListenPort: 1080, Enabled: true}
	mustCreate(t, db, &ib)
	rs := model.RuleSet{Tag: "geo", Type: "remote", URL: "https://example.com/geo.srs", DownloadDetour: managedRoutingNodeTag(ib.ID)}
	mustCreate(t, db, &rs)
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	router := gin.New()
	router.POST("/servers/:id/rules", app.createRule)
	router.PUT("/servers/:id/final", app.setFinalOutbound)
	router.PUT("/rulesets/:rulesetID", app.updateRuleSet)
	result := serveJSONRequest(t, router, http.MethodPost, fmt.Sprintf("/servers/%d/rules", target.ID), `{"outbound":"direct","match":{"rule_set":["geo"]}}`)
	if result.Code != http.StatusBadRequest {
		t.Fatalf("self download rule accepted: %d %s", result.Code, result.Body)
	}
	result = serveJSONRequest(t, router, http.MethodPut, fmt.Sprintf("/servers/%d/final", target.ID), fmt.Sprintf(`{"outbound":%q}`, managedRoutingNodeTag(ib.ID)))
	if result.Code != http.StatusBadRequest {
		t.Fatal("self default accepted")
	}
	db.Model(&rs).Update("download_detour", "direct")
	mustCreate(t, db, &model.RouteRule{ServerID: target.ID, Enabled: true, Outbound: "direct", Match: model.JSONText(`{"rule_set":["geo"]}`)})
	result = serveJSONRequest(t, router, http.MethodPut, fmt.Sprintf("/rulesets/%d", rs.ID), fmt.Sprintf(`{"tag":"geo","type":"remote","url":"https://example.com/geo.srs","download_detour":%q}`, managedRoutingNodeTag(ib.ID)))
	if result.Code != http.StatusBadRequest {
		t.Fatalf("self detour update accepted: %d %s", result.Code, result.Body)
	}
	db.First(&rs, rs.ID)
	if rs.DownloadDetour != "direct" {
		t.Fatal("invalid download target persisted")
	}
}
