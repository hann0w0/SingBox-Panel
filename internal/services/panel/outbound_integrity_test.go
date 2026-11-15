package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestSharedOutboundsRejectPrivateOwnersAndTags(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.create(t)
	fixture.router.POST("/servers/:id/outbounds", fixture.app.createOutbound)
	path := fmt.Sprintf("/servers/%d/outbounds", fixture.server.ID)
	for _, body := range []string{
		fmt.Sprintf(`{"tag":"owned","type":"socks","owner_user_id":%d,"settings":{"server":"exit.example","server_port":1080}}`, fixture.user.ID),
		`{"tag":"alice-home","type":"socks","settings":{"server":"exit.example","server_port":1080}}`,
	} {
		response := serveJSONRequest(t, fixture.router, http.MethodPost, path, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid shared outbound accepted: %d %s", response.Code, response.Body)
		}
	}
	tag, err := fixture.app.availableOutboundTag(fixture.server.ID, fixture.outbound.Tag)
	if err != nil || tag == fixture.outbound.Tag {
		t.Fatalf("preview reused private tag %q: %v", tag, err)
	}
	if _, err := fixture.app.orch.BuildServerConfig(&fixture.server); err != nil {
		t.Fatalf("rejected create poisoned config: %v", err)
	}
}

func TestPrivateOutboundDeletionProtectsManualRoutes(t *testing.T) {
	fixture := newForwardFixture(t)
	match := model.JSONText(`{"auth_user":["` + proxyTrafficName(&fixture.user) + `"]}`)
	if err := fixture.app.validateRule(fixture.db, fixture.server.ID, json.RawMessage(match), fixture.outbound.Tag); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, fixture.db, &model.RouteRule{ServerID: fixture.server.ID, Outbound: fixture.outbound.Tag, Match: match, Enabled: true})
	fixture.router.DELETE("/users/:id/outbounds/:outboundID", fixture.app.deleteAdminUserOutbound)
	fixture.router.DELETE("/users/:id", fixture.app.deleteUser)
	response := serveJSONRequest(t, fixture.router, http.MethodDelete, fmt.Sprintf("/users/%d/outbounds/%d", fixture.user.ID, fixture.outbound.ID), "")
	if response.Code != http.StatusConflict {
		t.Fatalf("referenced outbound deleted: %d %s", response.Code, response.Body)
	}
	response = serveJSONRequest(t, fixture.router, http.MethodDelete, fmt.Sprintf("/users/%d", fixture.user.ID), "")
	if response.Code != http.StatusOK {
		t.Fatalf("delete account: %d %s", response.Code, response.Body)
	}
	var rules int64
	fixture.db.Model(&model.RouteRule{}).Where("outbound = ?", fixture.outbound.Tag).Count(&rules)
	if rules != 0 {
		t.Fatalf("account deletion left dangling routes: %d", rules)
	}
}

func TestConfigBuildRejectsMissingRouteTargets(t *testing.T) {
	fixture := newForwardFixture(t)
	mustCreate(t, fixture.db, &model.RouteRule{ServerID: fixture.server.ID, Outbound: "missing", Enabled: true})
	if _, err := fixture.app.orch.BuildServerConfig(&fixture.server); err == nil || !strings.Contains(err.Error(), "missing outbound") {
		t.Fatalf("dangling route accepted: %v", err)
	}
}
