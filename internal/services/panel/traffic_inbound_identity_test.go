package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func inboundTrafficRouter(f userTrafficFixture) (*App, *gin.Engine) {
	hub := NewHub(f.db)
	app := &App{db: f.db, hub: hub, orch: NewOrchestrator(f.db, hub)}
	router := gin.New()
	router.POST("/servers/:id/inbounds", app.createInbound)
	router.PUT("/servers/:id/inbounds/:inboundID", app.updateInbound)
	router.DELETE("/servers/:id/inbounds/:inboundID", app.deleteInbound)
	router.POST("/servers/:id/import", app.importRemoteConfig)
	router.PUT("/servers/:id/raw", app.applyRawConfig)
	return app, router
}

func inboundTrafficRequest(t *testing.T, router *gin.Engine, method, path string, payload any, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != wantStatus {
		t.Fatalf("%s %s: %d, want %d: %s", method, path, response.Code, wantStatus, response.Body.String())
	}
	return response
}

func trafficInboundUpload(t *testing.T, db *gorm.DB, serverID, inboundID uint) uint64 {
	t.Helper()
	var total uint64
	if err := db.Model(&model.TrafficRecord{}).Where("server_id = ? AND inbound_id = ?", serverID, inboundID).
		Select("COALESCE(SUM(upload), 0)").Scan(&total).Error; err != nil {
		t.Fatal(err)
	}
	return total
}

func deliverInboundTrafficReport(t *testing.T, db *gorm.DB, serverID uint, snapshot *protocol.TrafficSnapshot) {
	t.Helper()
	// Each delivery uses a fresh hub, including duplicate delivery after restart.
	hub := NewHub(db)
	connection := hub.register(serverID, "127.0.0.1", nil)
	defer connection.close()
	envelope, err := protocol.NewEnvelope(protocol.EvtTraffic, "", protocol.TrafficEvt{Traffic: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	hub.handleEvent(connection, envelope)
	select {
	case response := <-connection.send:
		var ack protocol.TrafficAck
		if response.Type != protocol.EvtTrafficAck || response.Decode(&ack) != nil || ack.Session != snapshot.Report.Session || ack.Sequence != snapshot.Report.Sequence {
			t.Fatalf("unexpected traffic acknowledgment: %+v", response)
		}
	default:
		t.Fatal("persisted traffic report was not acknowledged")
	}
}

func TestInboundTrafficRenamePreservesOfflineBucketsAcrossRestart(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	app, router := inboundTrafficRouter(f)
	if _, err := app.orch.BuildServerConfig(&f.server); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/servers/%d/inbounds/%d", f.server.ID, f.multi.ID)
	for _, tag := range []string{"intermediate", "renamed"} {
		response := inboundTrafficRequest(t, router, http.MethodPut, path, gin.H{"tag": tag}, http.StatusOK)
		if !strings.Contains(response.Body.String(), `"apply_state":"pending"`) {
			t.Fatalf("offline rename was not pending: %s", response.Body.String())
		}
	}
	var current model.Inbound
	if err := f.db.First(&current, f.multi.ID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	snapshot := timestampedFixtureReport(f, now)
	if !time.Unix(snapshot.Report.Buckets[1].Time, 0).After(current.UpdatedAt) {
		t.Fatal("fixture must include old-tag traffic sampled after the offline rename")
	}
	for i := range snapshot.Report.Buckets {
		snapshot.Report.Buckets[i].Users[0].User = proxyTrafficName(&f.alice)
	}
	for _, tag := range []string{"intermediate", "renamed"} {
		snapshot.Report.Buckets = append(snapshot.Report.Buckets, protocol.TrafficTimeBucket{
			Time: now.Unix(), Upload: 25,
			Ports: []protocol.PortTrafficSnapshot{{Inbound: tag, Upload: 25}},
			Users: []protocol.UserTrafficSnapshot{{Inbound: tag, User: proxyTrafficName(&f.alice), Upload: 25}},
		})
	}
	for range 2 {
		deliverInboundTrafficReport(t, f.db, f.server.ID, snapshot)
	}
	if got := trafficInboundUpload(t, f.db, f.server.ID, f.multi.ID); got != 1250 {
		t.Fatalf("renamed inbound lost or duplicated queued traffic: %d", got)
	}
	if got := userRows(t, f.db)[f.alice.ID]; got != [2]uint64{1250, 0} {
		t.Fatalf("renamed inbound lost user attribution: %v", got)
	}
	var server model.Server
	if err := f.db.First(&server, f.server.ID).Error; err != nil || server.TrafficUpload != 1250 {
		t.Fatalf("server accounting changed: %+v, %v", server, err)
	}
	var oldBucket model.TrafficRecord
	if err := f.db.Where("server_id = ? AND inbound_id = ? AND bucket = ?", f.server.ID, f.multi.ID, now.Add(-time.Minute)).First(&oldBucket).Error; err != nil || oldBucket.Upload != 1000 {
		t.Fatalf("original collection time lost: %+v, %v", oldBucket, err)
	}
	// An inbound may return to its own original tag without losing later aliases.
	inboundTrafficRequest(t, router, http.MethodPut, path, gin.H{"tag": f.multi.Tag}, http.StatusOK)
	var count int64
	if err := f.db.Model(&model.InboundTrafficIdentity{}).Where("inbound_id = ?", f.multi.ID).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("rename history changed: %d, %v", count, err)
	}
}

func TestInboundTrafficAliasesSupportLegacyReportsAndCurrentUserMode(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	_, router := inboundTrafficRouter(f)
	path := fmt.Sprintf("/servers/%d/inbounds/%d", f.server.ID, f.multi.ID)
	inboundTrafficRequest(t, router, http.MethodPut, path, gin.H{"tag": "renamed"}, http.StatusOK)
	w := newTrafficWriter(f.db)
	f.report(t, w, []protocol.UserTrafficSnapshot{{Inbound: f.multi.Tag, User: f.alice.Email, Upload: 15}})
	if got := userRows(t, f.db)[f.alice.ID][0]; got != 15 {
		t.Fatalf("legacy user alias lost: %d", got)
	}
	if got := trafficInboundUpload(t, f.db, f.server.ID, f.multi.ID); got != 10 {
		t.Fatalf("legacy port alias lost: %d", got)
	}
	inboundTrafficRequest(t, router, http.MethodPut, path, gin.H{
		"settings": gin.H{"single_user": true, "uuid": "b831381d-6324-4d53-ad4f-8cda48b30811"},
	}, http.StatusOK)
	f.report(t, newTrafficWriter(f.db), []protocol.UserTrafficSnapshot{{Inbound: f.multi.Tag, User: f.alice.Email, Upload: 50}})
	if got := userRows(t, f.db)[f.alice.ID][0]; got != 15 {
		t.Fatalf("alias revived old multi-user configuration: %d", got)
	}
}

func TestInboundTrafficTombstonesRejectReuseAndSkipDeletedInbound(t *testing.T) {
	for _, removal := range []string{"api", "import"} {
		t.Run(removal, func(t *testing.T) {
			f := newUserTrafficFixture(t, model.ConfigModeManaged)
			_, router := inboundTrafficRouter(f)
			base := fmt.Sprintf("/servers/%d", f.server.ID)
			path := fmt.Sprintf("%s/inbounds/%d", base, f.multi.ID)
			inboundTrafficRequest(t, router, http.MethodPut, path, gin.H{"tag": "renamed"}, http.StatusOK)
			// Another live inbound cannot claim an alias, even before deletion.
			inboundTrafficRequest(t, router, http.MethodPut, fmt.Sprintf("%s/inbounds/%d", base, f.single.ID), gin.H{"tag": f.multi.Tag}, http.StatusBadRequest)
			if removal == "api" {
				inboundTrafficRequest(t, router, http.MethodDelete, path, nil, http.StatusOK)
			} else {
				empty, err := singbox.BuildServerConfig(singbox.ServerConfigInput{})
				if err != nil {
					t.Fatal(err)
				}
				inboundTrafficRequest(t, router, http.MethodPost, base+"/import", gin.H{"config": string(empty)}, http.StatusOK)
			}
			for _, tag := range []string{f.multi.Tag, "renamed"} {
				response := inboundTrafficRequest(t, router, http.MethodPost, base+"/inbounds", gin.H{"type": "vless", "tag": tag, "listen_port": 443}, http.StatusBadRequest)
				if !strings.Contains(response.Body.String(), errInboundTrafficTagReserved.Error()) {
					t.Fatalf("historical reuse lacks an actionable error: %s", response.Body.String())
				}
			}
			inboundTrafficRequest(t, router, http.MethodPost, base+"/inbounds", gin.H{"type": "vless", "tag": "fresh", "listen_port": 443, "settings": gin.H{"multi_user": true}}, http.StatusOK)
			var fresh model.Inbound
			if err := f.db.First(&fresh, "server_id = ? AND tag = ?", f.server.ID, "fresh").Error; err != nil {
				t.Fatal(err)
			}
			snapshot := timestampedFixtureReport(f, time.Now().UTC().Truncate(time.Minute))
			deliverInboundTrafficReport(t, f.db, f.server.ID, snapshot)
			if got := trafficInboundUpload(t, f.db, f.server.ID, fresh.ID); got != 0 {
				t.Fatalf("new inbound inherited old traffic: %d", got)
			}
			if got := trafficInboundUpload(t, f.db, f.server.ID, f.multi.ID); got != 0 || len(userRows(t, f.db)) != 0 {
				t.Fatal("deleted inbound attribution was unexpectedly revived")
			}
			if got := trafficInboundUpload(t, f.db, f.server.ID, 0); got != 1200 {
				t.Fatalf("deleting an inbound lost node traffic: %d", got)
			}
		})
	}
}

func TestInboundTrafficImportAndRawEditRejectHistoricalReuseAtomically(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	_, router := inboundTrafficRouter(f)
	base := fmt.Sprintf("/servers/%d", f.server.ID)
	inboundTrafficRequest(t, router, http.MethodPut, fmt.Sprintf("%s/inbounds/%d", base, f.multi.ID), gin.H{"tag": "renamed"}, http.StatusOK)
	var original model.Server
	if err := f.db.First(&original, f.server.ID).Error; err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"inbounds":[{"type":"socks","tag":"new-before-conflict","listen_port":1080},{"type":"socks","tag":%q,"listen_port":1081}],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`, f.multi.Tag)
	for _, endpoint := range []struct{ method, path string }{{http.MethodPost, "/import"}, {http.MethodPut, "/raw"}} {
		response := inboundTrafficRequest(t, router, endpoint.method, base+endpoint.path, gin.H{"config": raw}, http.StatusBadRequest)
		if !strings.Contains(response.Body.String(), errInboundTrafficTagReserved.Error()) {
			t.Fatalf("historical reuse error missing: %s", response.Body.String())
		}
		var inbounds []model.Inbound
		if err := f.db.Where("server_id = ?", f.server.ID).Order("id").Find(&inbounds).Error; err != nil || len(inbounds) != 2 || inbounds[0].ID != f.multi.ID || inbounds[0].Tag != "renamed" || inbounds[1].ID != f.single.ID {
			t.Fatalf("rejected import changed inbounds: %+v, %v", inbounds, err)
		}
		var count int64
		if err := f.db.Model(&model.InboundTrafficIdentity{}).Where("tag = ?", "new-before-conflict").Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("rejected import leaked a tag reservation: %d, %v", count, err)
		}
		var server model.Server
		if err := f.db.First(&server, f.server.ID).Error; err != nil || server.ConfigMode != original.ConfigMode || !bytes.Equal(server.RawConfig, original.RawConfig) {
			t.Fatalf("rejected raw config changed desired state: %+v, %v", server, err)
		}
	}
}

func TestInboundTrafficBindingsRollbackWithInboundWrites(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	_, router := inboundTrafficRouter(f)
	base := fmt.Sprintf("/servers/%d/inbounds", f.server.ID)
	if err := f.db.Exec(`CREATE TRIGGER reject_binding BEFORE INSERT ON inbound_traffic_identities WHEN NEW.tag = 'rejected-create' BEGIN SELECT RAISE(ABORT, 'simulated binding failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	inboundTrafficRequest(t, router, http.MethodPost, base, gin.H{"type": "vless", "tag": "rejected-create", "listen_port": 1443}, http.StatusInternalServerError)
	var count int64
	if err := f.db.Model(&model.Inbound{}).Where("tag = ?", "rejected-create").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed binding left a new inbound: %d, %v", count, err)
	}
	raw := `{"inbounds":[{"type":"socks","tag":"new-before-failure","listen_port":1080},{"type":"socks","tag":"rejected-create","listen_port":1081}]}`
	inboundTrafficRequest(t, router, http.MethodPost, fmt.Sprintf("/servers/%d/import", f.server.ID), gin.H{"config": raw}, http.StatusInternalServerError)
	for _, row := range []any{&model.Inbound{}, &model.InboundTrafficIdentity{}} {
		if err := f.db.Model(row).Where("tag IN ?", []string{"new-before-failure", "rejected-create"}).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("failed import left a new inbound or tag binding: %d, %v", count, err)
		}
	}
	if err := f.db.Exec(`CREATE TRIGGER reject_rename BEFORE UPDATE ON inbounds WHEN NEW.tag = 'rejected-rename' BEGIN SELECT RAISE(ABORT, 'simulated inbound failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	rule := model.RouteRule{ServerID: f.server.ID, Enabled: true, Match: model.JSONText(`{"inbound":["vless-multi"]}`), Outbound: "direct"}
	mustCreate(t, f.db, &rule)
	inboundTrafficRequest(t, router, http.MethodPut, fmt.Sprintf("%s/%d", base, f.multi.ID), gin.H{"tag": "rejected-rename"}, http.StatusInternalServerError)
	if err := f.db.Model(&model.InboundTrafficIdentity{}).Where("tag = ?", "rejected-rename").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed rename reserved a tag: %d, %v", count, err)
	}
	if err := f.db.First(&rule, rule.ID).Error; err != nil || !strings.Contains(string(rule.Match), f.multi.Tag) {
		t.Fatalf("failed rename changed route references: %+v, %v", rule, err)
	}
}

func TestInboundTrafficUnmodeledRawTagsCannotReuseHistory(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	_, router := inboundTrafficRouter(f)
	base := fmt.Sprintf("/servers/%d", f.server.ID)
	inboundTrafficRequest(t, router, http.MethodPut, fmt.Sprintf("%s/inbounds/%d", base, f.multi.ID), gin.H{"tag": "renamed"}, http.StatusOK)
	raw := fmt.Sprintf(`{"inbounds":[{"type":"http","tag":%q,"listen_port":1080}]}`, f.multi.Tag)
	parsed, err := singbox.ParseServerConfig([]byte(raw))
	if err != nil || len(parsed.Inbounds) != 0 {
		t.Fatalf("fixture must use an unmodeled inbound: %+v, %v", parsed, err)
	}
	for _, endpoint := range []struct{ method, path string }{{http.MethodPost, "/import"}, {http.MethodPut, "/raw"}} {
		response := inboundTrafficRequest(t, router, endpoint.method, base+endpoint.path, gin.H{"config": raw}, http.StatusBadRequest)
		if !strings.Contains(response.Body.String(), errInboundTrafficTagReserved.Error()) {
			t.Fatalf("unmodeled raw inbound bypassed tag reservation: %s", response.Body.String())
		}
	}
}

func TestInboundTrafficIdentityMigrationIsReplaySafeAndServerScoped(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	var migration schemaMigration
	for _, candidate := range applicationMigrations {
		if candidate.version == 28 {
			migration = candidate
			break
		}
	}
	if migration.up == nil || !migrationReplaySafe[migration.version] {
		t.Fatal("inbound identity migration is missing from the schema ledger")
	}
	otherServer := model.Server{Name: "other", AgentToken: "other-agent"}
	mustCreate(t, f.db, &otherServer)
	otherInbound := model.Inbound{ServerID: otherServer.ID, Tag: f.multi.Tag, Type: "vless", ListenPort: 443}
	mustCreate(t, f.db, &otherInbound)
	for range 2 {
		if err := migration.up(f.db); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := f.db.Model(&model.InboundTrafficIdentity{}).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("migration did not seed every current tag exactly once: %d, %v", count, err)
	}
	if err := f.db.Delete(&f.multi).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateInboundTrafficIdentities(f.db); err != nil {
		t.Fatal(err)
	}
	if err := checkInboundTrafficTag(f.db, f.server.ID, 0, f.multi.Tag); !errors.Is(err, errInboundTrafficTagReserved) {
		t.Fatalf("migration removed a deleted inbound tombstone: %v", err)
	}
	if err := checkInboundTrafficTag(f.db, otherServer.ID, otherInbound.ID, f.multi.Tag); err != nil {
		t.Fatalf("tag reservation leaked across servers: %v", err)
	}
	// A current row inserted outside the API cannot override an older tombstone.
	reused := model.Inbound{ServerID: f.server.ID, Tag: f.multi.Tag, Type: "vless", ListenPort: 443}
	mustCreate(t, f.db, &reused)
	if err := migrateInboundTrafficIdentities(f.db); !errors.Is(err, errInboundTrafficTagReserved) {
		t.Fatalf("migration rebound a historical tag: %v", err)
	}
	resolved, err := loadTrafficInbounds(f.db, f.server.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, inbound := range resolved {
		if inbound.Tag == f.multi.Tag {
			t.Fatal("deleted tag resolved to a different current inbound")
		}
	}
}

func TestInboundTrafficIdentityDistinguishesCaseOnlyTags(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	_, router := inboundTrafficRouter(f)
	path := fmt.Sprintf("/servers/%d/inbounds/%d", f.server.ID, f.multi.ID)
	inboundTrafficRequest(t, router, http.MethodPut, path, gin.H{"tag": "foo"}, http.StatusOK)
	inboundTrafficRequest(t, router, http.MethodPut, path, gin.H{"tag": "FOO"}, http.StatusOK)
	inboundTrafficRequest(t, router, http.MethodPut, path, gin.H{"tag": "foo"}, http.StatusOK)
	for _, tag := range []string{f.multi.Tag, "foo", "FOO"} {
		if err := checkInboundTrafficTag(f.db, f.server.ID, f.multi.ID, tag); err != nil {
			t.Fatalf("case-only tag %q lost its owner: %v", tag, err)
		}
	}
	var bindings []model.InboundTrafficIdentity
	if err := f.db.Where("inbound_id = ?", f.multi.ID).Order("id").Find(&bindings).Error; err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 3 || bindings[1].Tag != "foo" || bindings[2].Tag != "FOO" || bindings[1].TagKey == bindings[2].TagKey {
		t.Fatalf("case-sensitive rename history = %+v", bindings)
	}
}

func TestInboundTrafficImportedCurrentTagPreservesIdentity(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	app, router := inboundTrafficRouter(f)
	base := fmt.Sprintf("/servers/%d", f.server.ID)
	inboundTrafficRequest(t, router, http.MethodPut, fmt.Sprintf("%s/inbounds/%d", base, f.multi.ID), gin.H{"tag": "renamed"}, http.StatusOK)
	raw := []byte(`{"inbounds":[{"type":"vless","tag":"renamed","listen_port":443,"users":[{"uuid":"b831381d-6324-4d53-ad4f-8cda48b30811"}]}],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`)
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.applyImport(&f.server, parsed, raw); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{f.multi.Tag, "renamed"} {
		if err := checkInboundTrafficTag(f.db, f.server.ID, f.multi.ID, tag); err != nil {
			t.Fatalf("current-tag import changed original ownership of %s: %v", tag, err)
		}
	}
	var inbound model.Inbound
	if err := f.db.First(&inbound, f.multi.ID).Error; err != nil || inbound.Tag != "renamed" {
		t.Fatalf("current-tag import recreated the inbound: %+v, %v", inbound, err)
	}
}
