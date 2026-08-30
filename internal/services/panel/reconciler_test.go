package panel

import (
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestReconcilerExpiryIsDerivedAndReversible(t *testing.T) {
	db := testDB(t)
	future := time.Now().Add(time.Hour)
	user := model.User{
		Email: "expiry", Password: "x", Role: model.RoleUser, Enabled: true,
		SubToken: "expiry-sub", ProxyToken: "expiry-proxy", ServerIDs: []uint{7}, ExpireAt: &future,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	var changes [][]uint
	reconciler := NewReconciler(db, func(ids []uint) {
		changes = append(changes, append([]uint(nil), ids...))
	})
	reconciler.tick() // establish the initial active state

	past := time.Now().Add(-time.Hour)
	if err := db.Model(&user).Update("expire_at", past).Error; err != nil {
		t.Fatal(err)
	}
	reconciler.tick()
	if len(changes) != 1 || len(changes[0]) != 1 || changes[0][0] != 7 {
		t.Fatalf("expiry did not trigger node reconciliation: %+v", changes)
	}
	var stored model.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.Enabled {
		t.Fatal("expiry must not permanently disable the account")
	}

	later := time.Now().Add(2 * time.Hour)
	if err := db.Model(&user).Update("expire_at", later).Error; err != nil {
		t.Fatal(err)
	}
	reconciler.tick()
	if len(changes) != 2 {
		t.Fatalf("extending expiry did not restore proxy access: %+v", changes)
	}
}
