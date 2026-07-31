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
	"strings"
	"sync"
	"time"

	"github.com/hann0w0/singbox-panel/internal/protocol"
)

const maxTrafficResponseSize = 1 << 20

type trafficSampler struct {
	mu sync.Mutex

	endpoint     string
	lastUpload   uint64
	lastDownload uint64
	lastSample   time.Time
	haveSample   bool

	client *http.Client
}

func newTrafficSampler() *trafficSampler {
	return &trafficSampler{client: &http.Client{Timeout: 3 * time.Second}}
}

type localTrafficConfig struct {
	Endpoint string
	Secret   string
}

// discoverLocalTrafficConfig reads only the local Clash API address and secret
// from config.json. Raw-mode users keep full control of their experimental
// block; stats simply show unavailable when no safe local endpoint exists.
func discoverLocalTrafficConfig() (localTrafficConfig, error) {
	raw, err := os.ReadFile(ConfigFile)
	if err != nil {
		return localTrafficConfig{}, err
	}
	return parseLocalTrafficConfig(raw)
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

type clashTrafficResponse struct {
	UploadTotal   uint64 `json:"uploadTotal"`
	DownloadTotal uint64 `json:"downloadTotal"`
}

func (s *trafficSampler) sample(ctx context.Context) *protocol.TrafficSnapshot {
	cfg, err := discoverLocalTrafficConfig()
	if err != nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Endpoint, nil)
	if err != nil {
		return nil
	}
	if cfg.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Secret)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
		return nil
	}
	var counters clashTrafficResponse
	decoder := json.NewDecoder(io.LimitReader(res.Body, maxTrafficResponseSize))
	if err := decoder.Decode(&counters); err != nil {
		return nil
	}

	now := time.Now()
	snapshot := &protocol.TrafficSnapshot{
		UploadTotal:   counters.UploadTotal,
		DownloadTotal: counters.DownloadTotal,
		SampledAt:     now.Unix(),
	}
	s.mu.Lock()
	if s.haveSample && s.endpoint == cfg.Endpoint && now.After(s.lastSample) &&
		counters.UploadTotal >= s.lastUpload && counters.DownloadTotal >= s.lastDownload {
		seconds := now.Sub(s.lastSample).Seconds()
		if seconds > 0 {
			snapshot.UploadRate = uint64(float64(counters.UploadTotal-s.lastUpload) / seconds)
			snapshot.DownloadRate = uint64(float64(counters.DownloadTotal-s.lastDownload) / seconds)
		}
	}
	s.endpoint = cfg.Endpoint
	s.lastUpload = counters.UploadTotal
	s.lastDownload = counters.DownloadTotal
	s.lastSample = now
	s.haveSample = true
	s.mu.Unlock()
	return snapshot
}
