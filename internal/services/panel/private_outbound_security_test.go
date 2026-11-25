package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestUserOutboundImportRejectsUnsafeTargets(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.router.POST("/import", fixture.app.importUserOutbound)
	for _, host := range []string{
		"127.0.0.1", "0.0.0.0", "localhost", "10.0.0.1", "169.254.169.254", "100.100.100.200",
		"[::1]", "[fd00:ec2::254]", "0x7f000001", "0177.0.0.1", "2130706433", "127.1",
	} {
		t.Run(host, func(t *testing.T) {
			body := fmt.Sprintf(`{"uri":"vless://11111111-2222-4333-8444-555555555555@%s:9090#unsafe"}`, host)
			response := serveJSONRequest(t, fixture.router, http.MethodPost, "/import", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("unsafe outbound accepted: %d %s", response.Code, response.Body)
			}
		})
	}
	var count int64
	fixture.db.Model(&model.Outbound{}).Count(&count)
	if count != 1 {
		t.Fatalf("unsafe imports persisted %d rows", count-1)
	}
}

func TestPrivateOutboundRejectsPrivateDNSAndRollsBackFailedForwardApply(t *testing.T) {
	prevLookup := lookupPrivateOutboundIPAddrs
	lookupPrivateOutboundIPAddrs = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "rebind.example" {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	t.Cleanup(func() { lookupPrivateOutboundIPAddrs = prevLookup })

	fixture := newForwardFixture(t)
	fixture.router.POST("/import", fixture.app.importUserOutbound)
	fixture.router.POST("/outbounds", fixture.app.createUserOutbound)

	resp := serveJSONRequest(t, fixture.router, http.MethodPost, "/import", `{"uri":"vless://11111111-2222-4333-8444-555555555555@rebind.example:443#rebind"}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected private-DNS domain to be rejected, got %d %s", resp.Code, resp.Body.String())
	}

	// Bracketed IPv6 in manual outbound is normalized to unbracketed host.
	v6Resp := serveJSONRequest(t, fixture.router, http.MethodPost, "/outbounds", `{"tag":"v6-exit","type":"vless","settings":{"server":"[2001:4860:4860::8888]","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}}`)
	if v6Resp.Code != http.StatusOK {
		t.Fatalf("create IPv6 outbound: %d %s", v6Resp.Code, v6Resp.Body.String())
	}
	var v6Row model.Outbound
	if err := fixture.db.Where("tag = ?", "v6-exit").First(&v6Row).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(v6Row.Settings), "[2001:4860:4860::8888]") {
		t.Fatalf("IPv6 brackets were not stripped from stored settings: %s", v6Row.Settings)
	}

	// Connect an online Agent that rejects the apply command (e.g. restricted outbound validation failed on node).
	hub := NewHub(fixture.db)
	fixture.app.hub = hub
	fixture.app.orch = NewOrchestrator(fixture.db, hub)
	conn := &agentConn{
		serverID: fixture.server.ID,
		send:     make(chan protocol.Envelope, 4),
		done:     make(chan struct{}),
		pending:  make(map[string]chan protocol.CommandResultEvt),
	}
	hub.conns[fixture.server.ID] = conn
	t.Cleanup(func() { close(conn.done) })
	go func() {
		first := true
		for {
			select {
			case <-conn.done:
				return
			case env := <-conn.send:
				conn.mu.Lock()
				ch := conn.pending[env.ID]
				conn.mu.Unlock()
				if first {
					first = false
					ch <- protocol.CommandResultEvt{ID: env.ID, OK: false, Error: "restricted outbound rejected by agent"}
				} else {
					ch <- protocol.CommandResultEvt{ID: env.ID, OK: true}
				}
			}
		}
	}()

	forwardBody := fmt.Sprintf(`{"inbound_server_id":%d,"inbound_tag":%q,"outbound_id":%d}`, fixture.server.ID, fixture.inbound.Tag, fixture.outbound.ID)
	forwardResp := serveJSONRequest(t, fixture.router, http.MethodPost, "/forwards", forwardBody)
	if forwardResp.Code != http.StatusBadGateway {
		t.Fatalf("expected failed online apply to return 502 and roll back, got %d %s", forwardResp.Code, forwardResp.Body.String())
	}
	var forwardCount, ruleCount int64
	fixture.db.Model(&model.UserForward{}).Where("user_id = ?", fixture.user.ID).Count(&forwardCount)
	fixture.db.Model(&model.RouteRule{}).Where("server_id = ?", fixture.server.ID).Count(&ruleCount)
	if forwardCount != 0 || ruleCount != 0 {
		t.Fatalf("failed forward was not rolled back: forwards=%d rules=%d", forwardCount, ruleCount)
	}
}

func TestOverlayOutboundTagAvoidedAndIPv6Normalized(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.router.POST("/preview", fixture.app.previewUserOutbound)

	rawWithOverlay := []byte(`{
		"inbounds": [{"type":"vless","tag":"entry-in","listen":"::","listen_port":443,"users":[{"name":"u","uuid":"11111111-2222-4333-8444-555555555555"}]}],
		"outbounds": [
			{"type":"direct","tag":"direct"},
			{"type":"wireguard","tag":"wg-overlay","local_address":["10.0.0.2/32"],"private_key":"k","peers":[{"server":"1.1.1.1","server_port":51820,"public_key":"p"}]}
		]
	}`)
	parsedCfg, err := singbox.ParseServerConfig(rawWithOverlay)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.applyImport(&fixture.server, parsedCfg, rawWithOverlay); err != nil {
		t.Fatal(err)
	}

	adminTag, err := fixture.app.availableOutboundTag(fixture.server.ID, "wg-overlay")
	if err != nil || adminTag != "wg-overlay-2" {
		t.Fatalf("availableOutboundTag = %q, err = %v; want wg-overlay-2", adminTag, err)
	}
	privTag, err := availablePrivateOutboundTag(fixture.db, "wg-overlay")
	if err != nil || privTag != "wg-overlay-2" {
		t.Fatalf("availablePrivateOutboundTag = %q, err = %v; want wg-overlay-2", privTag, err)
	}

	colliding := model.Outbound{
		OwnerUserID: fixture.user.ID,
		Tag:         "wg-overlay",
		Type:        "vless",
		Settings:    model.JSONText(`{"server":"home.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`),
	}
	mustCreate(t, fixture.db, &colliding)
	forwardBody := fmt.Sprintf(`{"inbound_server_id":%d,"inbound_tag":%q,"outbound_id":%d}`, fixture.server.ID, fixture.inbound.Tag, colliding.ID)
	conflictResp := serveJSONRequest(t, fixture.router, http.MethodPost, "/forwards", forwardBody)
	if conflictResp.Code != http.StatusConflict {
		t.Fatalf("expected 409 on overlay outbound tag collision, got %d %s", conflictResp.Code, conflictResp.Body.String())
	}

	reqNode := customNodeReq{Name: "v6-custom", Protocol: "trojan", Address: "[2001:4860:4860::8844]", Port: 443, Params: json.RawMessage(`{"password":"secret","sni":"example.com"}`)}
	if _, err := validateCustomNode(&reqNode); err != nil {
		t.Fatalf("validateCustomNode IPv6: %v", err)
	}
	if reqNode.Address != "2001:4860:4860::8844" {
		t.Fatalf("validateCustomNode did not strip IPv6 brackets: %q", reqNode.Address)
	}
	cmd := outboundTestCommand("vless", outboundStoredSettings{Server: "[2001:4860:4860::8888]", ServerPort: 443})
	if cmd.Host != "2001:4860:4860::8888" {
		t.Fatalf("outboundTestCommand Host = %q", cmd.Host)
	}
}

func TestUserOutboundConcurrentImportsAllocateDistinctTags(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.router.POST("/import", fixture.app.importUserOutbound)
	responses := make(chan int, 2)
	for range 2 {
		go func() {
			response := serveJSONRequest(t, fixture.router, http.MethodPost, "/import", `{"uri":"vless://11111111-2222-4333-8444-555555555555@exit.example:443#same"}`)
			responses <- response.Code
		}()
	}
	for range 2 {
		if status := <-responses; status != http.StatusOK {
			t.Fatalf("import status=%d", status)
		}
	}
	var rows []model.Outbound
	fixture.db.Where("tag LIKE ?", "same%").Order("tag").Find(&rows)
	if len(rows) != 2 || rows[0].Tag == rows[1].Tag {
		t.Fatalf("tags collide: %+v", rows)
	}
}
