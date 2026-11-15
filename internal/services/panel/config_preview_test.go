package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestOfflineConfigPreview(t *testing.T) {
	for _, mode := range []string{model.ConfigModeManaged, model.ConfigModeRaw} {
		t.Run(mode, func(t *testing.T) {
			db := testDB(t)
			server := model.Server{Name: "offline", AgentToken: "offline", ConfigMode: mode, RawConfig: model.JSONText(`{"inbounds":[],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`)}
			if err := db.Create(&server).Error; err != nil {
				t.Fatal(err)
			}
			hub := NewHub(db)
			app := &App{db: db, hub: hub, orch: NewOrchestrator(db, hub)}
			router := gin.New()
			router.GET("/servers/:id/config", app.remoteConfig)
			record := httptest.NewRecorder()
			router.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/servers/"+strconv.Itoa(int(server.ID))+"/config", nil))
			if record.Code != http.StatusOK || record.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("preview: %d %s", record.Code, record.Body.String())
			}
			var result struct {
				Exists bool   `json:"exists"`
				Source string `json:"source"`
				Raw    string `json:"raw"`
			}
			if err := json.Unmarshal(record.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			want, err := app.orch.BuildServerConfig(&server)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Exists || result.Source != "panel" || result.Raw != string(want) || !json.Valid([]byte(result.Raw)) {
				t.Fatalf("incorrect preview: %+v", result)
			}
			record = httptest.NewRecorder()
			router.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/servers/999/config", nil))
			if record.Code != http.StatusNotFound {
				t.Fatalf("missing server: %d %s", record.Code, record.Body.String())
			}
		})
	}
}
