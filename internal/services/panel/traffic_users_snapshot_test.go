package panel

import (
	"net/http"
	"testing"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestUserTrafficSnapshotSurvivesSubscriptionResetAfterIdentityRead(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	if _, err := NewOrchestrator(f.db, NewHub(f.db)).BuildServerConfig(&f.server); err != nil {
		t.Fatal(err)
	}
	resetRan := false
	// Commit a reset immediately after the identity rows have been read. The
	// ownership check must use the same snapshot, not a second user query that
	// can observe the new seed alongside the old in-memory binding.
	if err := f.db.Callback().Query().After("gorm:after_query").Register("test:reset_after_identity_snapshot", func(tx *gorm.DB) {
		if resetRan || (tx.Statement.Table != "user_traffic_identities" && tx.Statement.Table != "identities") {
			return
		}
		resetRan = true
		if rec := resetSubscriptionForTest(t, &App{db: f.db}, f.alice.ID); rec.Code != http.StatusOK {
			t.Fatalf("interleaved reset: %d %s", rec.Code, rec.Body.String())
		}
	}); err != nil {
		t.Fatal(err)
	}
	f.report(t, newTrafficWriter(f.db), []protocol.UserTrafficSnapshot{
		{Inbound: f.multi.Tag, User: proxyTrafficName(&f.alice), Upload: 100, Download: 400},
		{Inbound: f.multi.Tag, User: f.alice.Email, Upload: 23, Download: 56},
		{Inbound: f.multi.Tag, User: proxyTrafficName(&f.namedUser), Upload: 7, Download: 70},
	})
	if !resetRan {
		t.Fatal("test did not interleave a subscription reset")
	}
	got := userRows(t, f.db)
	if got[f.alice.ID] != [2]uint64{123, 456} || got[f.namedUser.ID] != [2]uint64{7, 70} {
		t.Fatalf("concurrent reset lost or reassigned accepted user traffic: %v", got)
	}
	var stored model.User
	if err := f.db.First(&stored, f.alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ProxyToken == f.alice.ProxyToken {
		t.Fatal("the reset did not commit during traffic resolution")
	}
}
