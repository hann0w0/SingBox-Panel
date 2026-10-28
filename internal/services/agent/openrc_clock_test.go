package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testProcessClock(t *testing.T, boot time.Time, sampleUptime, latency time.Duration) processStartClock {
	t.Helper()
	root := t.TempDir()
	before := boot.Add(sampleUptime)
	uptime := (sampleUptime + latency/2).Truncate(processClockTick)
	writeTestFile(t, filepath.Join(root, "uptime"), fmt.Sprintf("%d.%02d 0.00\n", uptime/time.Second, uptime%time.Second/processClockTick))
	calls := 0
	clock, err := readProcessClockAt(root, func() time.Time {
		calls++
		if calls == 1 {
			return before
		}
		return before.Add(latency)
	})
	if err != nil {
		t.Fatal(err)
	}
	if clock.bootLower.After(boot) {
		t.Fatalf("boot lower bound %s is after actual boot %s", clock.bootLower, boot)
	}
	return clock
}

func writeProcessStartFixture(t *testing.T, root string, pid int, ticks uint64) {
	t.Helper()
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[19] = strconv.FormatUint(ticks, 10)
	writeTestFile(t, filepath.Join(root, strconv.Itoa(pid), "stat"), fmt.Sprintf("%d (sing-box) %s\n", pid, strings.Join(fields, " ")))
}

func TestOpenRCClockAcceptsRestartAfterFractionalBoot(t *testing.T) {
	boot := time.Unix(1700000000, 900000000)
	clock := testProcessClock(t, boot, 9*time.Second, 0)
	root := t.TempDir()
	writeProcessStartFixture(t, root, 321, 1015)
	started, err := readProcessStartTimeAt(root, 321, clock.bootLower)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	contents := []byte(`{"inbounds":[],"outbounds":[{"type":"direct"}]}`)
	writeTestFile(t, path, string(contents))
	written := boot.Add(10100 * time.Millisecond)
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatal(err)
	}
	if err := verifyManagedConfigFile(path, sha256.Sum256(contents), started); err != nil {
		t.Fatalf("a process started 50ms after the config was rejected: %v", err)
	}
	if err := verifyManagedConfigFile(path, sha256.Sum256([]byte("stale config")), started); err == nil {
		t.Fatal("the process clock bypassed config hash verification")
	}
}

func TestOpenRCClockBoundaryNeverAcceptsOldProcess(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "config.json")
	contents := []byte(`{"inbounds":[]}`)
	writeTestFile(t, path, string(contents))
	hash := sha256.Sum256(contents)
	for _, fraction := range []time.Duration{0, 100 * time.Millisecond, 900 * time.Millisecond, 999999999 * time.Nanosecond} {
		for _, latency := range []time.Duration{0, time.Millisecond, 50 * time.Millisecond} {
			boot := time.Unix(1700000000, int64(fraction))
			clock := testProcessClock(t, boot, 9123456789*time.Nanosecond, latency)
			written := boot.Add(10123456789 * time.Nanosecond)
			if err := os.Chtimes(path, written, written); err != nil {
				t.Fatal(err)
			}
			for _, sinceWrite := range []time.Duration{-time.Second, -time.Nanosecond, 0, clock.uncertainty} {
				actual := written.Add(sinceWrite)
				writeProcessStartFixture(t, root, 321, uint64(actual.Sub(boot)/processClockTick))
				started, err := readProcessStartTimeAt(root, 321, clock.bootLower)
				if err != nil {
					t.Fatal(err)
				}
				err = verifyManagedConfigFile(path, hash, started)
				if sinceWrite <= 0 && err == nil {
					t.Fatalf("old process accepted: boot fraction=%s latency=%s age=%s", fraction, latency, sinceWrite)
				}
				if sinceWrite == clock.uncertainty && err != nil {
					t.Fatalf("new process rejected after clock boundary: boot fraction=%s latency=%s: %v", fraction, latency, err)
				}
			}
		}
	}
}

func TestOpenRCConfigStartWaitHonorsClockBoundaryAndCancellation(t *testing.T) {
	manager := openRCServiceManager{clock: processStartClock{bootLower: time.Unix(1700000000, 0), uncertainty: 2 * processClockTick}}
	before := time.Now()
	if err := manager.waitForConfigStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(before); elapsed < manager.clock.uncertainty {
		t.Fatalf("config start wait was only %s, need %s", elapsed, manager.clock.uncertainty)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.waitForConfigStart(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled clock wait returned %v", err)
	}
	if err := (openRCServiceManager{}).waitForConfigStart(context.Background()); err == nil {
		t.Fatal("missing clock anchor was accepted")
	}
}

func TestReadProcessClockRejectsInvalidSamples(t *testing.T) {
	for _, raw := range []string{"", "not-uptime 0.00", "-1.00 0.00", "100000000000000000000.00 0.00"} {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "uptime"), raw)
		if _, err := readProcessClockAt(root, time.Now); err == nil {
			t.Fatalf("invalid uptime %q accepted", raw)
		}
	}
	root := t.TempDir()
	if _, err := readProcessClockAt(root, time.Now); err == nil {
		t.Fatal("missing uptime accepted")
	}
	writeProcessStartFixture(t, root, 321, ^uint64(0))
	if _, err := readProcessStartTimeAt(root, 321, time.Unix(1700000000, 0)); err == nil {
		t.Fatal("overflowing start ticks accepted")
	}
}
