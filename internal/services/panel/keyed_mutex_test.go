package panel

import (
	"runtime"
	"sync"
	"testing"
)

func TestKeyedMutexZeroValueSerializesAndReclaimsEntries(t *testing.T) {
	var locks keyedMutex[uint]
	var wg sync.WaitGroup
	inside := 0
	maxInside := 0
	var stateMu sync.Mutex
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := locks.lock(1)
			stateMu.Lock()
			inside++
			if inside > maxInside {
				maxInside = inside
			}
			stateMu.Unlock()
			runtime.Gosched()
			stateMu.Lock()
			inside--
			stateMu.Unlock()
			unlock()
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("same-key critical sections overlapped: max=%d", maxInside)
	}
	if size := locks.size(); size != 0 {
		t.Fatalf("keyed mutex retained unused entries: size=%d", size)
	}
}
