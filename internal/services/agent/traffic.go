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

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

// maxTrafficResponseSize bounds one /connections response. Busy nodes with
// thousands of open connections can exceed a few MiB of JSON.
const maxTrafficResponseSize = 16 << 20

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
	peakUpload      uint64
	peakDownload    uint64
	tcpConnections  int
	udpConnections  int
	client          *http.Client
	configCached    bool
	configFileInfo  os.FileInfo
	configModTime   time.Time
	configSize      int64
	configCheckedAt time.Time
	configValue     localTrafficConfig
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
		sequence := strings.TrimSuffix(fields[0], ":")
		if _, err := strconv.ParseUint(sequence, 10, 32); err == nil {
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

func parseLocalTrafficConfig(raw []byte) (localTrafficConfig, error) {
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

func (s *trafficSampler) discoverLocalTrafficConfig() (localTrafficConfig, error) {
	info, err := os.Stat(ConfigFile)
	if err != nil {
		s.configCached = false
		s.configFileInfo = nil
		return localTrafficConfig{}, err
	}
	if s.configCached && s.configFileInfo != nil && os.SameFile(s.configFileInfo, info) &&
		info.Size() == s.configSize && info.ModTime().Equal(s.configModTime) &&
		time.Since(s.configCheckedAt) < 30*time.Second {
		return s.configValue, nil
	}
	raw, readErr := readConfigFile()
	var cfg localTrafficConfig
	if readErr == nil {
		cfg, readErr = parseLocalTrafficConfig(raw)
	}
	if readErr != nil {
		s.configCached = false
		s.configFileInfo = nil
		return localTrafficConfig{}, readErr
	}
	s.configCached = true
	s.configFileInfo = info
	s.configModTime = info.ModTime()
	s.configSize = info.Size()
	s.configCheckedAt = time.Now()
	s.configValue = cfg
	return cfg, nil
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
	cfg, err := s.discoverLocalTrafficConfig()
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

	s.ingest(cfg.Endpoint, counters, time.Now())
}

// ingest folds one /connections response into the sampler state.
func (s *trafficSampler) ingest(endpoint string, counters clashTrafficResponse, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endpoint != "" && s.endpoint != endpoint {
		s.connections = make(map[string]trafficConnection)
		s.pendingPorts = make(map[string]protocol.PortTrafficSnapshot)
		s.haveConnections = false
		s.haveSample = false
	}

	uploadRate := uint64(0)
	downloadRate := uint64(0)
	sampleSeconds := 1.0
	if s.haveSample && s.endpoint == endpoint && now.After(s.lastSample) {
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
	sampleDeltas := make(map[string][2]uint64)
	for _, connection := range counters.Connections {
		if connection.ID == "" {
			continue
		}
		inbound := parseInboundTag(connection.Metadata.Type)
		current := trafficConnection{inbound: inbound, upload: connection.Upload, download: connection.Download}
		if s.haveConnections && inbound != "" {
			// A connection absent from the previous sample was opened since
			// then, so all of its bytes are new. Counting only increments of
			// already-known connections dropped short-lived connections and
			// the first second of every long one.
			uploadDelta, downloadDelta := connection.Upload, connection.Download
			if previous, ok := s.connections[connection.ID]; ok && previous.inbound == inbound {
				uploadDelta = counterDelta(connection.Upload, previous.upload)
				downloadDelta = counterDelta(connection.Download, previous.download)
			}
			if uploadDelta > 0 || downloadDelta > 0 {
				sample := sampleDeltas[inbound]
				sample[0] += uploadDelta
				sample[1] += downloadDelta
				sampleDeltas[inbound] = sample
			}
		}
		nextConnections[connection.ID] = current
	}
	// A port's rate is the sum of all its connections in this sample, not the
	// fastest single connection.
	for inbound, sample := range sampleDeltas {
		delta := s.pendingPorts[inbound]
		delta.Inbound = inbound
		delta.Upload += sample[0]
		delta.Download += sample[1]
		if rate := uint64(float64(sample[0]) / sampleSeconds); rate > delta.UploadRate {
			delta.UploadRate = rate
		}
		if rate := uint64(float64(sample[1]) / sampleSeconds); rate > delta.DownloadRate {
			delta.DownloadRate = rate
		}
		s.pendingPorts[inbound] = delta
	}
	s.connections = nextConnections
	s.haveConnections = true
	s.endpoint = endpoint
	s.lastUpload = counters.UploadTotal
	s.lastDownload = counters.DownloadTotal
	s.lastSample = now
	s.haveSample = true
	s.available = true
	s.uploadRate = uploadRate
	s.downloadRate = downloadRate
	if uploadRate > s.peakUpload {
		s.peakUpload = uploadRate
	}
	if downloadRate > s.peakDownload {
		s.peakDownload = downloadRate
	}
	s.tcpConnections, s.udpConnections = hostConnectionCounts()

}

func (s *trafficSampler) snapshot() *protocol.TrafficSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.available || !s.haveSample {
		return nil
	}
	snapshot := &protocol.TrafficSnapshot{
		UploadTotal:      s.lastUpload,
		DownloadTotal:    s.lastDownload,
		UploadRate:       s.uploadRate,
		DownloadRate:     s.downloadRate,
		TCPConnections:   s.tcpConnections,
		UDPConnections:   s.udpConnections,
		SampledAt:        s.lastSample.Unix(),
		PeakUploadRate:   s.peakUpload,
		PeakDownloadRate: s.peakDownload,
	}
	for _, delta := range s.pendingPorts {
		snapshot.Ports = append(snapshot.Ports, delta)
	}
	sort.Slice(snapshot.Ports, func(i, j int) bool { return snapshot.Ports[i].Inbound < snapshot.Ports[j].Inbound })
	return snapshot
}

func (s *trafficSampler) summarySnapshot() *protocol.TrafficSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.available || !s.haveSample {
		return nil
	}
	return &protocol.TrafficSnapshot{
		UploadTotal:    s.lastUpload,
		DownloadTotal:  s.lastDownload,
		UploadRate:     s.uploadRate,
		DownloadRate:   s.downloadRate,
		TCPConnections: s.tcpConnections,
		UDPConnections: s.udpConnections,
		SampledAt:      s.lastSample.Unix(),
	}
}

func (s *trafficSampler) acknowledge(snapshot *protocol.TrafficSnapshot) {
	if snapshot == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Clear the node peak only when no newer sample raised it meanwhile.
	if s.peakUpload <= snapshot.PeakUploadRate {
		s.peakUpload = 0
	}
	if s.peakDownload <= snapshot.PeakDownloadRate {
		s.peakDownload = 0
	}
	for _, sent := range snapshot.Ports {
		pending, ok := s.pendingPorts[sent.Inbound]
		if !ok {
			continue
		}
		pending.Upload = subtractCounter(pending.Upload, sent.Upload)
		pending.Download = subtractCounter(pending.Download, sent.Download)
		// Rates are maxima over a reporting window. Clear the sent maximum only
		// when no newer sample has raised it while the event was being queued.
		if pending.UploadRate <= sent.UploadRate {
			pending.UploadRate = 0
		}
		if pending.DownloadRate <= sent.DownloadRate {
			pending.DownloadRate = 0
		}
		if pending.Upload == 0 && pending.Download == 0 && pending.UploadRate == 0 && pending.DownloadRate == 0 {
			delete(s.pendingPorts, sent.Inbound)
		} else {
			s.pendingPorts[sent.Inbound] = pending
		}
	}
}

func subtractCounter(current, sent uint64) uint64 {
	if current <= sent {
		return 0
	}
	return current - sent
}
