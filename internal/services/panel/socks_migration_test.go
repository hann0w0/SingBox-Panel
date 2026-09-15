package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestSOCKSSingleUserMigrationPreservesCredentialsAndRawConfigs(t *testing.T) {
	db := testDB(t)
	managed := model.Server{Name: "managed", AgentToken: "managed", ConfigMode: model.ConfigModeManaged}
	raw := model.Server{Name: "raw", AgentToken: "raw", ConfigMode: model.ConfigModeRaw}
	for _, server := range []*model.Server{&managed, &raw} {
		if err := db.Create(server).Error; err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name         string
		serverID     uint
		settings     string
		wantUsername string
		wantPassword string
		unchanged    bool
	}{
		{"shared", managed.ID, `{"multi_user":true,"username":"alice","password":"existing","extension":{"keep":true}}`, "alice", "existing", false},
		{"placeholder", managed.ID, `{"multi_user":true,"username":"__singbox_panel_disabled__","password":"existing"}`, "singbox", "existing", false},
		{"missing", managed.ID, `{"multi_user":true}`, "singbox", "", false},
		{"missing password", managed.ID, `{"multi_user":true,"username":"alice"}`, "alice", "", false},
		{"missing username", managed.ID, `{"multi_user":true,"password":"existing"}`, "singbox", "existing", false},
		{"single user", managed.ID, `{"single_user":true,"username":"alice","password":"existing"}`, "", "", true},
		{"no auth", managed.ID, `{"single_user":true}`, "", "", true},
		{"raw", raw.ID, `{"multi_user":true}`, "", "", true},
	}
	inbounds := make([]model.Inbound, len(tests))
	for n, tc := range tests {
		inbounds[n] = model.Inbound{
			ServerID: tc.serverID, Tag: fmt.Sprintf("socks-%d", n), Type: model.InboundSocks,
			ListenPort: 1080 + n, Settings: model.JSONText(tc.settings), Enabled: true,
		}
		if err := db.Create(&inbounds[n]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Transaction(migrateSOCKSSingleUserInbounds); err != nil {
		t.Fatal(err)
	}
	for n, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.First(&inbounds[n], inbounds[n].ID).Error; err != nil {
				t.Fatal(err)
			}
			if tc.unchanged {
				if string(inbounds[n].Settings) != tc.settings {
					t.Fatal("explicit single-user or raw settings changed")
				}
				return
			}
			var settings singbox.InboundSettings
			if err := json.Unmarshal(inbounds[n].Settings, &settings); err != nil {
				t.Fatal(err)
			}
			if settings.MultiUser || !settings.SingleUser || settings.Username != tc.wantUsername {
				t.Fatal("legacy SOCKS was not migrated to shared credentials")
			}
			if tc.wantPassword != "" && settings.Password != tc.wantPassword {
				t.Fatal("existing password changed")
			}
			if tc.wantPassword == "" && len(settings.Password) != 64 {
				t.Fatal("missing shared password was not generated")
			}
			if n == 0 {
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(inbounds[n].Settings, &fields)
				if string(fields["extension"]) != `{"keep":true}` {
					t.Fatal("unrecognized settings were lost")
				}
			}
		})
	}
	if err := db.Transaction(migrateSOCKSSingleUserInbounds); err != nil {
		t.Fatal(err)
	}
	for _, before := range inbounds {
		var after model.Inbound
		if err := db.First(&after, before.ID).Error; err != nil {
			t.Fatal(err)
		}
		if string(after.Settings) != string(before.Settings) {
			t.Fatalf("migration changed inbound %d on its second run", before.ID)
		}
	}
}

func TestSOCKSLegacyNormalizationKeepsExplicitNoAuthAndStableLogin(t *testing.T) {
	open := singbox.InboundSettings{SingleUser: true}
	if err := fillInboundSecrets("socks", &open); err != nil {
		t.Fatal(err)
	}
	if open.Username != "" || open.Password != "" {
		t.Fatal("explicit no-auth SOCKS was changed")
	}
	legacy := singbox.InboundSettings{MultiUser: true}
	if err := fillInboundSecrets("socks", &legacy); err != nil {
		t.Fatal(err)
	}
	password := legacy.Password
	if err := fillInboundSecrets("socks", &legacy); err != nil {
		t.Fatal(err)
	}
	if password == "" || legacy.Password != password || legacy.MultiUser || !legacy.SingleUser {
		t.Fatal("converted SOCKS credentials were not stable")
	}
}

func TestSOCKSUpdateDistinguishesOmittedAndExplicitNoAuthCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name         string
		settings     string
		wantUsername string
		wantPassword string
	}{
		{"omitted", `{"single_user":true}`, "alice", "existing"},
		{"rename username", `{"username":"bob"}`, "bob", "existing"},
		{"explicit no authentication", `{"username":"","password":""}`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			server := model.Server{Name: "socks", AgentToken: "token", ConfigMode: model.ConfigModeManaged}
			if err := db.Create(&server).Error; err != nil {
				t.Fatal(err)
			}
			inbound := model.Inbound{
				ServerID: server.ID, Tag: "socks", Type: model.InboundSocks, ListenPort: 1080, Enabled: true,
				Settings: model.JSONText(`{"single_user":true,"username":"alice","password":"existing"}`),
			}
			if err := db.Create(&inbound).Error; err != nil {
				t.Fatal(err)
			}
			hub := NewHub(db)
			app := &App{db: db, hub: hub, orch: NewOrchestrator(db, hub)}
			router := gin.New()
			router.PUT("/servers/:id/inbounds/:inboundID", app.updateInbound)
			body := `{"settings":` + tc.settings + `}`
			request := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/servers/%d/inbounds/%d", server.ID, inbound.ID), strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("update status = %d", recorder.Code)
			}
			if err := db.First(&inbound, inbound.ID).Error; err != nil {
				t.Fatal(err)
			}
			var settings singbox.InboundSettings
			if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
				t.Fatal(err)
			}
			if settings.Username != tc.wantUsername || settings.Password != tc.wantPassword || settings.MultiUser || !settings.SingleUser {
				t.Fatal("SOCKS credential update did not preserve the requested authentication mode")
			}
		})
	}
}
