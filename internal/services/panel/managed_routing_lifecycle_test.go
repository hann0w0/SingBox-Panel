package panel

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func managedRoutingLifecycleFixture(t *testing.T) (*gorm.DB, model.Server, model.Server, model.Inbound) {
	t.Helper()
	db := testDB(t)
	target := model.Server{Name: "target", AgentToken: "lifecycle-target", ConfigMode: model.ConfigModeManaged, Address: "target.example"}
	source := model.Server{Name: "source", AgentToken: "lifecycle-source", ConfigMode: model.ConfigModeManaged}
	mustCreate(t, db, &target)
	mustCreate(t, db, &source)
	inbound := model.Inbound{ServerID: target.ID, Tag: "proxy", Type: model.InboundSocks, ListenPort: 1080, Enabled: true, Settings: model.JSONText(`{"single_user":true,"username":"user","password":"secret"}`)}
	mustCreate(t, db, &inbound)
	return db, target, source, inbound
}

func TestManagedRoutingInboundMutationRejectsReferences(t *testing.T) {
	for _, reference := range []string{"rule", "final", "ruleset"} {
		for _, operation := range []string{"delete", "disable"} {
			t.Run(reference+"/"+operation, func(t *testing.T) {
				db, target, source, inbound := managedRoutingLifecycleFixture(t)
				tag := managedRoutingNodeTag(inbound.ID)
				switch reference {
				case "rule":
					mustCreate(t, db, &model.RouteRule{ServerID: source.ID, Enabled: false, Outbound: tag})
				case "final":
					if err := db.Model(&source).Update("final_outbound", tag).Error; err != nil {
						t.Fatal(err)
					}
				case "ruleset":
					mustCreate(t, db, &model.RuleSet{Tag: "global", DownloadDetour: tag})
				}
				app := &App{db: db}
				router := gin.New()
				router.DELETE("/servers/:id/inbounds/:inboundID", app.deleteInbound)
				router.PUT("/servers/:id/inbounds/:inboundID", app.updateInbound)
				method, body := http.MethodDelete, ""
				if operation == "disable" {
					method, body = http.MethodPut, `{"enabled":false}`
				}
				res := serveJSONRequest(t, router, method, fmt.Sprintf("/servers/%d/inbounds/%d", target.ID, inbound.ID), body)
				if res.Code != http.StatusConflict {
					t.Fatalf("status = %d: %s", res.Code, res.Body.String())
				}
				var got model.Inbound
				if err := db.First(&got, inbound.ID).Error; err != nil || !got.Enabled {
					t.Fatalf("inbound changed: %+v, %v", got, err)
				}
			})
		}
	}
}

func TestManagedRoutingDeleteServerReferences(t *testing.T) {
	for _, reference := range []string{"self", "other", "global"} {
		t.Run(reference, func(t *testing.T) {
			db, target, source, inbound := managedRoutingLifecycleFixture(t)
			tag := managedRoutingNodeTag(inbound.ID)
			switch reference {
			case "self":
				mustCreate(t, db, &model.RouteRule{ServerID: target.ID, Outbound: tag})
				if err := db.Model(&target).Update("final_outbound", tag).Error; err != nil {
					t.Fatal(err)
				}
			case "other":
				mustCreate(t, db, &model.RouteRule{ServerID: source.ID, Outbound: tag})
			case "global":
				mustCreate(t, db, &model.RuleSet{Tag: "global", DownloadDetour: tag})
			}
			app := &App{db: db}
			router := gin.New()
			router.DELETE("/servers/:id", app.deleteServer)
			res := serveJSONRequest(t, router, http.MethodDelete, fmt.Sprintf("/servers/%d", target.ID), "")
			want := http.StatusConflict
			if reference == "self" {
				want = http.StatusOK
			}
			if res.Code != want {
				t.Fatalf("status = %d, want %d: %s", res.Code, want, res.Body.String())
			}
		})
	}
}

func TestManagedRoutingImportRemovalRollsBack(t *testing.T) {
	db, target, source, inbound := managedRoutingLifecycleFixture(t)
	keep := model.Inbound{ServerID: target.ID, Tag: "keep", Type: model.InboundSocks, ListenPort: 1081, Enabled: true, Settings: model.JSONText(`{"single_user":true}`)}
	mustCreate(t, db, &keep)
	mustCreate(t, db, &model.RouteRule{ServerID: source.ID, Outbound: managedRoutingNodeTag(inbound.ID)})
	err := db.Transaction(func(tx *gorm.DB) error {
		return syncImportedInbounds(tx, target.ID, []singbox.ParsedInbound{{Tag: keep.Tag, Type: "socks", ListenPort: 9999}})
	})
	if !errors.Is(err, errRoutingNodeInUse) {
		t.Fatalf("error = %v", err)
	}
	var got model.Inbound
	if err := db.First(&got, inbound.ID).Error; err != nil {
		t.Fatal(err)
	}
	got = model.Inbound{}
	if err := db.First(&got, keep.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ListenPort != keep.ListenPort {
		t.Fatalf("partial import escaped rollback: %+v", got)
	}
}

func TestManagedRoutingRawImportIsRejectedBeforeAgentCommand(t *testing.T) {
	db, target, source, inbound := managedRoutingLifecycleFixture(t)
	mustCreate(t, db, &model.RouteRule{ServerID: source.ID, Outbound: managedRoutingNodeTag(inbound.ID)})
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	router := gin.New()
	router.PUT("/servers/:id/raw", app.applyRawConfig)
	res := serveJSONRequest(t, router, http.MethodPut, fmt.Sprintf("/servers/%d/raw", target.ID), `{"config":"{\"inbounds\":[],\"outbounds\":[{\"type\":\"direct\",\"tag\":\"direct\"}],\"route\":{\"final\":\"direct\"}}"}`)
	// The disconnected Agent would return 502 if the handler sent a command.
	if res.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	raw := []byte(`{"dns":{"servers":[{"type":"local","tag":"dns"}]},"inbounds":[],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`)
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.applyImportUnlocked(&target, parsed, raw); !errors.Is(err, errManagedInboundRoutingProtected) {
		t.Fatalf("raw import error = %v", err)
	}
	var got model.Server
	if err := db.First(&got, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.EffectiveConfigMode() != model.ConfigModeManaged {
		t.Fatalf("mode changed: %s", got.ConfigMode)
	}
}

func TestManagedRoutingInboundMetadataDoesNotApplyConfig(t *testing.T) {
	db, target, _, inbound := managedRoutingLifecycleFixture(t)
	app := &App{db: db} // Applying a config with no orchestrator would panic.
	router := gin.New()
	router.PUT("/servers/:id/inbounds/:inboundID", app.updateInbound)
	res := serveJSONRequest(t, router, http.MethodPut, fmt.Sprintf("/servers/%d/inbounds/%d", target.ID, inbound.ID), `{"remark":"metadata only"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	before := model.Inbound{Settings: model.JSONText(`{"a":1,"b":2}`)}
	after := model.Inbound{Settings: model.JSONText(`{"b":2,"a":1}`), Remark: "new"}
	if managedInboundConfigChanged(before, after) || managedInboundConfigChanged(model.Inbound{}, model.Inbound{}) {
		t.Fatal("metadata or JSON formatting counts as config change")
	}
}

func TestManagedRoutingAddressChangeCallbacks(t *testing.T) {
	for _, explicitAddress := range []string{"", "fixed.example"} {
		t.Run(explicitAddress, func(t *testing.T) {
			db := testDB(t)
			server := model.Server{Name: "target", AgentToken: "callback-target", Address: explicitAddress, PublicIP: "203.0.113.1"}
			mustCreate(t, db, &server)
			hub := NewHub(db)
			conn := &agentConn{serverID: server.ID}
			hub.conns[server.ID] = conn
			changes := 0
			hub.AfterRoutingAddressChange = func(uint) { changes++ }
			hub.onRegister(conn, protocol.RegisterEvt{PublicIP: "203.0.113.2"})
			hub.onRegister(conn, protocol.RegisterEvt{PublicIP: "203.0.113.2"})
			hub.persistServerStatus(conn, protocol.StatusData{PublicIP: "203.0.113.3"})
			hub.persistServerStatus(conn, protocol.StatusData{PublicIP: "203.0.113.3"})
			want := 0
			if explicitAddress == "" {
				want = 2
			}
			if changes != want {
				t.Fatalf("address changes = %d, want %d", changes, want)
			}
		})
	}
}

func TestManagedRoutingManagedImportPreservesReferencedInbound(t *testing.T) {
	db, target, source, inbound := managedRoutingLifecycleFixture(t)
	mustCreate(t, db, &model.RouteRule{ServerID: source.ID, Outbound: managedRoutingNodeTag(inbound.ID)})
	raw, err := singbox.BuildServerConfig(singbox.ServerConfigInput{
		Inbounds: []singbox.InboundInput{{Tag: inbound.Tag, Type: "socks", ListenPort: 2080, Settings: singbox.InboundSettings{SingleUser: true, Username: "new-user", Password: "new-secret"}}},
		Final:    "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{db: db}
	if err := app.applyImportUnlocked(&target, parsed, raw); err != nil {
		t.Fatal(err)
	}
	var got model.Inbound
	if err := db.First(&got, inbound.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ID != inbound.ID || got.ListenPort != 2080 {
		t.Fatalf("managed import lost reference identity or config: %+v", got)
	}
	if err := db.First(&target, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if target.EffectiveConfigMode() != model.ConfigModeManaged {
		t.Fatalf("mode = %s", target.ConfigMode)
	}
}

func TestCollectServerManagedRoutingTagsIncludesSharedDetours(t *testing.T) {
	db, target, source, inbound := managedRoutingLifecycleFixture(t)
	finalTag := managedRoutingNodeTag(inbound.ID)
	mustCreate(t, db, &model.RuleSet{Tag: "shared", DownloadDetour: managedRoutingNodeTag(901)})
	mustCreate(t, db, &model.RouteRule{ServerID: source.ID, Outbound: managedRoutingNodeTag(902), Match: model.JSONText(`{"rule_set":["shared"]}`), Enabled: false})
	mustCreate(t, db, &model.RouteRule{ServerID: source.ID, Outbound: finalTag})
	mustCreate(t, db, &model.RouteRule{ServerID: target.ID, Outbound: managedRoutingNodeTag(903)})
	if err := db.Model(&source).Update("final_outbound", finalTag).Error; err != nil {
		t.Fatal(err)
	}
	tags, err := collectServerManagedRoutingTags(db, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{finalTag, managedRoutingNodeTag(901), managedRoutingNodeTag(902)}
	if !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
}

func TestManagedRoutingSourceLifecycleRefreshesTargets(t *testing.T) {
	for _, operation := range []string{"delete", "import-remove", "import-add"} {
		t.Run(operation, func(t *testing.T) {
			db, target, source, inbound := managedRoutingLifecycleFixture(t)
			inbound.Type = model.InboundVMess
			inbound.Settings = model.JSONText(`{"multi_user":true}`)
			if err := db.Save(&inbound).Error; err != nil {
				t.Fatal(err)
			}
			source.FinalOutbound = managedRoutingNodeTag(inbound.ID)
			if err := db.Save(&source).Error; err != nil {
				t.Fatal(err)
			}
			orch := NewOrchestrator(db, NewHub(db))
			var raw []byte
			var err error
			if operation == "import-add" {
				raw, err = orch.BuildServerConfig(&source)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&source).Update("final_outbound", "direct").Error; err != nil {
					t.Fatal(err)
				}
			} else {
				before, err := orch.BuildServerConfig(&target)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(before), managedRoutingProxyUserName) {
					t.Fatalf("routing identity missing before %s", operation)
				}
				raw, err = singbox.BuildServerConfig(singbox.ServerConfigInput{Final: "direct"})
				if err != nil {
					t.Fatal(err)
				}
			}
			// Model an already active target push: a refresh must enqueue another run.
			// This observes scheduling without connecting an Agent or racing a fast
			// offline push that could finish before the assertion.
			orch.asyncRunning[target.ID] = true
			app := &App{db: db, orch: orch}
			if operation == "delete" {
				router := gin.New()
				router.DELETE("/servers/:id", app.deleteServer)
				res := serveJSONRequest(t, router, http.MethodDelete, fmt.Sprintf("/servers/%d", source.ID), "")
				if res.Code != http.StatusOK {
					t.Fatalf("delete status = %d: %s", res.Code, res.Body.String())
				}
			} else {
				parsed, err := singbox.ParseServerConfig(raw)
				if err != nil {
					t.Fatal(err)
				}
				if err := app.applyImportUnlocked(&source, parsed, raw); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(3 * time.Second)
			scheduled := false
			for time.Now().Before(deadline) {
				orch.asyncMu.Lock()
				scheduled = orch.asyncPending[target.ID]
				orch.asyncMu.Unlock()
				if scheduled {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if !scheduled {
				t.Fatalf("%s did not refresh the routing target", operation)
			}
			after, err := orch.BuildServerConfig(&target)
			if err != nil {
				t.Fatal(err)
			}
			hasCredential := strings.Contains(string(after), managedRoutingProxyUserName)
			if hasCredential != (operation == "import-add") {
				t.Fatalf("target machine credential after %s = %v", operation, hasCredential)
			}
		})
	}
}

func TestManagedRoutingFailedImportDoesNotRefreshTargets(t *testing.T) {
	db, target, source, inbound := managedRoutingLifecycleFixture(t)
	if err := db.Model(&source).Update("final_outbound", managedRoutingNodeTag(inbound.ID)).Error; err != nil {
		t.Fatal(err)
	}
	protected := model.Inbound{ServerID: source.ID, Tag: "protected", Type: model.InboundSocks, ListenPort: 2080, Enabled: true, Settings: model.JSONText(`{"single_user":true}`)}
	mustCreate(t, db, &protected)
	mustCreate(t, db, &model.RouteRule{ServerID: target.ID, Outbound: managedRoutingNodeTag(protected.ID)})
	orch := NewOrchestrator(db, NewHub(db))
	orch.asyncRunning[target.ID] = true
	app := &App{db: db, orch: orch}
	raw := []byte(`{"dns":{"servers":[{"type":"local","tag":"dns"}]},"inbounds":[],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`)
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.applyImportUnlocked(&source, parsed, raw); !errors.Is(err, errManagedInboundRoutingProtected) {
		t.Fatalf("error = %v", err)
	}
	orch.asyncMu.Lock()
	pending := orch.asyncPending[target.ID]
	orch.asyncMu.Unlock()
	if pending {
		t.Fatal("failed import refreshed the old target")
	}
}
