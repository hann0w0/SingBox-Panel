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

const singboxReleaseCacheTTL = time.Hour

var (
	sbReleaseURL       = "https://api.github.com/repos/SagerNet/sing-box/releases?per_page=15"
	sbReleaseCache     SingboxLatestReleases
	sbReleaseCacheTime time.Time
	sbReleaseMutex     sync.RWMutex
)

// invalidateSingboxReleaseCache clears the cached latest versions so the next
// read re-fetches from GitHub. Called after a sing-box install/upgrade to avoid
// stale version comparisons until the cache expires.
func invalidateSingboxReleaseCache() {
	sbReleaseMutex.Lock()
	defer sbReleaseMutex.Unlock()
	sbReleaseCache = SingboxLatestReleases{}
	sbReleaseCacheTime = time.Time{}
}

func getLatestSingboxReleases() SingboxLatestReleases {
	sbReleaseMutex.RLock()
	if time.Since(sbReleaseCacheTime) < singboxReleaseCacheTTL && (sbReleaseCache.Stable != "" || sbReleaseCache.Beta != "") {
		defer sbReleaseMutex.RUnlock()
		return sbReleaseCache
	}
	sbReleaseMutex.RUnlock()

	sbReleaseMutex.Lock()
	defer sbReleaseMutex.Unlock()

	if time.Since(sbReleaseCacheTime) < singboxReleaseCacheTTL && (sbReleaseCache.Stable != "" || sbReleaseCache.Beta != "") {
		return sbReleaseCache
	}

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", sbReleaseURL, nil)
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
// Handles semver-like strings (e.g. "1.14.0", "1.15.0-alpha.10", "1.14.0-rc.1").
// Pre-release suffixes are considered older than the bare release, and
// pre-release stages order as alpha < beta < rc.
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
	return comparePrerelease(aPre, bPre)
}

// comparePrerelease compares dot-separated pre-release identifiers. Numeric
// identifiers compare numerically; the stage names sing-box publishes compare
// as alpha < beta < rc (other names fall back to string order). Numeric
// identifiers sort before names, and a shorter list sorts first, per semver.
func comparePrerelease(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if c := comparePrereleaseIdent(pa[i], pb[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(pa) < len(pb):
		return -1
	case len(pa) > len(pb):
		return 1
	}
	return 0
}

var prereleaseStageRank = map[string]int{"alpha": 1, "beta": 2, "rc": 3}

func comparePrereleaseIdent(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return cmpInt(na, nb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	ra, rb := prereleaseStageRank[strings.ToLower(a)], prereleaseStageRank[strings.ToLower(b)]
	if ra != 0 && rb != 0 {
		return cmpInt(ra, rb)
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
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

// checkSingboxUpdate reports whether a newer sing-box should be offered.
// Stable installs are only ever offered a newer stable release. Pre-release
// installs are offered a newer stable release first (e.g. 1.14.0-beta.17 →
// 1.14.2), otherwise a newer pre-release of the SAME minor line only — never a
// jump to the next minor's alpha, whose config schema the panel may not yet
// support (sing-box 1.15 alphas reject options 1.14 merely deprecated).
func checkSingboxUpdate(installedVersion string, releases SingboxLatestReleases) (hasUpdate bool, latestVersion string) {
	if installedVersion == "" {
		return false, ""
	}
	cleanInstalled := strings.TrimPrefix(installedVersion, "v")
	core, pre := splitSemver(cleanInstalled)

	if releases.Stable != "" && compareSemver(cleanInstalled, releases.Stable) < 0 {
		return true, releases.Stable
	}
	if pre != "" && releases.Beta != "" {
		betaCore, _ := splitSemver(releases.Beta)
		if minorLine(betaCore) == minorLine(core) && compareSemver(cleanInstalled, releases.Beta) < 0 {
			return true, releases.Beta
		}
	}
	return false, ""
}

// minorLine returns the "major.minor" prefix of a dotted version core.
func minorLine(core string) string {
	parts := strings.SplitN(core, ".", 3)
	if len(parts) < 2 {
		return core
	}
	return parts[0] + "." + parts[1]
}
