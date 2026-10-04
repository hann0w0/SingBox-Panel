package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestUserNodesListsGrantedNodesForActiveAccountsOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	srv := model.Server{Name: "香港", AgentToken: "user-nodes-agent", Address: "hk.example.com", Region: "HK"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	const uuid = "11111111-2222-3333-4444-555555555555"
	ib := model.Inbound{
		ServerID: srv.ID, Tag: "vless-in", Type: model.InboundVLESS, ListenPort: 30001, Enabled: true,
		Settings: model.JSONText(`{"uuid":"` + uuid + `"}`),
	}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	active := model.User{Email: "active", Password: "x", Role: model.RoleUser, Enabled: true, ServerIDs: []uint{srv.ID}, SubToken: "nodes-active", ProxyToken: "p-active"}
	disabled := model.User{Email: "disabled", Password: "x", Role: model.RoleUser, Enabled: false, ServerIDs: []uint{srv.ID}, SubToken: "nodes-disabled", ProxyToken: "p-disabled"}
	expired := model.User{Email: "expired", Password: "x", Role: model.RoleUser, Enabled: true, ExpireAt: &past, ServerIDs: []uint{srv.ID}, SubToken: "nodes-expired", ProxyToken: "p-expired"}
	for _, u := range []*model.User{&active, &disabled, &expired} {
		if err := db.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}

	a := &App{db: db}
	get := func(uid uint) *httptest.ResponseRecorder {
		r := gin.New()
		r.GET("/api/user/nodes", func(c *gin.Context) { c.Set("uid", uid) }, a.handleUserNodes)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/user/nodes", nil))
		return w
	}

	w := get(active.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("active user status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Nodes []NodeDetail `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Nodes) != 1 {
		t.Fatalf("active user nodes = %+v; want one node", resp.Nodes)
	}
	n := resp.Nodes[0]
	if n.Name != "香港" || n.Type != "vless" || n.Server != "hk.example.com" || n.Port != 30001 || n.Region != "HK" {
		t.Fatalf("active user node = %+v", n)
	}
	if !strings.HasPrefix(n.Link, "vless://"+uuid+"@") || n.Params["UUID"] != uuid {
		t.Fatalf("node card is missing its share link or params: %+v", n)
	}

	// Same gate as the subscription: a disabled or expired account must not
	// receive node credentials through the dashboard either.
	for _, u := range []model.User{disabled, expired} {
		if w := get(u.ID); w.Code != http.StatusForbidden {
			t.Fatalf("%s user status = %d; want 403, body = %s", u.Email, w.Code, w.Body.String())
		}
	}
}
