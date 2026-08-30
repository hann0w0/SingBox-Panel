package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

const (
	writeWait            = 10 * time.Second
	pongWait             = 60 * time.Second
	pingPeriod           = (pongWait * 9) / 10
	stableConnectionTime = 30 * time.Second
	minReconnectBackoff  = time.Second
	maxReconnectBackoff  = 30 * time.Second
	maxAgentMessageSize  = 16 << 20
)

// Client is the agent's reverse WebSocket connection to the panel. It handles
// dialing, auth, keepalive (ws ping/pong), command dispatch, and reconnection.
type Client struct {
	wsURL    string
	insecure bool
	token    atomic.Value // string

	heartbeatInterval time.Duration

	// Callbacks wired by the Agent orchestrator.
	Register  func() protocol.RegisterEvt
	Heartbeat func() protocol.HeartbeatEvt
	OnCommand func(ctx context.Context, env protocol.Envelope) protocol.CommandResultEvt
	// OnConnected runs after registration has been written successfully. The
	// self-updater uses it as a local readiness signal for automatic rollback.
	OnConnected    func()
	OnDisconnected func()

	// send is reused across reconnects, but every item is tagged with the
	// WebSocket session that produced it. A new writer discards old-session
	// leftovers, so command results and telemetry can never leak into a later
	// connection. Non-blocking spontaneous events drop the newest sample when the
	// queue is full; command results use trySend and wait for space or cancellation.
	send          chan sessionEnvelope
	nextSession   atomic.Uint64
	activeSession atomic.Uint64
}

type sessionEnvelope struct {
	session uint64
	env     protocol.Envelope
}

type clientSessionContextKey struct{}

// NewClient builds a client. panelURL may be a base origin (https://panel) or a
// full ws URL; the /api/agent/ws path is appended when absent.
func NewClient(panelURL, token string, insecure bool) *Client {
	c := &Client{
		wsURL:             deriveWSURL(panelURL),
		insecure:          insecure,
		heartbeatInterval: 10 * time.Second,
		send:              make(chan sessionEnvelope, 256),
	}
	c.token.Store(token)
	return c
}

func (c *Client) Token() string {
	token, _ := c.token.Load().(string)
	return token
}

// ValidatePanelURL prevents an Agent from silently downgrading its control
// channel. HTTP/WS and certificate bypasses are development-only escape hatches.
func ValidatePanelURL(raw string, insecure bool, environment string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return errors.New("panel URL must be an absolute URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("panel URL must not contain credentials, query, or fragment")
	}
	if u.Path != "" && u.Path != "/" && u.Path != "/api/agent/ws" {
		return errors.New("panel URL path must be empty or /api/agent/ws")
	}
	dev := strings.EqualFold(strings.TrimSpace(environment), "development")
	switch u.Scheme {
	case "https", "wss":
	case "http", "ws":
		if !dev {
			return errors.New("panel URL must use HTTPS/WSS in production; set SINGBOX_PANEL_ENV=development only for local testing")
		}
	default:
		return errors.New("panel URL scheme must be https, wss, http, or ws")
	}
	if insecure && !dev {
		return errors.New("--insecure is allowed only when SINGBOX_PANEL_ENV=development")
	}
	return nil
}

// SendEvent pushes a spontaneous event (no correlation id) to the panel.
// Non-blocking: if the buffer is full the event is dropped.
func (c *Client) SendEvent(t protocol.MessageType, payload any) {
	env, err := protocol.NewEnvelope(t, "", payload)
	if err != nil {
		return
	}
	session := c.activeSession.Load()
	if session == 0 {
		return
	}
	select {
	case c.send <- sessionEnvelope{session: session, env: env}:
	default:
	}
}

// SendEventForContext pushes command progress for the session carried by ctx.
// If that session has disconnected, the event is discarded instead of being
// delivered to whichever connection happens to be active later.
func (c *Client) SendEventForContext(ctx context.Context, t protocol.MessageType, id string, payload any) {
	session, _ := ctx.Value(clientSessionContextKey{}).(uint64)
	if session == 0 || ctx.Err() != nil {
		return
	}
	c.SendEventForSession(session, t, id, payload)
}

// SendEventForSession is used by streams that intentionally outlive the short
// command context that created them. The explicit session id still ensures a
// reconnect immediately invalidates the stream's events.
func (c *Client) SendEventForSession(session uint64, t protocol.MessageType, id string, payload any) {
	env, err := protocol.NewEnvelope(t, id, payload)
	if err != nil {
		return
	}
	if session == 0 || c.activeSession.Load() != session {
		return
	}
	select {
	case c.send <- sessionEnvelope{session: session, env: env}:
	default:
	}
}

func deriveWSURL(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return base
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	if !strings.Contains(u.Path, "/api/agent/ws") {
		u.Path = strings.TrimRight(u.Path, "/") + "/api/agent/ws"
	}
	return u.String()
}

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

// preferredIPFamily enforces the Agent connection policy: use IPv4 whenever
// the panel domain has at least one A record. IPv6 is selected only when the
// domain has no IPv4 address at all.
func preferredIPFamily(addrs []netip.Addr) (string, []netip.Addr) {
	v4 := make([]netip.Addr, 0, len(addrs))
	v6 := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		addr = addr.Unmap()
		switch {
		case addr.Is4():
			v4 = append(v4, addr)
		case addr.Is6():
			v6 = append(v6, addr)
		}
	}
	if len(v4) > 0 {
		return "tcp4", v4
	}
	return "tcp6", v6
}

func dialResolvedFamily(ctx context.Context, host, port string, addrs []netip.Addr, dial dialContextFunc) (net.Conn, error) {
	network, preferred := preferredIPFamily(addrs)
	if len(preferred) == 0 {
		return nil, fmt.Errorf("no IP address found for %s", host)
	}
	errs := make([]error, 0, len(preferred))
	for _, addr := range preferred {
		conn, err := dial(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("dial %s for %s: %w", network, host, errors.Join(errs...))
}

func dialIPv4First(ctx context.Context, _ string, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("split dial address %q: %w", address, err)
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	if literal, err := netip.ParseAddr(host); err == nil {
		network := "tcp6"
		if literal.Unmap().Is4() {
			network = "tcp4"
		}
		return dialer.DialContext(ctx, network, address)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	return dialResolvedFamily(ctx, host, port, addrs, dialer.DialContext)
}

// Run connects and stays connected, reconnecting with exponential backoff until
// ctx is cancelled.
func (c *Client) Run(ctx context.Context) {
	backoff := minReconnectBackoff
	for ctx.Err() == nil {
		connectedFor, err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// A connection that stayed healthy should not inherit failures from
			// weeks ago. Without this reset, every eventual disconnect permanently
			// pushed a long-running Agent toward the 30-second maximum.
			if connectedFor >= stableConnectionTime {
				backoff = minReconnectBackoff
			}
			delay := jitterReconnectBackoff(backoff)
			log.Printf("agent: connection lost: %v; retry in %s", err, delay.Round(time.Millisecond))
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
			backoff = followingReconnectBackoff(backoff, connectedFor)
			continue
		}
		backoff = minReconnectBackoff
	}
}

func followingReconnectBackoff(current, connectedFor time.Duration) time.Duration {
	if connectedFor >= stableConnectionTime {
		return minReconnectBackoff
	}
	if current < minReconnectBackoff {
		current = minReconnectBackoff
	}
	if current >= maxReconnectBackoff/2 {
		return maxReconnectBackoff
	}
	return current * 2
}

func jitterReconnectBackoff(base time.Duration) time.Duration {
	if base <= 0 {
		base = minReconnectBackoff
	}
	delta := base / 5 // full jitter in the [80%, 120%] range
	if delta <= 0 {
		return base
	}
	return base - delta + time.Duration(rand.Int64N(int64(2*delta)+1))
}

func (c *Client) connectOnce(ctx context.Context) (time.Duration, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		NetDialContext:   dialIPv4First,
	}
	if c.insecure {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+c.Token())

	conn, resp, err := dialer.DialContext(ctx, c.wsURL, hdr)
	if err != nil {
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
			return 0, fmt.Errorf("dial %s: %w (HTTP %d)", c.wsURL, err, resp.StatusCode)
		}
		return 0, fmt.Errorf("dial %s: %w", c.wsURL, err)
	}
	defer conn.Close()
	if c.OnDisconnected != nil {
		defer c.OnDisconnected()
	}
	connectedAt := time.Now()
	log.Printf("agent: connected to %s", c.wsURL)

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session := c.nextSession.Add(1)
	connCtx = context.WithValue(connCtx, clientSessionContextKey{}, session)

	// Register synchronously before starting the sole asynchronous writer. A
	// successful WriteJSON is the readiness boundary used by self-update: the new
	// binary started, authenticated, and can speak the current wire protocol.
	if c.Register != nil {
		env, err := protocol.NewEnvelope(protocol.EvtRegister, "", c.Register())
		if err != nil {
			return time.Since(connectedAt), fmt.Errorf("build register event: %w", err)
		}
		_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
		if err := conn.WriteJSON(env); err != nil {
			return time.Since(connectedAt), fmt.Errorf("write register event: %w", err)
		}
	}
	c.activeSession.Store(session)
	defer c.activeSession.CompareAndSwap(session, 0)
	if c.OnConnected != nil {
		c.OnConnected()
	}

	go c.writeLoop(connCtx, conn, session)

	// Heartbeat loop.
	go c.periodic(connCtx, c.heartbeatInterval, func() {
		if c.Heartbeat == nil {
			return
		}
		if env, err := protocol.NewEnvelope(protocol.EvtHeartbeat, "", c.Heartbeat()); err == nil {
			c.trySend(connCtx, env)
		}
	})
	err = c.readLoop(connCtx, conn)
	return time.Since(connectedAt), err
}

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) error {
	conn.SetReadLimit(maxAgentMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))

		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		if c.OnCommand == nil {
			continue
		}
		// Commands belong to this WebSocket session. A disconnect cancels active
		// work, while the Agent handler adds a command-specific deadline so a
		// stuck package manager or systemctl cannot run for the Agent lifetime.
		go func(env protocol.Envelope) {
			res := c.OnCommand(ctx, env)
			if out, err := protocol.NewEnvelope(protocol.EvtCommandResult, env.ID, res); err == nil {
				c.trySend(ctx, out)
			}
		}(env)
	}
}

func (c *Client) writeLoop(ctx context.Context, conn *websocket.Conn, session uint64) {
	ping := time.NewTicker(pingPeriod)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case queued := <-c.send:
			if queued.session != session {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteJSON(queued.env); err != nil {
				_ = conn.Close() // unblock readLoop
				return
			}
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

func (c *Client) periodic(ctx context.Context, d time.Duration, fn func()) {
	if d <= 0 {
		d = 30 * time.Second
	}
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}

func (c *Client) trySend(ctx context.Context, env protocol.Envelope) {
	session, _ := ctx.Value(clientSessionContextKey{}).(uint64)
	if session == 0 || ctx.Err() != nil || c.activeSession.Load() != session {
		return
	}
	select {
	case c.send <- sessionEnvelope{session: session, env: env}:
	case <-ctx.Done():
	}
}
