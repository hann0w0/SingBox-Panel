package panel

import (
	"encoding/json"
	"errors"
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

func TestInboundAddressFamilies(t *testing.T) {
	now := time.Now()
	server := model.Server{Name: "dual", Address: "ipv4-only.example.com", PublicIP: "203.0.113.7", PublicIPv6: "2001:db8::7", PublicIPv4UpdatedAt: &now, PublicIPv6UpdatedAt: &now}
	for _, test := range []struct {
		name, listen, host string
		ipv6               bool
	}{
		{"dual", "::", "ipv4-only.example.com", false},
		{"v4", "0.0.0.0", "203.0.113.7", false},
		{"v6", "::", "2001:db8::7", true},
		{"imported", "2001:db8::8", "2001:db8::8", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := singbox.InboundSettings{Listen: test.listen, IPv6Only: test.ipv6, TLS: singbox.TLSSettings{Enabled: true}}
			host, err := inboundNodeAddress(&server, &settings)
			if err != nil || host != test.host {
				t.Fatalf("address=%q err=%v", host, err)
			}
			if test.name != "dual" && settings.TLS.ServerName != server.Address {
				t.Fatalf("TLS domain lost: %+v", settings.TLS)
			}
		})
	}
	for _, listen := range []string{"127.0.0.1", "::1"} {
		if _, err := inboundNodeAddress(&server, &singbox.InboundSettings{Listen: listen}); !errors.Is(err, errLocalInbound) {
			t.Fatalf("local export accepted: %v", err)
		}
		settings := singbox.InboundSettings{Listen: listen, SubscriptionServer: "edge.example.com"}
		if _, _, err := subscriptionNodeAddress(&server, &settings); !errors.Is(err, errLocalInbound) {
			t.Fatalf("override made a local-only inbound subscribable: %v", err)
		}
	}
	settings := singbox.InboundSettings{Listen: "::", IPv6Only: true, TLS: singbox.TLSSettings{ServerName: "tls.example.com"}}
	if host, err := inboundNodeAddress(&server, &settings); err != nil || host != "2001:db8::7" || settings.TLS.ServerName != "tls.example.com" {
		t.Fatalf("explicit SNI changed or wrong IPv6 host: %q %+v %v", host, settings.TLS, err)
	}
	server.PublicIPv6 = ""
	if _, err := inboundNodeAddress(&server, &settings); err == nil {
		t.Fatal("IPv6-only inbound without public IPv6 must fail instead of falling back to domain")
	}
	settings.SubscriptionServer = "edge.example.com"
	if host, _, err := subscriptionNodeAddress(&server, &settings); err != nil || host != settings.SubscriptionServer {
		t.Fatalf("explicit subscription address requires a detected public address: %q %v", host, err)
	}
	server.Address = ""
	server.PublicIPv6 = "2001:db8::7"
	if host, err := inboundNodeAddress(&server, &settings); err != nil || host != "2001:db8::7" {
		t.Fatalf("IPv6 fallback=%q err=%v", host, err)
	}
	stale := now.Add(-3 * time.Minute)
	server.PublicIPv6UpdatedAt = &stale
	if host, err := inboundNodeAddress(&server, &settings); err != nil || host != "2001:db8::7" {
		t.Fatalf("offline server lost its last confirmed IPv6: %q %v", host, err)
	}
	if err := resolveInboundListen(&server, &settings); err != nil || settings.Listen != "2001:db8::7" {
		t.Fatal("offline server cannot keep its confirmed IPv6 listener")
	}
}

func TestServerAddressAvailability(t *testing.T) {
	now := time.Now()
	server := model.Server{PublicIP: "203.0.113.7", PublicIPv6: "2001:db8::7", PublicIPv6UpdatedAt: &now}
	setServerAddressAvailability(&server)
	if !server.IPv4Available || !server.IPv6Available {
		t.Fatal("available addresses hidden")
	}
	stale := now.Add(-3 * time.Minute)
	server.PublicIPv6UpdatedAt = &stale
	setServerAddressAvailability(&server)
	if !server.IPv6Available {
		t.Fatal("an offline server's last confirmed address must remain usable")
	}
	server.PublicIPv6UpdatedAt = &now
	server.PublicIPv6 = ""
	setServerAddressAvailability(&server)
	if server.IPv6Available {
		t.Fatal("missing IPv6 advertised")
	}
}

func TestAddressReportsUpdateExportsSubscriptionsAndListeners(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "test", AgentToken: "test-address"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	app := &App{db: db, hub: hub, orch: NewOrchestrator(db, hub)}
	connection := hub.register(server.ID, "", nil)
	defer connection.close()
	changes := 0
	hub.AfterAddressChange = func(uint) { changes++ }
	value4, value6 := "203.0.113.7", "2001:db8::7"
	hub.onRegister(connection, protocol.RegisterEvt{PublicIPv4: &value4, PublicIPv6: &value6})
	if changes != 1 {
		t.Fatalf("register changes=%d", changes)
	}
	for index, settings := range []singbox.InboundSettings{
		{Listen: "::", IPv6Only: true}, {Listen: "0.0.0.0"}, {Listen: "127.0.0.1"},
	} {
		raw, _ := json.Marshal(settings)
		inbound := model.Inbound{ServerID: server.ID, Tag: fmt.Sprintf("node-%d", index), Type: model.InboundSocks, ListenPort: 20000 + index, Enabled: true, Settings: raw}
		if err := db.Create(&inbound).Error; err != nil {
			t.Fatal(err)
		}
	}
	user := model.User{ServerIDs: []uint{server.ID}}
	router := gin.New()
	router.GET("/servers/:id/export", app.serverNodeFormats)
	check := func(want4, want6 string) {
		t.Helper()
		if err := db.First(&server, server.ID).Error; err != nil {
			t.Fatal(err)
		}
		nodes, err := app.gatherNodesWithError(&user)
		if err != nil || len(nodes) != 2 || nodes[0].server != want6 || nodes[1].server != want4 {
			t.Fatalf("nodes=%+v err=%v", nodes, err)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/servers/%d/export", server.ID), nil))
		if response.Code != 200 || !strings.Contains(response.Body.String(), want4) || !strings.Contains(response.Body.String(), want6) || strings.Contains(response.Body.String(), "127.0.0.1") {
			t.Fatalf("export: %d %s", response.Code, response.Body.String())
		}
		raw, err := app.orch.BuildServerConfig(&server)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Inbounds []struct {
				Listen string `json:"listen"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		if config.Inbounds[0].Listen != want6 || config.Inbounds[1].Listen != "0.0.0.0" || config.Inbounds[2].Listen != "127.0.0.1" {
			t.Fatalf("wrong listeners: %s", raw)
		}
		if strings.Contains(string(raw), "source_ip_cidr") {
			t.Fatalf("single-stack binding added unnecessary IPv4 reject rules: %s", raw)
		}
		client, err := app.inboundOutboundClient(nodes[0].orderID, 99)
		if err != nil || client.Server != want6 {
			t.Fatalf("forward address=%q err=%v", client.Server, err)
		}
	}
	check(value4, value6)
	value4, value6 = "203.0.113.9", "2001:db8::9"
	hub.onHeartbeat(connection, protocol.HeartbeatEvt{PublicIPv4: &value4, PublicIPv6: &value6})
	if changes != 2 {
		t.Fatalf("heartbeat changes=%d", changes)
	}
	check(value4, value6)
	hub.onHeartbeat(connection, protocol.HeartbeatEvt{})
	if changes != 2 {
		t.Fatal("old Agent cleared addresses")
	}
	invalid := "::ffff:203.0.113.1"
	hub.onHeartbeat(connection, protocol.HeartbeatEvt{PublicIPv6: &invalid})
	check(value4, value6)
	empty := ""
	hub.onHeartbeat(connection, protocol.HeartbeatEvt{PublicIPv6: &empty})
	remaining, err := app.gatherNodesWithError(&user)
	if err != nil {
		t.Fatalf("broken IPv6 inbound should degrade gracefully when healthy IPv4 inbound exists: %v", err)
	}
	if len(remaining) != 1 || remaining[0].server != value4 {
		t.Fatalf("expected only healthy IPv4 node %q after IPv6 loss, got %+v", value4, remaining)
	}
	if err := db.First(&server, server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := app.orch.BuildServerConfig(&server); err == nil {
		t.Fatal("missing IPv6 fell back to wildcard")
	}
}

func TestAddressChangeRebuildsDependentOutbound(t *testing.T) {
	fixture := newNodeImportFixture(t)
	if err := fixture.db.Model(&fixture.source).Update("address", "").Error; err != nil {
		t.Fatal(err)
	}
	settings := singbox.InboundSettings{Listen: "::", IPv6Only: true, SingleUser: true, UUID: "11111111-2222-4333-8444-555555555555"}
	raw, _ := json.Marshal(settings)
	if err := fixture.db.Model(&fixture.sourceIB).Update("settings", raw).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&fixture.source).Updates(map[string]any{"public_ipv6": "2001:db8::7"}).Error; err != nil {
		t.Fatal(err)
	}
	body, err := fixture.app.outboundSourceBody(fixture.target.ID, model.OutboundSourceInbound, fixture.sourceIB.ID)
	if err != nil {
		t.Fatal(err)
	}
	outbound := model.Outbound{ServerID: fixture.target.ID, Tag: "source", Type: body.Type, SourceKind: model.OutboundSourceInbound, SourceID: fixture.sourceIB.ID, Settings: model.JSONText(body.Settings)}
	if err := fixture.db.Create(&outbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&fixture.source).Update("public_ipv6", "2001:db8::9").Error; err != nil {
		t.Fatal(err)
	}
	config, err := fixture.app.orch.BuildServerConfig(&fixture.target)
	if err != nil || !strings.Contains(string(config), "2001:db8::9") || strings.Contains(string(config), "2001:db8::7") {
		t.Fatalf("stale dependent: %s %v", config, err)
	}
	if err := fixture.db.Model(&fixture.source).Update("public_ipv6", "").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.orch.BuildServerConfig(&fixture.target); !errors.Is(err, errNodeAddress) {
		t.Fatalf("missing address silently used stale snapshot: %v", err)
	}
}

func TestInboundClientPortMultiplexes443AcrossSubscriptionsAndExports(t *testing.T) {
	db := testDB(t)
	server := model.Server{
		Name:       "edge-hk",
		Address:    "edge.example.com",
		PublicIP:   "203.0.113.10",
		AgentToken: "test-client-port",
	}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	app := &App{db: db, hub: hub, orch: NewOrchestrator(db, hub)}

	vlessSettings := singbox.InboundSettings{
		Listen:     "127.0.0.1",
		ClientPort: 443,
		SingleUser: true,
		UUID:       "11111111-2222-4333-8444-555555555555",
		TLS: singbox.TLSSettings{
			Enabled:    true,
			ServerName: "vless.example.com",
			ACMEDomain: "vless.example.com",
		},
	}
	trojanSettings := singbox.InboundSettings{
		Listen:     "::",
		ClientPort: 443,
		SingleUser: true,
		Password:   "trojan-secret",
		TLS: singbox.TLSSettings{
			Enabled:    true,
			ServerName: "trojan.example.com",
			ACMEDomain: "trojan.example.com",
		},
	}

	if err := app.validateInboundConfiguration(db, server.ID, 0, "vless-in", "vless", 1443, vlessSettings); err != nil {
		t.Fatalf("vless validation failed: %v", err)
	}
	vlessRaw, _ := json.Marshal(vlessSettings)
	vlessIB := model.Inbound{ServerID: server.ID, Tag: "vless-in", Type: model.InboundVLESS, ListenPort: 1443, Enabled: true, Settings: vlessRaw}
	if err := db.Create(&vlessIB).Error; err != nil {
		t.Fatal(err)
	}

	// Sharing client_port=443 across multiple protocols on distinct listen_ports is valid.
	if err := app.validateInboundConfiguration(db, server.ID, 0, "trojan-in", "trojan", 2443, trojanSettings); err != nil {
		t.Fatalf("trojan sharing client_port=443 rejected: %v", err)
	}
	trojanRaw, _ := json.Marshal(trojanSettings)
	trojanIB := model.Inbound{ServerID: server.ID, Tag: "trojan-in", Type: model.InboundTrojan, ListenPort: 2443, Enabled: true, Settings: trojanRaw}
	if err := db.Create(&trojanIB).Error; err != nil {
		t.Fatal(err)
	}

	// Subscriptions advertise 443 for both protocols, including the 127.0.0.1 listener.
	user := model.User{ServerIDs: []uint{server.ID}}
	nodes, err := app.gatherNodesWithError(&user)
	if err != nil {
		t.Fatalf("gatherNodesWithError: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d subscription nodes, want 2: %+v", len(nodes), nodes)
	}
	for _, n := range nodes {
		if n.server != "edge.example.com" || n.port != 443 {
			t.Fatalf("node %s exported %s:%d, want edge.example.com:443", n.tag, n.server, n.port)
		}
	}

	// Admin single-server node format export also advertises 443.
	router := gin.New()
	router.GET("/servers/:id/export", app.serverNodeFormats)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/servers/%d/export", server.ID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", rec.Code, rec.Body.String())
	}
	var exported struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if !strings.Contains(exported.URI, "@edge.example.com:443") || strings.Contains(exported.URI, ":1443") || strings.Contains(exported.URI, ":2443") {
		t.Fatalf("unexpected exported URIs: %s", exported.URI)
	}

	// Cross-server outbound import dials client_port 443.
	client, err := app.inboundOutboundClient(vlessIB.ID, 99)
	if err != nil || client.Server != "edge.example.com" || client.ServerPort != 443 {
		t.Fatalf("inboundOutboundClient = %+v err=%v", client, err)
	}

	// Server sing-box config still binds the actual service ports 1443 and 2443.
	cfg, err := app.orch.BuildServerConfig(&server)
	if err != nil {
		t.Fatalf("BuildServerConfig: %v", err)
	}
	if !strings.Contains(string(cfg), `"listen_port": 1443`) || !strings.Contains(string(cfg), `"listen_port": 2443`) {
		t.Fatalf("server config lost service listen ports: %s", cfg)
	}
	if strings.Contains(string(cfg), "client_port") {
		t.Fatalf("server config leaked client_port: %s", cfg)
	}
}

func TestInboundSubscriptionServerOverride(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "entry", Address: "203.0.113.10", AgentToken: "subscription-address", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &server)
	user := model.User{Email: "reader", Role: model.RoleUser, Enabled: true, ForwardingEnabled: true, ServerIDs: []uint{server.ID}, SubToken: "address-subscription"}
	mustCreate(t, db, &user)
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	router := gin.New()
	router.POST("/servers/:id/inbounds", app.createInbound)
	router.PUT("/servers/:id/inbounds/:inboundID", app.updateInbound)
	router.GET("/servers/:id/export", app.serverNodeFormats)
	router.GET("/sub/:token", app.handleSubscription)
	baseURL := fmt.Sprintf("/servers/%d", server.ID)
	createBody := `{"type":"trojan","tag":"entry-in","listen_port":1443,"settings":{
		"server":"  EDGE.example.com. ","client_port":443,"multi_user":true,
		"tls":{"enabled":true,"certificate_path":"/fixture.crt","key_path":"/fixture.key"},
		"transport":{"type":"ws","path":"/proxy","headers":{"Host":"tls.example.com"}}
	}}`
	res := serveJSONRequest(t, router, http.MethodPost, baseURL+"/inbounds", createBody)
	if res.Code != http.StatusOK {
		t.Fatalf("create inbound: %d %s", res.Code, res.Body)
	}
	var created struct{ Inbound model.Inbound }
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	ib := created.Inbound
	var settings singbox.InboundSettings
	if err := json.Unmarshal(ib.Settings, &settings); err != nil || settings.SubscriptionServer != "edge.example.com" {
		t.Fatalf("normalized subscription server = %q, err=%v", settings.SubscriptionServer, err)
	}
	private := model.Outbound{OwnerUserID: user.ID, Tag: "home", Type: "vless"}
	mustCreate(t, db, &private)
	mustCreate(t, db, &model.UserForward{UserID: user.ID, ServerID: server.ID, InboundID: ib.ID, OutboundID: private.ID})

	details, err := app.userNodeDetails(&user)
	if err != nil || len(details) != 2 {
		t.Fatalf("expected primary and forwarding nodes, got %d, err=%v", len(details), err)
	}
	for _, detail := range details {
		if detail.Server != "edge.example.com" || detail.Port != 443 || detail.Params["SNI"] != server.Address {
			t.Fatalf("unexpected node address, port or SNI: %q %d %q", detail.Server, detail.Port, detail.Params["SNI"])
		}
	}
	for _, format := range []struct {
		name string
		want []string
	}{
		{"links", []string{"@edge.example.com:443", "sni=tls.example.com", "host=tls.example.com"}},
		{"sing-box", []string{`"server": "edge.example.com"`, `"server_name": "tls.example.com"`, `"Host": "tls.example.com"`}},
		{"clash", []string{"server: edge.example.com", "sni: 203.0.113.10", "Host: tls.example.com"}},
		{"shadowrocket", []string{"server: edge.example.com", "sni: 203.0.113.10"}},
		{"surge", []string{"trojan, edge.example.com, 443", "sni=203.0.113.10", "Host:tls.example.com"}},
	} {
		t.Run(format.name, func(t *testing.T) {
			res := serveJSONRequest(t, router, http.MethodGet, "/sub/"+user.SubToken+"?target="+format.name, "")
			if res.Code != http.StatusOK {
				t.Fatalf("subscription status = %d", res.Code)
			}
			for _, want := range format.want {
				if !strings.Contains(res.Body.String(), want) {
					t.Errorf("subscription missing %q", want)
				}
			}
		})
	}

	client, err := app.inboundOutboundClient(ib.ID, server.ID+1)
	if err != nil || client.Server != server.Address || client.ServerPort != 443 {
		t.Fatalf("cross-server outbound host=%q, port=%d, err=%v", client.Server, client.ServerPort, err)
	}
	res = serveJSONRequest(t, router, http.MethodGet, fmt.Sprintf("%s/export?user_id=%d", baseURL, user.ID), "")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "edge.example.com") || !strings.Contains(res.Body.String(), server.Address) {
		t.Fatalf("admin node export picked up the subscription address, status=%d", res.Code)
	}
	raw, err := singbox.BuildInbound(singbox.InboundInput{Tag: ib.Tag, Type: string(ib.Type), ListenPort: ib.ListenPort, Settings: settings})
	if err != nil || strings.Contains(string(raw), "edge.example.com") || !strings.Contains(string(raw), `"listen_port":1443`) {
		t.Fatalf("server inbound changed unexpectedly, err=%v", err)
	}

	path := fmt.Sprintf("%s/inbounds/%d", baseURL, ib.ID)
	var before model.Server
	if err := db.First(&before, server.ID).Error; err != nil {
		t.Fatal(err)
	}
	res = serveJSONRequest(t, router, http.MethodPut, path, `{"settings":{"server":"https://example.com:443"}}`)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "订阅节点地址无效") {
		t.Fatalf("invalid subscription address accepted, status=%d", res.Code)
	}
	res = serveJSONRequest(t, router, http.MethodPut, path, `{"settings":{"server":"[2001:db8::42]"}}`)
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "apply_state") {
		t.Fatalf("subscription address edit should not stage a server configuration, status=%d", res.Code)
	}
	details, err = app.userNodeDetails(&user)
	if err != nil || len(details) != 2 || details[0].Server != "2001:db8::42" || !strings.Contains(details[0].Link, "@[2001:db8::42]:443") {
		t.Fatalf("IPv6 subscription address did not render correctly, err=%v", err)
	}
	res = serveJSONRequest(t, router, http.MethodPut, path, `{"settings":{"server":""}}`)
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "apply_state") {
		t.Fatalf("clearing the subscription address should not stage a server configuration, status=%d", res.Code)
	}
	details, err = app.userNodeDetails(&user)
	if err != nil || len(details) != 2 || details[0].Server != server.Address || details[1].Server != server.Address {
		t.Fatalf("clearing the override did not restore the existing address rule, err=%v", err)
	}
	var after model.Server
	if err := db.First(&after, server.ID).Error; err != nil || after.ConfigRevision != before.ConfigRevision {
		t.Fatalf("subscription address edit changed the server configuration revision, err=%v", err)
	}
}
