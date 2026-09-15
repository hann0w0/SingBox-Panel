package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSingboxReleaseCacheRefresh(t *testing.T) {
	requests := 0
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(status)
		if status == http.StatusOK {
			_ = json.NewEncoder(w).Encode([]githubRelease{
				{TagName: "v1.15.0-alpha.4", Prerelease: true},
				{TagName: "v1.14.1"},
			})
		}
	}))
	defer server.Close()

	sbReleaseMutex.Lock()
	previousURL, previousCache, previousTime := sbReleaseURL, sbReleaseCache, sbReleaseCacheTime
	sbReleaseURL = server.URL
	sbReleaseCache = SingboxLatestReleases{Stable: "1.14.0"}
	sbReleaseCacheTime = time.Now().Add(-30 * time.Minute)
	sbReleaseMutex.Unlock()
	t.Cleanup(func() {
		sbReleaseMutex.Lock()
		defer sbReleaseMutex.Unlock()
		sbReleaseURL, sbReleaseCache, sbReleaseCacheTime = previousURL, previousCache, previousTime
	})

	if got := getLatestSingboxReleases(); got.Stable != "1.14.0" || requests != 0 {
		t.Fatalf("fresh cache should be reused: releases=%+v, requests=%d", got, requests)
	}

	// Reproduce a panel that cached 1.14.0 two hours before checking again.
	sbReleaseMutex.Lock()
	sbReleaseCacheTime = time.Now().Add(-2 * time.Hour)
	sbReleaseMutex.Unlock()
	releases := getLatestSingboxReleases()
	if releases.Stable != "1.14.1" || releases.Beta != "1.15.0-alpha.4" || requests != 1 {
		t.Fatalf("expired cache should fetch current releases: releases=%+v, requests=%d", releases, requests)
	}
	if hasUpdate, latest := checkSingboxUpdate("1.14.0", releases); !hasUpdate || latest != "1.14.1" {
		t.Fatalf("missing stable update: hasUpdate=%v, latest=%q", hasUpdate, latest)
	}
	if got := getLatestSingboxReleases(); got != releases || requests != 1 {
		t.Fatalf("refreshed cache should be reused: releases=%+v, requests=%d", got, requests)
	}

	// A temporary GitHub failure must preserve the last successful result.
	status = http.StatusServiceUnavailable
	sbReleaseMutex.Lock()
	sbReleaseCacheTime = time.Now().Add(-2 * time.Hour)
	expiredAt := sbReleaseCacheTime
	sbReleaseMutex.Unlock()
	if got := getLatestSingboxReleases(); got != releases || requests != 2 {
		t.Fatalf("failed refresh should preserve cached releases: releases=%+v, requests=%d", got, requests)
	}
	if !sbReleaseCacheTime.Equal(expiredAt) {
		t.Fatal("failed refresh must not renew cache expiry")
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		// equal
		{"1.14.0", "1.14.0", 0},
		{"1.14.0-rc.1", "1.14.0-rc.1", 0},
		// a < b
		{"1.13.0", "1.14.0", -1},
		{"1.14.0", "1.14.1", -1},
		{"1.9.0", "1.10.0", -1},
		{"1.14.0-rc.1", "1.14.0", -1}, // pre-release < release
		// a > b
		{"1.15.0", "1.14.0", 1},
		{"1.14.1", "1.14.0", 1},
		{"1.10.0", "1.9.0", 1},
		{"1.14.0", "1.14.0-rc.1", 1}, // release > pre-release
	}
	for _, tt := range tests {
		got := compareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCheckSingboxUpdate_InstalledIsNewer(t *testing.T) {
	// When installed version is newer than cached latest,
	// should NOT report an update.
	releases := SingboxLatestReleases{Stable: "1.14.0", Beta: "1.15.0-rc.1"}

	// Installed 1.15.0 > cached stable 1.14.0 → no update
	hasUp, latest := checkSingboxUpdate("1.15.0", releases)
	if hasUp {
		t.Errorf("1.15.0 vs stable=1.14.0: hasUpdate=%v, latest=%q — should be false", hasUp, latest)
	}

	// Installed 1.14.0 == cached stable 1.14.0 → no update
	hasUp, latest = checkSingboxUpdate("1.14.0", releases)
	if hasUp {
		t.Errorf("1.14.0 vs stable=1.14.0: hasUpdate=%v, latest=%q — should be false", hasUp, latest)
	}

	// Installed 1.13.0 < cached stable 1.14.0 → has update
	hasUp, latest = checkSingboxUpdate("1.13.0", releases)
	if !hasUp {
		t.Errorf("1.13.0 vs stable=1.14.0: hasUpdate=%v — should be true", hasUp)
	}
	if latest != "1.14.0" {
		t.Errorf("latest should be 1.14.0, got %q", latest)
	}
}

func TestCheckSingboxUpdate_BetaIsNewer(t *testing.T) {
	releases := SingboxLatestReleases{Stable: "1.14.0", Beta: "1.15.0-beta.1"}

	// Installed beta 1.15.0-beta.1 == cached beta 1.15.0-beta.1 → no update
	hasUp, _ := checkSingboxUpdate("1.15.0-beta.1", releases)
	if hasUp {
		t.Error("same beta version should not report update")
	}

	// Installed beta 1.15.0-beta.2 > cached beta 1.15.0-beta.1 → no update
	hasUp, _ = checkSingboxUpdate("1.15.0-beta.2", releases)
	if hasUp {
		t.Error("newer beta version should not report update")
	}
}
