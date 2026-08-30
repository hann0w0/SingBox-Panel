package agent

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestPreferredIPFamilyAlwaysChoosesIPv4WhenAvailable(t *testing.T) {
	network, addrs := preferredIPFamily([]netip.Addr{
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("192.0.2.10"),
		netip.MustParseAddr("2001:db8::2"),
		netip.MustParseAddr("192.0.2.11"),
	})
	if network != "tcp4" {
		t.Fatalf("network = %q; want tcp4", network)
	}
	want := []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.11")}
	if !reflect.DeepEqual(addrs, want) {
		t.Fatalf("addresses = %v; want %v", addrs, want)
	}
}

func TestReconnectBackoffResetsAfterStableConnection(t *testing.T) {
	if got := followingReconnectBackoff(maxReconnectBackoff, stableConnectionTime); got != minReconnectBackoff {
		t.Fatalf("backoff after stable connection = %s; want %s", got, minReconnectBackoff)
	}
}

func TestReconnectBackoffGrowsAndCapsForShortFailures(t *testing.T) {
	backoff := minReconnectBackoff
	for i := 0; i < 10; i++ {
		backoff = followingReconnectBackoff(backoff, 5*time.Second)
	}
	if backoff != maxReconnectBackoff {
		t.Fatalf("capped backoff = %s; want %s", backoff, maxReconnectBackoff)
	}
}

func TestReconnectJitterStaysWithinBounds(t *testing.T) {
	base := 10 * time.Second
	for i := 0; i < 100; i++ {
		got := jitterReconnectBackoff(base)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("jittered backoff = %s; want 8s..12s", got)
		}
	}
}

func TestPreferredIPFamilyUsesIPv6OnlyWithoutIPv4(t *testing.T) {
	network, addrs := preferredIPFamily([]netip.Addr{
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("2001:db8::2"),
	})
	if network != "tcp6" || len(addrs) != 2 {
		t.Fatalf("network=%q addresses=%v; want IPv6-only selection", network, addrs)
	}
}

func TestDialResolvedFamilyNeverFallsBackToIPv6WhenIPv4Exists(t *testing.T) {
	var networks []string
	_, err := dialResolvedFamily(context.Background(), "panel.example.com", "443", []netip.Addr{
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("192.0.2.10"),
	}, func(_ context.Context, network, _ string) (net.Conn, error) {
		networks = append(networks, network)
		return nil, errors.New("test dial failure")
	})
	if err == nil {
		t.Fatal("dial unexpectedly succeeded")
	}
	if !reflect.DeepEqual(networks, []string{"tcp4"}) {
		t.Fatalf("dialed networks = %v; want IPv4 only", networks)
	}
}

func TestOutboundEventsAreScopedToTheirWebSocketSession(t *testing.T) {
	c := NewClient("https://panel.example.com", "token", false)

	// Telemetry produced while disconnected is intentionally discarded.
	c.SendEvent(protocol.EvtTraffic, protocol.TrafficEvt{})
	if len(c.send) != 0 {
		t.Fatal("offline telemetry was queued for a future connection")
	}

	c.activeSession.Store(1)
	c.SendEvent(protocol.EvtTraffic, protocol.TrafficEvt{})
	first := <-c.send
	if first.session != 1 {
		t.Fatalf("telemetry session = %d, want 1", first.session)
	}

	oldCtx := context.WithValue(context.Background(), clientSessionContextKey{}, uint64(1))
	c.activeSession.Store(2)
	c.SendEventForContext(oldCtx, protocol.EvtProgress, "old", protocol.ProgressEvt{})
	c.SendEventForSession(1, protocol.EvtLog, "old", protocol.LogEvt{})
	if len(c.send) != 0 {
		t.Fatal("old-session events were queued after reconnect")
	}

	newCtx := context.WithValue(context.Background(), clientSessionContextKey{}, uint64(2))
	c.SendEventForContext(newCtx, protocol.EvtProgress, "new", protocol.ProgressEvt{})
	current := <-c.send
	if current.session != 2 || current.env.ID != "new" {
		t.Fatalf("current event = %#v", current)
	}
}
