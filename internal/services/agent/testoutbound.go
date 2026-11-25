package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

// allowLoopbackTargetForTest lets unit tests spin up a local TCP/UDP listener
// on 127.0.0.1 while production probes refuse loopback and unspecified IPs.
var allowLoopbackTargetForTest bool

const (
	testOutboundTimeout = 8 * time.Second
	// quicProbeAttempts spreads retransmissions over the timeout: UDP has no
	// handshake, so a single lost datagram must not read as "down".
	quicProbeAttempts = 3
	// RFC 9000 §14.1: servers only answer an unknown-version Initial that is at
	// least 1200 bytes, which also keeps the probe from being an amplifier.
	quicProbeSize = 1200
	// 0x?a?a?a?a versions are reserved (RFC 9000 §15) and never supported, so
	// every compliant server must reply with Version Negotiation.
	quicProbeVersion uint32 = 0x1a2a3a4a
)

// TestOutbound checks whether a landing server is reachable from this node by
// opening a TCP connection to it. A plain ICMP ping is deliberately avoided:
// many providers drop ICMP while the proxy port is perfectly reachable, and a
// TCP connect also proves the port itself is open.
//
// QUIC landings (hysteria2 / tuic) only listen on UDP; for them the agent sends
// a QUIC version-negotiation probe, which every QUIC server answers without
// credentials. Obfuscated QUIC answers nothing, so only DNS can be verified.
func TestOutbound(ctx context.Context, cmd protocol.TestOutboundCmd) protocol.TestOutboundData {
	host, port := strings.TrimSpace(cmd.Host), cmd.Port
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if host == "" || port <= 0 || port > 65535 {
		return protocol.TestOutboundData{Error: "落地地址或端口无效"}
	}
	if !allowLoopbackTargetForTest && isDisallowedProbeHost(host) {
		return protocol.TestOutboundData{Error: "落地地址不能指向本机回环或未指定地址"}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dialCtx, cancel := context.WithTimeout(ctx, testOutboundTimeout)
	defer cancel()

	switch cmd.Network {
	case "quic":
		if cmd.Obfuscated {
			return resolveOnly(dialCtx, host)
		}
		return probeQUIC(dialCtx, addr)
	default:
		return probeTCP(dialCtx, addr)
	}
}

func probeDialer() net.Dialer {
	if allowLoopbackTargetForTest {
		return net.Dialer{}
	}
	return net.Dialer{
		Control: func(network, address string, c syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if isDisallowedProbeHost(host) {
				return errors.New("落地地址解析到本机回环或未指定地址，已拒绝探测")
			}
			return nil
		},
	}
}

var disallowedProbePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.100.100.200/32"), // Alibaba Cloud metadata
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("fd00:ec2::254/128"), // AWS EC2 IPv6 IMDS
}

func isDisallowedProbeHost(raw string) bool {
	host := strings.TrimSuffix(strings.ToLower(strings.Trim(strings.TrimSpace(raw), "[]")), ".")
	if zoneAt := strings.LastIndexByte(host, '%'); zoneAt >= 0 {
		host = host[:zoneAt]
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if addr.IsLoopback() || addr.IsUnspecified() || addr.IsLinkLocalUnicast() ||
			addr.IsLinkLocalMulticast() || addr.IsMulticast() {
			return true
		}
		for _, prefix := range disallowedProbePrefixes {
			if prefix.Contains(addr) {
				return true
			}
		}
	}
	return false
}

func probeTCP(ctx context.Context, addr string) protocol.TestOutboundData {
	d := probeDialer()
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return protocol.TestOutboundData{LatencyMS: elapsed, Error: err.Error()}
	}
	_ = conn.Close()
	return protocol.TestOutboundData{OK: true, LatencyMS: elapsed}
}

func resolveOnly(ctx context.Context, host string) protocol.TestOutboundData {
	start := time.Now()
	if net.ParseIP(host) == nil {
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return protocol.TestOutboundData{LatencyMS: time.Since(start).Milliseconds(), Error: err.Error()}
		}
		if !allowLoopbackTargetForTest {
			for _, a := range addrs {
				if isDisallowedProbeHost(a) {
					return protocol.TestOutboundData{
						LatencyMS: time.Since(start).Milliseconds(),
						Error:     "落地地址解析到本机回环或未指定地址，已拒绝探测",
					}
				}
			}
		}
	}
	return protocol.TestOutboundData{
		Inconclusive: true,
		Note:         "已启用混淆的 QUIC 落地不会响应未握手的探测包，仅验证了地址解析，无法确认 UDP 端口是否可达",
	}
}

// probeQUIC sends a padded long-header packet carrying a reserved version and
// waits for the server's Version Negotiation reply echoing our connection IDs.
func probeQUIC(ctx context.Context, addr string) protocol.TestOutboundData {
	d := probeDialer()
	start := time.Now()
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return protocol.TestOutboundData{LatencyMS: time.Since(start).Milliseconds(), Error: err.Error()}
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	packet, dcid, scid, err := buildQUICProbe()
	if err != nil {
		return protocol.TestOutboundData{Error: err.Error()}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(testOutboundTimeout)
	}
	slice := time.Until(deadline) / quicProbeAttempts
	buf := make([]byte, 2048)
	for attempt := 0; attempt < quicProbeAttempts; attempt++ {
		sent := time.Now()
		if _, err := conn.Write(packet); err != nil {
			return protocol.TestOutboundData{LatencyMS: time.Since(start).Milliseconds(), Error: err.Error()}
		}
		attemptEnd := sent.Add(slice)
		if attempt == quicProbeAttempts-1 || attemptEnd.After(deadline) {
			attemptEnd = deadline
		}
		_ = conn.SetReadDeadline(attemptEnd)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				if errors.Is(err, syscall.ECONNREFUSED) {
					return protocol.TestOutboundData{
						LatencyMS: time.Since(sent).Milliseconds(),
						Error:     "UDP 端口未开放（对端返回 ICMP 端口不可达）",
					}
				}
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() && ctx.Err() == nil {
					break // retransmit
				}
				if ctx.Err() != nil {
					break
				}
				return protocol.TestOutboundData{LatencyMS: time.Since(start).Milliseconds(), Error: err.Error()}
			}
			if isQUICVersionNegotiation(buf[:n], dcid, scid) {
				return protocol.TestOutboundData{OK: true, LatencyMS: time.Since(sent).Milliseconds()}
			}
			// Unrelated datagram on the connected socket: keep waiting.
		}
		if ctx.Err() != nil {
			break
		}
	}
	return protocol.TestOutboundData{
		LatencyMS: time.Since(start).Milliseconds(),
		Error:     "未收到 QUIC 响应：UDP 端口可能未开放或被防火墙丢弃",
	}
}

func buildQUICProbe() (packet, dcid, scid []byte, err error) {
	packet = make([]byte, quicProbeSize)
	if _, err = rand.Read(packet); err != nil {
		return nil, nil, nil, err
	}
	// Long header, fixed bit set; the remaining bits are version-specific and
	// left random.
	packet[0] = 0xc0 | (packet[0] & 0x3f)
	binary.BigEndian.PutUint32(packet[1:5], quicProbeVersion)
	packet[5] = 8
	dcid = append([]byte(nil), packet[6:14]...)
	packet[14] = 8
	scid = append([]byte(nil), packet[15:23]...)
	return packet, dcid, scid, nil
}

// isQUICVersionNegotiation reports whether p is a Version Negotiation packet
// answering our probe: version 0, and the connection IDs swapped back to us
// (RFC 9000 §17.2.1).
func isQUICVersionNegotiation(p, dcid, scid []byte) bool {
	if len(p) < 7 || p[0]&0x80 == 0 || binary.BigEndian.Uint32(p[1:5]) != 0 {
		return false
	}
	off := 5
	dl := int(p[off])
	off++
	if len(p) < off+dl+1 || !bytes.Equal(p[off:off+dl], scid) {
		return false
	}
	off += dl
	sl := int(p[off])
	off++
	if len(p) < off+sl || !bytes.Equal(p[off:off+sl], dcid) {
		return false
	}
	return true
}
