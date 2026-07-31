package panel

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"

	"github.com/singpanel/singpanel/internal/model"
)

// Reconciler periodically disables expired users.
//
// IMPORTANT: this only cuts off the PANEL — an expired account can no longer
// log in, pull its subscription, or list nodes. It does NOT disconnect anyone
// from the proxies. Inbounds are single-credential, so the node's config
// carries one fixed credential that is identical for every user and does not
// change when an account is disabled; a client that already saved it keeps
// working. Revoking proxy access requires either per-user credentials on the
// inbound or rotating that inbound's secret (which cuts off everyone on it).
type Reconciler struct {
	db       *gorm.DB
	interval time.Duration
}

// NewReconciler builds a Reconciler.
func NewReconciler(db *gorm.DB) *Reconciler {
	return &Reconciler{db: db, interval: 30 * time.Second}
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
	var users []model.User
	r.db.Where("enabled = ? AND role = ?", true, model.RoleUser).Find(&users)

	for i := range users {
		u := &users[i]
		if u.Expired(now) {
			if err := r.db.Model(&model.User{}).Where("id = ?", u.ID).Update("enabled", false).Error; err == nil {
				log.Printf("reconciler: disabled user %d (%s) — expired", u.ID, u.Email)
			}
		}
	}
}

// ResetServersOffline clears stale online flags at panel startup (agents will
// re-register and flip themselves back online).
func ResetServersOffline(db *gorm.DB) {
	db.Model(&model.Server{}).Where("online = ?", true).Update("online", false)
}
