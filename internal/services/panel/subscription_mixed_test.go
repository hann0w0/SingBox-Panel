package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestYAMLSubscriptionsKeepStructuredSOCKSCredentials(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "managed", Address: "managed.example.com", AgentToken: "agent-token"}
	mustCreate(t, db, &server)
	inbound := model.Inbound{
		ServerID: server.ID, Tag: "vless", Type: model.InboundVLESS, ListenPort: 443,
		Enabled: true, Settings: model.JSONText(`{"multi_user":true}`),
	}
	mustCreate(t, db, &inbound)
	alice := model.User{
		Email: "alice", Enabled: true, SubToken: "alice-sub", ProxyToken: "alice-proxy",
		ServerIDs: []uint{server.ID},
	}
	bob := model.User{
		Email: "bob", Enabled: true, SubToken: "bob-sub", ProxyToken: "bob-proxy",
		ServerIDs: []uint{server.ID},
	}
	mustCreate(t, db, &alice)
	mustCreate(t, db, &bob)
	app := &App{db: db}
	router := gin.New()
	router.POST("/nodes", app.createCustomNode)
	router.GET("/sub/:token", app.handleSubscription)

	for _, protocol := range []string{"socks", "mixed"} {
		for _, authenticated := range []bool{false, true} {
			name := protocol + "-anonymous"
			params := map[string]string{}
			if authenticated {
				name = protocol + "-authenticated"
				params["username"], params["password"] = "proxy-user", "proxy-password"
			}
			payload, err := json.Marshal(map[string]any{
				"name": name, "protocol": protocol, "address": "proxy.example.com", "port": 1080,
				"params": params, "user_ids": []uint{alice.ID}, "all_users": false,
			})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/nodes", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("create %s: %d %s", name, response.Code, response.Body.String())
			}
		}
	}

	for _, format := range []string{"clash", "shadowrocket"} {
		t.Run(format, func(t *testing.T) {
			for _, user := range []model.User{alice, bob} {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sub/"+user.SubToken+"?target="+format, nil))
				if response.Code != http.StatusOK {
					t.Fatalf("subscription: %d %s", response.Code, response.Body.String())
				}
				var document struct {
					Proxies []map[string]any `yaml:"proxies"`
				}
				if err := yaml.Unmarshal(response.Body.Bytes(), &document); err != nil {
					t.Fatal(err)
				}
				wantCount := 1
				if user.ID == alice.ID {
					wantCount = 5
				}
				if len(document.Proxies) != wantCount {
					t.Fatalf("%s received %d proxies, want %d", user.Email, len(document.Proxies), wantCount)
				}
				byName := map[string]map[string]any{}
				for _, proxy := range document.Proxies {
					name, _ := proxy["name"].(string)
					byName[name] = proxy
				}
				managed := byName["managed"]
				if managed["type"] != "vless" || managed["uuid"] != proxyIdentity(&user, inbound.ID).UUID {
					t.Fatalf("%s managed identity changed: %v", user.Email, managed)
				}
				if user.ID != alice.ID {
					continue
				}
				for _, protocol := range []string{"socks", "mixed"} {
					authenticated := byName[protocol+"-authenticated"]
					if authenticated["type"] != "socks5" || authenticated["username"] != "proxy-user" || authenticated["password"] != "proxy-password" {
						t.Errorf("%s credentials lost: %v", protocol, authenticated)
					}
					anonymous := byName[protocol+"-anonymous"]
					if anonymous["type"] != "socks5" || anonymous["username"] != nil || anonymous["password"] != nil {
						t.Errorf("%s anonymous credentials changed: %v", protocol, anonymous)
					}
				}
			}
		})
	}
}
