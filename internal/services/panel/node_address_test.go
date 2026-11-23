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
	}
	settings := singbox.InboundSettings{Listen: "::", IPv6Only: true, TLS: singbox.TLSSettings{ServerName: "tls.example.com"}}
	if host, err := inboundNodeAddress(&server, &settings); err != nil || host != "2001:db8::7" || settings.TLS.ServerName != "tls.example.com" {
		t.Fatalf("explicit SNI changed or wrong IPv6 host: %q %+v %v", host, settings.TLS, err)
	}
	server.PublicIPv6 = ""
	if _, err := inboundNodeAddress(&server, &settings); err == nil {
		t.Fatal("IPv6-only inbound without public IPv6 must fail instead of falling back to domain")
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
