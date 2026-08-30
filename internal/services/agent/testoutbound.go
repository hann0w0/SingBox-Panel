package agent

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

// TestOutbound checks whether a landing server is reachable from this node by
// opening a TCP connection to it. A plain ICMP ping is deliberately avoided:
// many providers drop ICMP while the proxy port is perfectly reachable, and a
// TCP connect also proves the port itself is open.
func TestOutbound(ctx context.Context, host string, port int) protocol.TestOutboundData {
	if host == "" || port <= 0 || port > 65535 {
		return protocol.TestOutboundData{Error: "落地地址或端口无效"}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var d net.Dialer
	start := time.Now()
	conn, err := d.DialContext(dialCtx, "tcp", addr)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return protocol.TestOutboundData{LatencyMS: elapsed, Error: err.Error()}
	}
	_ = conn.Close()
	return protocol.TestOutboundData{OK: true, LatencyMS: elapsed}
}
