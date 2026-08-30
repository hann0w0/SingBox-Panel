package panel

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestUpdateAndApplyServerOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	servers := []model.Server{
		{Name: "a", AgentToken: "order-a"},
		{Name: "b", AgentToken: "order-b"},
		{Name: "c", AgentToken: "order-c"},
	}
	for i := range servers {
		if err := db.Create(&servers[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	a := &App{db: db}
	r := gin.New()
	r.PUT("/servers/order", a.updateServerOrder)
	body := []byte(fmt.Sprintf(`{"ids":[%d,%d,%d]}`, servers[2].ID, servers[0].ID, servers[1].ID))
	req := httptest.NewRequest(http.MethodPut, "/servers/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("order status = %d, body = %s", w.Code, w.Body.String())
	}

	var got []model.Server
	db.Order("id").Find(&got)
	applyServerOrder(db, got)
	if got[0].ID != servers[2].ID || got[1].ID != servers[0].ID || got[2].ID != servers[1].ID {
		t.Fatalf("unexpected order: %v, %v, %v", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestUpdateServerOrderRejectsStaleList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	for _, srv := range []model.Server{{Name: "a", AgentToken: "stale-a"}, {Name: "b", AgentToken: "stale-b"}} {
		if err := db.Create(&srv).Error; err != nil {
			t.Fatal(err)
		}
	}
	a := &App{db: db}
	r := gin.New()
	r.PUT("/servers/order", a.updateServerOrder)
	req := httptest.NewRequest(http.MethodPut, "/servers/order", bytes.NewBufferString(`{"ids":[1]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale order status = %d, body = %s", w.Code, w.Body.String())
	}
}
