package agent

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func withLoopbackAllowed(t *testing.T) {
	t.Helper()
	prev := allowLoopbackTargetForTest
	allowLoopbackTargetForTest = true
	t.Cleanup(func() { allowLoopbackTargetForTest = prev })
}

// fakeQUICServer answers like any RFC 9000 server does to an unknown version:
// a Version Negotiation packet with the connection IDs swapped.
func fakeQUICServer(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			p := buf[:n]
			if n < quicProbeSize || p[0]&0x80 == 0 {
				continue // servers drop undersized unknown-version packets
			}
			dl := int(p[5])
			dcid := p[6 : 6+dl]
			sl := int(p[6+dl])
			scid := p[7+dl : 7+dl+sl]
			resp := []byte{0x80}
			resp = binary.BigEndian.AppendUint32(resp, 0)
			resp = append(resp, byte(len(scid)))
			resp = append(resp, scid...)
			resp = append(resp, byte(len(dcid)))
			resp = append(resp, dcid...)
			resp = binary.BigEndian.AppendUint32(resp, 0x00000001)
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func TestTestOutboundRejectsLoopbackAndUnspecifiedTargetsByDefault(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "foo.localhost", "::1", "[::1]", "0.0.0.0", "::", "100.100.100.200", "fd00:ec2::254", "[fd00:ec2::254]"} {
		res := TestOutbound(context.Background(), protocol.TestOutboundCmd{Host: host, Port: 443})
		if res.OK || !strings.Contains(res.Error, "回环") {
			t.Fatalf("host %q not rejected: %+v", host, res)
		}
	}
}

func TestTestOutboundStripsIPv6BracketsBeforeDial(t *testing.T) {
	withLoopbackAllowed(t)
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	res := TestOutbound(context.Background(), protocol.TestOutboundCmd{Host: "[::1]", Port: port})
	if !res.OK {
		t.Fatalf("bracketed IPv6 probe failed: %+v", res)
	}
}

func TestTestOutboundQUICProbeSucceedsAgainstQUICServer(t *testing.T) {
	withLoopbackAllowed(t)
	port := fakeQUICServer(t)
	res := TestOutbound(context.Background(), protocol.TestOutboundCmd{Host: "127.0.0.1", Port: port, Network: "quic"})
	if !res.OK || res.Inconclusive {
		t.Fatalf("result = %+v", res)
	}
}

func TestTestOutboundQUICProbeReportsClosedPort(t *testing.T) {
	withLoopbackAllowed(t)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close()
	res := TestOutbound(context.Background(), protocol.TestOutboundCmd{Host: "127.0.0.1", Port: port, Network: "quic"})
	if res.OK || res.Error == "" {
		t.Fatalf("closed UDP port reported reachable: %+v", res)
	}
}

func TestTestOutboundObfuscatedQUICIsInconclusive(t *testing.T) {
	withLoopbackAllowed(t)
	res := TestOutbound(context.Background(), protocol.TestOutboundCmd{Host: "127.0.0.1", Port: 443, Network: "quic", Obfuscated: true})
	if res.OK || !res.Inconclusive || !strings.Contains(res.Note, "混淆") {
		t.Fatalf("result = %+v", res)
	}
}

func TestTestOutboundTCPStillDialsTCP(t *testing.T) {
	withLoopbackAllowed(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	res := TestOutbound(context.Background(), protocol.TestOutboundCmd{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port})
	if !res.OK {
		t.Fatalf("result = %+v", res)
	}
}

func TestIsQUICVersionNegotiationRequiresEchoedConnectionIDs(t *testing.T) {
	_, dcid, scid, err := buildQUICProbe()
	if err != nil {
		t.Fatal(err)
	}
	good := []byte{0x80, 0, 0, 0, 0, byte(len(scid))}
	good = append(good, scid...)
	good = append(good, byte(len(dcid)))
	good = append(good, dcid...)
	if !isQUICVersionNegotiation(good, dcid, scid) {
		t.Fatal("valid version negotiation rejected")
	}
	if isQUICVersionNegotiation(good, scid, dcid) {
		t.Fatal("packet with unswapped connection IDs accepted")
	}
	if isQUICVersionNegotiation(good[:8], dcid, scid) {
		t.Fatal("truncated packet accepted")
	}
}
