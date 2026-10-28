package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const panelSourceCacheTTL = 10 * time.Minute

var (
	panelSourceHeadURL    = "https://api.github.com/repos/" + githubRepo + "/commits/main"
	panelSourceCompareURL = "https://api.github.com/repos/" + githubRepo + "/compare/%s...main"
	panelSourceMu         sync.Mutex
	panelSourceCache      panelSourceSnapshot
	panelSourceCacheAt    time.Time
	commitPattern         = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sourceResponseMaxSize = int64(1 << 20)
)

type panelSourceSnapshot struct {
	Commit    string `json:"commit"`
	Message   string `json:"message,omitempty"`
	Committed string `json:"committed_at,omitempty"`
}

type githubSourceCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message   string `json:"message"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

func validCommitSHA(s string) bool {
	return commitPattern.MatchString(strings.ToLower(strings.TrimSpace(s)))
}

func normalizeCommitSHA(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if !validCommitSHA(s) {
		return ""
	}
	return s
}

// latestPanelSourceCommit returns the current head of main. The cache preserves
// the last successful snapshot across temporary GitHub failures.
func latestPanelSourceCommit(refresh bool) (panelSourceSnapshot, error) {
	panelSourceMu.Lock()
	defer panelSourceMu.Unlock()
	if !refresh && panelSourceCache.Commit != "" && time.Since(panelSourceCacheAt) < panelSourceCacheTTL {
		return panelSourceCache, nil
	}
	resp, err := httpGetGitHub(panelSourceHeadURL, 8*time.Second)
	if err != nil {
		if panelSourceCache.Commit != "" {
			return panelSourceCache, nil
		}
		return panelSourceSnapshot{}, fmt.Errorf("访问 GitHub 源码失败：%w", err)
	}
	defer resp.Body.Close()
	var commit githubSourceCommit
	if err := json.NewDecoder(io.LimitReader(resp.Body, sourceResponseMaxSize)).Decode(&commit); err != nil {
		if panelSourceCache.Commit != "" {
			return panelSourceCache, nil
		}
		return panelSourceSnapshot{}, fmt.Errorf("解析源码提交失败：%w", err)
	}
	if !validCommitSHA(commit.SHA) {
		if panelSourceCache.Commit != "" {
			return panelSourceCache, nil
		}
		return panelSourceSnapshot{}, fmt.Errorf("源码提交 SHA 无效")
	}
	panelSourceCache = panelSourceSnapshot{
		Commit:    strings.ToLower(commit.SHA),
		Message:   summaryLine(commit.Commit.Message),
		Committed: commit.Commit.Committer.Date,
	}
	panelSourceCacheAt = time.Now()
	return panelSourceCache, nil
}

// comparePanelSource asks GitHub whether the running commit is behind main.
// A missing or locally built commit is unknown, not an update.
func comparePanelSource(ctx context.Context, current string) (status string, ahead, behind int, err error) {
	current = strings.ToLower(strings.TrimSpace(current))
	if !validCommitSHA(current) {
		return "unknown", 0, 0, nil
	}
	endpoint := fmt.Sprintf(panelSourceCompareURL, url.PathEscape(current))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("User-Agent", "singbox-panel")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("比较源码版本失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity {
		return "unknown", 0, 0, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, 0, fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
	}
	var result githubCompare
	if err := json.NewDecoder(io.LimitReader(resp.Body, sourceResponseMaxSize)).Decode(&result); err != nil {
		return "", 0, 0, fmt.Errorf("解析源码比较结果失败：%w", err)
	}
	switch result.Status {
	case "identical":
		return "current", 0, 0, nil
	case "behind", "diverged":
		if result.Status == "behind" {
			return "behind", result.AheadBy, result.BehindBy, nil
		}
		// GitHub orders a diverged comparison the other way around: the current
		// commit is ahead of main by AheadBy and main is behind by BehindBy.
		return "behind", result.BehindBy, result.AheadBy, nil
	case "ahead":
		return "ahead", result.AheadBy, result.BehindBy, nil
	default:
		return "unknown", 0, 0, nil
	}
}

type githubCompare struct {
	Status   string `json:"status"`
	AheadBy  int    `json:"ahead_by"`
	BehindBy int    `json:"behind_by"`
}

func summaryLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
