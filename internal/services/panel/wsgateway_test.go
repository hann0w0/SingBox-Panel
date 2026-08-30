package panel

import (
	"fmt"
	"testing"
	"time"
)

func TestWSHandshakeLimiterBlocksBurstsPerPeer(t *testing.T) {
	l := newWSHandshakeLimiter()
	now := time.Unix(100, 0)
	for i := 0; i < wsHandshakeBurst; i++ {
		if wait := l.allow("203.0.113.10", now); wait != 0 {
			t.Fatalf("attempt %d was blocked early for %s", i, wait)
		}
	}
	if wait := l.allow("203.0.113.10", now); wait <= 0 {
		t.Fatal("burst was not blocked")
	}
	if wait := l.allow("203.0.113.11", now); wait != 0 {
		t.Fatalf("different peer was blocked for %s", wait)
	}
}

func TestWSHandshakeLimiterExpiresOldAttempts(t *testing.T) {
	l := newWSHandshakeLimiter()
	old := time.Unix(100, 0)
	for i := 0; i < wsHandshakeBurst; i++ {
		l.allow("203.0.113.10", old)
	}
	if wait := l.allow("203.0.113.10", old.Add(wsHandshakeWindow+time.Second)); wait != 0 {
		t.Fatalf("expired attempts still blocked for %s", wait)
	}
}

func TestWSHandshakeLimiterCapsDistinctPeers(t *testing.T) {
	l := newWSHandshakeLimiter()
	now := time.Unix(100, 0)
	latest := ""
	for i := 0; i < maxWSHandshakePeers+128; i++ {
		latest = fmt.Sprintf("203.0.%d.%d", i/256, i%256)
		if wait := l.allow(latest, now.Add(time.Duration(i)*time.Nanosecond)); wait != 0 {
			t.Fatalf("distinct peer %d was unexpectedly blocked for %s", i, wait)
		}
	}
	if len(l.entries) != maxWSHandshakePeers {
		t.Fatalf("entry count = %d, want %d", len(l.entries), maxWSHandshakePeers)
	}
	if _, ok := l.entries[latest]; !ok {
		t.Fatal("most recently admitted peer was evicted")
	}
}
