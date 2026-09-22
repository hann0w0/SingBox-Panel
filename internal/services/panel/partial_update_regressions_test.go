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

func serveJSONRequest(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	return res
}

func TestUpdateServerPreservesOmittedMetadata(t *testing.T) {
	db := testDB(t)
	srv := model.Server{
		Name: "old-name", Address: "node.example.com", Region: "HK", Remark: "keep this",
		AgentToken: "server-update-token",
	}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}

	app := &App{db: db, serverOperations: newKeyedMutex[uint]()}
	router := gin.New()
	router.PUT("/servers/:id", app.updateServer)
	res := serveJSONRequest(t, router, http.MethodPut, fmt.Sprintf("/servers/%d", srv.ID), `{"name":"new-name"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}

	var got model.Server
	if err := db.First(&got, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Name != "new-name" || got.Address != srv.Address || got.Region != srv.Region || got.Remark != srv.Remark {
		t.Fatalf("partial server update changed omitted metadata: got name=%q address=%q region=%q remark=%q", got.Name, got.Address, got.Region, got.Remark)
	}
}

func TestUpdateInboundPreservesOmittedRemark(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "node", Address: "node.example.com", AgentToken: "inbound-update-token", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	ib := model.Inbound{
		ServerID: srv.ID, Type: model.InboundSocks, Tag: "old-tag", ListenPort: 10080,
		Settings: model.JSONText(`{"single_user":true,"username":"alice","password":"secret"}`),
		Remark:   "keep inbound remark", Enabled: true,
	}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatal(err)
	}

	app := &App{
		db: db, hub: NewHub(db), orch: NewOrchestrator(db, NewHub(db)),
		serverOperations: newKeyedMutex[uint](),
	}
	router := gin.New()
	router.PUT("/servers/:id/inbounds/:inboundID", app.updateInbound)
	path := fmt.Sprintf("/servers/%d/inbounds/%d", srv.ID, ib.ID)
	res := serveJSONRequest(t, router, http.MethodPut, path, `{"tag":"new-tag"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}

	var got model.Inbound
	if err := db.First(&got, ib.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Tag != "new-tag" || got.Remark != ib.Remark {
		t.Fatalf("partial inbound update changed omitted fields: tag=%q remark=%q", got.Tag, got.Remark)
	}
}

func TestUpdateOutboundPreservesOmittedRemarkAndSort(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "node", Address: "node.example.com", AgentToken: "outbound-update-token", ConfigMode: model.ConfigModeManaged}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	ob := model.Outbound{
		ServerID: srv.ID, Tag: "old-tag", Type: "socks",
		Settings: model.JSONText(`{"server":"proxy.example.com","server_port":1080}`),
		Remark:   "keep outbound remark", Sort: 37,
	}
	if err := db.Create(&ob).Error; err != nil {
		t.Fatal(err)
	}

	app := &App{
		db: db, hub: NewHub(db), orch: NewOrchestrator(db, NewHub(db)),
		serverOperations: newKeyedMutex[uint](),
	}
	router := gin.New()
	router.PUT("/servers/:id/outbounds/:outboundID", app.updateOutbound)
	path := fmt.Sprintf("/servers/%d/outbounds/%d", srv.ID, ob.ID)
	res := serveJSONRequest(t, router, http.MethodPut, path, `{"tag":"new-tag"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}

	var got model.Outbound
	if err := db.First(&got, ob.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Tag != "new-tag" || got.Remark != ob.Remark || got.Sort != ob.Sort {
		t.Fatalf("partial outbound update changed omitted fields: tag=%q remark=%q sort=%d", got.Tag, got.Remark, got.Sort)
	}
}
