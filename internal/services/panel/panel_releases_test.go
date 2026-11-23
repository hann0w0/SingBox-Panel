package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMaintenanceReleasesListsOnlyInstallableReleases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bin := "singbox-panel-linux-" + runtime.GOARCH
	singleBinary := func() string {
		return fmt.Sprintf(`[{"name":%q},{"name":"checksums.txt"}]`, bin)
	}
	legacySplit := func(tag string) string {
		return fmt.Sprintf(`[{"name":%q},{"name":"checksums.txt"},{"name":"singbox-panel-web-%s.tar.gz"},{"name":"singbox-panel-agents-%s.tar.gz"}]`, bin, tag, tag)
	}
	body := `[
		{"tag_name":"v1.0.18","name":"v1.0.18","published_at":"2026-11-24T00:00:00Z","assets":` + singleBinary() + `},
		{"tag_name":"v1.0.17","name":"v1.0.17","assets":` + singleBinary() + `},
		{"tag_name":"v1.0.16","name":"v1.0.16","assets":` + legacySplit("v1.0.16") + `},
		{"tag_name":"v1.0.15","draft":true,"assets":` + singleBinary() + `},
		{"tag_name":"v1.0.14","assets":[{"name":` + fmt.Sprintf("%q", bin) + `}]},
		{"tag_name":"bad tag; rm","assets":` + singleBinary() + `}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()

	oldURL := panelReleaseListURL
	panelReleaseListURL = srv.URL
	panelReleaseListMu.Lock()
	panelReleaseListCache = nil
	panelReleaseListMu.Unlock()
	t.Cleanup(func() {
		panelReleaseListURL = oldURL
		panelReleaseListMu.Lock()
		panelReleaseListCache = nil
		panelReleaseListMu.Unlock()
	})

	a := &App{version: "v1.0.17"}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/maintenance/releases?refresh=1", nil)
	a.maintenanceReleases(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Releases []panelReleaseInfo `json:"releases"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Releases) != 2 || got.Releases[0].Version != "v1.0.18" || got.Releases[1].Version != "v1.0.17" {
		t.Fatalf("releases = %+v", got.Releases)
	}
	if got.Releases[0].Current || !got.Releases[1].Current {
		t.Fatalf("current flags wrong: %+v", got.Releases)
	}
}
