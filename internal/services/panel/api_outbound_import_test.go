package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestOutboundImportPreservesClientWireConfig(t *testing.T) {
	links := []string{
		"vless://11111111-2222-4333-8444-555555555555@example.com:443?security=reality&sni=cdn.example.com&pbk=public-key&sid=abcd&fp=firefox&flow=xtls-rprx-vision#VLESS",
		"trojan://secret@example.com:443?security=tls&sni=cdn.example.com&type=ws&path=%2Fws&host=cdn.example.com#Trojan",
		"ss://2022-blake3-aes-128-gcm:server-key%3Auser-key@example.com:443?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dcdn.example.com#SS",
		"hysteria2://secret@example.com:443?sni=cdn.example.com&obfs=salamander&obfs-password=obfs-secret#HY2",
		"tuic://11111111-2222-4333-8444-555555555555:secret@example.com:443?congestion_control=bbr&udp_relay_mode=quic&sni=cdn.example.com#TUIC",
		"anytls://secret@example.com:443?sni=cdn.example.com#AnyTLS",
		"socks5://alice:secret@[2001:db8::1]:1080#SOCKS",
	}
	for _, link := range links {
		t.Run(strings.Split(link, ":")[0], func(t *testing.T) {
			cn, err := singbox.ParseShareLink(link)
			if err != nil {
				t.Fatal(err)
			}
			body, err := outboundFromClientNode(cn)
			if err != nil {
				t.Fatal(err)
			}
			var st outboundStoredSettings
			if err := json.Unmarshal(body.Settings, &st); err != nil {
				t.Fatal(err)
			}
			rendered, err := singbox.BuildServerConfig(singbox.ServerConfigInput{Outbounds: []singbox.OutboundInput{{
				Tag: body.Tag, Type: body.Type, Server: st.Server, ServerPort: st.ServerPort,
				UUID: st.UUID, Username: st.Username, Password: st.Password, Settings: st.Settings,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			expected, err := singbox.BuildClientOutbound(cn)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			var config struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			if err := json.Unmarshal(expected, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(rendered, &config); err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			for _, ob := range config.Outbounds {
				if ob["tag"] == body.Tag {
					got = ob
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("wire config changed: got=%v want=%v", got, want)
			}
			migrated, err := globalNodeFromOutbound(model.Outbound{Tag: body.Tag, Type: body.Type, Settings: model.JSONText(body.Settings)})
			if err != nil {
				t.Fatal(err)
			}
			n, ok := (&App{}).customNodeToNode(&migrated)
			if !ok {
				t.Fatal("migrated node cannot be rendered")
			}
			migratedRaw, err := singbox.BuildClientOutbound(n.clientNode())
			if err != nil {
				t.Fatal(err)
			}
			var migratedWire map[string]any
			if err := json.Unmarshal(migratedRaw, &migratedWire); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(migratedWire, want) {
				t.Fatalf("migration changed protocol fields: got=%v want=%v", migratedWire, want)
			}
		})
	}
}

func TestOutboundImportPreviewAndSave(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "target", AgentToken: "target-token", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	link := "socks5://alice:secret@example.com:1080#landing"
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/servers/:id/preview", app.previewOutboundImport)
	router.POST("/servers/:id/outbounds", app.createOutbound)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, fmt.Sprintf("/servers/%d/%s", srv.ID, path), bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	preview := call(http.MethodPost, "preview", gin.H{"uri": link})
	if preview.Code != 200 || preview.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	var result struct {
		Outbound outboundReq `json:"outbound"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&model.Outbound{}).Count(&count)
	if count != 0 {
		t.Fatal("preview mutated database")
	}
	saved := call(http.MethodPost, "outbounds", result.Outbound)
	if saved.Code != 200 {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	again := call(http.MethodPost, "preview", gin.H{"uri": link})
	if again.Code != 200 || !strings.Contains(again.Body.String(), `"tag":"landing-2"`) {
		t.Fatalf("duplicate tag not resolved: %s", again.Body.String())
	}
	for _, bad := range []gin.H{
		{}, {"uri": ""}, {"uri": "   "},
		{"uri": "https://subscription.example.com"}, {"uri": link + "\n" + link}, {"uri": "socks5://127.0.0.1:1080"},
		{"uri": "socks5://example.com:0"}, {"uri": "hysteria://secret@example.com:443"},
	} {
		w := call(http.MethodPost, "preview", bad)
		if w.Code != 400 {
			t.Fatalf("bad input accepted: %v => %d %s", bad, w.Code, w.Body.String())
		}
	}
	reserved := call(http.MethodPost, "preview", gin.H{"uri": strings.Split(link, "#")[0] + "#direct"})
	if reserved.Code != 200 || !strings.Contains(reserved.Body.String(), `"tag":"direct-2"`) {
		t.Fatalf("reserved tag: %s", reserved.Body.String())
	}
	db.Model(&srv).Update("config_mode", model.ConfigModeRaw)
	if w := call(http.MethodPost, "preview", gin.H{"uri": link}); w.Code != 409 {
		t.Fatalf("raw mode: %d", w.Code)
	}
}
