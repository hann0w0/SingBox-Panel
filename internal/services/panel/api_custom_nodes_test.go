package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
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
		`{"psk":"secret","version":3}`,
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

func TestListCustomNodesIncludesNormalizedDetail(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	if err := db.Create(&model.CustomNode{
		Name: "🇹🇼 TW test", Link: "socks5://alice:secret@example.com:1080#TW%20test", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/custom-nodes", app.listCustomNodes)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/custom-nodes", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Nodes []struct {
			Detail *customNodeDetail `json:"detail"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Nodes) != 1 || response.Nodes[0].Detail == nil {
		t.Fatalf("detail missing: %s", w.Body.String())
	}
	detail := response.Nodes[0].Detail
	if detail.Region != "TW" || detail.Protocol != "socks" || detail.Address != "example.com" || detail.Port != 1080 {
		t.Fatalf("unexpected detail: %+v", detail)
	}
	if detail.URI == "" || !bytes.HasPrefix([]byte(detail.URI), []byte("socks5://alice:secret@example.com:1080")) {
		t.Fatalf("unexpected URI: %q", detail.URI)
	}
	if detail.Params["用户名"] != "alice" || detail.Params["密码"] != "secret" {
		t.Fatalf("unexpected params: %+v", detail.Params)
	}
}

func TestSubscriptionManagedCustomNodeCanBeUpdatedAndDeleted(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	source := model.CustomNodeSubscription{Name: "机场 A", URL: "https://example.com/sub", NodeCount: 1}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	user := model.User{Email: "assigned-user", SubToken: "assigned-user-token"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	node := model.CustomNode{
		Name: "旧名称", Link: "socks5://127.0.0.1:1080", Enabled: true,
		SubscriptionID: &source.ID, SubscriptionKey: "managed-node-key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/custom-nodes/:id", app.updateCustomNode)
	router.DELETE("/custom-nodes/:id", app.deleteCustomNode)

	payload, err := json.Marshal(map[string]any{
		"name": "新名称", "link": "socks5://127.0.0.1:2080", "enabled": true,
		"all_users": false, "user_ids": []uint{user.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	nodePath := "/custom-nodes/" + strconv.FormatUint(uint64(node.ID), 10)
	req := httptest.NewRequest(http.MethodPut, nodePath, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", w.Code, w.Body.String())
	}
	var updated model.CustomNode
	if err := db.First(&updated, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Name != "新名称" || updated.Link != "socks5://127.0.0.1:2080" {
		t.Fatalf("updated node = %+v", updated)
	}
	if len(updated.UserIDs) != 1 || updated.UserIDs[0] != user.ID {
		t.Fatalf("serialized user audience was not persisted: %v", updated.UserIDs)
	}
	if updated.SubscriptionID == nil || *updated.SubscriptionID != source.ID {
		t.Fatalf("subscription ownership was lost: %+v", updated.SubscriptionID)
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, nodePath, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", w.Code, w.Body.String())
	}
	var count int64
	if err := db.Model(&model.CustomNode{}).Where("id = ?", node.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("node still exists after delete")
	}
	if err := db.First(&source, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if source.NodeCount != 0 {
		t.Fatalf("subscription node count = %d; want 0", source.NodeCount)
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
