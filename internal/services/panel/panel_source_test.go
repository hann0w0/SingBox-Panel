package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	testSourceCurrent = "0123456789abcdef0123456789abcdef01234567"
	testSourceLatest  = "89abcdef0123456789abcdef0123456789abcdef"
)

func resetPanelSourceTestState(t *testing.T) {
	t.Helper()
	panelSourceMu.Lock()
	previousHeadURL := panelSourceHeadURL
	previousCompareURL := panelSourceCompareURL
	previousCache := panelSourceCache
	previousAt := panelSourceCacheAt
	panelSourceCache = panelSourceSnapshot{}
	panelSourceCacheAt = time.Time{}
	panelSourceMu.Unlock()
	t.Cleanup(func() {
		panelSourceMu.Lock()
		panelSourceHeadURL = previousHeadURL
		panelSourceCompareURL = previousCompareURL
		panelSourceCache = previousCache
		panelSourceCacheAt = previousAt
		panelSourceMu.Unlock()
	})
}

func sourceTestServer(t *testing.T, headStatus int, headBody string) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var headRequests, compareRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/commits/main"):
			headRequests.Add(1)
			w.WriteHeader(headStatus)
			_, _ = w.Write([]byte(headBody))
		case strings.Contains(r.URL.Path, "/compare/"):
			compareRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"behind","ahead_by":4,"behind_by":2}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &headRequests, &compareRequests
}

func configurePanelSourceTestServer(t *testing.T, endpoint string) {
	t.Helper()
	panelSourceMu.Lock()
	panelSourceHeadURL = endpoint + "/commits/main"
	panelSourceCompareURL = endpoint + "/compare/%s...main"
	panelSourceMu.Unlock()
}

func TestPanelSourceCommitCacheAndFallback(t *testing.T) {
	resetPanelSourceTestState(t)
	headBody := `{"sha":"` + testSourceLatest + `","commit":{"message":"fix: current head\n\nbody","committer":{"date":"2026-10-28T01:02:03Z"}}}`
	srv, headRequests, _ := sourceTestServer(t, http.StatusOK, headBody)
	configurePanelSourceTestServer(t, srv.URL)

	first, err := latestPanelSourceCommit(false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Commit != testSourceLatest || first.Message != "fix: current head" || first.Committed != "2026-10-28T01:02:03Z" {
		t.Fatalf("source snapshot = %+v", first)
	}
	if got, err := latestPanelSourceCommit(false); err != nil || got != first {
		t.Fatalf("cached source snapshot = %+v, err=%v", got, err)
	}
	if headRequests.Load() != 1 {
		t.Fatalf("GitHub source requests = %d, want 1", headRequests.Load())
	}

	panelSourceMu.Lock()
	panelSourceCacheAt = time.Now().Add(-panelSourceCacheTTL - time.Minute)
	panelSourceMu.Unlock()
	if got, err := latestPanelSourceCommit(false); err != nil || got != first || headRequests.Load() != 2 {
		t.Fatalf("failed refresh did not preserve cache: got=%+v err=%v requests=%d", got, err, headRequests.Load())
	}
}

func TestPanelSourceRejectsInvalidCommit(t *testing.T) {
	resetPanelSourceTestState(t)
	srv, _, _ := sourceTestServer(t, http.StatusOK, `{"sha":"not-a-commit"}`)
	configurePanelSourceTestServer(t, srv.URL)
	if _, err := latestPanelSourceCommit(false); err == nil {
		t.Fatal("accepted an invalid source commit")
	}
}

func TestComparePanelSource(t *testing.T) {
	resetPanelSourceTestState(t)
	var status atomic.Value
	status.Store("identical")
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !strings.Contains(r.URL.Path, testSourceCurrent) {
			t.Errorf("compare path does not contain current commit: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch status.Load() {
		case "not-found":
			w.WriteHeader(http.StatusNotFound)
		case "behind":
			_, _ = w.Write([]byte(`{"status":"behind","ahead_by":0,"behind_by":4}`))
		case "ahead":
			_, _ = w.Write([]byte(`{"status":"ahead","ahead_by":2,"behind_by":0}`))
		case "diverged":
			_, _ = w.Write([]byte(`{"status":"diverged","ahead_by":2,"behind_by":5}`))
		default:
			_, _ = w.Write([]byte(`{"status":"identical","ahead_by":0,"behind_by":0}`))
		}
	}))
	defer srv.Close()
	configurePanelSourceTestServer(t, srv.URL)

	for _, tc := range []struct {
		status        string
		wantStatus    string
		wantAhead     int
		wantBehind    int
		wantRequested bool
	}{
		{status: "identical", wantStatus: "current", wantRequested: true},
		{status: "behind", wantStatus: "behind", wantBehind: 4, wantRequested: true},
		{status: "ahead", wantStatus: "ahead", wantAhead: 2, wantRequested: true},
		{status: "diverged", wantStatus: "behind", wantAhead: 5, wantBehind: 2, wantRequested: true},
		{status: "not-found", wantStatus: "unknown", wantRequested: true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			status.Store(tc.status)
			before := requests.Load()
			gotStatus, ahead, behind, err := comparePanelSource(t.Context(), strings.ToUpper(testSourceCurrent))
			if err != nil || gotStatus != tc.wantStatus || ahead != tc.wantAhead || behind != tc.wantBehind {
				t.Fatalf("compare = %s/%d/%d err=%v; want %s/%d/%d", gotStatus, ahead, behind, err, tc.wantStatus, tc.wantAhead, tc.wantBehind)
			}
			if requests.Load() != before+1 {
				t.Fatal("compare request was not sent")
			}
		})
	}

	before := requests.Load()
	gotStatus, ahead, behind, err := comparePanelSource(t.Context(), "")
	if err != nil || gotStatus != "unknown" || ahead != 0 || behind != 0 || requests.Load() != before {
		t.Fatalf("unknown commit created a comparison: %s/%d/%d err=%v", gotStatus, ahead, behind, err)
	}
}

func TestMaintenanceInfoReportsSourceStatus(t *testing.T) {
	resetPanelSourceTestState(t)
	headBody := `{"sha":"` + testSourceLatest + `","commit":{"message":"feat: source head","committer":{"date":"2026-10-28T01:02:03Z"}}}`
	srv, _, _ := sourceTestServer(t, http.StatusOK, headBody)
	configurePanelSourceTestServer(t, srv.URL)
	panelReleaseMutex.Lock()
	previousTag, previousAt := panelReleaseTag, panelReleaseAt
	panelReleaseTag, panelReleaseAt = "v1.0.7", time.Now()
	panelReleaseMutex.Unlock()
	t.Cleanup(func() {
		panelReleaseMutex.Lock()
		panelReleaseTag, panelReleaseAt = previousTag, previousAt
		panelReleaseMutex.Unlock()
	})

	a := &App{version: "v1.0.8", commit: testSourceCurrent, startedAt: time.Now()}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/info", nil)
	a.maintenanceInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["current_commit"] != testSourceCurrent || got["latest_source_commit"] != testSourceLatest ||
		got["source_status"] != "behind" || got["source_has_update"] != true ||
		got["source_behind"] != float64(2) || got["source_branch"] != "main" {
		t.Fatalf("source maintenance fields = %+v", got)
	}
	if got["latest_version"] != "v1.0.7" || got["has_update"] != true {
		t.Fatalf("source update was not reported through has_update: %+v", got)
	}
}

func TestMaintenanceInfoUnknownCommitDoesNotReportSourceUpdate(t *testing.T) {
	resetPanelSourceTestState(t)
	headBody := `{"sha":"` + testSourceLatest + `","commit":{"message":"feat: source head","committer":{"date":"2026-10-28T01:02:03Z"}}}`
	srv, _, compareRequests := sourceTestServer(t, http.StatusOK, headBody)
	configurePanelSourceTestServer(t, srv.URL)
	panelReleaseMutex.Lock()
	previousTag, previousAt := panelReleaseTag, panelReleaseAt
	panelReleaseTag, panelReleaseAt = "v1.0.8", time.Now()
	panelReleaseMutex.Unlock()
	t.Cleanup(func() {
		panelReleaseMutex.Lock()
		panelReleaseTag, panelReleaseAt = previousTag, previousAt
		panelReleaseMutex.Unlock()
	})

	a := &App{version: "v1.0.8", startedAt: time.Now()}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/info", nil)
	a.maintenanceInfo(c)
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["source_status"] != "unknown" || got["source_has_update"] != false {
		t.Fatalf("unknown build commit produced update status: %+v", got)
	}
	if got["has_update"] != false {
		t.Fatalf("unknown build commit produced an update: %+v", got)
	}
	if compareRequests.Load() != 0 {
		t.Fatal("unknown commit caused an unnecessary compare request")
	}
}
