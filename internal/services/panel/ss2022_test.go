package panel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

const ss2022ChaChaMethod = "2022-blake3-chacha20-poly1305"

func ss2022TestApp(t *testing.T) (*App, *gin.Engine, model.Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{Name: "ss-node", Address: "node.example.com", AgentToken: "ss-token", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	app := &App{db: db, hub: hub, orch: NewOrchestrator(db, hub), serverOperations: newKeyedMutex[uint]()}
	router := gin.New()
	router.POST("/servers/:id/inbounds", app.createInbound)
	router.PUT("/servers/:id/inbounds/:inboundID", app.updateInbound)
	router.GET("/servers/:id/node-formats", app.serverNodeFormats)
	return app, router, server
}

func TestSS2022ChaChaProvisioningRejectsBeforeNormalization(t *testing.T) {
	settings := singbox.InboundSettings{Method: ss2022ChaChaMethod, MultiUser: true}
	if err := fillInboundSecrets("shadowsocks", &settings); err == nil || !strings.Contains(err.Error(), "multi-user") {
		t.Fatalf("provisioning error = %v", err)
	}
	if !settings.MultiUser || settings.SingleUser || settings.SSServerPSK != "" {
		t.Fatalf("invalid mode was silently normalized: %+v", settings)
	}
	legacy := singbox.InboundSettings{Method: "aes-128-gcm", MultiUser: true, SSServerPSK: "legacy-shared-key"}
	if err := fillInboundSecrets("shadowsocks", &legacy); err != nil || legacy.MultiUser || !legacy.SingleUser || legacy.SSServerPSK != "legacy-shared-key" {
		t.Fatalf("legacy shared-credential behavior changed: %+v, err=%v", legacy, err)
	}
}

func TestSS2022ChaChaMultiUserAPICreateRejected(t *testing.T) {
	app, router, server := ss2022TestApp(t)
	for _, singleUser := range []bool{false, true} {
		body := fmt.Sprintf(`{"type":"shadowsocks","tag":"invalid","listen_port":8388,"settings":{"method":%q,"multi_user":true,"single_user":%t}}`, ss2022ChaChaMethod, singleUser)
		res := serveJSONRequest(t, router, http.MethodPost, fmt.Sprintf("/servers/%d/inbounds", server.ID), body)
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "multi-user") {
			t.Fatalf("single_user=%v create response: %d %s", singleUser, res.Code, res.Body.String())
		}
	}
	var count int64
	if err := app.db.Model(&model.Inbound{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("invalid inbound was persisted: count=%d err=%v", count, err)
	}
}

func TestSS2022ChaChaMultiUserAPIUpdateRejected(t *testing.T) {
	app, router, server := ss2022TestApp(t)
	settings := singbox.InboundSettings{Method: "2022-blake3-aes-256-gcm", MultiUser: true, SSServerPSK: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{ServerID: server.ID, Tag: "ss-aes", Type: model.InboundShadowsocks, ListenPort: 8388, Enabled: true, Settings: model.JSONText(raw)}
	if err := app.db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/servers/%d/inbounds/%d", server.ID, inbound.ID)
	body := fmt.Sprintf(`{"tag":"changed","settings":{"method":%q,"multi_user":true}}`, ss2022ChaChaMethod)
	res := serveJSONRequest(t, router, http.MethodPut, path, body)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "multi-user") {
		t.Fatalf("invalid edit response: %d %s", res.Code, res.Body.String())
	}
	var stored model.Inbound
	if err := app.db.First(&stored, inbound.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Tag != inbound.Tag || string(stored.Settings) != string(raw) {
		t.Fatalf("rejected edit changed persisted settings: %+v", stored)
	}
}

func TestSS2022StoredChaChaMultiUserFailsUntilExplicitlyRepaired(t *testing.T) {
	app, router, server := ss2022TestApp(t)
	settings := singbox.InboundSettings{Method: ss2022ChaChaMethod, MultiUser: true, SSServerPSK: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{ServerID: server.ID, Tag: "invalid-chacha", Type: model.InboundShadowsocks, ListenPort: 8388, Enabled: true, Settings: model.JSONText(raw)}
	if err := app.db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	user := model.User{ServerIDs: []uint{server.ID}}
	if _, err := app.orch.BuildServerConfig(&server); err == nil || !strings.Contains(err.Error(), inbound.Tag) || !strings.Contains(err.Error(), "disable multi_user") {
		t.Fatalf("invalid stored config silently compiled: %v", err)
	}
	if _, err := app.gatherNodesWithError(&user); err == nil || !strings.Contains(err.Error(), inbound.Tag) {
		t.Fatalf("invalid stored credentials were silently exported: %v", err)
	}
	formatsPath := fmt.Sprintf("/servers/%d/node-formats", server.ID)
	res := serveJSONRequest(t, router, http.MethodGet, formatsPath, "")
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), inbound.Tag) || strings.Contains(res.Body.String(), settings.SSServerPSK) {
		t.Fatalf("invalid node-format export: %d %s", res.Code, res.Body.String())
	}
	path := fmt.Sprintf("/servers/%d/inbounds/%d", server.ID, inbound.ID)
	res = serveJSONRequest(t, router, http.MethodPut, path, `{"tag":"renamed"}`)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "multi-user") {
		t.Fatalf("invalid tag-only edit: %d %s", res.Code, res.Body.String())
	}
	var unchanged model.Inbound
	if err := app.db.First(&unchanged, inbound.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Tag != inbound.Tag || string(unchanged.Settings) != string(raw) {
		t.Fatal("rejected operations mutated the invalid row")
	}
	res = serveJSONRequest(t, router, http.MethodPut, path, fmt.Sprintf(`{"settings":{"method":%q,"multi_user":false}}`, ss2022ChaChaMethod))
	if res.Code != http.StatusOK {
		t.Fatalf("explicit repair failed: %d %s", res.Code, res.Body.String())
	}
	if _, err := app.orch.BuildServerConfig(&server); err != nil {
		t.Fatalf("repaired config failed: %v", err)
	}
	nodes, err := app.gatherNodesWithError(&user)
	if err != nil || len(nodes) != 1 || nodes[0].settings.MultiUser || !nodes[0].settings.SingleUser || nodes[0].settings.SSServerPSK != settings.SSServerPSK {
		t.Fatalf("repair changed the shared key: %+v err=%v", nodes, err)
	}
	res = serveJSONRequest(t, router, http.MethodGet, formatsPath, "")
	if res.Code != http.StatusOK {
		t.Fatalf("repaired export failed: %d %s", res.Code, res.Body.String())
	}
}

func TestSS2022ValidAPIModesKeepKeysAndSubscriptions(t *testing.T) {
	for _, tc := range []struct {
		method    string
		multiUser bool
		keyLen    int
	}{
		{"2022-blake3-aes-128-gcm", true, 16},
		{"2022-blake3-aes-256-gcm", true, 32},
		{ss2022ChaChaMethod, false, 32},
	} {
		t.Run(tc.method, func(t *testing.T) {
			app, router, server := ss2022TestApp(t)
			body := fmt.Sprintf(`{"type":"shadowsocks","tag":"ss","listen_port":8388,"settings":{"method":%q,"multi_user":%t}}`, tc.method, tc.multiUser)
			res := serveJSONRequest(t, router, http.MethodPost, fmt.Sprintf("/servers/%d/inbounds", server.ID), body)
			if res.Code != http.StatusOK {
				t.Fatalf("valid create: %d %s", res.Code, res.Body.String())
			}
			var inbound model.Inbound
			if err := app.db.Where("server_id = ?", server.ID).First(&inbound).Error; err != nil {
				t.Fatal(err)
			}
			var settings singbox.InboundSettings
			if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
				t.Fatal(err)
			}
			key, err := base64.StdEncoding.DecodeString(settings.SSServerPSK)
			if err != nil || len(key) != tc.keyLen || settings.MultiUser != tc.multiUser || settings.SingleUser == tc.multiUser {
				t.Fatalf("generated settings = %+v, keyLen=%d, err=%v", settings, len(key), err)
			}
			user := model.User{Email: "ss-user", Enabled: true, SubToken: "ss-sub", ProxyToken: "ss-proxy", ServerIDs: []uint{server.ID}}
			if err := app.db.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			raw, err := app.orch.BuildServerConfig(&server)
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Inbounds []map[string]any `json:"inbounds"`
			}
			if err := json.Unmarshal(raw, &config); err != nil || len(config.Inbounds) != 1 {
				t.Fatalf("generated config = %s, err=%v", raw, err)
			}
			wire := config.Inbounds[0]
			wantPassword := settings.SSServerPSK
			if tc.multiUser {
				users, ok := wire["users"].([]any)
				if !ok || len(users) != 1 {
					t.Fatalf("missing AES user: %s", raw)
				}
				wantPassword += ":" + users[0].(map[string]any)["password"].(string)
			} else if _, exists := wire["users"]; exists {
				t.Fatalf("ChaCha must not emit users[]: %s", raw)
			}
			nodes, err := app.gatherNodesWithError(&user)
			if err != nil || len(nodes) != 1 {
				t.Fatalf("subscription nodes = %+v, err=%v", nodes, err)
			}
			if got := singbox.SSClientPassword(nodes[0].settings, nodes[0].user.Password); got != wantPassword {
				t.Fatalf("subscription password = %q, want %q", got, wantPassword)
			}
		})
	}
}
