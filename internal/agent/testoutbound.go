package agent

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/hann0w0/singbox-panel/internal/protocol"
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

// TestEgress measures how long this node takes to reach the public internet by
// fetching a small 204 endpoint. It goes out over the node's normal route (not
// through sing-box), so it reports the machine's own egress latency.
func TestEgress(ctx context.Context, rawURL string) protocol.TestEgressData {
	if rawURL == "" {
		rawURL = protocol.DefaultEgressURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return protocol.TestEgressData{Error: "探测地址无效"}
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return protocol.TestEgressData{Error: err.Error()}
	}
	// No redirect following: the probe should measure one round trip.
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return protocol.TestEgressData{LatencyMS: elapsed, Error: err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	return protocol.TestEgressData{
		OK:        resp.StatusCode < 400,
		LatencyMS: elapsed,
		Status:    resp.StatusCode,
	}
}
