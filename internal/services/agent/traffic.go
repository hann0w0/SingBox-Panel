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

	// ledger is the single per-connection account fed by both the Clash poll
	// and the sing-box API connection stream (see trafficledger.go).
	ledger          map[string]*ledgerEntry
	tombstones      map[string]*ledgerEntry
	tombstoneOrder  []string
	startedAt       time.Time
	haveConnections bool
	portWindow      map[string][2]uint64
	pendingPorts    map[string]protocol.PortTrafficSnapshot
	pendingUsers    map[userTrafficKey]protocol.UserTrafficSnapshot
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
	ID       string    `json:"id"`
	Upload   uint64    `json:"upload"`
	Download uint64    `json:"download"`
	Start    time.Time `json:"start"`
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
		ledger:       make(map[string]*ledgerEntry),
		tombstones:   make(map[string]*ledgerEntry),
		startedAt:    time.Now(),
		portWindow:   make(map[string][2]uint64),
		pendingPorts: make(map[string]protocol.PortTrafficSnapshot),
		pendingUsers: make(map[userTrafficKey]protocol.UserTrafficSnapshot),
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
	requestedAt := time.Now()
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

	s.ingestAt(cfg.Endpoint, counters, requestedAt, time.Now())
}

// ingest folds one /connections response into the sampler state.
func (s *trafficSampler) ingest(endpoint string, counters clashTrafficResponse, now time.Time) {
	s.ingestAt(endpoint, counters, now, now)
}

// ingestAt is ingest with the time the request was issued. Ledger entries
// first seen after that moment (from the connection stream) cannot be in the
// response and must not be treated as closed.
func (s *trafficSampler) ingestAt(endpoint string, counters clashTrafficResponse, requestedAt, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endpoint != "" && s.endpoint != endpoint {
		// A different controller is a different sing-box instance view; node
		// totals restart. Connection ids are global UUIDs, so the ledger and
		// unsent deltas stay valid.
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

	// The Clash API exposes per-connection counters and the inbound tag in
	// metadata.type, for example "vless/vless-in". Every observation goes
	// through the shared ledger, which attributes only bytes not yet counted.
	present := make(map[string]bool, len(counters.Connections))
	for _, connection := range counters.Connections {
		if connection.ID == "" {
			continue
		}
		present[connection.ID] = true
		// Without a creation time, the very first sample is the baseline: those
		// connections' history predates the accounting window.
		baseline := !s.haveConnections
		if !connection.Start.IsZero() {
			baseline = connection.Start.Before(s.startedAt)
		}
		s.observeConnection(connection.ID, parseInboundTag(connection.Metadata.Type), "",
			[2]uint64{connection.Upload, connection.Download}, baseline, now)
	}
	s.closeMissing(present, requestedAt)
	// A port's rate is the sum of all its connections over this sample window
	// (including bytes the stream reported in between), not the fastest one.
	for inbound, sample := range s.portWindow {
		delta := s.pendingPorts[inbound]
		delta.Inbound = inbound
		if rate := uint64(float64(sample[0]) / sampleSeconds); rate > delta.UploadRate {
			delta.UploadRate = rate
		}
		if rate := uint64(float64(sample[1]) / sampleSeconds); rate > delta.DownloadRate {
			delta.DownloadRate = rate
		}
		s.pendingPorts[inbound] = delta
	}
	clear(s.portWindow)
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
	for _, delta := range s.pendingUsers {
		snapshot.Users = append(snapshot.Users, delta)
	}
	sort.Slice(snapshot.Users, func(i, j int) bool {
		if snapshot.Users[i].Inbound != snapshot.Users[j].Inbound {
			return snapshot.Users[i].Inbound < snapshot.Users[j].Inbound
		}
		return snapshot.Users[i].User < snapshot.Users[j].User
	})
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
	for _, sent := range snapshot.Users {
		key := userTrafficKey{inbound: sent.Inbound, user: sent.User}
		pending, ok := s.pendingUsers[key]
		if !ok {
			continue
		}
		pending.Upload = subtractCounter(pending.Upload, sent.Upload)
		pending.Download = subtractCounter(pending.Download, sent.Download)
		if pending.Upload == 0 && pending.Download == 0 {
			delete(s.pendingUsers, key)
		} else {
			s.pendingUsers[key] = pending
		}
	}
}

func subtractCounter(current, sent uint64) uint64 {
	if current <= sent {
		return 0
	}
	return current - sent
}
