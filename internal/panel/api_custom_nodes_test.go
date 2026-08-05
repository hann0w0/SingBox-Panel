package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/model"
)

func TestCreateCustomNodeAcceptsTypedSnellParams(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/custom-nodes", app.createCustomNode)

	payload, err := json.Marshal(map[string]any{
		"name":      "RFC",
		"protocol":  "snell",
		"address":   "104.21.1.2",
		"port":      10023,
		"all_users": true,
		"enabled":   true,
		"params": map[string]any{
			"psk":       "secret-psk",
			"version":   5,
			"obfs_mode": "none",
			"mode":      "default",
			// The form submits values of several JSON types in one params object.
			"insecure": false,
			"up_mbps":  100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/custom-nodes", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", w.Code, w.Body.String())
	}

	var node model.CustomNode
	if err := db.First(&node).Error; err != nil {
		t.Fatal(err)
	}
	if node.Protocol != "snell" || node.Address != "104.21.1.2" || node.Port != 10023 {
		t.Fatalf("stored node = %+v", node)
	}
	var params struct {
		PSK     string `json:"psk"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(node.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.PSK != "secret-psk" || params.Version != 5 {
		t.Fatalf("stored params = %+v", params)
	}
}

func TestValidateCustomNodeRejectsInvalidSnellVersion(t *testing.T) {
	for _, params := range []string{
		`{"psk":"secret","version":4}`,
		`{"psk":"secret","version":"5"}`,
	} {
		req := customNodeReq{
			Protocol: "snell",
			Address:  "node.example.com",
			Port:     10023,
			Params:   json.RawMessage(params),
		}
		if _, err := validateCustomNode(&req); err == nil {
			t.Fatalf("params %s unexpectedly passed validation", params)
		}
	}
}
