package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestUserOutboundPreviewAndManualCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)

	activeUser := model.User{
		Email: "fwd-user", Password: "hashed-password", Role: model.RoleUser,
		Enabled: true, ForwardingEnabled: true, TokenVersion: 1, SubToken: "sub-fwd",
	}
	lockedUser := model.User{
		Email: "plain-user", Password: "hashed-password", Role: model.RoleUser,
		Enabled: true, ForwardingEnabled: false, TokenVersion: 1, SubToken: "sub-plain",
	}
	if err := db.Create(&activeUser).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&lockedUser).Error; err != nil {
		t.Fatal(err)
	}

	auth := NewAuth("9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a0", db)
	app := &App{db: db, auth: auth, orch: NewOrchestrator(db, NewHub(db))}

	router := gin.New()
	userGroup := router.Group("/api/user")
	userGroup.Use(auth.Middleware())
	userGroup.POST("/outbounds", app.createUserOutbound)
	userGroup.POST("/outbounds/preview", app.previewUserOutbound)

	activeTok, err := auth.Issue(&activeUser, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	lockedTok, err := auth.Issue(&lockedUser, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Preview parses valid URI with Cache-Control: no-store and candidate tag.
	prevReq := httptest.NewRequest(http.MethodPost, "/api/user/outbounds/preview", bytes.NewReader([]byte(`{"uri":"vless://11111111-1111-1111-1111-111111111111@hk.example.com:443?encryption=none&type=tcp#HK-Preview"}`)))
	prevReq.Header.Set("Content-Type", "application/json")
	prevReq.Header.Set("Authorization", "Bearer "+activeTok)
	prevRec := httptest.NewRecorder()
	router.ServeHTTP(prevRec, prevReq)
	if prevRec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", prevRec.Code, prevRec.Body.String())
	}
	if cc := prevRec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	var prevResp struct {
		Outbound struct {
			Tag      string          `json:"tag"`
			Type     string          `json:"type"`
			Settings json.RawMessage `json:"settings"`
		} `json:"outbound"`
	}
	if err := json.Unmarshal(prevRec.Body.Bytes(), &prevResp); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if prevResp.Outbound.Type != "vless" || prevResp.Outbound.Tag != "HK-Preview" {
		t.Fatalf("unexpected preview outbound: %+v", prevResp.Outbound)
	}

	// Preview rejects unauthenticated http:// links and private/loopback targets.
	for _, badURI := range []string{
		"http://hk.example.com:8080#noauth",
		"vless://11111111-1111-1111-1111-111111111111@127.0.0.1:443#loopback",
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/user/outbounds/preview", bytes.NewReader([]byte(`{"uri":"`+badURI+`"}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+activeTok)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %q, got %d (%s)", badURI, rec.Code, rec.Body.String())
		}
	}

	// Manual create stores outbound and auto-suffixes tag collisions.
	manualPayload := []byte(`{"tag":"manual-vless","type":"vless","settings":{"server":"hk.example.com","server_port":443,"uuid":"11111111-1111-1111-1111-111111111111"}}`)
	for idx, wantTag := range []string{"manual-vless", "manual-vless-2"} {
		req := httptest.NewRequest(http.MethodPost, "/api/user/outbounds", bytes.NewReader(manualPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+activeTok)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("manual create #%d status = %d, body = %s", idx+1, rec.Code, rec.Body.String())
		}
		var createResp struct {
			Outbound UserPrivateOutbound `json:"outbound"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
			t.Fatal(err)
		}
		if createResp.Outbound.Tag != wantTag {
			t.Fatalf("manual create #%d tag = %q, want %q", idx+1, createResp.Outbound.Tag, wantTag)
		}
	}

	// Manual create rejects RFC1918 host with 400.
	privateReq := httptest.NewRequest(http.MethodPost, "/api/user/outbounds", bytes.NewReader([]byte(`{"tag":"bad-lan","type":"vless","settings":{"server":"10.0.0.5","server_port":443,"uuid":"11111111-1111-1111-1111-111111111111"}}`)))
	privateReq.Header.Set("Content-Type", "application/json")
	privateReq.Header.Set("Authorization", "Bearer "+activeTok)
	privateRec := httptest.NewRecorder()
	router.ServeHTTP(privateRec, privateReq)
	if privateRec.Code != http.StatusBadRequest {
		t.Fatalf("RFC1918 manual create status = %d, want 400", privateRec.Code)
	}

	// Disabled forwarding account is rejected with 403.
	for _, endpoint := range []string{"/api/user/outbounds/preview", "/api/user/outbounds"} {
		req := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(manualPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+lockedTok)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s locked user status = %d, want 403", endpoint, rec.Code)
		}
	}
}
