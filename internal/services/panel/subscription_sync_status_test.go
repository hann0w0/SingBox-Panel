package panel

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestSubscriptionSyncStatusMigrationPreservesHistory(t *testing.T) {
	cfg := config.Default()
	cfg.Database.DSN = filepath.Join(t.TempDir(), "status-upgrade.db")
	cfg.Admin.Email, cfg.Admin.Password = "admin", "test-password"
	db, err := InitDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	reopen := func() {
		t.Helper()
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = InitDB(cfg)
		if err != nil {
			t.Fatal(err)
		}
	}
	lastSync := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	lastSuccess := lastSync.Add(-time.Hour)
	source := model.CustomNodeSubscription{
		Name: "legacy source", URL: "https://example.com/subscription", NodeCount: 29,
		LastError: "legacy diagnostic retained without classification", LastSyncAt: &lastSync, LastSuccessAt: &lastSuccess,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	node := model.CustomNode{Name: "legacy node", SubscriptionID: &source.ID, SubscriptionKey: "legacy-key", UserIDs: []uint{7}}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&source, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&node, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.CustomNodeSubscription{}, "LastSyncStatus"); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 31).Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn(&model.CustomNodeSubscription{}, "LastSyncStatus") {
		t.Fatal("v30 fixture still has the status column")
	}
	if version, err := schemaVersionInDB(db); err != nil || version != 30 {
		t.Fatalf("legacy schema = %d, %v; want v30", version, err)
	}
	reopen()
	var migrated model.CustomNodeSubscription
	if err := db.First(&migrated, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(migrated, source) {
		t.Fatalf("migration classified or changed legacy history: got %+v want %+v", migrated, source)
	}
	var preservedNode model.CustomNode
	if err := db.First(&preservedNode, node.ID).Error; err != nil || !reflect.DeepEqual(preservedNode, node) {
		t.Fatalf("migration changed saved node/grants: %+v, %v", preservedNode, err)
	}
	if version, err := schemaVersionInDB(db); err != nil || version != 31 {
		t.Fatalf("upgraded schema = %d, %v; want v31", version, err)
	}
	if err := db.Model(&migrated).Update("last_sync_status", model.SubscriptionSyncPartial).Error; err != nil {
		t.Fatal(err)
	}
	for _, migration := range applicationMigrations {
		if migration.version == 31 {
			if err := migration.up(db); err != nil {
				t.Fatal(err)
			}
		}
	}
	reopen()
	var restarted model.CustomNodeSubscription
	if err := db.First(&restarted, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restarted.LastSyncStatus != model.SubscriptionSyncPartial || restarted.LastError != source.LastError || !reflect.DeepEqual(restarted.LastSuccessAt, source.LastSuccessAt) {
		t.Fatalf("migration replay/restart overwrote persisted status/history: %+v", restarted)
	}
}

func TestSubscriptionSyncStatusPersistsTransitions(t *testing.T) {
	valid := subscriptionReconcileProxy("source-valid", "valid.example.com")
	previous := subscriptionReconcileProxy("source-failed", "failed.example.com")
	f := newSubscriptionReconcileFixture(t, valid, previous)
	f.grantPartialAndOrder(t, f.nodes(t)[1])
	read := func() model.CustomNodeSubscription {
		t.Helper()
		var source model.CustomNodeSubscription
		if err := f.db.First(&source, f.source.ID).Error; err != nil {
			t.Fatal(err)
		}
		return source
	}
	initial := read()
	if initial.LastSyncStatus != model.SubscriptionSyncSuccess || initial.LastSyncAt == nil || initial.LastSuccessAt == nil || initial.LastError != "" {
		t.Fatalf("initial successful sync status = %+v", initial)
	}
	f.body = subscriptionReconcileYAML(valid, subscriptionReconcileInvalid("source-failed"))
	if _, err := f.manager.sync(context.Background(), f.source.ID); err != nil {
		t.Fatal(err)
	}
	partial := read()
	if partial.LastSyncStatus != model.SubscriptionSyncPartial || partial.LastError == "" || !reflect.DeepEqual(partial.LastSuccessAt, initial.LastSuccessAt) || partial.LastSyncAt == nil || !partial.LastSyncAt.After(*initial.LastSyncAt) {
		t.Fatalf("partial sync status/history = %+v", partial)
	}
	beforeFailureNodes, beforeFailureOrder := f.nodes(t), f.order(t)
	fetch := f.manager.fetch
	fetchError := errors.New("synthetic subscription fetch failure")
	f.manager.fetch = func(context.Context, *url.URL) ([]byte, error) { return nil, fetchError }
	if _, err := f.manager.sync(context.Background(), f.source.ID); !errors.Is(err, fetchError) {
		t.Fatalf("fetch failure = %v, want %v", err, fetchError)
	}
	failed := read()
	if failed.LastSyncStatus != model.SubscriptionSyncFailed || failed.LastError != fetchError.Error() || !reflect.DeepEqual(failed.LastSuccessAt, initial.LastSuccessAt) || failed.NodeCount != partial.NodeCount || failed.LastSyncAt == nil || !failed.LastSyncAt.After(*partial.LastSyncAt) {
		t.Fatalf("failed sync status/history = %+v", failed)
	}
	if !reflect.DeepEqual(f.nodes(t), beforeFailureNodes) || !reflect.DeepEqual(f.order(t), beforeFailureOrder) {
		t.Fatal("fetch failure changed saved nodes, grants or user order")
	}
	f.manager.fetch = fetch
	f.body = subscriptionReconcileYAML(valid, previous)
	if _, err := f.manager.sync(context.Background(), f.source.ID); err != nil {
		t.Fatal(err)
	}
	recovered := read()
	if recovered.LastSyncStatus != model.SubscriptionSyncSuccess || recovered.LastError != "" || recovered.LastSuccessAt == nil || !recovered.LastSuccessAt.After(*initial.LastSuccessAt) || !reflect.DeepEqual(recovered.LastSyncAt, recovered.LastSuccessAt) {
		t.Fatalf("recovered sync status/history = %+v", recovered)
	}
}
