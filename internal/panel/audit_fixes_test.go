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
)

// M2: createCustomNode without an explicit all_users must default to "nobody",
// never "publish to every account". Empty user_ids means unassigned.
func TestCreateCustomNodeDefaultsToNobody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	a := &App{db: db}
	r := gin.New()
	r.POST("/custom-nodes", a.createCustomNode)

	body := `{"name":"solo","link":"vless://11111111-2222-3333-4444-555555555555@example.com:443?encryption=none&security=tls#solo"}`
	req := httptest.NewRequest(http.MethodPost, "/custom-nodes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Node model.CustomNode `json:"node"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Node.AllUsers {
		t.Fatalf("node with empty audience defaulted to all_users=true: %+v", resp.Node)
	}
}

// M1: deleting a user removes its ID from every custom node audience list
// (both UserIDs and ExcludedUserIDs), so no stale grant can linger.
func TestDeleteUserCleansCustomNodeAudience(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	u := model.User{Email: "doomed", Password: "x", Role: model.RoleUser, Enabled: true, SubToken: "s1", ProxyToken: "p1"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	assigned := model.CustomNode{Name: "n1", AllUsers: false, UserIDs: []uint{u.ID}}
	if err := db.Create(&assigned).Error; err != nil {
		t.Fatal(err)
	}
	excluded := model.CustomNode{Name: "n2", AllUsers: true, ExcludedUserIDs: []uint{u.ID}}
	if err := db.Create(&excluded).Error; err != nil {
		t.Fatal(err)
	}

	a := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	r := gin.New()
	r.DELETE("/users/:id", a.deleteUser)
	req := httptest.NewRequest(http.MethodDelete, "/users/"+strconv.FormatUint(uint64(u.ID), 10), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", w.Code, w.Body.String())
	}

	var reloaded model.CustomNode
	if err := db.First(&reloaded, assigned.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range reloaded.UserIDs {
		if id == u.ID {
			t.Fatalf("deleted user still in UserIDs: %v", reloaded.UserIDs)
		}
	}
	var reloadedAll model.CustomNode
	if err := db.First(&reloadedAll, excluded.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range reloadedAll.ExcludedUserIDs {
		if id == u.ID {
			t.Fatalf("deleted user still in ExcludedUserIDs: %v", reloadedAll.ExcludedUserIDs)
		}
	}
}

// H2: updateUser must reject unknown inbound IDs with 400 instead of silently
// dropping the assignment.
func TestUpdateUserRejectsUnknownInboundID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	u := model.User{Email: "keep", Password: "x", Role: model.RoleUser, Enabled: true, SubToken: "s2"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{db: db}
	r := gin.New()
	r.PUT("/users/:id", a.updateUser)
	body := `{"inbound_ids":[999999]}`
	req := httptest.NewRequest(http.MethodPut, "/users/"+strconv.FormatUint(uint64(u.ID), 10), bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s (want 400)", w.Code, w.Body.String())
	}
}

// H2: updateUser merges an inbound's owning server into ServerIDs so the two
// lists can never drift (proxy-access refresh covers every provisioned node).
func TestUpdateUserMergesInboundServerIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	srv := model.Server{Name: "srv", AgentToken: "tok"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	inb := model.Inbound{ServerID: srv.ID, Tag: "in", Type: model.InboundVLESS, ListenPort: 443}
	if err := db.Create(&inb).Error; err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "merge", Password: "x", Role: model.RoleUser, Enabled: true, SubToken: "s3"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	r := gin.New()
	r.PUT("/users/:id", a.updateUser)
	body := `{"inbound_ids":[` + strconv.FormatUint(uint64(inb.ID), 10) + `]}`
	req := httptest.NewRequest(http.MethodPut, "/users/"+strconv.FormatUint(uint64(u.ID), 10), bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var reloaded model.User
	if err := db.First(&reloaded, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range reloaded.ServerIDs {
		if id == srv.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("inbound's server %d not merged into ServerIDs: %v", srv.ID, reloaded.ServerIDs)
	}
}
