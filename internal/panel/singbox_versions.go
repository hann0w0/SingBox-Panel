package panel

import (
	"encoding/json"
	"net/http"
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

func checkSingboxUpdate(installedVersion string, releases SingboxLatestReleases) (hasUpdate bool, latestVersion string) {
	if installedVersion == "" {
		return false, ""
	}
	cleanInstalled := strings.TrimPrefix(installedVersion, "v")
	isBeta := strings.Contains(cleanInstalled, "beta") || strings.Contains(cleanInstalled, "alpha") || strings.Contains(cleanInstalled, "rc")

	if isBeta {
		if releases.Beta != "" && releases.Beta != cleanInstalled {
			return true, releases.Beta
		}
	} else {
		if releases.Stable != "" && releases.Stable != cleanInstalled {
			return true, releases.Stable
		}
	}
	return false, ""
}
