package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// fixture builds two managed servers, each with one inbound, plus one external
// node, and a router for the import endpoints.
type nodeImportFixture struct {
	db       *gorm.DB
	app      *App
	source   model.Server
	target   model.Server
	sourceIB model.Inbound
	targetIB model.Inbound
	router   *gin.Engine
}

func newNodeImportFixture(t *testing.T) *nodeImportFixture {
	t.Helper()
	db := testDB(t)
	f := &nodeImportFixture{
		db:     db,
		app:    &App{db: db, orch: NewOrchestrator(db, NewHub(db))},
		source: model.Server{Name: "香港", Address: "hk.example.com", AgentToken: "{{SECRET_hktoken0000000000000000000000000000000000000000000000000000}}", ConfigMode: model.ConfigModeManaged},
		target: model.Server{Name: "日本", Address: "jp.example.com", AgentToken: "{{SECRET_jptoken0000000000000000000000000000000000000000000000000000}}", ConfigMode: model.ConfigModeManaged},
	}
	for _, srv := range []*model.Server{&f.source, &f.target} {
		if err := db.Create(srv).Error; err != nil {
			t.Fatal(err)
		}
	}
	settings, err := json.Marshal(singbox.InboundSettings{
		MultiUser: true,
		TLS: singbox.TLSSettings{Enabled: true, ServerName: "cdn.example.com", Reality: singbox.RealitySettings{
			Enabled: true, HandshakeServer: "www.microsoft.com", PublicKey: "{{SECRET_pubkey0000}}", PrivateKey: "{{SECRET_privkey000}}",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.sourceIB = model.Inbound{ServerID: f.source.ID, Tag: "vless-in", Type: model.InboundVLESS, ListenPort: 443, Enabled: true, Settings: settings}
	if err := db.Create(&f.sourceIB).Error; err != nil {
		t.Fatal(err)
	}
	targetSettings, err := json.Marshal(singbox.InboundSettings{
		SingleUser: true, UUID: "11111111-2222-4333-8444-555555555555",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.targetIB = model.Inbound{ServerID: f.target.ID, Tag: "vless-in", Type: model.InboundVLESS, ListenPort: 443, Enabled: true, Settings: targetSettings}
	if err := db.Create(&f.targetIB).Error; err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	f.router = gin.New()
	f.router.GET("/servers/:id/sources", f.app.listOutboundSources)
	f.router.POST("/servers/:id/preview", f.app.previewOutboundImport)
	f.router.POST("/servers/:id/outbounds", f.app.createOutbound)
	return f
}

func (f *nodeImportFixture) call(t *testing.T, method, serverID, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, fmt.Sprintf("/servers/%s/%s", serverID, path), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func TestOutboundSourceListingMarksOwnNodesAndUnavailableSources(t *testing.T) {
	f := newNodeImportFixture(t)
	recorder := f.call(t, http.MethodGet, fmt.Sprint(f.target.ID), "sources", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list: %d %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Sources []outboundSource `json:"sources"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sources) != 2 {
		t.Fatalf("want the other server's inbound plus nothing else, got %+v", body.Sources)
	}
	foreign := body.Sources[0]
	if foreign.Kind != model.OutboundSourceInbound || foreign.ID != f.sourceIB.ID {
		t.Fatalf("unexpected first source: %+v", foreign)
	}
	if foreign.Disabled || foreign.Server != "hk.example.com" || foreign.Port != 443 {
		t.Fatalf("foreign source not usable: %+v", foreign)
	}
}

func TestOutboundSourceListingExcludesTargetServerNodes(t *testing.T) {
	f := newNodeImportFixture(t)
	recorder := f.call(t, http.MethodGet, fmt.Sprint(f.source.ID), "sources", nil)
	var body struct {
		Sources []outboundSource `json:"sources"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range body.Sources {
		if row.ID == f.sourceIB.ID && row.Kind == model.OutboundSourceInbound {
			if !row.Disabled || row.Reason == "" {
				t.Fatalf("a server's own inbound must be offered but disabled: %+v", row)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("the server's own inbound is missing from the list")
	}
}

func TestInboundOutboundImportFollowsLiveSourceAndIssuesCredential(t *testing.T) {
	f := newNodeImportFixture(t)
	preview := f.call(t, http.MethodPost, fmt.Sprint(f.target.ID), "preview", gin.H{
		"source_kind": model.OutboundSourceInbound, "source_id": f.sourceIB.ID,
	})
	if preview.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	var result struct {
		Outbound outboundReq `json:"outbound"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Outbound.Tag == "" || result.Outbound.Type != "vless" {
		t.Fatalf("preview did not produce an outbound: %+v", result.Outbound)
	}
	if result.Outbound.Tag != "香港 - vless-in" {
		t.Fatalf("tag = %q", result.Outbound.Tag)
	}

	saved := f.call(t, http.MethodPost, fmt.Sprint(f.target.ID), "outbounds", gin.H{
		"tag": result.Outbound.Tag, "type": result.Outbound.Type, "settings": result.Outbound.Settings,
		"source_kind": model.OutboundSourceInbound, "source_id": f.sourceIB.ID,
	})
	if saved.Code != http.StatusOK {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	var stored model.Outbound
	if err := f.db.Where("server_id = ?", f.target.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.Referenced() || stored.SourceID != f.sourceIB.ID {
		t.Fatalf("source link not persisted: %+v", stored)
	}

	// The source server must provision the consumer's dedicated credential.
	orch := &Orchestrator{db: f.db, app: f.app}
	raw, err := orch.BuildServerConfig(&f.source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), routingOutboundUserName) {
		t.Fatalf("source config lacks the routing credential: %s", raw)
	}

	// Changing the source's TLS server name must reach the target's outbound
	// without re-importing it.
	if err := f.db.Model(&model.Inbound{}).Where("id = ?", f.sourceIB.ID).
		Update("settings", mustJSON(t, singbox.InboundSettings{
			MultiUser: true,
			TLS: singbox.TLSSettings{Enabled: true, ServerName: "new-cdn.example.com", Reality: singbox.RealitySettings{
				Enabled: true, HandshakeServer: "www.microsoft.com", PublicKey: "{{SECRET_pubkey0000}}", PrivateKey: "{{SECRET_privkey000}}",
			}},
		})).Error; err != nil {
		t.Fatal(err)
	}
	targetRaw, err := orch.BuildServerConfig(&f.target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(targetRaw), "new-cdn.example.com") {
		t.Fatalf("target outbound did not follow the edited source: %s", targetRaw)
	}
	if strings.Contains(string(targetRaw), "cdn.example.com\"") && !strings.Contains(string(targetRaw), "new-cdn.example.com") {
		t.Fatalf("target outbound kept the stale source snapshot: %s", targetRaw)
	}
}

func mustJSON(t *testing.T, value any) model.JSONText {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return model.JSONText(raw)
}

func TestInboundReferencedByOutboundCannotBeDeletedOrDisabled(t *testing.T) {
	f := newNodeImportFixture(t)
	if err := f.db.Create(&model.Outbound{
		ServerID: f.target.ID, Tag: "via-hk", Type: "vless",
		SourceKind: model.OutboundSourceInbound, SourceID: f.sourceIB.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	app := f.app
	router.DELETE("/servers/:id/inbounds/:inboundID", app.deleteInbound)
	router.PUT("/servers/:id/inbounds/:inboundID", app.updateInbound)
	call := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, fmt.Sprintf("/servers/%d/inbounds/%d", f.source.ID, f.sourceIB.ID), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	if w := call(http.MethodDelete, ""); w.Code != http.StatusConflict {
		t.Fatalf("delete referenced inbound = %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPut, `{"enabled":false}`); w.Code != http.StatusConflict {
		t.Fatalf("disable referenced inbound = %d %s", w.Code, w.Body.String())
	}
	// Disabling an unreferenced inbound still works.
	if err := f.db.Delete(&model.Outbound{}, "server_id = ?", f.target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if w := call(http.MethodPut, `{"enabled":false}`); w.Code != http.StatusOK {
		t.Fatalf("disable unreferenced inbound = %d %s", w.Code, w.Body.String())
	}
}

func TestCustomNodeImportUsesTheNodeCredential(t *testing.T) {
	f := newNodeImportFixture(t)
	node := model.CustomNode{
		Name: "机场节点 A", Enabled: true, Link: "trojan://airport-secret@a.example.net:443?sni=a.example.net#airport",
	}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	preview := f.call(t, http.MethodPost, fmt.Sprint(f.target.ID), "preview", gin.H{
		"source_kind": model.OutboundSourceCustomNode, "source_id": node.ID,
	})
	if preview.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	if err := f.db.Model(&model.CustomNode{}).Where("id = ?", node.ID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if w := f.call(t, http.MethodPost, fmt.Sprint(f.target.ID), "preview", gin.H{
		"source_kind": model.OutboundSourceCustomNode, "source_id": node.ID,
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("disabled custom node accepted: %d %s", w.Code, w.Body.String())
	}
}
