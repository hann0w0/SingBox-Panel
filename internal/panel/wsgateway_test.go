package panel

import (
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
