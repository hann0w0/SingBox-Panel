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

func TestBatchCustomNodeOperations(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/custom-nodes/batch-delete", app.batchDeleteCustomNodes)
	router.POST("/custom-nodes/batch-group", app.batchSetCustomNodeGroup)

	mk := func(name, group string) *model.CustomNode {
		return &model.CustomNode{
			Name: name, Group: group, Enabled: true,
			Link: "socks5://127.0.0.1:1080",
		}
	}
	if err := db.Create(mk("a", "机场A")).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(mk("b", "机场A")).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(mk("c", "机场B")).Error; err != nil {
		t.Fatal(err)
	}
	var all []model.CustomNode
	if err := db.Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("seed count = %d", len(all))
	}

	post := func(path string, body any) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// Batch move: ids 1,2 -> 机场C
	w := post("/custom-nodes/batch-group", map[string]any{"ids": []uint{all[0].ID, all[1].ID}, "group": "机场C"})
	if w.Code != http.StatusOK {
		t.Fatalf("batch-group status = %d body=%s", w.Code, w.Body.String())
	}
	var first, second model.CustomNode
	if err := db.First(&first, all[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&second, all[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if first.Group != "机场C" || second.Group != "机场C" {
		t.Fatalf("groups after move: %s / %s", first.Group, second.Group)
	}
	// Clearing group via empty string.
	w = post("/custom-nodes/batch-group", map[string]any{"ids": []uint{all[1].ID}, "group": "  "})
	if w.Code != http.StatusOK {
		t.Fatalf("batch-group clear status = %d", w.Code)
	}
	if err := db.First(&second, all[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if second.Group != "" {
		t.Fatalf("group not cleared: %q", second.Group)
	}

	// Batch delete two nodes.
	w = post("/custom-nodes/batch-delete", map[string]any{"ids": []uint{all[0].ID, all[1].ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("batch-delete status = %d body=%s", w.Code, w.Body.String())
	}
	var left int64
	if err := db.Model(&model.CustomNode{}).Count(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatalf("nodes left = %d; want 1", left)
	}

	// Empty selection must be rejected.
	w = post("/custom-nodes/batch-delete", map[string]any{"ids": []uint{}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty batch-delete status = %d", w.Code)
	}
}
