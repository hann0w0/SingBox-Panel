package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/model"
)

func TestUserAccessReplacesAssignmentsAtomically(t *testing.T) {
	db := testDB(t)
	serverA := model.Server{Name: "A", AgentToken: "access-a"}
	serverB := model.Server{Name: "B", AgentToken: "access-b"}
	if err := db.Create(&serverA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&serverB).Error; err != nil {
		t.Fatal(err)
	}
	inbounds := []model.Inbound{
		{ServerID: serverA.ID, Tag: "a1", Type: model.InboundVLESS, ListenPort: 10001, Enabled: true},
		{ServerID: serverA.ID, Tag: "a2", Type: model.InboundTrojan, ListenPort: 10002, Enabled: true},
		{ServerID: serverB.ID, Tag: "b1", Type: model.InboundShadowsocks, ListenPort: 10003, Enabled: true},
	}
	if err := db.Create(&inbounds).Error; err != nil {
		t.Fatal(err)
	}
	user := model.User{
		Email: "access-user", Password: "unused", Role: model.RoleUser, Enabled: true,
		ServerIDs: []uint{serverA.ID}, InboundIDs: []uint{}, SubToken: "access-user-token",
	}
	other := model.User{
		Email: "future-user", Password: "unused", Role: model.RoleUser, Enabled: true,
		SubToken: "future-user-token",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	globalNode := model.CustomNode{Name: "global", Link: "socks5://127.0.0.1:1080#global", AllUsers: true, Enabled: true}
	scopedNode := model.CustomNode{Name: "scoped", Link: "socks5://127.0.0.1:1081#scoped", AllUsers: false, Enabled: true}
	if err := db.Create(&globalNode).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&scopedNode).Error; err != nil {
		t.Fatal(err)
	}

	legacy, err := effectiveUserAccess(db, &user)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(legacy.InboundIDs), fmt.Sprint([]uint{inbounds[0].ID, inbounds[1].ID}); got != want {
		t.Fatalf("legacy wildcard inbounds = %s; want %s", got, want)
	}
	if got, want := fmt.Sprint(legacy.CustomNodeIDs), fmt.Sprint([]uint{globalNode.ID}); got != want {
		t.Fatalf("initial custom nodes = %s; want %s", got, want)
	}

	gin.SetMode(gin.TestMode)
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	router := gin.New()
	router.PUT("/users/:id/access", app.updateUserAccess)

	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/users/%d/access", user.ID), bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	validBody, err := json.Marshal(map[string]any{
		"inbound_ids":     []uint{inbounds[2].ID, inbounds[0].ID, inbounds[0].ID},
		"custom_node_ids": []uint{scopedNode.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := put(string(validBody)); w.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", w.Code, w.Body.String())
	}
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(user.ServerIDs), fmt.Sprint([]uint{serverA.ID, serverB.ID}); got != want {
		t.Fatalf("server ids = %s; want %s", got, want)
	}
	if got, want := fmt.Sprint(user.InboundIDs), fmt.Sprint([]uint{inbounds[0].ID, inbounds[2].ID}); got != want {
		t.Fatalf("inbound ids = %s; want %s", got, want)
	}
	if err := db.First(&globalNode, globalNode.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&scopedNode, scopedNode.ID).Error; err != nil {
		t.Fatal(err)
	}
	if globalNode.HasUser(user.ID) || !globalNode.HasUser(other.ID) {
		t.Fatalf("global audience exclusion is wrong: %+v", globalNode.ExcludedUserIDs)
	}
	if !scopedNode.HasUser(user.ID) || scopedNode.HasUser(other.ID) {
		t.Fatalf("scoped audience is wrong: %+v", scopedNode.UserIDs)
	}

	beforeServers := fmt.Sprint(user.ServerIDs)
	beforeInbounds := fmt.Sprint(user.InboundIDs)
	invalidBody, err := json.Marshal(map[string]any{
		"inbound_ids":     []uint{inbounds[1].ID},
		"custom_node_ids": []uint{scopedNode.ID + globalNode.ID + 9999},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := put(string(invalidBody)); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid update status = %d, body=%s", w.Code, w.Body.String())
	}
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(user.ServerIDs) != beforeServers || fmt.Sprint(user.InboundIDs) != beforeInbounds {
		t.Fatalf("invalid request changed managed access: servers=%v inbounds=%v", user.ServerIDs, user.InboundIDs)
	}
}

func TestCustomNodeHasUser(t *testing.T) {
	for _, tc := range []struct {
		name string
		node model.CustomNode
		want bool
	}{
		{name: "empty audience", node: model.CustomNode{}, want: false},
		{name: "allow list", node: model.CustomNode{UserIDs: []uint{7}}, want: true},
		{name: "all users", node: model.CustomNode{AllUsers: true}, want: true},
		{name: "excluded from all", node: model.CustomNode{AllUsers: true, ExcludedUserIDs: []uint{7}}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.node.HasUser(7); got != tc.want {
				t.Fatalf("HasUser(7) = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestListUsersIncludesEffectiveNodeCount(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "count-server", AgentToken: "count-token"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	inbounds := []model.Inbound{
		{ServerID: server.ID, Tag: "one", Type: model.InboundVLESS, ListenPort: 12001, Enabled: true},
		{ServerID: server.ID, Tag: "two", Type: model.InboundTrojan, ListenPort: 12002, Enabled: true},
	}
	if err := db.Create(&inbounds).Error; err != nil {
		t.Fatal(err)
	}
	wildcardUser := model.User{
		Email: "wildcard", Password: "unused", Role: model.RoleUser, Enabled: true,
		ServerIDs: []uint{server.ID}, SubToken: "count-wildcard-token",
	}
	explicitUser := model.User{
		Email: "explicit", Password: "unused", Role: model.RoleUser, Enabled: true,
		ServerIDs: []uint{server.ID}, InboundIDs: []uint{inbounds[0].ID}, SubToken: "count-explicit-token",
	}
	if err := db.Create(&wildcardUser).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&explicitUser).Error; err != nil {
		t.Fatal(err)
	}
	nodes := []model.CustomNode{
		{Name: "global", Link: "socks5://127.0.0.1:13001#global", AllUsers: true, Enabled: true, ExcludedUserIDs: []uint{explicitUser.ID}},
		{Name: "scoped", Link: "socks5://127.0.0.1:13002#scoped", UserIDs: []uint{wildcardUser.ID}, Enabled: true},
	}
	if err := db.Create(&nodes).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	app := &App{db: db}
	router := gin.New()
	router.GET("/users", app.listUsers)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Users []struct {
			ID        uint `json:"id"`
			NodeCount int  `json:"node_count"`
		} `json:"users"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	counts := make(map[uint]int, len(response.Users))
	for _, user := range response.Users {
		counts[user.ID] = user.NodeCount
	}
	if got, want := counts[wildcardUser.ID], 4; got != want {
		t.Fatalf("wildcard node_count = %d; want %d", got, want)
	}
	if got, want := counts[explicitUser.ID], 1; got != want {
		t.Fatalf("explicit node_count = %d; want %d", got, want)
	}
}
