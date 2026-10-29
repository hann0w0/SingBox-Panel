package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func subscriptionSyncForTest(t *testing.T, app *App, userID uint) subscriptionSyncStatus {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/user/subscription-sync?user_id=999", nil)
	c.Set("uid", userID)
	app.handleSubscriptionSync(c)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("sync status: %d %s", rec.Code, rec.Body.String())
	}
	var status subscriptionSyncStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func nextSubscriptionConfig(t *testing.T, conn *agentConn) protocol.Envelope {
	t.Helper()
	select {
	case envelope := <-conn.send:
		if envelope.Type != protocol.CmdApplyConfig {
			t.Fatalf("unexpected command: %s", envelope.Type)
		}
		return envelope
	case <-time.After(3 * time.Second):
		t.Fatal("missing subscription config push")
		return protocol.Envelope{}
	}
}

func replySubscriptionConfig(conn *agentConn, envelope protocol.Envelope, ok bool) {
	conn.mu.Lock()
	result := conn.pending[envelope.ID]
	conn.mu.Unlock()
	result <- protocol.CommandResultEvt{ID: envelope.ID, OK: ok, Error: "private Agent apply error"}
}

func TestSubscriptionResetFailedPushRemainsDurableAndRetriesAfterRestart(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	hub := NewHub(f.db)
	orch := NewOrchestrator(f.db, hub)
	app := &App{db: f.db, orch: orch}
	conn := hub.register(f.server.ID, "", nil)
	defer conn.close()
	if rec := resetSubscriptionForTest(t, app, f.alice.ID); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"apply_state":"pending"`) {
		t.Fatalf("reset must distinguish persistence from delivery: %d %s", rec.Code, rec.Body.String())
	}
	for range asyncPushAttempts {
		replySubscriptionConfig(conn, nextSubscriptionConfig(t, conn), false)
	}
	waitForSubscriptionResetPush(t, orch)
	if got := subscriptionSyncForTest(t, app, f.alice.ID); got != (subscriptionSyncStatus{ConfigFailed, 1, 1}) {
		t.Fatalf("exhausted retries lost failure state: %+v", got)
	}
	var stored model.Server
	if err := f.db.First(&stored, f.server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ProxySyncRevision != 1 || stored.ProxySyncAppliedRevision != 0 || !stored.ProxySyncFailed {
		t.Fatalf("pending revision was not persisted: %+v", stored)
	}
	// Fresh process-local orchestrator and reconciler state must recover the
	// database task without another reset, user edit or Agent reconnection.
	restarted := NewOrchestrator(f.db, hub)
	r := NewReconciler(f.db, func(ids []uint) {
		for _, id := range ids {
			restarted.PushConfigIfManaged(id)
		}
	})
	r.tick()
	envelope := nextSubscriptionConfig(t, conn)
	if err := f.db.First(&f.alice, f.alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(envelope.Payload, []byte(proxyIdentity(&f.alice, f.multi.ID).UUID)) {
		t.Fatal("retry did not use the saved rotated credentials")
	}
	replySubscriptionConfig(conn, envelope, true)
	waitForSubscriptionResetPush(t, restarted)
	if got := subscriptionSyncForTest(t, app, f.alice.ID); got != (subscriptionSyncStatus{ConfigApplied, 0, 0}) {
		t.Fatalf("successful acknowledgement did not settle the revision: %+v", got)
	}
	r.tick()
	if len(conn.send) != 0 {
		t.Fatal("settled revision was scheduled again")
	}
}

func TestSubscriptionResetOlderAckCannotSettleNewerReset(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	hub := NewHub(f.db)
	orch := NewOrchestrator(f.db, hub)
	app := &App{db: f.db, orch: orch}
	conn := hub.register(f.server.ID, "", nil)
	defer conn.close()
	if rec := resetSubscriptionForTest(t, app, f.alice.ID); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	first := nextSubscriptionConfig(t, conn)
	if rec := resetSubscriptionForTest(t, app, f.alice.ID); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	replySubscriptionConfig(conn, first, true)
	second := nextSubscriptionConfig(t, conn)
	if bytes.Equal(first.Payload, second.Payload) {
		t.Fatal("second reset reused the first config")
	}
	if got := subscriptionSyncForTest(t, app, f.alice.ID); got != (subscriptionSyncStatus{ConfigPending, 1, 0}) {
		t.Fatalf("old acknowledgement cleared a newer reset: %+v", got)
	}
	replySubscriptionConfig(conn, second, true)
	waitForSubscriptionResetPush(t, orch)
	var stored model.Server
	if err := f.db.First(&stored, f.server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ProxySyncRevision != 2 || stored.ProxySyncAppliedRevision != 2 || stored.ProxySyncFailed {
		t.Fatalf("latest acknowledgement not recorded: %+v", stored)
	}
}

func TestSubscriptionResetQueueFailureRollsBackSeedsAndTrafficBindings(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	if err := f.db.Callback().Update().Before("gorm:update").Register("test:reject_sync_queue", func(tx *gorm.DB) {
		if tx.Statement.Table == "servers" {
			tx.AddError(errors.New("cannot persist synchronization task"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if rec := resetSubscriptionForTest(t, &App{db: f.db}, f.alice.ID); rec.Code != http.StatusInternalServerError {
		t.Fatalf("queue failure returned %d", rec.Code)
	}
	var stored model.User
	if err := f.db.First(&stored, f.alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.SubToken != f.alice.SubToken || stored.ProxyToken != f.alice.ProxyToken {
		t.Fatal("reset committed credentials without a durable sync task")
	}
	var bindings []model.UserTrafficIdentity
	if err := f.db.Where("user_id = ?", f.alice.ID).Find(&bindings).Error; err != nil {
		t.Fatal(err)
	}
	for _, binding := range bindings {
		if binding.UserKey != proxyTrafficName(&f.alice) {
			t.Fatal("queue failure did not roll back traffic identity rotation")
		}
	}
}

func TestSubscriptionSyncOnlyTracksGrantedManagedMultiUserNodes(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	shared := model.Server{Name: "shared", AgentToken: "shared"}
	raw := model.Server{Name: "raw", AgentToken: "raw", ConfigMode: model.ConfigModeRaw, RawConfig: model.JSONText(`{"inbounds":[]}`)}
	foreign := model.Server{Name: "foreign", AgentToken: "foreign"}
	for _, server := range []*model.Server{&shared, &raw, &foreign} {
		mustCreate(t, f.db, server)
		settings := model.JSONText(`{"multi_user":true}`)
		if server.ID == shared.ID {
			settings = model.JSONText(`{"single_user":true,"uuid":"shared-uuid"}`)
		}
		mustCreate(t, f.db, &model.Inbound{ServerID: server.ID, Tag: "test", Type: "vless", Enabled: true, Settings: settings})
	}
	f.alice.ServerIDs = []uint{f.server.ID, shared.ID, raw.ID}
	if err := f.db.Save(&f.alice).Error; err != nil {
		t.Fatal(err)
	}
	app := &App{db: f.db}
	if rec := resetSubscriptionForTest(t, app, f.alice.ID); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	var servers []model.Server
	if err := f.db.Find(&servers).Error; err != nil {
		t.Fatal(err)
	}
	for _, server := range servers {
		want := uint64(0)
		if server.ID == f.server.ID {
			want = 1
		}
		if server.ProxySyncRevision != want {
			t.Fatalf("unexpected revision on server %d: %d", server.ID, server.ProxySyncRevision)
		}
	}
	f.namedUser.InboundIDs = []uint{f.single.ID}
	if err := f.db.Save(&f.namedUser).Error; err != nil {
		t.Fatal(err)
	}
	if got := subscriptionSyncForTest(t, app, f.namedUser.ID); got.ApplyState != ConfigApplied {
		t.Fatalf("another inbound's pending credentials leaked into shared-only account: %+v", got)
	}
	if got := subscriptionSyncForTest(t, app, f.alice.ID); got != (subscriptionSyncStatus{ConfigPending, 1, 0}) {
		t.Fatalf("wrong pending status: %+v", got)
	}
}

func TestSubscriptionSyncRecordsBuildFailuresAndOfflineAsPending(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	app := &App{db: f.db}
	if rec := resetSubscriptionForTest(t, app, f.alice.ID); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	orch := NewOrchestrator(f.db, NewHub(f.db))
	if err := orch.PushConfig(context.Background(), f.server.ID); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("offline push: %v", err)
	}
	if got := subscriptionSyncForTest(t, app, f.alice.ID); got != (subscriptionSyncStatus{ConfigPending, 1, 0}) {
		t.Fatalf("offline revision was settled or marked failed: %+v", got)
	}
	mustCreate(t, f.db, &model.Outbound{ServerID: f.server.ID, Tag: "broken", Type: "vless", Settings: model.JSONText(`{broken`)})
	if err := orch.PushConfig(context.Background(), f.server.ID); err == nil {
		t.Fatal("invalid configuration was not rejected")
	}
	if got := subscriptionSyncForTest(t, app, f.alice.ID); got != (subscriptionSyncStatus{ConfigFailed, 1, 1}) {
		t.Fatalf("build failure was not recorded: %+v", got)
	}
}

func TestSubscriptionSyncMigrationAddsZeroedRevisionsWithoutChangingNodes(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "existing", AgentToken: "unchanged", ConfigInitialized: true}
	mustCreate(t, db, &server)
	for _, column := range []string{"ProxySyncRevision", "ProxySyncAppliedRevision", "ProxySyncFailed"} {
		if err := db.Migrator().DropColumn(&model.Server{}, column); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := applicationMigrations[len(applicationMigrations)-1].up(db); err != nil {
			t.Fatal(err)
		}
	}
	var stored model.Server
	if err := db.First(&stored, server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ProxySyncRevision != 0 || stored.ProxySyncAppliedRevision != 0 || stored.ProxySyncFailed ||
		stored.AgentToken != server.AgentToken || !stored.ConfigInitialized {
		t.Fatalf("migration changed an existing node: %+v", stored)
	}
}
