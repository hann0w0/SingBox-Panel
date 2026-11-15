package panel

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

// A burst of due sources must not fan out into one goroutine (and one SQLite
// write transaction) per source, and the scan must not return until the round
// is finished so the next tick cannot fetch the same sources again.
func TestCustomNodeSubscriptionSyncDueIsBounded(t *testing.T) {
	db := testDB(t)
	const total = 12
	for i := 0; i < total; i++ {
		src := model.CustomNodeSubscription{
			Name: fmt.Sprintf("s%d", i), URL: "https://example.com/sub",
			AutoUpdate: true, UpdateIntervalMinutes: 60,
		}
		if err := db.Create(&src).Error; err != nil {
			t.Fatal(err)
		}
	}
	m := newCustomNodeSubscriptionManager(db)
	var inFlight, peak int32
	var mu sync.Mutex
	seen := map[uint]int{}
	m.syncSource = func(ctx context.Context, id uint) error {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		mu.Lock()
		seen[id]++
		mu.Unlock()
		return nil
	}

	m.syncDue(context.Background())

	if got := atomic.LoadInt32(&inFlight); got != 0 {
		t.Fatalf("syncDue returned with %d syncs still running", got)
	}
	if p := atomic.LoadInt32(&peak); p > customNodeSubscriptionSyncWorkers {
		t.Fatalf("peak concurrency = %d, want <= %d", p, customNodeSubscriptionSyncWorkers)
	}
	if len(seen) != total {
		t.Fatalf("synced %d sources, want %d", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("source %d synced %d times", id, n)
		}
	}
}
