package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestMultipleConsumerServersGetDistinctRoutingIdentities(t *testing.T) {
	f := newNodeImportFixture(t)
	consumer2 := model.Server{Name: "新加坡", Address: "sg.example.com", AgentToken: "sg-tok", ConfigMode: model.ConfigModeManaged}
	if err := f.db.Create(&consumer2).Error; err != nil {
		t.Fatal(err)
	}
	for _, cid := range []uint{f.target.ID, consumer2.ID} {
		if err := f.db.Create(&model.Outbound{
			ServerID: cid, Tag: "via-hk", Type: "vless",
			SourceKind: model.OutboundSourceInbound, SourceID: f.sourceIB.ID,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	orch := &Orchestrator{db: f.db, app: f.app}
	raw, err := orch.BuildServerConfig(&f.source)
	if err != nil {
		t.Fatal(err)
	}
	name1 := routingOutboundUsername(f.target.ID)
	name2 := routingOutboundUsername(consumer2.ID)
	if name1 == name2 || !strings.Contains(string(raw), name1) || !strings.Contains(string(raw), name2) {
		t.Fatalf("consumer routing identities missing or collided (%q vs %q): %s", name1, name2, raw)
	}
	if _, _, err := validateUsername(name1); err == nil {
		t.Fatal("reserved consumer route identity was accepted by validateUsername")
	}
	if !reservedTrafficUsername(name1) {
		t.Fatal("reservedTrafficUsername rejected consumer route identity")
	}
}

type fakeDialect struct {
	gorm.Dialector
	name string
}

func (d fakeDialect) Name() string { return d.name }

func TestJSONTextGormDBDataTypeUsesLongtextOnMySQL(t *testing.T) {
	mysqlDB := &gorm.DB{Config: &gorm.Config{Dialector: fakeDialect{name: "mysql"}}}
	sqliteDB := &gorm.DB{Config: &gorm.Config{Dialector: fakeDialect{name: "sqlite"}}}
	if got := (model.JSONText{}).GormDBDataType(mysqlDB, &schema.Field{}); got != "LONGTEXT" {
		t.Fatalf("mysql GormDBDataType = %q; want LONGTEXT", got)
	}
	if got := (model.JSONText{}).GormDBDataType(sqliteDB, &schema.Field{}); got != "TEXT" {
		t.Fatalf("sqlite GormDBDataType = %q; want TEXT", got)
	}
}

func TestClashTrojanRealityAndSnellV4Export(t *testing.T) {
	trojan := clashProxy(node{
		name: "tj-reality", server: "tr.example.com", port: 443, typ: "trojan",
		settings: singbox.InboundSettings{
			TLS: singbox.TLSSettings{
				Enabled:    true,
				ServerName: "www.apple.com",
				Reality:    singbox.RealitySettings{Enabled: true, PublicKey: "pub-key", ShortID: []string{"01ab"}},
			},
		},
		user: singbox.ProxyUser{Password: "pw"},
	}, map[string]int{})
	ro, _ := trojan["reality-opts"].(map[string]any)
	if ro == nil || ro["public-key"] != "pub-key" || ro["short-id"] != "01ab" {
		t.Fatalf("trojan reality-opts missing: %+v", trojan)
	}

	snellNode := node{
		name: "snell-v4", server: "sn.example.com", port: 8443, typ: "snell",
		settings: singbox.InboundSettings{
			SnellVersion:  4,
			SnellPSK:      "psk-secret",
			SnellObfsMode: "http",
			SnellObfsHost: "cdn.example.com",
		},
	}
	clashSnell := clashProxy(snellNode, map[string]int{})
	opts, _ := clashSnell["obfs-opts"].(map[string]any)
	if opts == nil || opts["mode"] != "http" || opts["host"] != "cdn.example.com" {
		t.Fatalf("clash snell v4 obfs-opts missing: %+v", clashSnell)
	}
	surgeSnell := surgeProxy(snellNode, "snell-v4")
	if !strings.Contains(surgeSnell, "obfs=http") || !strings.Contains(surgeSnell, "obfs-host=cdn.example.com") {
		t.Fatalf("surge snell v4 obfs-host missing: %s", surgeSnell)
	}
}

func TestTestOutboundResolvesReferencedSourceLiveAddress(t *testing.T) {
	f := newNodeImportFixture(t)
	hub := NewHub(f.db)
	f.app.hub = hub
	f.app.orch.hub = hub
	ob := model.Outbound{
		ServerID: f.target.ID, Tag: "live-src", Type: "vless",
		SourceKind: model.OutboundSourceInbound, SourceID: f.sourceIB.ID,
		Settings: model.JSONText(`{"server":"stale.example.com","server_port":8443}`),
	}
	if err := f.db.Create(&ob).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.Server{}).Where("id = ?", f.source.ID).Update("address", "live.example.com").Error; err != nil {
		t.Fatal(err)
	}

	conn := hub.register(f.target.ID, "", nil)
	defer conn.close()
	done := make(chan protocol.TestOutboundCmd, 1)
	go func() {
		select {
		case env := <-conn.send:
			var cmd protocol.TestOutboundCmd
			_ = json.Unmarshal(env.Payload, &cmd)
			done <- cmd
			payload, _ := json.Marshal(protocol.TestOutboundData{OK: true, LatencyMS: 12})
			conn.mu.Lock()
			ch := conn.pending[env.ID]
			conn.mu.Unlock()
			ch <- protocol.CommandResultEvt{ID: env.ID, OK: true, Data: payload}
		case <-time.After(3 * time.Second):
		}
	}()

	router := gin.New()
	router.POST("/servers/:id/outbounds/:outboundID/test", f.app.testOutbound)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/servers/%d/outbounds/%d/test", f.target.ID, ob.ID), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "live.example.com:443") {
		t.Fatalf("testOutbound = %d %s", rec.Code, rec.Body.String())
	}
	select {
	case cmd := <-done:
		if cmd.Host != "live.example.com" || cmd.Port != 443 {
			t.Fatalf("CmdTestOutbound probed %+v; want live.example.com:443", cmd)
		}
	case <-context.Background().Done():
	}
}
