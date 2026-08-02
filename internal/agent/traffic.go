package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hann0w0/singbox-panel/internal/protocol"
)

const maxTrafficResponseSize = 4 << 20

type trafficSampler struct {
	mu sync.Mutex

	endpoint     string
	lastUpload   uint64
	lastDownload uint64
	lastSample   time.Time
	haveSample   bool

	connections     map[string]trafficConnection
	haveConnections bool
	pendingPorts    map[string]protocol.PortTrafficSnapshot
	available       bool
	uploadRate      uint64
	downloadRate    uint64
	tcpConnections  int
	udpConnections  int
	client          *http.Client
}

type trafficConnection struct {
	inbound  string
	upload   uint64
	download uint64
}

type localTrafficConfig struct {
	Endpoint string
	Secret   string
}

type clashTrafficResponse struct {
	UploadTotal   uint64            `json:"uploadTotal"`
	DownloadTotal uint64            `json:"downloadTotal"`
	Connections   []clashConnection `json:"connections"`
}

type clashConnection struct {
	ID       string `json:"id"`
	Upload   uint64 `json:"upload"`
	Download uint64 `json:"download"`
	Metadata struct {
		Network string `json:"network"`
		Type    string `json:"type"`
	} `json:"metadata"`
}

// hostConnectionCounts follows the same host-level perspective used by most
// VPS probes. Proxy connection counts from Clash API intentionally exclude
// SSH, panel, DNS and other sockets owned by the host.
func hostConnectionCounts() (tcp, udp int) {
	tcp = countProcSockets("/proc/net/tcp", true) + countProcSockets("/proc/net/tcp6", true)
	udp = countProcSockets("/proc/net/udp", false) + countProcSockets("/proc/net/udp6", false)
	return tcp, udp
}

func countProcSockets(path string, excludeListen bool) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	count := 0
	for lineIndex, line := range strings.Split(string(data), "\n") {
		if lineIndex == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if excludeListen && fields[3] == "0A" {
			continue
		}
		if _, err := strconv.ParseUint(fields[0], 10, 32); err == nil {
			count++
		}
	}
	return count
}

func newTrafficSampler() *trafficSampler {
	return &trafficSampler{
		connections:  make(map[string]trafficConnection),
		pendingPorts: make(map[string]protocol.PortTrafficSnapshot),
		client:       &http.Client{Timeout: 3 * time.Second},
	}
}

// discoverLocalTrafficConfig reads only the loopback Clash API address and
// secret from config.json. Raw-mode users keep control of their config; stats
// are available there only when they already enabled a local API.
func discoverLocalTrafficConfig() (localTrafficConfig, error) {
	raw, err := os.ReadFile(ConfigFile)
	if err != nil {
		return localTrafficConfig{}, err
	}
	var root struct {
		Experimental struct {
			ClashAPI struct {
				ExternalController string `json:"external_controller"`
				Secret             string `json:"secret"`
			} `json:"clash_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return localTrafficConfig{}, err
	}
	address := strings.TrimSpace(root.Experimental.ClashAPI.ExternalController)
	if address == "" {
		return localTrafficConfig{}, fmt.Errorf("clash api disabled")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return localTrafficConfig{}, fmt.Errorf("invalid clash api address: %w", err)
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "", "0.0.0.0", "localhost":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	default:
		addr, err := netip.ParseAddr(host)
		if err != nil || !addr.IsLoopback() {
			return localTrafficConfig{}, fmt.Errorf("clash api is not loopback-only")
		}
	}
	return localTrafficConfig{
		Endpoint: (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/connections"}).String(),
		Secret:   root.Experimental.ClashAPI.Secret,
	}, nil
}

func parseInboundTag(connectionType string) string {
	_, tag, ok := strings.Cut(connectionType, "/")
	if !ok {
		return ""
	}
	return strings.TrimSpace(tag)
}

func counterDelta(current, previous uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	return current
}

// run samples locally at a short interval. The panel heartbeat remains slow,
// but short-lived connections are observed here instead of only at heartbeat
// time.
func (s *trafficSampler) run(ctx context.Context) {
	s.poll(ctx)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.poll(ctx)
		}
	}
}

func (s *trafficSampler) poll(ctx context.Context) {
	cfg, err := discoverLocalTrafficConfig()
	if err != nil {
		s.mu.Lock()
		s.available = false
		s.mu.Unlock()
		return
	}
	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, cfg.Endpoint, nil)
	if err != nil {
		s.mu.Lock()
		s.available = false
		s.mu.Unlock()
		return
	}
	if cfg.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Secret)
	}
	res, err := s.client.Do(req)
	if err != nil {
		s.mu.Lock()
		s.available = false
		s.mu.Unlock()
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
		s.mu.Lock()
		s.available = false
		s.mu.Unlock()
		return
	}
	var counters clashTrafficResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, maxTrafficResponseSize)).Decode(&counters); err != nil {
		s.mu.Lock()
		s.available = false
		s.mu.Unlock()
		return
	}

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endpoint != "" && s.endpoint != cfg.Endpoint {
		s.connections = make(map[string]trafficConnection)
		s.pendingPorts = make(map[string]protocol.PortTrafficSnapshot)
		s.haveConnections = false
		s.haveSample = false
	}

	uploadRate := uint64(0)
	downloadRate := uint64(0)
	sampleSeconds := 1.0
	if s.haveSample && s.endpoint == cfg.Endpoint && now.After(s.lastSample) {
		sampleSeconds = now.Sub(s.lastSample).Seconds()
		if sampleSeconds > 0 {
			uploadRate = uint64(float64(counterDelta(counters.UploadTotal, s.lastUpload)) / sampleSeconds)
			downloadRate = uint64(float64(counterDelta(counters.DownloadTotal, s.lastDownload)) / sampleSeconds)
		}
	}
	if sampleSeconds <= 0 {
		sampleSeconds = 1
	}

	// The Clash API exposes per-connection counters and inbound tag in
	// metadata.type, for example "vless/vless-in". Sampling deltas provides
	// useful per-port history without requiring the optional V2Ray API build tag.
	nextConnections := make(map[string]trafficConnection, len(counters.Connections))
	for _, connection := range counters.Connections {
		if connection.ID == "" {
			continue
		}
		inbound := parseInboundTag(connection.Metadata.Type)
		current := trafficConnection{inbound: inbound, upload: connection.Upload, download: connection.Download}
		if s.haveConnections {
			if previous, ok := s.connections[connection.ID]; ok && previous.inbound == inbound && inbound != "" {
				delta := s.pendingPorts[inbound]
				delta.Inbound = inbound
				uploadDelta := counterDelta(connection.Upload, previous.upload)
				downloadDelta := counterDelta(connection.Download, previous.download)
				delta.Upload += uploadDelta
				delta.Download += downloadDelta
				uploadRate := uint64(float64(uploadDelta) / sampleSeconds)
				downloadRate := uint64(float64(downloadDelta) / sampleSeconds)
				if uploadRate > delta.UploadRate {
					delta.UploadRate = uploadRate
				}
				if downloadRate > delta.DownloadRate {
					delta.DownloadRate = downloadRate
				}
				s.pendingPorts[inbound] = delta
			}
		}
		nextConnections[connection.ID] = current
	}
	s.connections = nextConnections
	s.haveConnections = true
	s.endpoint = cfg.Endpoint
	s.lastUpload = counters.UploadTotal
	s.lastDownload = counters.DownloadTotal
	s.lastSample = now
	s.haveSample = true
	s.available = true
	s.uploadRate = uploadRate
	s.downloadRate = downloadRate
	s.tcpConnections, s.udpConnections = hostConnectionCounts()

}

func (s *trafficSampler) snapshot() *protocol.TrafficSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.available || !s.haveSample {
		return nil
	}
	snapshot := &protocol.TrafficSnapshot{
		UploadTotal:    s.lastUpload,
		DownloadTotal:  s.lastDownload,
		UploadRate:     s.uploadRate,
		DownloadRate:   s.downloadRate,
		TCPConnections: s.tcpConnections,
		UDPConnections: s.udpConnections,
		SampledAt:      s.lastSample.Unix(),
	}
	for _, delta := range s.pendingPorts {
		snapshot.Ports = append(snapshot.Ports, delta)
	}
	sort.Slice(snapshot.Ports, func(i, j int) bool { return snapshot.Ports[i].Inbound < snapshot.Ports[j].Inbound })
	s.pendingPorts = make(map[string]protocol.PortTrafficSnapshot)
	return snapshot
}
