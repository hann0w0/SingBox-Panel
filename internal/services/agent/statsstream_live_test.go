package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveStatsStreamAgainstSingbox runs against a real sing-box when
// SBTEST_CONFIG points at its config (manual integration check).
func TestLiveStatsStreamAgainstSingbox(t *testing.T) {
	path := os.Getenv("SBTEST_CONFIG")
	if path == "" {
		t.Skip("SBTEST_CONFIG not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sampler := newTrafficSampler()
	stream := newStatsStream(sampler)
	stream.readConfig = func() ([]byte, error) { return os.ReadFile(path) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if os.Getenv("SBTEST_STREAM") != "0" {
		go stream.run(ctx)
	}
	clash, err := parseLocalTrafficConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(mustDuration(t, os.Getenv("SBTEST_DURATION")))
	for time.Now().Before(deadline) {
		requestedAt := time.Now()
		req, _ := http.NewRequest(http.MethodGet, clash.Endpoint, nil)
		req.Header.Set("Authorization", "Bearer "+clash.Secret)
		if res, err := sampler.client.Do(req); err == nil {
			var counters clashTrafficResponse
			decodeErr := json.NewDecoder(res.Body).Decode(&counters)
			res.Body.Close()
			if decodeErr == nil && res.StatusCode == http.StatusOK {
				sampler.ingestAt(clash.Endpoint, counters, requestedAt, time.Now())
			} else {
				fmt.Fprintln(os.Stderr, "clash poll:", res.StatusCode, decodeErr)
			}
		} else {
			fmt.Fprintln(os.Stderr, "clash poll:", err)
		}
		time.Sleep(time.Second)
	}
	snap := sampler.snapshot()
	out, _ := json.MarshalIndent(map[string]any{"ports": snap.Ports, "users": snap.Users, "total_up": snap.UploadTotal, "total_down": snap.DownloadTotal}, "", " ")
	fmt.Println(string(out))
}

func mustDuration(t *testing.T, v string) time.Duration {
	if v == "" {
		return 10 * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
