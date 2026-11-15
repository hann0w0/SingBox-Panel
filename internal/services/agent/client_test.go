package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

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

func TestConnectOnceClosesWebSocketOnContextCancel(t *testing.T) {
	connected := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		close(connected)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", false)
	client.wsURL = "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.connectOnce(ctx)
		done <- err
	}()

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not establish WebSocket")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connectOnce did not stop promptly after context cancellation")
	}
}

func TestOutboundEventsAreScopedToTheirWebSocketSession(t *testing.T) {
	c := NewClient("https://panel.example.com", "token", false)

	// Telemetry produced while disconnected is intentionally discarded.
	if c.SendEvent(protocol.EvtTraffic, protocol.TrafficEvt{}) {
		t.Fatal("offline telemetry reported as queued")
	}
	if len(c.send) != 0 {
		t.Fatal("offline telemetry was queued for a future connection")
	}

	c.activeSession.Store(1)
	if !c.SendEvent(protocol.EvtTraffic, protocol.TrafficEvt{}) {
		t.Fatal("connected telemetry was not queued")
	}
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

func TestDisconnectCompletesQueuedReceiptsBeforeNextSession(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", false)
	client.wsURL = "ws" + strings.TrimPrefix(server.URL, "http")

	for _, cancelBeforeWrite := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		receipts := make(chan bool, 1)
		client.OnConnected = func() {
			if !client.sendEvent(protocol.EvtTraffic, protocol.TrafficEvt{}, func(written bool) {
				receipts <- written
				cancel()
			}) {
				t.Error("connected event was not queued")
				cancel()
			}
			if cancelBeforeWrite {
				// The writer starts after OnConnected, so this event is still queued.
				cancel()
			}
		}
		_, _ = client.connectOnce(ctx)
		cancel()
		select {
		case written := <-receipts:
			if written == cancelBeforeWrite {
				t.Fatalf("write receipt = %v, cancelBeforeWrite = %v", written, cancelBeforeWrite)
			}
		default:
			t.Fatal("session shutdown did not complete its event receipt")
		}
		if client.activeSession.Load() != 0 || len(client.send) != 0 {
			t.Fatal("session returned before its writer and queue were cleaned up")
		}
	}
}

func TestCommandDispatchBoundsWorkKeepsTrafficACKAndDropsOldQueue(t *testing.T) {
	connections := make(chan *websocket.Conn, 2)
	release := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connections <- conn
		<-release
	}))
	defer server.Close()
	defer close(release)
	client := NewClient(server.URL, "token", false)
	client.wsURL = "ws" + strings.TrimPrefix(server.URL, "http")
	started := make(chan string, maxSessionCommands+maxQueuedCommands+1)
	canceled := make(chan string, maxSessionCommands+1)
	acks := make(chan protocol.TrafficAck, 1)
	client.OnCommand = func(ctx context.Context, env protocol.Envelope) protocol.CommandResultEvt {
		started <- env.ID
		<-ctx.Done()
		canceled <- env.ID
		return protocol.CommandResultEvt{ID: env.ID, OK: false, Error: ctx.Err().Error()}
	}
	client.OnTrafficAck = func(ack protocol.TrafficAck) { acks <- ack }

	for session := 0; session < 2; session++ {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		done := make(chan struct{})
		go func() {
			_, _ = client.connectOnce(ctx)
			close(done)
		}()
		var conn *websocket.Conn
		select {
		case conn = <-connections:
		case <-time.After(3 * time.Second):
			t.Fatal("client did not connect")
		}
		t.Cleanup(func() { _ = conn.Close() })
		send := func(typ protocol.MessageType, id string, payload any) {
			t.Helper()
			env, err := protocol.NewEnvelope(typ, id, payload)
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if err := conn.WriteJSON(env); err != nil {
				t.Fatal(err)
			}
		}
		active := maxSessionCommands
		if session == 1 {
			active = 1
		}
		for command := 0; command < active; command++ {
			id := fmt.Sprintf("session-%d-active-%d", session, command)
			send(protocol.CmdGetStatus, id, struct{}{})
			select {
			case got := <-started:
				if got != id {
					t.Fatalf("stale queued command started: %q, want %q", got, id)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not dispatch command")
			}
		}
		if session == 0 {
			for command := 0; command < maxQueuedCommands; command++ {
				send(protocol.CmdGetStatus, fmt.Sprintf("queued-%d", command), struct{}{})
			}
			send(protocol.CmdGetStatus, "overflow", struct{}{})
			ack := protocol.TrafficAck{Session: "accounting-session", Sequence: 42}
			send(protocol.EvtTrafficAck, "", ack)
			select {
			case got := <-acks:
				if got != ack {
					t.Fatalf("ACK changed: %+v", got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("command overload blocked traffic ACK")
			}
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			var env protocol.Envelope
			if err := conn.ReadJSON(&env); err != nil {
				t.Fatal(err)
			}
			var result protocol.CommandResultEvt
			if err := env.Decode(&result); err != nil {
				t.Fatal(err)
			}
			if env.Type != protocol.EvtCommandResult || env.ID != "overflow" || result.ID != env.ID || result.OK || !strings.Contains(result.Error, "queue is full") {
				t.Fatalf("missing explicit overflow failure: %+v %+v", env, result)
			}
			select {
			case id := <-started:
				t.Fatalf("concurrency limit exceeded: %q", id)
			default:
			}
		}
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("disconnect did not cancel and join command workers")
		}
		for command := 0; command < active; command++ {
			select {
			case <-canceled:
			default:
				t.Fatal("command context survived disconnect")
			}
		}
		if len(started) != 0 || len(client.send) != 0 {
			t.Fatal("disconnect retained queued work or command results")
		}
		_ = conn.Close()
	}
}
