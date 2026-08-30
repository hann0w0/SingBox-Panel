package panel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestReadinessReflectsDatabaseConnectivity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	app := &App{db: db}
	router := gin.New()
	router.GET("/api/ready", app.handleReady)

	request := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/ready", nil))
		return recorder
	}

	if recorder := request(); recorder.Code != http.StatusOK {
		t.Fatalf("ready status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if recorder := request(); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed database status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestReadinessRejectsMissingDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := &App{}
	router := gin.New()
	router.GET("/api/ready", app.handleReady)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing database status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
