package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestManagedMultiUserImportSuppressesBogusSkippedWarning(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.router.POST("/servers/:id/import-config", fixture.app.importRemoteConfig)

	// Add a second panel user so the multi-user inbound has 2 panel-generated users.
	second := model.User{
		Email:      "bob@example.com",
		Role:       model.RoleUser,
		Enabled:    true,
		ServerIDs:  []uint{fixture.server.ID},
		ProxyToken: "bob-proxy-token",
		SubToken:   "bob-sub-token",
	}
	if err := fixture.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}

	raw, err := fixture.app.orch.BuildServerConfig(&fixture.server)
	if err != nil {
		t.Fatal(err)
	}

	// Edit an unrelated top-level setting (log.level) while leaving the panel
	// multi-user list intact, mirroring saving raw config.json on a managed node.
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["log"] = map[string]any{"level": "fatal", "timestamp": true}
	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	callImport := func(cfg []byte, dryRun bool) importSummary {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"config": string(cfg), "dry_run": dryRun})
		rec := serveJSONRequest(t, fixture.router, http.MethodPost, fmt.Sprintf("/servers/%d/import-config", fixture.server.ID), string(payload))
		if rec.Code != http.StatusOK {
			t.Fatalf("import-config (dry_run=%v) failed: %d %s", dryRun, rec.Code, rec.Body.String())
		}
		var resp struct {
			Summary importSummary `json:"summary"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return resp.Summary
	}

	drySum := callImport(edited, true)
	if len(drySum.Skipped) != 0 {
		t.Fatalf("dry-run reported unexpected skipped items for panel-managed multi-user inbound: %v", drySum.Skipped)
	}
	if len(drySum.Inbounds) != 1 || drySum.Inbounds[0].SingleUser || drySum.Inbounds[0].Users != 2 {
		t.Fatalf("unexpected dry-run inbound summary: %+v", drySum.Inbounds)
	}

	appliedSum := callImport(edited, false)
	if len(appliedSum.Skipped) != 0 {
		t.Fatalf("import reported unexpected skipped items for panel-managed multi-user inbound: %v", appliedSum.Skipped)
	}
	fixture.db.First(&fixture.inbound, fixture.inbound.ID)
	var savedSettings singbox.InboundSettings
	if err := json.Unmarshal(fixture.inbound.Settings, &savedSettings); err != nil {
		t.Fatal(err)
	}
	if !savedSettings.MultiUser || savedSettings.SingleUser {
		t.Fatalf("expected multi-user mode preserved, got %+v", savedSettings)
	}

	// Changing VLESS flow across the same panel-managed users should still be
	// recognized as managed without emitting a bogus manual-credentials warning.
	inbounds := doc["inbounds"].([]any)
	firstInbound := inbounds[0].(map[string]any)
	users := firstInbound["users"].([]any)
	for _, entry := range users {
		entry.(map[string]any)["flow"] = "xtls-rprx-vision"
	}
	flowEdited, _ := json.Marshal(doc)
	if sum := callImport(flowEdited, true); len(sum.Skipped) != 0 {
		t.Fatalf("flow edit on panel-managed users produced unexpected skipped items: %v", sum.Skipped)
	}

	// Appending a genuine hand-written user must still be flagged in Skipped.
	firstInbound["users"] = append(users, map[string]any{
		"name": "manual-friend",
		"uuid": "33333333-3333-4333-8333-333333333333",
	})
	manualEdited, _ := json.Marshal(doc)
	manualSum := callImport(manualEdited, true)
	if len(manualSum.Skipped) != 1 || !strings.Contains(manualSum.Skipped[0], "含 3 个手写用户凭证") {
		t.Fatalf("expected manual credential warning, got %v", manualSum.Skipped)
	}
}

func TestManualMultiUserImportOnFreshServerStillWarns(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.router.POST("/servers/:id/import-config", fixture.app.importRemoteConfig)

	fresh := model.Server{Name: "Fresh", Address: "fresh.example.com", AgentToken: "fresh-token"}
	if err := fixture.db.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	raw := `{
		"inbounds": [{
			"type": "vless",
			"tag": "vless-manual",
			"listen": "::",
			"listen_port": 443,
			"users": [
				{"name": "alice", "uuid": "11111111-1111-4111-8111-111111111111"},
				{"name": "bob", "uuid": "22222222-2222-4222-8222-222222222222"}
			]
		}],
		"outbounds": [{"type": "direct", "tag": "direct"}],
		"route": {"final": "direct"}
	}`
	payload, _ := json.Marshal(map[string]any{"config": raw, "dry_run": true})
	rec := serveJSONRequest(t, fixture.router, http.MethodPost, fmt.Sprintf("/servers/%d/import-config", fresh.ID), string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh import failed: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Summary importSummary `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Summary.Skipped) != 1 || !strings.Contains(resp.Summary.Skipped[0], "vless-manual") {
		t.Fatalf("expected manual multi-user warning on fresh server, got %v", resp.Summary.Skipped)
	}
}

func TestManagedMultiUserImportPreservedWithOverlayOutboundAndSingleUserDrift(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.router.POST("/servers/:id/import-config", fixture.app.importRemoteConfig)

	raw, err := fixture.app.orch.BuildServerConfig(&fixture.server)
	if err != nil {
		t.Fatal(err)
	}
	// Inject an overlay-only selector outbound and set route.final to it.
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	outbounds := doc["outbounds"].([]any)
	doc["outbounds"] = append(outbounds, map[string]any{
		"type":      "selector",
		"tag":       "overlay-select",
		"outbounds": []string{"direct"},
	})
	doc["route"].(map[string]any)["final"] = "overlay-select"
	withOverlay, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"config": string(withOverlay), "dry_run": false})
	rec := serveJSONRequest(t, fixture.router, http.MethodPost, fmt.Sprintf("/servers/%d/import-config", fixture.server.ID), string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("overlay import failed: %d %s", rec.Code, rec.Body.String())
	}

	// Rotate the single user's proxy_token in DB so raw users[] (1 user) drifts
	// from generated users[], then re-import with an edited log level.
	if err := fixture.db.Model(&fixture.user).Update("proxy_token", "rotated-single-user-token").Error; err != nil {
		t.Fatal(err)
	}
	doc["log"] = map[string]any{"level": "warn"}
	driftedRaw, _ := json.Marshal(doc)
	driftedPayload, _ := json.Marshal(map[string]any{"config": string(driftedRaw), "dry_run": false})
	rec2 := serveJSONRequest(t, fixture.router, http.MethodPost, fmt.Sprintf("/servers/%d/import-config", fixture.server.ID), string(driftedPayload))
	if rec2.Code != http.StatusOK {
		t.Fatalf("second overlay import failed: %d %s", rec2.Code, rec2.Body.String())
	}

	var updatedInbound model.Inbound
	if err := fixture.db.First(&updatedInbound, fixture.inbound.ID).Error; err != nil {
		t.Fatal(err)
	}
	var st singbox.InboundSettings
	if err := json.Unmarshal(updatedInbound.Settings, &st); err != nil {
		t.Fatal(err)
	}
	if !st.MultiUser || st.SingleUser {
		t.Fatalf("multi-user inbound silently downgraded to single-user: %+v", st)
	}
}

func TestSyncImportedInboundsUnlinksCrossServerConsumerOutbounds(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.router.POST("/servers/:id/import-config", fixture.app.importRemoteConfig)

	consumerServer := model.Server{Name: "Consumer", Address: "consumer.example.com", AgentToken: "consumer-token"}
	if err := fixture.db.Create(&consumerServer).Error; err != nil {
		t.Fatal(err)
	}
	consumerOb := model.Outbound{
		ServerID:   consumerServer.ID,
		Tag:        "to-origin",
		Type:       "vless",
		SourceKind: model.OutboundSourceInbound,
		SourceID:   fixture.inbound.ID,
		Settings:   model.JSONText(`{"server":"entry.example.com","server_port":443}`),
	}
	if err := fixture.db.Create(&consumerOb).Error; err != nil {
		t.Fatal(err)
	}

	emptyInboundsCfg := `{"inbounds":[],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`
	payload, _ := json.Marshal(map[string]any{"config": emptyInboundsCfg, "dry_run": false})
	rec := serveJSONRequest(t, fixture.router, http.MethodPost, fmt.Sprintf("/servers/%d/import-config", fixture.server.ID), string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("import-config failed: %d %s", rec.Code, rec.Body.String())
	}

	var refreshed model.Outbound
	if err := fixture.db.First(&refreshed, consumerOb.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.SourceKind != "" || refreshed.SourceID != 0 {
		t.Fatalf("deleted inbound left dangling cross-server source reference: %+v", refreshed)
	}
}
