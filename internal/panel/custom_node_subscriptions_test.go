package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

func TestCustomNodeSubscriptionKeyTracksNameNotEndpoint(t *testing.T) {
	a := singbox.ImportedNode{
		Name: "Hong Kong 01", Protocol: "vless", Address: "old.example.com", Port: 443,
		Params: map[string]any{"uuid": "old", "tls": "tls"},
	}
	b := singbox.ImportedNode{
		Name: "Hong Kong 01", Protocol: "vless", Address: "new.example.com", Port: 8443,
		Params: map[string]any{"uuid": "new", "tls": "reality"},
	}
	ka, err := customNodeSubscriptionKey(a, 0)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := customNodeSubscriptionKey(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb {
		t.Fatalf("rotated endpoint changed key: %s != %s", ka, kb)
	}
	b.Name = "Hong Kong 02"
	kc, err := customNodeSubscriptionKey(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if kc == ka {
		t.Fatal("renamed node kept the same key")
	}
}

func TestSubscriptionCustomNodeUsesSourceSettings(t *testing.T) {
	source := model.CustomNodeSubscription{ID: 7, Group: "airport", Enabled: false, BaseSortOrder: 20}
	item := singbox.ImportedNode{
		Name: "node", Protocol: "trojan", Address: "example.com", Port: 443,
		Params: map[string]any{"password": "secret", "tls": "tls"},
	}
	row, key, err := subscriptionCustomNode(source, item, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if row.SubscriptionID == nil || *row.SubscriptionID != source.ID || row.SubscriptionKey != key {
		t.Fatalf("subscription identity not set: %+v", row)
	}
	if row.Group != "airport" || row.Enabled || row.SortOrder != 23 {
		t.Fatalf("source settings not applied: %+v", row)
	}
	if row.AllUsers || len(row.UserIDs) != 0 || len(row.ExcludedUserIDs) != 0 {
		t.Fatalf("new subscription node was assigned to users: %+v", row)
	}
	var params map[string]any
	if err := json.Unmarshal(row.Params, &params); err != nil || params["password"] != "secret" {
		t.Fatalf("params not retained: %s, %v", row.Params, err)
	}
}

func TestUpdateCustomNodeSubscriptionAppliesSourceSettings(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	source := model.CustomNodeSubscription{
		Name: "旧订阅", URL: "https://example.com/old", Group: "旧分组",
		Enabled: true, AutoUpdate: true, UpdateIntervalMinutes: 60, BaseSortOrder: 20,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	node := model.CustomNode{
		Name: "节点", Link: "socks5://127.0.0.1:1080", Group: source.Group,
		Enabled: true, SortOrder: 23, SubscriptionID: &source.ID, SubscriptionKey: "node-key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{
		"name": "新订阅", "url": "https://example.com/new", "group": "新分组",
		"enabled": false, "auto_update": false, "update_interval_minutes": 120,
		"base_sort_order": 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/custom-node-subscriptions/:id", app.updateCustomNodeSubscription)
	path := "/custom-node-subscriptions/" + strconv.FormatUint(uint64(source.ID), 10)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", w.Code, w.Body.String())
	}
	if err := db.First(&source, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if source.Name != "新订阅" || source.Group != "新分组" || source.Enabled || source.AutoUpdate || source.BaseSortOrder != 30 {
		t.Fatalf("subscription not updated: %+v", source)
	}
	if err := db.First(&node, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if node.Group != "新分组" || node.Enabled || node.SortOrder != 33 {
		t.Fatalf("managed node settings not updated: %+v", node)
	}
}

func TestDeleteCustomNodeSubscriptionDeletesManagedNodes(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	source := model.CustomNodeSubscription{Name: "订阅", URL: "https://example.com/sub"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	node := model.CustomNode{
		Name: "节点", Link: "socks5://127.0.0.1:1080", Enabled: true,
		SubscriptionID: &source.ID, SubscriptionKey: "node-key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.DELETE("/custom-node-subscriptions/:id", app.deleteCustomNodeSubscription)
	path := "/custom-node-subscriptions/" + strconv.FormatUint(uint64(source.ID), 10)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", w.Code, w.Body.String())
	}
	var sourceCount, nodeCount int64
	if err := db.Model(&model.CustomNodeSubscription{}).Where("id = ?", source.ID).Count(&sourceCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CustomNode{}).Where("subscription_id = ?", source.ID).Count(&nodeCount).Error; err != nil {
		t.Fatal(err)
	}
	if sourceCount != 0 || nodeCount != 0 {
		t.Fatalf("delete left source=%d nodes=%d", sourceCount, nodeCount)
	}
}

func TestSQLiteDSNAddsPragmas(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{input: "panel.db", want: "panel.db?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"},
		{input: "file:panel.db?cache=shared", want: "file:panel.db?cache=shared&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"},
		{input: ":memory:", want: ":memory:?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"},
	} {
		if got := sqliteDSN(tc.input); got != tc.want {
			t.Fatalf("sqliteDSN(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}
