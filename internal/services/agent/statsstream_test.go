package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// ---- protobuf/gRPC-Web builders mirroring daemon/started_service.proto ----

type testConn struct {
	id, inbound, user string
	createdAt, closed int64
	up, down          int64
}

func encodeTestConnection(c testConn) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, c.id)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, c.inbound)
	b = protowire.AppendTag(b, 3, protowire.BytesType) // inboundType, ignored
	b = protowire.AppendString(b, "vless")
	if c.user != "" {
		b = protowire.AppendTag(b, 10, protowire.BytesType)
		b = protowire.AppendString(b, c.user)
	}
	b = protowire.AppendTag(b, 12, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(c.createdAt))
	if c.closed != 0 {
		b = protowire.AppendTag(b, 13, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(c.closed))
	}
	b = protowire.AppendTag(b, 16, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(c.up))
	b = protowire.AppendTag(b, 17, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(c.down))
	b = protowire.AppendTag(b, 21, protowire.BytesType) // repeated chainList, ignored
	b = protowire.AppendString(b, "direct")
	return b
}

type testEvent struct {
	typ      int
	id       string
	conn     *testConn
	up, down int64
}

func encodeTestEvents(reset bool, events ...testEvent) []byte {
	var b []byte
	for _, e := range events {
		var ev []byte
		ev = protowire.AppendTag(ev, 1, protowire.VarintType)
		ev = protowire.AppendVarint(ev, uint64(e.typ))
		ev = protowire.AppendTag(ev, 2, protowire.BytesType)
		ev = protowire.AppendString(ev, e.id)
		if e.conn != nil {
			ev = protowire.AppendTag(ev, 3, protowire.BytesType)
			ev = protowire.AppendBytes(ev, encodeTestConnection(*e.conn))
		}
		if e.up != 0 {
			ev = protowire.AppendTag(ev, 4, protowire.VarintType)
			ev = protowire.AppendVarint(ev, uint64(e.up))
		}
		if e.down != 0 {
			ev = protowire.AppendTag(ev, 5, protowire.VarintType)
			ev = protowire.AppendVarint(ev, uint64(e.down))
		}
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendBytes(b, ev)
	}
	if reset {
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, 1)
	}
	return b
}

func trailerFrame(status string) []byte {
	payload := []byte("grpc-status: " + status + "\r\ngrpc-message: \r\n")
	frame := grpcWebFrame(payload)
	frame[0] = 0x80
	return frame
}

// ---- config discovery ----

func TestParseStatsStreamConfig(t *testing.T) {
	cfg, err := parseStatsStreamConfig([]byte(`{"services":[
		{"type":"api","tag":"other","listen":"127.0.0.1","listen_port":1,"secret":"a"},
		{"type":"api","tag":"panel-stats","listen":"127.0.0.1","listen_port":29092,"secret":"s"}]}`))
	if err != nil || cfg.URL != "http://127.0.0.1:29092/daemon.StartedService/SubscribeConnections" || cfg.Secret != "s" {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	for name, raw := range map[string]string{
		"no services": `{}`,
		"no secret":   `{"services":[{"type":"api","listen":"127.0.0.1","listen_port":29092}]}`,
		"public":      `{"services":[{"type":"api","listen":"0.0.0.0","listen_port":29092,"secret":"s"}]}`,
		"tls":         `{"services":[{"type":"api","listen":"127.0.0.1","listen_port":29092,"secret":"s","tls":{"enabled":true}}]}`,
		"other type":  `{"services":[{"type":"ssm-api","listen":"127.0.0.1","listen_port":29092,"secret":"s"}]}`,
	} {
		if _, err := parseStatsStreamConfig([]byte(raw)); !errors.Is(err, errStatsStreamUnavailable) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// ---- framing and decoding ----

func TestDecodeConnectionEventsRoundTrip(t *testing.T) {
	payload := encodeTestEvents(true,
		testEvent{typ: connectionEventNew, id: "a", conn: &testConn{id: "a", inbound: "in", user: "alice", createdAt: 1700000000123, up: 10, down: 1 << 40}},
		testEvent{typ: connectionEventUpdate, id: "a", up: 5, down: 7},
		testEvent{typ: connectionEventClosed, id: "b"},
	)
	events, err := decodeConnectionEvents(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !events.Reset || len(events.Events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	first := events.Events[0]
	if first.Connection == nil || first.Connection.User != "alice" || first.Connection.Inbound != "in" ||
		first.Connection.DownlinkTotal != 1<<40 || first.Connection.CreatedAt != 1700000000123 {
		t.Fatalf("first = %+v / %+v", first, first.Connection)
	}
	if events.Events[1].Type != connectionEventUpdate || events.Events[1].UplinkDelta != 5 || events.Events[1].DownlinkDelta != 7 {
		t.Fatalf("update = %+v", events.Events[1])
	}
	if events.Events[2].Type != connectionEventClosed || events.Events[2].Connection != nil {
		t.Fatalf("closed = %+v", events.Events[2])
	}
	if _, err := decodeConnectionEvents([]byte{0x0a, 0x05, 0x01}); err == nil {
		t.Fatal("truncated message must fail")
	}
}

func TestReadGRPCWebFrameLimits(t *testing.T) {
	frames := append(grpcWebFrame([]byte("abc")), trailerFrame("0")...)
	r := strings.NewReader(string(frames))
	payload, trailer, err := readGRPCWebFrame(r)
	if err != nil || trailer || string(payload) != "abc" {
		t.Fatalf("first frame = %q %v %v", payload, trailer, err)
	}
	payload, trailer, err = readGRPCWebFrame(r)
	if err != nil || !trailer || trailerStatus(payload) != nil {
		t.Fatalf("trailer = %q %v %v", payload, trailer, err)
	}
	compressed := grpcWebFrame([]byte("x"))
	compressed[0] = 0x01
	if _, _, err := readGRPCWebFrame(strings.NewReader(string(compressed))); err == nil {
		t.Fatal("compressed frame accepted")
	}
	huge := []byte{0, 0xff, 0xff, 0xff, 0xff}
	if _, _, err := readGRPCWebFrame(strings.NewReader(string(huge))); err == nil {
		t.Fatal("oversized frame accepted")
	}
	if err := trailerStatus([]byte("grpc-status: 16\r\ngrpc-message: unauthorized")); !errors.Is(err, errStatsStreamUnavailable) {
		t.Fatalf("unauthenticated trailer = %v", err)
	}
}

// ---- end-to-end subscription against a fake API service ----

func fakeStatsService(t *testing.T, handler http.HandlerFunc) (*statsStream, *trafficSampler) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	address, port, _ := strings.Cut(host, ":")
	sampler := newTrafficSampler()
	stream := newStatsStream(sampler)
	stream.readConfig = func() ([]byte, error) {
		return []byte(`{"services":[{"type":"api","tag":"panel-stats","listen":"` + address + `","listen_port":` + port + `,"secret":"sec"}]}`), nil
	}
	return stream, sampler
}

func TestStatsStreamAttributesUsersExactly(t *testing.T) {
	var gotAuth, gotType string
	var gotBody []byte
	stream, sampler := fakeStatsService(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		now := time.Now().UnixMilli()
		w.Header().Set("Content-Type", "application/grpc-web+proto")
		_, _ = w.Write(grpcWebFrame(encodeTestEvents(true,
			testEvent{typ: connectionEventNew, id: "a", conn: &testConn{id: "a", inbound: "vless-in", user: "alice", createdAt: now, up: 100, down: 1000}},
			// Closed before this subscription but after the Agent started.
			testEvent{typ: connectionEventNew, id: "c", conn: &testConn{id: "c", inbound: "vless-in", user: "bob", createdAt: now, closed: now, up: 3, down: 30}},
		)))
		w.(http.Flusher).Flush()
		_, _ = w.Write(grpcWebFrame(encodeTestEvents(false,
			testEvent{typ: connectionEventUpdate, id: "a", up: 50, down: 500},
			testEvent{typ: connectionEventNew, id: "b", conn: &testConn{id: "b", inbound: "vless-in", user: "bob", createdAt: now, up: 7, down: 70}},
			testEvent{typ: connectionEventClosed, id: "a", conn: &testConn{id: "a", inbound: "vless-in", user: "alice", createdAt: now, closed: now, up: 160, down: 1600}},
		)))
		_, _ = w.Write(trailerFrame("0"))
	})
	stream.sampler.startedAt = time.Now().Add(-time.Minute)
	if err := stream.subscribeOnce(context.Background()); err != io.EOF {
		t.Fatalf("subscribe = %v", err)
	}
	if gotAuth != "Bearer sec" || gotType != "application/grpc-web+proto" {
		t.Fatalf("request headers: auth=%q type=%q", gotAuth, gotType)
	}
	if events, err := decodeConnectionEvents(gotBody[5:]); err != nil || len(events.Events) != 0 {
		t.Fatalf("request body must be a SubscribeConnectionsRequest frame: %v", err)
	}
	alice := sampler.pendingUser("vless-in", "alice")
	bob := sampler.pendingUser("vless-in", "bob")
	if alice.Upload != 160 || alice.Download != 1600 || bob.Upload != 10 || bob.Download != 100 {
		t.Fatalf("alice=%+v bob=%+v", alice, bob)
	}
	if port := sampler.pendingPorts["vless-in"]; port.Upload != 170 || port.Download != 1700 {
		t.Fatalf("port = %+v", port)
	}
}

func TestStatsStreamErrors(t *testing.T) {
	stream, _ := fakeStatsService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Grpc-Status", "16")
		w.Header().Set("Grpc-Message", "unauthorized")
		w.WriteHeader(http.StatusOK)
	})
	if err := stream.subscribeOnce(context.Background()); !errors.Is(err, errStatsStreamUnavailable) {
		t.Fatalf("trailers-only unauthenticated = %v", err)
	}
	stream, _ = fakeStatsService(t, http.NotFound)
	if err := stream.subscribeOnce(context.Background()); !errors.Is(err, errStatsStreamUnavailable) {
		t.Fatalf("404 = %v", err)
	}
	stream, _ = fakeStatsService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(grpcWebFrame(encodeTestEvents(true)))
	})
	if err := stream.subscribeOnce(context.Background()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("stream cut without trailer = %v", err)
	}
}

// ---- ledger: the two sources never double count ----

func clashConn(id, inbound string, up, down uint64, start time.Time) clashConnection {
	c := trafficConn(id, inbound, up, down)
	c.Start = start
	return c
}

func TestLedgerStreamAfterClashFallbackDoesNotDoubleCount(t *testing.T) {
	sampler := newTrafficSampler()
	t0 := time.Unix(1_700_000_000, 0)
	sampler.startedAt = t0
	created := t0.Add(time.Second)
	// The stream is down: Clash alone counts the connection.
	sampler.ingest("e", clashTrafficResponse{Connections: []clashConnection{clashConn("x", "in", 0, 0, created)}}, t0.Add(2*time.Second))
	sampler.ingest("e", clashTrafficResponse{Connections: []clashConnection{clashConn("x", "in", 40, 400, created)}}, t0.Add(3*time.Second))
	// The stream reconnects: its reset batch reports the same connection.
	session := newStreamSession(t0.Add(4 * time.Second))
	sampler.applyStreamEvents(session, connectionEvents{Reset: true, Events: []connectionEvent{{
		Type: connectionEventNew, ID: "x",
		Connection: &streamConnection{ID: "x", Inbound: "in", User: "alice", CreatedAt: created.UnixMilli(), UplinkTotal: 50, DownlinkTotal: 500},
	}}}, t0.Add(4*time.Second))
	sampler.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{
		{Type: connectionEventUpdate, ID: "x", UplinkDelta: 10, DownlinkDelta: 100},
		{Type: connectionEventClosed, ID: "x"},
	}}, t0.Add(5*time.Second))
	// A later Clash poll still lists it (request issued before the close).
	sampler.ingestAt("e", clashTrafficResponse{Connections: []clashConnection{clashConn("x", "in", 55, 550, created)}}, t0.Add(4500*time.Millisecond), t0.Add(6*time.Second))
	port := sampler.pendingPorts["in"]
	if port.Upload != 60 || port.Download != 600 {
		t.Fatalf("port = %+v, want exactly 60/600", port)
	}
	// The user became known after Clash counted the first bytes: credited once.
	if user := sampler.pendingUser("in", "alice"); user.Upload != 60 || user.Download != 600 {
		t.Fatalf("user = %+v, want back-filled 60/600", user)
	}
	// A fresh subscription replays the closed connection: nothing new.
	replay := newStreamSession(t0.Add(7 * time.Second))
	sampler.applyStreamEvents(replay, connectionEvents{Reset: true, Events: []connectionEvent{{
		Type: connectionEventNew, ID: "x",
		Connection: &streamConnection{ID: "x", Inbound: "in", User: "alice", CreatedAt: created.UnixMilli(), ClosedAt: t0.Add(5 * time.Second).UnixMilli(), UplinkTotal: 60, DownlinkTotal: 600},
	}}}, t0.Add(7*time.Second))
	if port := sampler.pendingPorts["in"]; port.Upload != 60 || port.Download != 600 {
		t.Fatalf("replayed closed connection counted again: %+v", port)
	}
}

func TestLedgerBaselinesConnectionsOlderThanAgent(t *testing.T) {
	sampler := newTrafficSampler()
	t0 := time.Unix(1_700_000_000, 0)
	sampler.startedAt = t0
	session := newStreamSession(t0)
	sampler.applyStreamEvents(session, connectionEvents{Reset: true, Events: []connectionEvent{
		{Type: connectionEventNew, ID: "old", Connection: &streamConnection{ID: "old", Inbound: "in", User: "u", CreatedAt: t0.Add(-time.Hour).UnixMilli(), UplinkTotal: 9000, DownlinkTotal: 9000}},
		{Type: connectionEventNew, ID: "gone", Connection: &streamConnection{ID: "gone", Inbound: "in", User: "u", CreatedAt: t0.Add(-time.Hour).UnixMilli(), ClosedAt: t0.Add(-time.Minute).UnixMilli(), UplinkTotal: 5000, DownlinkTotal: 5000}},
	}}, t0)
	sampler.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{
		{Type: connectionEventUpdate, ID: "old", UplinkDelta: 1, DownlinkDelta: 2},
	}}, t0.Add(time.Second))
	if user := sampler.pendingUser("in", "u"); user.Upload != 1 || user.Download != 2 {
		t.Fatalf("user = %+v, want only post-start bytes 1/2", user)
	}
}

func TestLedgerUsersSnapshotAndAcknowledge(t *testing.T) {
	sampler := newTrafficSampler()
	sampler.available, sampler.haveSample, sampler.lastSample = true, true, time.Now()
	sampler.mu.Lock()
	sampler.observeConnection("a", "in", "alice", [2]uint64{10, 20}, false, time.Now())
	sampler.mu.Unlock()
	snap := sampler.snapshot()
	if len(snap.Users) != 1 || snap.Users[0].User != "alice" || snap.Users[0].Download != 20 {
		t.Fatalf("users = %+v", snap.Users)
	}
	if summary := sampler.summarySnapshot(); len(summary.Users) != 0 {
		t.Fatal("heartbeat summary must not carry user deltas")
	}
	sampler.mu.Lock()
	sampler.observeConnection("a", "in", "alice", [2]uint64{15, 25}, false, time.Now())
	sampler.mu.Unlock()
	sampler.acknowledge(snap)
	if user := sampler.pendingUser("in", "alice"); user.Upload != 5 || user.Download != 5 {
		t.Fatalf("after ack = %+v, want newer 5/5", user)
	}
}

func TestLedgerTombstonesAreBounded(t *testing.T) {
	sampler := newTrafficSampler()
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	for i := 0; i < maxTrafficTombstones+10; i++ {
		id := strings.Repeat("x", 1) + string(rune('a'+i%26)) + time.Duration(i).String()
		sampler.observeConnection(id, "in", "", [2]uint64{1, 1}, false, time.Now())
		sampler.closeConnection(id)
	}
	if len(sampler.tombstones) != maxTrafficTombstones || len(sampler.tombstoneOrder) != maxTrafficTombstones || len(sampler.ledger) != 0 {
		t.Fatalf("tombstones=%d order=%d ledger=%d", len(sampler.tombstones), len(sampler.tombstoneOrder), len(sampler.ledger))
	}
}
