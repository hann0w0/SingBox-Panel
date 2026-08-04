package panel

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
)

// Reconciler watches derived user access state. Expiry is not persisted into
// Enabled: extending an expired account should make it active again without an
// administrator also having to flip a second switch. When the derived state
// changes, managed multi-user nodes are regenerated so the user's independent
// credential is added or removed.
type Reconciler struct {
	db             *gorm.DB
	interval       time.Duration
	active         map[uint]bool
	onAccessChange func([]uint)
}

// NewReconciler builds a Reconciler.
func NewReconciler(db *gorm.DB, onAccessChange func([]uint)) *Reconciler {
	return &Reconciler{db: db, interval: 30 * time.Second, active: map[uint]bool{}, onAccessChange: onAccessChange}
}

// Run ticks until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	r.tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick()
		}
	}
}

func (r *Reconciler) tick() {
	now := time.Now()
	if err := pruneTrafficRecords(r.db, now); err != nil {
		log.Printf("reconciler: prune traffic records: %v", err)
	}
	var users []model.User
	if err := r.db.Find(&users).Error; err != nil {
		return
	}

	seen := make(map[uint]bool, len(users))
	for i := range users {
		u := &users[i]
		active := u.Enabled && !u.Expired(now)
		previous, known := r.active[u.ID]
		if known && previous != active && r.onAccessChange != nil {
			r.onAccessChange(u.ServerIDs)
			log.Printf("reconciler: user %d (%s) proxy access changed to active=%t", u.ID, u.Email, active)
		}
		r.active[u.ID] = active
		seen[u.ID] = true
	}
	for id := range r.active {
		if !seen[id] {
			delete(r.active, id)
		}
	}
}

// ResetServersOffline clears stale online flags at panel startup (agents will
// re-register and flip themselves back online).
func ResetServersOffline(db *gorm.DB) {
	db.Model(&model.Server{}).Where("online = ?", true).Update("online", false)
}
