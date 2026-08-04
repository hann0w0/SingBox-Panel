package panel

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
}

type SingboxLatestReleases struct {
	Stable string `json:"stable"`
	Beta   string `json:"beta"`
}

var (
	sbReleaseCache     SingboxLatestReleases
	sbReleaseCacheTime time.Time
	sbReleaseMutex     sync.RWMutex
)

// invalidateSingboxReleaseCache clears the cached latest versions so the next
// read re-fetches from GitHub. Called after a sing-box install/upgrade to avoid
// stale version comparisons for up to 24h.
func invalidateSingboxReleaseCache() {
	sbReleaseMutex.Lock()
	defer sbReleaseMutex.Unlock()
	sbReleaseCache = SingboxLatestReleases{}
	sbReleaseCacheTime = time.Time{}
}

func getLatestSingboxReleases() SingboxLatestReleases {
	sbReleaseMutex.RLock()
	if time.Since(sbReleaseCacheTime) < 24*time.Hour && (sbReleaseCache.Stable != "" || sbReleaseCache.Beta != "") {
		defer sbReleaseMutex.RUnlock()
		return sbReleaseCache
	}
	sbReleaseMutex.RUnlock()

	sbReleaseMutex.Lock()
	defer sbReleaseMutex.Unlock()

	if time.Since(sbReleaseCacheTime) < 24*time.Hour && (sbReleaseCache.Stable != "" || sbReleaseCache.Beta != "") {
		return sbReleaseCache
	}

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/SagerNet/sing-box/releases?per_page=15", nil)
	if err != nil {
		return sbReleaseCache
	}
	req.Header.Set("User-Agent", "singbox-panel")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return sbReleaseCache
	}
	defer resp.Body.Close()

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return sbReleaseCache
	}

	var stable, beta string
	for _, rel := range releases {
		tag := strings.TrimPrefix(rel.TagName, "v")
		if rel.Prerelease {
			if beta == "" {
				beta = tag
			}
		} else {
			if stable == "" {
				stable = tag
			}
		}
		if stable != "" && beta != "" {
			break
		}
	}

	if stable != "" || beta != "" {
		sbReleaseCache = SingboxLatestReleases{Stable: stable, Beta: beta}
		sbReleaseCacheTime = time.Now()
	}
	return sbReleaseCache
}

// compareSemver returns -1 if a < b, 0 if a == b, 1 if a > b.
// Handles semver-like strings (e.g. "1.14.0", "1.14.0-rc.1").
// Pre-release suffixes are considered older than the bare release.
func compareSemver(a, b string) int {
	if a == b {
		return 0
	}
	aCore, aPre := splitSemver(a)
	bCore, bPre := splitSemver(b)

	if c := compareDotted(aCore, bCore); c != 0 {
		return c
	}
	if aPre == "" && bPre != "" {
		return 1
	}
	if aPre != "" && bPre == "" {
		return -1
	}
	return compareDotted(aPre, bPre)
}

func splitSemver(v string) (core, pre string) {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

func compareDotted(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		va, vb := 0, 0
		if i < len(pa) {
			va, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			vb, _ = strconv.Atoi(pb[i])
		}
		if va < vb {
			return -1
		}
		if va > vb {
			return 1
		}
	}
	return 0
}

func checkSingboxUpdate(installedVersion string, releases SingboxLatestReleases) (hasUpdate bool, latestVersion string) {
	if installedVersion == "" {
		return false, ""
	}
	cleanInstalled := strings.TrimPrefix(installedVersion, "v")
	isBeta := strings.Contains(cleanInstalled, "beta") || strings.Contains(cleanInstalled, "alpha") || strings.Contains(cleanInstalled, "rc")

	if isBeta {
		if releases.Beta != "" && compareSemver(cleanInstalled, releases.Beta) < 0 {
			return true, releases.Beta
		}
	} else {
		if releases.Stable != "" && compareSemver(cleanInstalled, releases.Stable) < 0 {
			return true, releases.Stable
		}
	}
	return false, ""
}
