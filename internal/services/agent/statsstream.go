package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// The sing-box 1.14+ API service streams connection events that carry the
// authenticated proxy user, which the Clash API does not expose. The Agent
// speaks gRPC-Web (plain HTTP/1.1, accepted by the service's web bridge) and
// decodes the few protobuf fields it needs, so no gRPC stack is required.

const (
	statsStreamMethod     = "/daemon.StartedService/SubscribeConnections"
	statsStreamMaxFrame   = 64 << 20
	statsStreamInterval   = time.Second
	statsStreamMaxBackoff = 30 * time.Second
	// statsStreamStableAfter resets the reconnect backoff once a stream has
	// stayed up this long.
	statsStreamStableAfter = 30 * time.Second
	// statsStreamIdleRetry is the delay when the node has no API service
	// (older sing-box or an unmanaged config).
	statsStreamIdleRetry = 15 * time.Second
)

// errStatsStreamUnavailable means the node has no usable API service.
var errStatsStreamUnavailable = errors.New("sing-box api service unavailable")

type statsStreamConfig struct {
	URL    string
	Secret string
}

// parseStatsStreamConfig finds a loopback API service with a secret in a
// sing-box config. The panel's own service (tag "panel-stats") wins.
func parseStatsStreamConfig(raw []byte) (statsStreamConfig, error) {
	var root struct {
		Services []struct {
			Type       string `json:"type"`
			Tag        string `json:"tag"`
			Listen     string `json:"listen"`
			ListenPort int    `json:"listen_port"`
			Secret     string `json:"secret"`
			TLS        *struct {
				Enabled bool `json:"enabled"`
			} `json:"tls"`
		} `json:"services"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return statsStreamConfig{}, err
	}
	var found *statsStreamConfig
	for _, service := range root.Services {
		if service.Type != "api" || service.Secret == "" || service.ListenPort <= 0 || service.ListenPort > 65535 {
			continue
		}
		if service.TLS != nil && service.TLS.Enabled {
			continue
		}
		host := strings.Trim(strings.TrimSpace(service.Listen), "[]")
		switch host {
		case "localhost":
			host = "127.0.0.1"
		default:
			addr, err := netip.ParseAddr(host)
			if err != nil || !addr.IsLoopback() {
				continue
			}
		}
		cfg := statsStreamConfig{
			URL: (&url.URL{
				Scheme: "http",
				Host:   net.JoinHostPort(host, strconv.Itoa(service.ListenPort)),
				Path:   statsStreamMethod,
			}).String(),
			Secret: service.Secret,
		}
		if service.Tag == "panel-stats" {
			return cfg, nil
		}
		if found == nil {
			found = &cfg
		}
	}
	if found == nil {
		return statsStreamConfig{}, errStatsStreamUnavailable
	}
	return *found, nil
}

// connectionEvent is the decoded subset of daemon.ConnectionEvent.
type connectionEvent struct {
	Type          int // 0 new, 1 update, 2 closed
	ID            string
	Connection    *streamConnection
	UplinkDelta   int64
	DownlinkDelta int64
	ClosedAt      int64
}

// streamConnection is the decoded subset of daemon.Connection.
type streamConnection struct {
	ID            string
	Inbound       string
	User          string
	CreatedAt     int64 // unix milliseconds
	ClosedAt      int64 // unix milliseconds, 0 while open
	UplinkTotal   int64
	DownlinkTotal int64
}

const (
	connectionEventNew    = 0
	connectionEventUpdate = 1
	connectionEventClosed = 2
)

// connectionEvents is daemon.ConnectionEvents.
type connectionEvents struct {
	Events []connectionEvent
	Reset  bool
}

func decodeConnectionEvents(b []byte) (connectionEvents, error) {
	var out connectionEvents
	err := walkProto(b, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
		switch {
		case num == 1 && typ == protowire.BytesType:
			event, err := decodeConnectionEvent(value)
			if err != nil {
				return err
			}
			out.Events = append(out.Events, event)
		case num == 2 && typ == protowire.VarintType:
			out.Reset = varint != 0
		}
		return nil
	})
	return out, err
}

func decodeConnectionEvent(b []byte) (connectionEvent, error) {
	var out connectionEvent
	err := walkProto(b, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
		switch {
		case num == 1 && typ == protowire.VarintType:
			out.Type = int(varint)
		case num == 2 && typ == protowire.BytesType:
			out.ID = string(value)
		case num == 3 && typ == protowire.BytesType:
			connection, err := decodeStreamConnection(value)
			if err != nil {
				return err
			}
			out.Connection = &connection
		case num == 4 && typ == protowire.VarintType:
			out.UplinkDelta = int64(varint)
		case num == 5 && typ == protowire.VarintType:
			out.DownlinkDelta = int64(varint)
		case num == 6 && typ == protowire.VarintType:
			out.ClosedAt = int64(varint)
		}
		return nil
	})
	return out, err
}

func decodeStreamConnection(b []byte) (streamConnection, error) {
	var out streamConnection
	err := walkProto(b, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
		switch {
		case num == 1 && typ == protowire.BytesType:
			out.ID = string(value)
		case num == 2 && typ == protowire.BytesType:
			out.Inbound = string(value)
		case num == 10 && typ == protowire.BytesType:
			out.User = string(value)
		case num == 12 && typ == protowire.VarintType:
			out.CreatedAt = int64(varint)
		case num == 13 && typ == protowire.VarintType:
			out.ClosedAt = int64(varint)
		case num == 16 && typ == protowire.VarintType:
			out.UplinkTotal = int64(varint)
		case num == 17 && typ == protowire.VarintType:
			out.DownlinkTotal = int64(varint)
		}
		return nil
	})
	return out, err
}

// walkProto visits every field of a protobuf message, skipping unknown ones.
func walkProto(b []byte, visit func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		var value []byte
		var varint uint64
		switch typ {
		case protowire.VarintType:
			varint, n = protowire.ConsumeVarint(b)
		case protowire.BytesType:
			value, n = protowire.ConsumeBytes(b)
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if typ == protowire.VarintType || typ == protowire.BytesType {
			if err := visit(num, typ, value, varint); err != nil {
				return err
			}
		}
	}
	return nil
}

// grpcWebFrame wraps one uncompressed protobuf message.
func grpcWebFrame(message []byte) []byte {
	frame := make([]byte, 5+len(message))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(message)))
	copy(frame[5:], message)
	return frame
}

// readGRPCWebFrame returns the next frame. trailer reports the final frame.
func readGRPCWebFrame(r io.Reader) (payload []byte, trailer bool, err error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, false, err
	}
	flags := header[0]
	if flags&0x01 != 0 {
		return nil, false, fmt.Errorf("compressed grpc-web frame not supported")
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > statsStreamMaxFrame {
		return nil, false, fmt.Errorf("grpc-web frame of %d bytes exceeds limit", length)
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, false, err
	}
	return payload, flags&0x80 != 0, nil
}

// grpcStatusError converts a grpc-status/grpc-message pair into an error.
func grpcStatusError(status, message string) error {
	status = strings.TrimSpace(status)
	if status == "" || status == "0" {
		return nil
	}
	if status == "12" || status == "16" {
		// UNIMPLEMENTED / UNAUTHENTICATED: wrong build or secret.
		return fmt.Errorf("%w: grpc status %s %s", errStatsStreamUnavailable, status, message)
	}
	return fmt.Errorf("grpc status %s: %s", status, message)
}

func trailerStatus(payload []byte) error {
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(payload, '\r', '\n'))))
	header, err := reader.ReadMIMEHeader()
	if err != nil && len(header) == 0 {
		return fmt.Errorf("invalid grpc-web trailer: %w", err)
	}
	return grpcStatusError(header.Get("Grpc-Status"), header.Get("Grpc-Message"))
}

// statsStream owns one Agent-lifetime subscription loop.
type statsStream struct {
	sampler    *trafficSampler
	client     *http.Client
	readConfig func() ([]byte, error)
	now        func() time.Time
}

func newStatsStream(sampler *trafficSampler) *statsStream {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		ResponseHeaderTimeout: 10 * time.Second,
		DisableCompression:    true,
	}
	return &statsStream{
		sampler:    sampler,
		client:     &http.Client{Transport: transport},
		readConfig: readConfigFile,
		now:        time.Now,
	}
}

// run keeps a subscription open for the Agent's lifetime. Config pushes
// restart sing-box, which ends the stream; the loop re-reads the config and
// resubscribes.
func (st *statsStream) run(ctx context.Context) {
	backoff := time.Second
	for {
		started := st.now()
		err := st.subscribeOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		delay := backoff
		if errors.Is(err, errStatsStreamUnavailable) {
			delay = statsStreamIdleRetry
		} else if st.now().Sub(started) >= statsStreamStableAfter {
			backoff = time.Second
			delay = backoff
		} else {
			backoff = min(backoff*2, statsStreamMaxBackoff)
		}
		jitter := time.Duration(rand.Int64N(int64(delay)/5 + 1))
		timer := time.NewTimer(delay + jitter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (st *statsStream) subscribeOnce(ctx context.Context) error {
	raw, err := st.readConfig()
	if err != nil {
		return fmt.Errorf("%w: %v", errStatsStreamUnavailable, err)
	}
	cfg, err := parseStatsStreamConfig(raw)
	if err != nil {
		return err
	}
	request := protowire.AppendTag(nil, 1, protowire.VarintType)
	request = protowire.AppendVarint(request, uint64(statsStreamInterval))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(grpcWebFrame(request)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("Accept", "application/grpc-web+proto")
	req.Header.Set("X-Grpc-Web", "1")
	req.Header.Set("Authorization", "Bearer "+cfg.Secret)
	subscribedAt := st.now()
	res, err := st.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return errStatsStreamUnavailable
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("api service returned HTTP %d", res.StatusCode)
	}
	// A "trailers-only" error response carries its status in the headers.
	if err := grpcStatusError(res.Header.Get("Grpc-Status"), res.Header.Get("Grpc-Message")); err != nil {
		return err
	}
	body := bufio.NewReaderSize(res.Body, 64<<10)
	session := newStreamSession(subscribedAt)
	for {
		payload, trailer, err := readGRPCWebFrame(body)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		if trailer {
			if err := trailerStatus(payload); err != nil {
				return err
			}
			return io.EOF
		}
		events, err := decodeConnectionEvents(payload)
		if err != nil {
			return fmt.Errorf("decode connection events: %w", err)
		}
		st.sampler.applyStreamEvents(session, events, st.now())
	}
}

// streamSession is the per-subscription view needed to turn UPDATE deltas
// into cumulative totals for the shared ledger.
type streamSession struct {
	subscribedAt time.Time
	totals       map[string][2]uint64
}

func newStreamSession(subscribedAt time.Time) *streamSession {
	return &streamSession{subscribedAt: subscribedAt, totals: make(map[string][2]uint64)}
}

func streamTotals(connection *streamConnection) [2]uint64 {
	return [2]uint64{nonNegative(connection.UplinkTotal), nonNegative(connection.DownlinkTotal)}
}

func nonNegative(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// applyStreamEvents folds one ConnectionEvents message into the ledger.
func (s *trafficSampler) applyStreamEvents(session *streamSession, events connectionEvents, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var active map[string]bool
	if events.Reset {
		active = make(map[string]bool, len(events.Events))
		clear(session.totals)
	}
	for _, event := range events.Events {
		id := event.ID
		if id == "" && event.Connection != nil {
			id = event.Connection.ID
		}
		if id == "" {
			continue
		}
		switch event.Type {
		case connectionEventNew:
			connection := event.Connection
			if connection == nil {
				continue
			}
			totals := streamTotals(connection)
			baseline := time.UnixMilli(connection.CreatedAt).Before(s.startedAt)
			s.observeConnection(id, connection.Inbound, connection.User, totals, baseline, now)
			if connection.ClosedAt != 0 {
				// A recently closed connection replayed by a new subscription.
				s.closeConnection(id)
				continue
			}
			session.totals[id] = totals
			if active != nil {
				active[id] = true
			}
		case connectionEventUpdate:
			totals, known := session.totals[id]
			if !known {
				continue // its NEW snapshot was missed; Clash still observes it
			}
			totals[0] += nonNegative(event.UplinkDelta)
			totals[1] += nonNegative(event.DownlinkDelta)
			session.totals[id] = totals
			s.observeConnection(id, "", "", totals, false, now)
		case connectionEventClosed:
			if event.Connection != nil {
				s.observeConnection(id, event.Connection.Inbound, event.Connection.User,
					streamTotals(event.Connection), false, now)
			} else if totals, known := session.totals[id]; known {
				s.observeConnection(id, "", "", totals, false, now)
			}
			delete(session.totals, id)
			s.closeConnection(id)
		}
	}
	if active != nil {
		s.closeMissing(active, session.subscribedAt)
	}
}
