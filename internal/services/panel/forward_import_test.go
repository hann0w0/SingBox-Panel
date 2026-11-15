package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestManagedForwardConfigImportPreservesOwnershipAndIdentity(t *testing.T) {
	fixture := newForwardFixture(t)
	forward := fixture.create(t)
	raw, err := fixture.app.orch.BuildServerConfig(&fixture.server)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.applyImport(&fixture.server, parsed, raw); err != nil {
		t.Fatal(err)
	}
	fixture.db.First(&fixture.server, fixture.server.ID)
	fixture.db.First(&fixture.inbound, fixture.inbound.ID)
	var settings singbox.InboundSettings
	json.Unmarshal(fixture.inbound.Settings, &settings)
	if fixture.server.EffectiveConfigMode() != model.ConfigModeManaged || !settings.MultiUser {
		t.Fatalf("managed mode lost: mode=%s settings=%+v", fixture.server.ConfigMode, settings)
	}
	var shared int64
	fixture.db.Model(&model.Outbound{}).Where("server_id = ? AND tag = ?", fixture.server.ID, fixture.outbound.Tag).Count(&shared)
	if shared != 0 {
		t.Fatalf("private outbound copied into shared list: %d", shared)
	}
	var rule model.RouteRule
	if err := fixture.db.Where("outbound = ?", fixture.outbound.Tag).First(&rule).Error; err != nil {
		t.Fatal(err)
	}
	var match singbox.RuleInput
	json.Unmarshal(rule.Match, &match)
	if len(match.AuthUser) != 1 || match.AuthUser[0] != forwardTrafficName(&fixture.user, forward.ID) {
		t.Fatalf("forward became global: %+v", match)
	}
	if _, err := fixture.app.orch.BuildServerConfig(&fixture.server); err != nil {
		t.Fatalf("round-trip configuration broken: %v", err)
	}
	fixture.db.Model(&fixture.server).Updates(map[string]any{"config_mode": model.ConfigModeRaw, "raw_config": model.JSONText(raw)})
	if err := fixture.app.applyImport(&fixture.server, parsed, raw); err != nil {
		t.Fatal(err)
	}
	fixture.db.First(&fixture.server, fixture.server.ID)
	if fixture.server.EffectiveConfigMode() != model.ConfigModeManaged {
		t.Fatal("identical raw apply lost managed ownership")
	}
}

func TestImportedSharedOutboundCannotOverwritePrivateTag(t *testing.T) {
	fixture := newForwardFixture(t)
	raw := []byte(`{"outbounds":[{"type":"socks","tag":"alice-home","server":"other.example","server_port":1080}]}`)
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.applyImport(&fixture.server, parsed, raw); err == nil {
		t.Fatal("colliding private outbound imported")
	}
	var count int64
	fixture.db.Model(&model.Outbound{}).Where("owner_user_id = 0").Count(&count)
	if count != 0 {
		t.Fatalf("failed import left shared rows: %d", count)
	}
}

func TestRawApplyRejectsPrivateTagConflictBeforeAgentCommand(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.db.First(&fixture.server, fixture.server.ID)
	previousRaw := string(fixture.server.RawConfig)
	fixture.router.POST("/servers/:id/apply-raw", fixture.app.applyRawConfig)
	body, _ := json.Marshal(map[string]any{"config": `{"outbounds":[{"type":"socks","tag":"alice-home","server":"other.example","server_port":1080}]}`})
	response := serveJSONRequest(t, fixture.router, http.MethodPost, fmt.Sprintf("/servers/%d/apply-raw", fixture.server.ID), string(body))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("conflict reached agent: %d %s", response.Code, response.Body)
	}
	fixture.db.First(&fixture.server, fixture.server.ID)
	if fixture.server.EffectiveConfigMode() != model.ConfigModeManaged || string(fixture.server.RawConfig) != previousRaw {
		t.Fatal("rejected apply changed config mode")
	}
}
