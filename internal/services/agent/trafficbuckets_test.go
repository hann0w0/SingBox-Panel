package agent

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestOfflineTrafficKeepsUTCResetBoundary(t *testing.T) {
	s := newTrafficSampler()
	reset := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s.ingest("api", clashTrafficResponse{}, reset.Add(-2*time.Minute))
	s.ingest("api", clashTrafficResponse{UploadTotal: 1000}, reset.Add(-time.Minute))
	s.ingest("api", clashTrafficResponse{UploadTotal: 1200}, reset.Add(time.Minute))
	report := s.accountingSnapshot().Report
	var before, after uint64
	for _, b := range report.Buckets {
		if b.Time < reset.Unix() {
			before += b.Upload
		} else {
			after += b.Upload
		}
	}
	if before != 1000 || after != 200 {
		t.Fatalf("usage before/after reset = %d/%d", before, after)
	}
	s.available = false
	if snap := s.accountingSnapshot(); !snap.Unavailable || !reflect.DeepEqual(snap.Report, report) {
		t.Fatal("API failure lost the pending batch")
	}
}

func TestLateUserIdentityPreservesOriginalBucket(t *testing.T) {
	s := newTrafficSampler()
	reset := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s.ingest("api", clashTrafficResponse{}, reset.Add(-2*time.Minute))
	s.observeConnection("c", "in", "", [2]uint64{100, 200}, false, reset.Add(-time.Minute))
	s.observeConnection("c", "in", "alice", [2]uint64{120, 240}, false, reset.Add(time.Minute))
	report := s.accountingSnapshot().Report
	var before, after uint64
	for _, b := range report.Buckets {
		for _, u := range b.Users {
			if b.Time < reset.Unix() {
				before += u.Upload
			} else {
				after += u.Upload
			}
		}
	}
	if before != 100 || after != 20 {
		t.Fatalf("user usage before/after reset = %d/%d", before, after)
	}
}

func TestLateUserIdentityDoesNotRebaseCurrentMinuteBuckets(t *testing.T) {
	s := newTrafficSampler()
	t0 := time.Date(2026, 10, 1, 10, 15, 0, 0, time.UTC)
	t1 := time.Date(2026, 10, 1, 10, 16, 0, 0, time.UTC)

	s.ingest("api", clashTrafficResponse{}, t0.Add(-time.Minute))
	s.ingest("api", clashTrafficResponse{UploadTotal: 500}, t0)
	// Connection counted by Clash before user identity arrives -> unassigned[10:00:00].
	s.observeConnection("c1", "in", "", [2]uint64{100, 0}, false, t0)
	// Newer live traffic at 10:16.
	s.ingest("api", clashTrafficResponse{UploadTotal: 800}, t1)
	// User identity arrives at 10:16; replaying unassigned[10:00:00] must NOT rebase 10:15/10:16 back to 10:00.
	s.observeConnection("c1", "in", "alice", [2]uint64{150, 0}, false, t1)

	report := s.accountingSnapshot().Report
	byTime := make(map[int64]uint64)
	for _, b := range report.Buckets {
		byTime[b.Time] += b.Upload
	}
	if byTime[t0.Unix()] != 500 || byTime[t1.Unix()] != 300 {
		t.Fatalf("live minute buckets were corrupted by late user replay: %+v", byTime)
	}
}

func TestAccountingSnapshotReportClonePreventsRebaseRace(t *testing.T) {
	s := newTrafficSampler()
	future := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	s.ingest("api", clashTrafficResponse{}, future.Add(-2*time.Minute))
	s.ingest("api", clashTrafficResponse{UploadTotal: 100}, future.Add(-time.Minute))
	s.ingest("api", clashTrafficResponse{UploadTotal: 200}, future)

	snap := s.accountingSnapshot()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if _, err := json.Marshal(snap); err != nil {
				t.Errorf("marshal snapshot: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			s.mu.Lock()
			s.rebaseFutureClockLocked(now.Unix())
			s.mu.Unlock()
		}
	}()
	wg.Wait()
}
