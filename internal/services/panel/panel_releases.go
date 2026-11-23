package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// panelReleaseInfo is one installable panel release offered for update or
// rollback in the settings page.
type panelReleaseInfo struct {
	Version     string `json:"version"`
	Name        string `json:"name,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	Prerelease  bool   `json:"prerelease"`
	Current     bool   `json:"current"`
}

const panelReleaseListTTL = 10 * time.Minute

var (
	panelReleaseListURL   = "https://api.github.com/repos/" + githubRepo + "/releases?per_page=30"
	panelReleaseListMu    sync.Mutex
	panelReleaseListCache []panelReleaseInfo
	panelReleaseListAt    time.Time
)

type githubReleaseDetail struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

// installablePanelReleases lists published releases that ship every asset the
// self-update needs for this architecture, newest first.
func installablePanelReleases(refresh bool) ([]panelReleaseInfo, error) {
	panelReleaseListMu.Lock()
	defer panelReleaseListMu.Unlock()
	if !refresh && panelReleaseListCache != nil && time.Since(panelReleaseListAt) < panelReleaseListTTL {
		return panelReleaseListCache, nil
	}
	resp, err := httpGetGitHub(panelReleaseListURL, 8*time.Second)
	if err != nil {
		if panelReleaseListCache != nil {
			return panelReleaseListCache, nil
		}
		return nil, fmt.Errorf("访问 GitHub 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if panelReleaseListCache != nil {
			return panelReleaseListCache, nil
		}
		return nil, fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
	}
	var releases []githubReleaseDetail
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&releases); err != nil {
		if panelReleaseListCache != nil {
			return panelReleaseListCache, nil
		}
		return nil, fmt.Errorf("解析 Release 失败：%w", err)
	}
	binary := "singbox-panel-linux-" + runtime.GOARCH
	list := make([]panelReleaseInfo, 0, len(releases))
	for _, rel := range releases {
		if rel.Draft || !validReleaseTag(rel.TagName) {
			continue
		}
		assets := map[string]bool{}
		for _, asset := range rel.Assets {
			assets[asset.Name] = true
		}
		if !assets[binary] || !assets["singbox-panel-web-"+rel.TagName+".tar.gz"] || !assets["singbox-panel-agents-"+rel.TagName+".tar.gz"] {
			continue
		}
		list = append(list, panelReleaseInfo{Version: rel.TagName, Name: rel.Name, PublishedAt: rel.PublishedAt, Prerelease: rel.Prerelease})
	}
	panelReleaseListCache = list
	panelReleaseListAt = time.Now()
	return list, nil
}

// GET /api/admin/maintenance/releases — installable releases for update or rollback.
func (a *App) maintenanceReleases(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	refresh := c.Query("refresh") == "1" || strings.EqualFold(c.Query("refresh"), "true")
	list, err := installablePanelReleases(refresh)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	out := make([]panelReleaseInfo, len(list))
	for i, rel := range list {
		rel.Current = sameVersion(rel.Version, a.version)
		out[i] = rel
	}
	c.JSON(http.StatusOK, gin.H{"releases": out})
}
