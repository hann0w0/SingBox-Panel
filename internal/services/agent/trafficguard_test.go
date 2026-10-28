package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func trafficGuardConfig(t *testing.T, multiUser bool, count int) []byte {
	t.Helper()
	var inbounds []singbox.InboundInput
	for i := 0; i < count; i++ {
		in := singbox.InboundInput{
			Tag: []string{"secure", "second"}[i], Type: "vless", ListenPort: 31000 + i*2,
			Settings: singbox.InboundSettings{
				SingleUser: !multiUser, UUID: "11111111-2222-4333-8444-555555555555",
				TLS: singbox.TLSSettings{Enabled: true, ServerName: "music.apple.com", Reality: singbox.RealitySettings{
					Enabled: true, HandshakeServer: "music.apple.com", HandshakeServerPort: 443,
					PrivateKey: "UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc", PublicKey: "public-key", ShortID: []string{"abcd"},
					Guard: &singbox.RealityGuardSettings{Enabled: true, InternalPort: 31001 + i*2, Tag: []string{"front", "edge"}[i]},
				}},
			},
		}
		if multiUser {
			in.Users = []singbox.ProxyUser{
				{Name: "alice", UUID: "11111111-2222-4333-8444-555555555555"},
				{Name: "bob", UUID: "22222222-2222-4333-8444-555555555555"},
			}
		}
		inbounds = append(inbounds, in)
	}
	// The suffix is deliberately not a real guard.
	inbounds = append(inbounds, singbox.InboundInput{Tag: "ordinary-guard", Type: "socks", ListenPort: 32000})
	raw, err := singbox.BuildServerConfig(singbox.ServerConfigInput{
		Inbounds: inbounds, StatsController: "127.0.0.1:29091", StatsSecret: "sec", StatsAPI: "127.0.0.1:29092",
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestTrafficGuardRecognizesGeneratedSingleAndMultiUserLayouts(t *testing.T) {
	for _, multiUser := range []bool{false, true} {
		raw := trafficGuardConfig(t, multiUser, 2)
		want := map[string]bool{"front": true, "edge": true}
		if got := parseRealityGuardInbounds(raw); !reflect.DeepEqual(got, want) {
			t.Fatalf("multi-user %v guards = %v, want %v", multiUser, got, want)
		}
		cfg, err := parseLocalTrafficConfig(raw)
		if err != nil || !reflect.DeepEqual(cfg.GuardInbounds, want) {
			t.Fatalf("poll config lost layout: %+v, %v", cfg, err)
		}
	}
	if got := parseRealityGuardInbounds([]byte(`{}`)); got == nil || len(got) != 0 {
		t.Fatalf("parsed unguarded config must be distinct from unknown layout: %v", got)
	}
}

func TestTrafficGuardDoesNotInferExclusionFromSimilarLayouts(t *testing.T) {
	gate := func(root map[string]any) map[string]any { return root["inbounds"].([]any)[0].(map[string]any) }
	inner := func(root map[string]any) map[string]any { return root["inbounds"].([]any)[1].(map[string]any) }
	rules := func(root map[string]any) []any { return root["route"].(map[string]any)["rules"].([]any) }
	for name, mutate := range map[string]func(map[string]any){
		"public target":     func(root map[string]any) { gate(root)["override_address"] = "192.0.2.1" },
		"wrong target port": func(root map[string]any) { gate(root)["override_port"] = 31003 },
		"nonlocal inner":    func(root map[string]any) { inner(root)["listen"] = "::" },
		"non-reality inner": func(root map[string]any) { delete(inner(root)["tls"].(map[string]any), "reality") },
		"extra gate option": func(root map[string]any) { gate(root)["detour"] = "other" },
		"logical OR":        func(root map[string]any) { rules(root)[1].(map[string]any)["mode"] = "or" },
		"wrong allow port": func(root map[string]any) {
			rules(root)[1].(map[string]any)["rules"].([]any)[1].(map[string]any)["port"] = []int{31002}
		},
		"missing deny":        func(root map[string]any) { root["route"].(map[string]any)["rules"] = rules(root)[:2] },
		"nonterminal deny":    func(root map[string]any) { rules(root)[2].(map[string]any)["method"] = "default" },
		"non-direct outbound": func(root map[string]any) { root["outbounds"].([]any)[0].(map[string]any)["type"] = "socks" },
		"duplicate tag":       func(root map[string]any) { inner(root)["tag"] = "front" },
		"port collision":      func(root map[string]any) { root["inbounds"].([]any)[2].(map[string]any)["listen_port"] = 31001 },
		"nonleading chain": func(root map[string]any) {
			root["route"].(map[string]any)["rules"] = append([]any{map[string]any{"action": "route", "outbound": "direct"}}, rules(root)...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var root map[string]any
			if err := json.Unmarshal(trafficGuardConfig(t, false, 1), &root); err != nil {
				t.Fatal(err)
			}
			mutate(root)
			raw, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			if guards := parseRealityGuardInbounds(raw); len(guards) != 0 {
				t.Fatalf("ordinary or unrecognized traffic would be excluded: %v", guards)
			}
		})
	}
}

func guardClashConnection(id, inbound, typ string, up uint64, created time.Time) clashConnection {
	c := clashConn(id, inbound, up, 2*up, created)
	c.Metadata.Type = typ + "/" + inbound
	return c
}

func guardStreamEvent(typ int, id, inbound, inboundType, user string, up int64, created time.Time) connectionEvent {
	c := &streamConnection{ID: id, Inbound: inbound, InboundType: inboundType, User: user,
		UplinkTotal: up, DownlinkTotal: 2 * up, CreatedAt: created.UnixMilli()}
	if typ == connectionEventClosed {
		c.ClosedAt = created.Add(time.Second).UnixMilli()
	}
	return connectionEvent{Type: typ, ID: id, Connection: c}
}

func guardSession(now time.Time, guards map[string]bool) *streamSession {
	session := newStreamSession(now)
	session.guardInbounds = guards
	return session
}

func ingestGuardSample(s *trafficSampler, guards map[string]bool, now time.Time, raw uint64, connections ...clashConnection) {
	s.ingestWithLayoutAt("api", clashTrafficResponse{UploadTotal: raw, DownloadTotal: 2 * raw, Connections: connections}, guards, now, now)
}

func TestTrafficGuardCountsInnerAndOrdinaryTrafficOnceAcrossSources(t *testing.T) {
	s := newTrafficSampler()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	old := start.Add(-time.Minute)
	guards := parseRealityGuardInbounds(trafficGuardConfig(t, true, 1))
	session := guardSession(start, guards)
	ingestGuardSample(s, guards, start, 2500,
		guardClashConnection("i", "secure", "vless", 1000, old),
		guardClashConnection("o", "front", "direct", 1200, old),
		guardClashConnection("n", "ordinary-guard", "socks", 300, old))
	ingestGuardSample(s, guards, start.Add(time.Second), 2740,
		guardClashConnection("i", "secure", "vless", 1100, old),
		guardClashConnection("o", "front", "direct", 1320, old),
		guardClashConnection("n", "ordinary-guard", "socks", 320, old))
	s.setStatsStreamAvailable(true)
	s.applyStreamEvents(session, connectionEvents{Reset: true, Events: []connectionEvent{
		guardStreamEvent(connectionEventNew, "i", "secure", "vless", "alice", 1100, old),
		guardStreamEvent(connectionEventNew, "o", "front", "direct", "", 1320, old),
		guardStreamEvent(connectionEventNew, "n", "ordinary-guard", "socks", "", 320, old),
	}}, start.Add(1500*time.Millisecond))
	s.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{
		guardStreamEvent(connectionEventClosed, "i", "secure", "vless", "alice", 1125, old),
		guardStreamEvent(connectionEventClosed, "o", "front", "direct", "", 1360, old),
		guardStreamEvent(connectionEventNew, "short-i", "secure", "vless", "bob", 10, start.Add(time.Second)),
		guardStreamEvent(connectionEventClosed, "short-i", "secure", "vless", "bob", 15, start.Add(time.Second)),
		guardStreamEvent(connectionEventClosed, "short-o", "front", "direct", "", 40, start.Add(time.Second)),
	}}, start.Add(2*time.Second))
	// The doubled raw counters are deliberately unrelated to the desired node total.
	ingestGuardSample(s, guards, start.Add(3*time.Second), 9000,
		guardClashConnection("n", "ordinary-guard", "socks", 320, old))
	snap := s.snapshot()
	if snap == nil || snap.UploadTotal != 160 || snap.DownloadTotal != 320 || snap.UploadRate != 20 {
		t.Fatalf("guard live snapshot = %+v", snap)
	}
	if _, found := s.pendingPorts["front"]; found {
		t.Fatal("synthetic outer leg leaked into port accounting")
	}
	if got := s.pendingUser("secure", "alice").Upload; got != 125 {
		t.Fatalf("late user identity or CLOSED was lost/doubled: %d", got)
	}
	if got := s.pendingUser("secure", "bob").Upload; got != 15 {
		t.Fatalf("short-lived user traffic = %d", got)
	}
	if node, ports, users := reportUploadTotals(s.accountingSnapshot().Report); node != 160 || ports != 160 || users != 140 {
		t.Fatalf("node/ports/users = %d/%d/%d", node, ports, users)
	}
}

func TestTrafficGuardStreamFirstStartupKeepsBaseline(t *testing.T) {
	s := newTrafficSampler()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s.startedAt = start
	guards := map[string]bool{"front": true}
	session := guardSession(start, guards)
	created := start.Add(time.Second)
	s.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{
		guardStreamEvent(connectionEventNew, "i", "secure", "vless", "alice", 10, created),
		guardStreamEvent(connectionEventNew, "o", "front", "direct", "", 30, created),
	}}, created)
	s.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{
		{Type: connectionEventUpdate, ID: "i", UplinkDelta: 30, DownlinkDelta: 60},
		{Type: connectionEventUpdate, ID: "o", UplinkDelta: 40, DownlinkDelta: 80},
	}}, start.Add(2*time.Second))
	if snap := s.accountingSnapshot(); snap.Report != nil || !snap.Unavailable {
		t.Fatalf("pre-baseline bytes reported: %+v", snap)
	}
	ingestGuardSample(s, guards, start.Add(3*time.Second), 250,
		guardClashConnection("i", "secure", "vless", 100, created),
		guardClashConnection("o", "front", "direct", 150, created))
	s.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{
		{Type: connectionEventUpdate, ID: "i", UplinkDelta: 100, DownlinkDelta: 200},
		guardStreamEvent(connectionEventClosed, "o", "front", "direct", "", 200, created),
		guardStreamEvent(connectionEventClosed, "old", "secure", "vless", "alice", 500, created),
	}}, start.Add(4*time.Second))
	if node, ports, users := reportUploadTotals(s.accountingSnapshot().Report); node != 40 || ports != 40 || users != 40 {
		t.Fatalf("startup history charged = %d/%d/%d", node, ports, users)
	}
}

func TestTrafficGuardDisconnectReconnectRetainsReportAndDeduplicatesReplay(t *testing.T) {
	s := newTrafficSampler()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	guards := map[string]bool{"front": true}
	ingestGuardSample(s, guards, start, 1000)
	session := guardSession(start, guards)
	closed := guardStreamEvent(connectionEventClosed, "short", "secure", "vless", "alice", 70, start.Add(time.Second))
	s.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{closed}}, start.Add(2*time.Second))
	s.setStatsStreamAvailable(true)
	first := s.accountingSnapshot()
	if first.Unavailable || first.UploadTotal != 70 || first.Report == nil {
		t.Fatalf("connected snapshot = %+v", first)
	}
	s.setStatsStreamAvailable(false)
	if s.snapshot() != nil || s.summarySnapshot() != nil {
		t.Fatal("guard polling alone must not advertise complete live statistics")
	}
	retry := s.accountingSnapshot()
	if !retry.Unavailable || retry.Report != first.Report {
		t.Fatal("disconnect lost or mutated the immutable pending report")
	}
	session = guardSession(start.Add(3*time.Second), guards)
	s.applyStreamEvents(session, connectionEvents{Reset: true, Events: []connectionEvent{closed}}, start.Add(3*time.Second))
	s.acknowledgeReport(protocol.TrafficAck{Session: first.Report.Session, Sequence: first.Report.Sequence})
	closed.Connection.UplinkTotal = 75
	closed.Connection.DownlinkTotal = 150
	s.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{closed}}, start.Add(4*time.Second))
	s.setStatsStreamAvailable(true)
	if node, ports, users := reportUploadTotals(s.accountingSnapshot().Report); node != 5 || ports != 5 || users != 5 {
		t.Fatalf("replay/final CLOSED charged = %d/%d/%d", node, ports, users)
	}
}

func TestTrafficGuardDisableKeepsBoundaryAndOldConnectionClassification(t *testing.T) {
	s := newTrafficSampler()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	guards := map[string]bool{"front": true}
	ingestGuardSample(s, guards, start, 1000)
	session := guardSession(start, guards)
	created := start.Add(time.Second)
	s.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{
		guardStreamEvent(connectionEventNew, "i", "secure", "vless", "alice", 10, created),
		guardStreamEvent(connectionEventNew, "o", "front", "direct", "", 20, created),
	}}, created)
	unguarded := map[string]bool{}
	// A reset of the raw totals after disabling the guard must not change the
	// ledger boundary or retroactively classify an old outer as ordinary.
	ingestGuardSample(s, unguarded, start.Add(2*time.Second), 5,
		guardClashConnection("i", "secure", "vless", 15, created),
		guardClashConnection("o", "front", "direct", 30, created))
	s.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{
		guardStreamEvent(connectionEventClosed, "i", "secure", "vless", "alice", 25, created),
		guardStreamEvent(connectionEventClosed, "o", "front", "direct", "", 50, created),
		guardStreamEvent(connectionEventClosed, "unseen-old-o", "front", "direct", "", 70, created),
	}}, start.Add(3*time.Second))
	newSession := guardSession(start.Add(2*time.Second), unguarded)
	s.applyStreamEvents(newSession, connectionEvents{Events: []connectionEvent{
		guardStreamEvent(connectionEventClosed, "new-vless", "front", "vless", "bob", 7, start.Add(2*time.Second)),
		guardStreamEvent(connectionEventClosed, "new-direct", "front", "direct", "", 9, start.Add(2*time.Second)),
	}}, start.Add(4*time.Second))
	if !s.connectionAccounting || s.nodeCounted[0] != 41 {
		t.Fatalf("guard disable changed node boundary: mode %v totals %v", s.connectionAccounting, s.nodeCounted)
	}
	if node, ports, users := reportUploadTotals(s.accountingSnapshot().Report); node != 41 || ports != 41 || users != 32 {
		t.Fatalf("late tails or reused tags charged = %d/%d/%d", node, ports, users)
	}
}

func TestTrafficGuardActivationPreservesPortUserWindowAndPendingACK(t *testing.T) {
	s := newTrafficSampler()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	unguarded := map[string]bool{}
	ingestGuardSample(s, unguarded, start, 0)
	created := start.Add(time.Second)
	normalSession := guardSession(start, unguarded)
	s.applyStreamEvents(normalSession, connectionEvents{Events: []connectionEvent{
		guardStreamEvent(connectionEventNew, "i", "secure", "vless", "alice", 100, created),
		guardStreamEvent(connectionEventNew, "normal-direct", "front", "direct", "", 10, created),
	}}, created)
	ingestGuardSample(s, unguarded, start.Add(2*time.Second), 110,
		guardClashConnection("i", "secure", "vless", 100, created),
		guardClashConnection("normal-direct", "front", "direct", 10, created))
	first := s.accountingSnapshot().Report
	if node, ports, users := reportUploadTotals(first); node != 110 || ports != 110 || users != 100 {
		t.Fatalf("ordinary pending batch = %d/%d/%d", node, ports, users)
	}
	guards := map[string]bool{"front": true}
	session := guardSession(start.Add(3*time.Second), guards)
	s.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{
		guardStreamEvent(connectionEventNew, "i", "secure", "vless", "alice", 120, created),
	}}, start.Add(3*time.Second))
	if !s.haveSample || s.nodeHaveSample {
		t.Fatal("activation reset the user/port boundary instead of only the node boundary")
	}
	ingestGuardSample(s, guards, start.Add(4*time.Second), 200,
		guardClashConnection("i", "secure", "vless", 140, created),
		guardClashConnection("normal-direct", "front", "direct", 20, created))
	s.applyStreamEvents(session, connectionEvents{Events: []connectionEvent{
		guardStreamEvent(connectionEventClosed, "i", "secure", "vless", "alice", 160, created),
		guardStreamEvent(connectionEventClosed, "normal-direct", "front", "direct", "", 30, created),
	}}, start.Add(5*time.Second))
	if retry := s.accountingSnapshot().Report; retry != first {
		t.Fatal("node mode switch discarded or replaced the pending ACK batch")
	}
	s.acknowledgeReport(protocol.TrafficAck{Session: first.Session, Sequence: first.Sequence})
	if node, ports, users := reportUploadTotals(s.accountingSnapshot().Report); node != 30 || ports != 80 || users != 60 {
		t.Fatalf("switch interval must preserve port/user bytes: %d/%d/%d", node, ports, users)
	}
	if s.tombstones["normal-direct"].guardOuter {
		t.Fatal("already classified normal direct was reclassified by a reused guard tag")
	}
}

func TestTrafficGuardUninitializedLayoutDefersDirectClassification(t *testing.T) {
	for _, outer := range []bool{false, true} {
		s := newTrafficSampler()
		start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		guards := map[string]bool{"front": true}
		ingestGuardSample(s, guards, start, 0)
		created := start.Add(time.Second)
		unknown := newStreamSession(start)
		s.applyStreamEvents(unknown, connectionEvents{Events: []connectionEvent{
			guardStreamEvent(connectionEventNew, "direct", "front", "direct", "", 30, created),
		}}, created)
		if s.nodeCounted[0] != 0 || s.ledger["direct"].guardClassified {
			t.Fatal("uninitialized guard set was mistaken for an unguarded layout")
		}
		layout := map[string]bool{}
		if outer {
			layout = guards
		}
		s.applyStreamEvents(guardSession(start, layout), connectionEvents{Events: []connectionEvent{
			guardStreamEvent(connectionEventClosed, "direct", "front", "direct", "", 20, created),
		}}, start.Add(2*time.Second))
		want := uint64(30)
		if outer {
			want = 0
		}
		if node, ports, users := reportUploadTotals(s.accountingSnapshot().Report); node != want || ports != want || users != 0 {
			t.Fatalf("outer %v delayed classification charged = %d/%d/%d, want %d", outer, node, ports, users, want)
		}
	}
}

func TestStatsStreamCapturesGuardLayoutBeforeFirstFrame(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	stream, s := fakeStatsService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/grpc-web+proto")
		_, _ = w.Write(grpcWebFrame(encodeTestEvents(true,
			testEvent{typ: connectionEventNew, id: "outer", conn: &testConn{id: "outer", inbound: "front", inboundType: "direct", createdAt: start.Add(time.Second).UnixMilli(), up: 50}},
			testEvent{typ: connectionEventNew, id: "inner", conn: &testConn{id: "inner", inbound: "secure", user: "alice", createdAt: start.Add(time.Second).UnixMilli(), up: 20}},
		)))
		_, _ = w.Write(trailerFrame("0"))
	})
	baseRead := stream.readConfig
	stream.readConfig = func() ([]byte, error) {
		base, err := baseRead()
		if err != nil {
			return nil, err
		}
		var service, root map[string]json.RawMessage
		if err := json.Unmarshal(base, &service); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(trafficGuardConfig(t, true, 1), &root); err != nil {
			return nil, err
		}
		root["services"] = service["services"]
		return json.Marshal(root)
	}
	ingestGuardSample(s, map[string]bool{"front": true}, start, 0)
	if err := stream.subscribeOnce(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
	if s.statsStreamAvailable {
		t.Fatal("closed subscription still marked available")
	}
	if node, ports, users := reportUploadTotals(s.accountingSnapshot().Report); node != 20 || ports != 20 || users != 20 {
		t.Fatalf("first frame classified without config: %d/%d/%d", node, ports, users)
	}
}

func TestTrafficWithoutGuardKeepsGlobalCounterAndResetAccounting(t *testing.T) {
	s := newTrafficSampler()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	unguarded := map[string]bool{}
	ingestGuardSample(s, unguarded, start, 100)
	ingestGuardSample(s, unguarded, start.Add(time.Second), 200)
	ingestGuardSample(s, unguarded, start.Add(2*time.Second), 5)
	if s.connectionAccounting || s.snapshot() == nil {
		t.Fatal("ordinary nodes must keep global counters without a stats stream")
	}
	if node, ports, users := reportUploadTotals(s.accountingSnapshot().Report); node != 105 || ports != 0 || users != 0 {
		t.Fatalf("ordinary global counters changed: %d/%d/%d", node, ports, users)
	}
}
