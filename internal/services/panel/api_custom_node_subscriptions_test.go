package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestCreateCustomNodeSubscriptionPreservesAutoUpdate(t *testing.T) {
	for _, test := range []struct {
		name       string
		fields     string
		autoUpdate bool
	}{
		{name: "omitted defaults", autoUpdate: true},
		{name: "enabled", fields: `,"auto_update":true`, autoUpdate: true},
		{name: "manual updates", fields: `,"auto_update":false`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := testDB(t)
			app := &App{db: db, customSubscriptions: newCustomNodeSubscriptionManager(db)}
			router := gin.New()
			router.POST("/subscriptions", app.createCustomNodeSubscription)
			// The local URL is rejected before dialing; only persistence is under test.
			body := `{"name":"source","url":"http://127.0.0.1/sub"` + test.fields + `}`
			req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("create status = %d: %s", response.Code, response.Body.String())
			}
			var result struct {
				Subscription model.CustomNodeSubscription `json:"subscription"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var saved model.CustomNodeSubscription
			if err := db.First(&saved, result.Subscription.ID).Error; err != nil {
				t.Fatal(err)
			}
			for name, source := range map[string]model.CustomNodeSubscription{"response": result.Subscription, "database": saved} {
				if source.AutoUpdate != test.autoUpdate {
					t.Errorf("%s auto_update = %t, want %t", name, source.AutoUpdate, test.autoUpdate)
				}
			}
		})
	}
}

func TestCreateCustomNodeSubscriptionRollsBackSwitchFailure(t *testing.T) {
	db := testDB(t)
	if err := db.Exec(`CREATE TRIGGER reject_subscription_switches BEFORE UPDATE OF auto_update ON custom_node_subscriptions
		BEGIN SELECT RAISE(ABORT, 'simulated switch update failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	app := &App{db: db}
	router := gin.New()
	router.POST("/subscriptions", app.createCustomNodeSubscription)
	req := httptest.NewRequest(http.MethodPost, "/subscriptions", bytes.NewBufferString(
		`{"name":"source","url":"http://127.0.0.1/sub","auto_update":false}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("create status = %d: %s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&model.CustomNodeSubscription{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed create retained %d enabled subscriptions", count)
	}
}
