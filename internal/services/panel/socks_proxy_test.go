package panel

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestSOCKSPartialCredentialsImportSaveAndExport(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	user := model.User{Email: "socks-user", Enabled: true, SubToken: "socks-user-sub"}
	mustCreate(t, db, &user)
	source := model.CustomNodeSubscription{Name: "SOCKS fixture", URL: "https://provider.example.com/sub", Enabled: true}
	mustCreate(t, db, &source)
	credentials := map[string][2]string{
		"anonymous":     {"", ""},
		"authenticated": {"alice", "secret"},
		// Keep the percent-encoded, base64-looking username and explicit empty
		// password from the reported URI shape; do not reinterpret credentials.
		"username-only": {base64.StdEncoding.EncodeToString([]byte(":test")), ""},
		"password-only": {"", "p+a:ss/?#"},
	}
	var links []string
	for name, auth := range credentials {
		u := &url.URL{Scheme: "socks5", Host: "proxy.example.com:1080", Fragment: name}
		if auth[0] != "" || auth[1] != "" {
			u.User = url.UserPassword(auth[0], auth[1])
		}
		links = append(links, strings.ReplaceAll(u.String(), "=", "%3D"))
		params, err := json.Marshal(map[string]string{"username": auth[0], "password": auth[1]})
		if err != nil {
			t.Fatal(err)
		}
		for _, protocol := range []string{"socks", "mixed"} {
			req := customNodeReq{Protocol: protocol, Address: "proxy.example.com", Port: 1080, Params: params}
			if _, err := validateCustomNode(&req); err != nil {
				t.Fatalf("structured %s/%s: %v", protocol, name, err)
			}
		}
	}
	parsed, err := singbox.ParseSubscription([]byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))))
	if err != nil || len(parsed.Nodes) != len(credentials) || len(parsed.Skipped) != 0 {
		t.Fatalf("parse: nodes=%d skipped=%d error=%v", len(parsed.Nodes), len(parsed.Skipped), err)
	}
	for i, item := range parsed.Nodes {
		row, _, err := subscriptionCustomNode(source, item, i, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		row.UserIDs = []uint{user.ID}
		mustCreate(t, db, &row)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/sub/:token", app.handleSubscription)
	for _, format := range []string{"links", "singbox", "clash", "shadowrocket", "surge"} {
		t.Run(format, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sub/"+user.SubToken+"?target="+format, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("export: %d %s", response.Code, response.Body.String())
			}
			result, err := singbox.ParseSubscription(response.Body.Bytes())
			if err != nil || len(result.Nodes) != len(credentials) || len(result.Skipped) != 0 {
				t.Fatalf("export parse: nodes=%d skipped=%d error=%v", len(result.Nodes), len(result.Skipped), err)
			}
			seen := map[string]bool{}
			for _, item := range result.Nodes {
				auth, ok := credentials[item.Name]
				username, _ := item.Params["username"].(string)
				password, _ := item.Params["password"].(string)
				if !ok || item.Protocol != "socks" || username != auth[0] || password != auth[1] {
					t.Fatalf("%s credentials changed: %#v", item.Name, item.Params)
				}
				seen[item.Name] = true
			}
			if len(seen) != len(credentials) {
				t.Fatalf("exported names = %v", seen)
			}
		})
	}
}
