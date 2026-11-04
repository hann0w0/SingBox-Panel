package panel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestHTTPProxyImportSaveAndSubscriptionRoundTrip(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	alice := model.User{Email: "http-alice", Enabled: true, SubToken: "http-alice-sub"}
	bob := model.User{Email: "http-bob", Enabled: true, SubToken: "http-bob-sub"}
	mustCreate(t, db, &alice)
	mustCreate(t, db, &bob)
	source := model.CustomNodeSubscription{Name: "HTTP fixture", URL: "https://provider.example.com/sub", Enabled: true}
	mustCreate(t, db, &source)
	// The real-world source uses type:http + tls:true, including anonymous
	// nodes and a username with no password. Keep only synthetic endpoints.
	parsed, err := singbox.ParseSubscription([]byte(`proxies:
  - {name: source-anonymous, type: http, server: proxy.example.com, port: 8443, tls: true}
  - {name: source-authenticated, type: http, server: proxy.example.com, port: 8443, tls: true, username: user, password: "secret,with:@symbols", sni: tls.example.com, skip-cert-verify: true, alpn: [h2, http/1.1]}
  - {name: source-username-only, type: http, server: proxy.example.com, port: 8443, tls: true, username: literal-username}
`))
	if err != nil || len(parsed.Nodes) != 3 || len(parsed.Skipped) != 0 {
		t.Fatalf("parse HTTP source: %v, nodes=%d skipped=%d", err, len(parsed.Nodes), len(parsed.Skipped))
	}
	for i, item := range parsed.Nodes {
		row, _, err := subscriptionCustomNode(source, item, i, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		row.UserIDs = []uint{alice.ID}
		mustCreate(t, db, &row)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/nodes", app.createCustomNode)
	router.GET("/sub/:token", app.handleSubscription)
	for _, definition := range []map[string]any{
		{"name": "manual-http", "protocol": "http", "address": "proxy.example.com", "port": 8080, "params": map[string]any{}},
		{"name": "manual-https", "protocol": "https", "address": "proxy.example.com", "port": 8443, "params": map[string]any{"username": "literal-username"}},
		{"name": "uri-https", "link": "https://literal-username@proxy.example.com:8443#URI"},
	} {
		definition["all_users"] = false
		definition["user_ids"] = []uint{alice.ID}
		payload, err := json.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/nodes", bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("create %s: %d %s", definition["name"], response.Code, response.Body.String())
		}
	}
	var canonical model.CustomNode
	if err := db.Where("name = ?", "manual-https").First(&canonical).Error; err != nil {
		t.Fatal(err)
	}
	if canonical.Protocol != "http" {
		t.Fatalf("HTTPS must be stored as HTTP + TLS, got %q", canonical.Protocol)
	}
	unassigned, err := app.gatherNodesWithError(&bob)
	if err != nil || len(unassigned) != 0 {
		t.Fatalf("unassigned user received HTTP nodes: count=%d error=%v", len(unassigned), err)
	}
	for _, target := range []string{"clash", "shadowrocket", "singbox", "surge", "links"} {
		t.Run(target, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sub/"+alice.SubToken+"?target="+target, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("subscription: %d %s", response.Code, response.Body.String())
			}
			result, err := singbox.ParseSubscription(response.Body.Bytes())
			if err != nil || len(result.Nodes) != 6 || len(result.Skipped) != 0 {
				t.Fatalf("exported HTTP subscription cannot round trip: %v, nodes=%d skipped=%d", err, len(result.Nodes), len(result.Skipped))
			}
			for _, item := range result.Nodes {
				if item.Protocol != "http" {
					t.Fatalf("wrong protocol %q", item.Protocol)
				}
				secure := item.Params["tls"] == "tls"
				if secure != (item.Name != "manual-http") {
					t.Fatalf("%s lost TLS mode: %#v", item.Name, item.Params)
				}
				switch item.Name {
				case "source-authenticated":
					if item.Params["username"] != "user" || item.Params["password"] != "secret,with:@symbols" || item.Params["sni"] != "tls.example.com" || item.Params["insecure"] != true || item.Params["alpn"] != "h2,http/1.1" {
						t.Fatalf("HTTP auth/TLS fields changed: %#v", item.Params)
					}
				case "source-username-only", "manual-https", "uri-https":
					password, _ := item.Params["password"].(string)
					if item.Params["username"] != "literal-username" || password != "" {
						t.Fatalf("username-only HTTP authentication changed: %#v", item.Params)
					}
				default:
					username, _ := item.Params["username"].(string)
					password, _ := item.Params["password"].(string)
					if username != "" || password != "" {
						t.Fatalf("anonymous HTTP authentication changed: %#v", item.Params)
					}
				}
			}
		})
	}
}

func TestHTTPProxyRequestFieldsSurviveSave(t *testing.T) {
	req := customNodeReq{Protocol: "http", Address: "proxy.example.com", Port: 8080, Params: json.RawMessage(`{"http_path":"/connect","http_headers":{"X-Test":["one","two"],"Host":"connect.example.com"},"username":"null","password":"None"}`)}
	if _, err := validateCustomNode(&req); err != nil {
		t.Fatal(err)
	}
	n := customNodeFromParams(t, req.Protocol, string(req.Params))
	if n.udpEnabled() {
		t.Fatal("HTTP CONNECT must not advertise UDP")
	}
	raw, err := singbox.BuildClientOutbound(n.clientNode())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := singbox.ParseSubscription([]byte(`{"outbounds":[` + string(raw) + `]}`))
	if err != nil || len(parsed.Nodes) != 1 {
		t.Fatalf("HTTP request fields: %v", err)
	}
	p := parsed.Nodes[0].Params
	if p["http_path"] != "/connect" || p["username"] != "null" || p["password"] != "None" || !reflect.DeepEqual(p["http_headers"], map[string][]string{"X-Test": {"one", "two"}, "Host": {"connect.example.com"}}) {
		t.Fatalf("HTTP request fields changed: %#v", p)
	}
	if clashProxy(n, map[string]int{}) != nil || surgeProxy(n, "HTTP") != "" {
		t.Fatal("formats without a CONNECT path must not silently discard it")
	}
	n.settings.HTTPPath = "/"
	if clashProxy(n, map[string]int{}) != nil || surgeProxy(n, "HTTP") != "" {
		t.Fatal("an explicit root CONNECT path must not be treated as the default")
	}
	n.settings.HTTPPath = ""
	if clashProxy(n, map[string]int{}) != nil {
		t.Fatal("Clash HTTP headers must not contain arrays")
	}
	n.settings.HTTPHeaders = map[string][]string{"Host": {"connect.example.com"}}
	proxy := clashProxy(n, map[string]int{})
	if proxy == nil || !reflect.DeepEqual(proxy["headers"], map[string]any{"Host": "connect.example.com"}) {
		t.Fatalf("Clash must retain representable HTTP headers: %#v", proxy)
	}
}

func TestHTTPProxyExportsRetainOneSidedAuthentication(t *testing.T) {
	for _, params := range []string{
		`{"username":"user"}`,
		`{"password":"secret"}`,
	} {
		n := customNodeFromParams(t, "http", params)
		proxy := clashProxy(n, map[string]int{})
		headers, _ := proxy["headers"].(map[string]any)
		if headers["Proxy-Authorization"] != "Basic "+base64.StdEncoding.EncodeToString([]byte(n.settings.Username+":"+n.settings.Password)) {
			t.Fatalf("Clash one-sided authentication became anonymous: %#v", proxy)
		}
		line := surgeProxy(n, "HTTP")
		if !strings.Contains(line, ", username=") || !strings.Contains(line, ", password=") || !strings.Contains(line, `""`) {
			t.Fatalf("Surge must explicitly retain empty credential fields: %s", line)
		}
		if len(n.settings.HTTPHeaders) != 0 {
			t.Fatal("rendering mutated stored HTTP headers")
		}
	}
	n := customNodeFromParams(t, "http", `{"username":"user","http_headers":{"proxy-authorization":"Bearer explicit"}}`)
	proxy := clashProxy(n, map[string]int{})
	if headers, _ := proxy["headers"].(map[string]any); len(headers) != 1 || headers["proxy-authorization"] != "Bearer explicit" {
		t.Fatalf("explicit HTTP authorization header was overwritten: %#v", proxy)
	}
}

func TestSubscriptionOnlyOutboundTypesKeepServerImportRaw(t *testing.T) {
	for _, typ := range []string{"http", "hysteria"} {
		t.Run(typ, func(t *testing.T) {
			settings := singbox.InboundSettings{TLS: singbox.TLSSettings{Enabled: true}, UpMbps: 30, DownMbps: 80}
			raw, err := singbox.BuildServerConfig(singbox.ServerConfigInput{
				Outbounds: []singbox.OutboundInput{{Type: typ, Tag: "external", Server: "proxy.example.com", ServerPort: 443, Password: "secret", Settings: settings}},
				Final:     "external",
			})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := singbox.ParseServerConfig(raw)
			if err != nil || len(parsed.Outbounds) != 1 {
				t.Fatalf("parse config: %v", err)
			}
			rebuilt, err := buildManagedConfigFromImport(parsed, raw)
			if err != nil || !equivalentJSON(raw, rebuilt) {
				t.Fatalf("fixture must otherwise be a lossless managed candidate: %v", err)
			}
			if mode := importedConfigMode(parsed, raw); mode != model.ConfigModeRaw {
				t.Fatalf("subscription-only protocol must keep raw server editing: %s", mode)
			}
		})
	}
}

func TestCustomNodeProtocolAliasesShareNormalization(t *testing.T) {
	for _, tc := range []struct{ protocol, want, params string }{
		{"socks5", "socks", `{}`},
		{"https", "http", `{}`},
		{"https", "http", `null`},
		{"ss", "shadowsocks", `{"password":"secret","method":"aes-256-gcm"}`},
		{"hy1", "hysteria", `{"password":"secret"}`},
		{"hy2", "hysteria2", `{"password":"secret"}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			req := customNodeReq{Protocol: tc.protocol, Address: "proxy.example.com", Port: 443, Params: json.RawMessage(tc.params)}
			if _, err := validateCustomNode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Protocol != tc.want || normalizeSubscriptionProtocol(tc.protocol) != tc.want {
				t.Fatalf("normalization diverged: protocol=%q filter=%q want=%q", req.Protocol, normalizeSubscriptionProtocol(tc.protocol), tc.want)
			}
			n := customNodeFromParams(t, req.Protocol, string(req.Params))
			if n.typ != tc.want || (tc.protocol == "https" && !n.settings.TLS.Enabled) {
				t.Fatalf("normalized node lost settings: type=%q TLS=%t", n.typ, n.settings.TLS.Enabled)
			}
		})
	}
}
