package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestAdminSelfPasswordUpdateRotatesTokenWithoutLoggingOut(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)

	admin := model.User{
		Email: "root-admin", Password: "hashed-old-password", Role: model.RoleAdmin,
		Enabled: true, TokenVersion: 1, SubToken: "sub-admin",
	}
	other := model.User{
		Email: "normal-user", Password: "hashed-user-password", Role: model.RoleUser,
		Enabled: true, TokenVersion: 1, SubToken: "sub-user",
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}

	auth := NewAuth("9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a0", db)
	app := &App{db: db, auth: auth, orch: NewOrchestrator(db, NewHub(db))}

	oldTok, err := auth.Issue(&admin, time.Hour)
	if err != nil {
		t.Fatalf("issue initial token: %v", err)
	}

	router := gin.New()
	router.PUT("/api/admin/users/:id", auth.Middleware(), AdminOnly(), app.updateUser)
	router.GET("/api/ping", auth.Middleware(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"uid": currentUID(c)})
	})

	reqBody := []byte(`{"password":"new-admin-password-2026"}`)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/admin/users/%d", admin.ID), bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+oldTok)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("self password update status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var selfResp struct {
		User  model.User `json:"user"`
		Token string     `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &selfResp); err != nil {
		t.Fatalf("decode self update response: %v", err)
	}
	if selfResp.Token == "" || selfResp.Token == oldTok {
		t.Fatalf("expected rotated replacement token, got %q (old=%q)", selfResp.Token, oldTok)
	}
	var refreshed model.User
	if err := db.First(&refreshed, admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.TokenVersion <= admin.TokenVersion {
		t.Fatalf("expected token_version increment in DB, got %d <= %d", refreshed.TokenVersion, admin.TokenVersion)
	}

	// Old token must immediately fail session verification after token_version bump.
	oldPing := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	oldPing.Header.Set("Authorization", "Bearer "+oldTok)
	oldPingRec := httptest.NewRecorder()
	router.ServeHTTP(oldPingRec, oldPing)
	if oldPingRec.Code != http.StatusUnauthorized {
		t.Fatalf("old token status = %d, want 401", oldPingRec.Code)
	}

	// Replacement token must authenticate subsequent requests seamlessly.
	newPing := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	newPing.Header.Set("Authorization", "Bearer "+selfResp.Token)
	newPingRec := httptest.NewRecorder()
	router.ServeHTTP(newPingRec, newPing)
	if newPingRec.Code != http.StatusOK {
		t.Fatalf("replacement token status = %d, want 200 (%s)", newPingRec.Code, newPingRec.Body.String())
	}

	// Updating another user must NOT return a session token for the target user.
	otherReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/admin/users/%d", other.ID), bytes.NewReader([]byte(`{"password":"other-new-password-2026"}`)))
	otherReq.Header.Set("Content-Type", "application/json")
	otherReq.Header.Set("Authorization", "Bearer "+selfResp.Token)
	otherRec := httptest.NewRecorder()
	router.ServeHTTP(otherRec, otherReq)
	if otherRec.Code != http.StatusOK {
		t.Fatalf("other password update status = %d, body = %s", otherRec.Code, otherRec.Body.String())
	}
	var otherResp map[string]any
	if err := json.Unmarshal(otherRec.Body.Bytes(), &otherResp); err != nil {
		t.Fatalf("decode other update response: %v", err)
	}
	if tok, hasToken := otherResp["token"]; hasToken && tok != "" {
		t.Fatalf("updating another user unexpectedly returned token: %v", tok)
	}
}
