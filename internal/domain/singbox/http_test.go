package singbox

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestHTTPShareLinkRoundTrip(t *testing.T) {
	for _, secure := range []bool{false, true} {
		for _, auth := range []struct{ username, password string }{
			{}, {"user:name@edge", "p+a:ss/?#"}, {"bnVsbC1saXRlcmFs", ""}, {"null", "None"},
		} {
			n := ClientNode{
				Name: "HTTP / 香港", Type: "http", Server: "2001:db8::1", ServerPort: 8443,
				Settings: InboundSettings{Username: auth.username, Password: auth.password},
			}
			if secure {
				n.Settings.TLS = TLSSettings{Enabled: true, ServerName: "tls.example.com", Insecure: true, ALPN: []string{"h2", "http/1.1"}, Fingerprint: "firefox"}
			}
			link, err := BuildShareLink(n)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseShareLink(link)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(parsed, n) {
				t.Fatalf("round trip %s: got %#v settings %#v, want %#v settings %#v", link, parsed, parsed.Settings, n, n.Settings)
			}
			out, err := BuildClientOutbound(parsed)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatal(err)
			}
			ob, ok := parseOutbound(m)
			if !ok || ob.Type != "http" || ob.Username != auth.username || ob.Password != auth.password || !reflect.DeepEqual(ob.Settings.TLS, n.Settings.TLS) {
				// Client rendering fills a default uTLS fingerprint only when TLS is enabled;
				// all TLS fixtures explicitly specify it to verify its preservation.
				t.Fatalf("outbound round trip lost HTTP settings: %s -> %+v", out, ob)
			}
		}
	}
}

func TestHTTPShareLinkDefaultsAndResourceURLs(t *testing.T) {
	for _, tc := range []struct {
		link string
		port int
		tls  bool
	}{
		{"http://proxy.example.com", 80, false}, {"https://proxy.example.com", 443, true},
	} {
		n, err := ParseShareLink(tc.link)
		if err != nil || n.Type != "http" || n.ServerPort != tc.port || n.Settings.TLS.Enabled != tc.tls {
			t.Fatalf("%s: %+v %v", tc.link, n, err)
		}
	}
	for _, link := range []string{"https://example.com/subscription/token", "http://example.com:99999", "unknown://example.com:443", "snell://secret@example.com:443"} {
		if _, err := ParseShareLink(link); err == nil {
			t.Fatalf("must reject %q", link)
		}
	}
}

func TestHTTPSubscriptionFormats(t *testing.T) {
	for _, tc := range []struct {
		name, raw          string
		username, password string
	}{
		{"clash anonymous", `proxies: [{name: HTTP, type: http, server: proxy.example.com, port: 8443, tls: true, sni: tls.example.com, skip-cert-verify: true, alpn: [h2, http/1.1]}]`, "", ""},
		{"clash username only", `proxies: [{name: HTTP, type: http, server: proxy.example.com, port: 8443, tls: true, username: bnVsbC1saXRlcmFs, sni: tls.example.com, skip-cert-verify: true, alpn: [h2, http/1.1]}]`, "bnVsbC1saXRlcmFs", ""},
		{"clash authenticated", `proxies: [{name: HTTP, type: http, server: proxy.example.com, port: 8443, tls: true, username: user, password: secret, sni: tls.example.com, skip-cert-verify: true, alpn: [h2, http/1.1]}]`, "user", "secret"},
		{"clash https alias", `proxies: [{name: HTTP, type: https, server: proxy.example.com, port: 8443, username: user, password: secret, sni: tls.example.com, skip-cert-verify: true, alpn: "h2,http/1.1"}]`, "user", "secret"},
		{"surge", "[Proxy]\nHTTP = https, proxy.example.com, 8443, username=user, password=secret, sni=tls.example.com, skip-cert-verify=true, alpn=\"h2,http/1.1\"", "user", "secret"},
		{"loon", "[Proxy]\nHTTP = https, proxy.example.com, 8443, user, secret, sni=tls.example.com, skip-cert-verify=true, alpn=\"h2,http/1.1\"", "user", "secret"},
		{"quantumult", "[server_local]\nhttp = proxy.example.com:8443, username=user, password=secret, over-tls=true, tls-host=tls.example.com, tls-verification=false, alpn=\"h2,http/1.1\", tag=HTTP", "user", "secret"},
		{"v2ray", `{"outbounds":[{"tag":"HTTP","protocol":"http","settings":{"servers":[{"address":"proxy.example.com","port":8443,"users":[{"user":"user","pass":"secret"}]}]},"streamSettings":{"security":"tls","tlsSettings":{"serverName":"tls.example.com","allowInsecure":true,"alpn":["h2","http/1.1"]}}}]}`, "user", "secret"},
		{"sing-box", `{"outbounds":[{"tag":"HTTP","type":"http","server":"proxy.example.com","server_port":8443,"username":"user","password":"secret","tls":{"enabled":true,"server_name":"tls.example.com","insecure":true,"alpn":["h2","http/1.1"]}}]}`, "user", "secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := ParseSubscription([]byte(tc.raw))
			if err != nil || len(r.Nodes) != 1 || len(r.Skipped) != 0 {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			n := r.Nodes[0]
			if n.Protocol != "http" || n.Params["tls"] != "tls" || n.Params["sni"] != "tls.example.com" || n.Params["insecure"] != true || n.Params["alpn"] != "h2,http/1.1" {
				t.Fatalf("HTTP/TLS fields lost: %+v", n)
			}
			if value, _ := n.Params["username"].(string); value != tc.username {
				t.Fatalf("username lost: %#v", n.Params)
			}
			if value, _ := n.Params["password"].(string); value != tc.password {
				t.Fatalf("password lost: %#v", n.Params)
			}
		})
	}
	link := "https://user:secret@proxy.example.com:8443?sni=tls.example.com#HTTP"
	for _, raw := range []string{link, base64.StdEncoding.EncodeToString([]byte(link))} {
		r, err := ParseSubscription([]byte(raw))
		if err != nil || len(r.Nodes) != 1 || r.Nodes[0].Params["username"] != "user" || r.Nodes[0].Params["tls"] != "tls" {
			t.Fatalf("HTTP URI list: %+v %v", r, err)
		}
	}
}

func TestHTTPStructuredPathAndHeaders(t *testing.T) {
	for _, raw := range []string{
		`{"outbounds":[{"type":"http","tag":"HTTP","server":"proxy.example.com","server_port":8080,"path":"/connect","headers":{"X-Test":["one","two"],"Host":"proxy.example.com"}}]}`,
		`proxies: [{name: HTTP, type: http, server: proxy.example.com, port: 8080, path: /connect, headers: {X-Test: [one, two], Host: proxy.example.com}}]`,
	} {
		r, err := ParseSubscription([]byte(raw))
		if err != nil || len(r.Nodes) != 1 {
			t.Fatalf("parse: %+v %v", r, err)
		}
		p := r.Nodes[0].Params
		headers, ok := p["http_headers"].(map[string][]string)
		if p["http_path"] != "/connect" || p["udp"] != false || !ok || !reflect.DeepEqual(headers["X-Test"], []string{"one", "two"}) || !reflect.DeepEqual(headers["Host"], []string{"proxy.example.com"}) {
			t.Fatalf("HTTP path/headers lost: %#v", p)
		}
		n := ClientNode{Type: "http", Name: "HTTP", Server: "proxy.example.com", ServerPort: 8080, Settings: InboundSettings{HTTPPath: p["http_path"].(string), HTTPHeaders: headers}}
		out, err := BuildClientOutbound(n)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		ob, ok := parseOutbound(m)
		if !ok || ob.Settings.HTTPPath != "/connect" || !reflect.DeepEqual(ob.Settings.HTTPHeaders, headers) {
			t.Fatalf("outbound lost HTTP request fields: %s", out)
		}
		if _, err := BuildShareLink(n); err == nil {
			t.Fatal("HTTP URI must report unrepresentable path/headers")
		}
	}
}

func TestHTTPRootCONNECTPathRequiresStructuredFormat(t *testing.T) {
	raw := `{"outbounds":[{"type":"http","tag":"HTTP","server":"proxy.example.com","server_port":8080,"path":"/"}]}`
	r, err := ParseSubscription([]byte(raw))
	if err != nil || len(r.Nodes) != 1 || r.Nodes[0].Params["http_path"] != "/" {
		t.Fatalf("explicit root CONNECT path lost: %+v %v", r, err)
	}
	n := ClientNode{Type: "http", Server: "proxy.example.com", ServerPort: 8080, Settings: InboundSettings{HTTPPath: "/"}}
	if _, err := BuildShareLink(n); err == nil {
		t.Fatal("explicit root CONNECT path cannot be expressed in a proxy URI")
	}
	out, err := BuildClientOutbound(n)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["path"] != "/" {
		t.Fatalf("root CONNECT path lost from outbound: %s", out)
	}
	endpoint, err := ParseShareLink("http://proxy.example.com:8080/")
	if err != nil || endpoint.Settings.HTTPPath != "" {
		t.Fatalf("URI root must remain an ordinary endpoint: %+v %v", endpoint, err)
	}
}

func TestHTTPPasswordOnlyOutboundAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name     string
		user     ProxyUser
		settings InboundSettings
		want     map[string][]string
	}{
		{name: "settings password", settings: InboundSettings{Password: "secret"}, want: map[string][]string{"Proxy-Authorization": {"Basic OnNlY3JldA=="}}},
		{name: "user password", user: ProxyUser{Password: "secret"}, want: map[string][]string{"Proxy-Authorization": {"Basic OnNlY3JldA=="}}},
		{name: "other headers retained", settings: InboundSettings{Password: "secret", HTTPHeaders: map[string][]string{"X-Test": {"one", "two"}}}, want: map[string][]string{"X-Test": {"one", "two"}, "Proxy-Authorization": {"Basic OnNlY3JldA=="}}},
		{name: "explicit authorization retained", settings: InboundSettings{Password: "secret", HTTPHeaders: map[string][]string{"proxy-authorization": {"Bearer explicit"}}}, want: map[string][]string{"proxy-authorization": {"Bearer explicit"}}},
		{name: "empty explicit authorization retained", settings: InboundSettings{Password: "secret", HTTPHeaders: map[string][]string{"PROXY-AUTHORIZATION": {""}}}, want: map[string][]string{"PROXY-AUTHORIZATION": {""}}},
		{name: "username uses native authentication", settings: InboundSettings{Username: "user", Password: "secret"}, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.settings)
			if err != nil {
				t.Fatal(err)
			}
			n := ClientNode{Type: "http", Server: "proxy.example.com", ServerPort: 8080, User: tc.user, Settings: tc.settings}
			out, err := BuildClientOutbound(n)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatal(err)
			}
			got := stringValuesMap(mMap(m, "headers"))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("authorization headers = %#v, want %#v; outbound=%s", got, tc.want, out)
			}
			after, err := json.Marshal(tc.settings)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("caller settings changed: before=%s after=%s", before, after)
			}
		})
	}
}

func TestHysteriaOutboundImportRoundTrip(t *testing.T) {
	n := ClientNode{Type: "hysteria", Name: "Hy1", Server: "hy.example.com", ServerPort: 443, User: ProxyUser{Password: "auth:literal"}, Settings: InboundSettings{TLS: TLSSettings{Enabled: true, ServerName: "tls.example.com", Insecure: true, ALPN: []string{"h3"}}, UpMbps: 30, DownMbps: 80, ObfsPassword: "obfs"}}
	out, err := BuildClientOutbound(n)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseSubscription([]byte(`{"outbounds":[` + string(out) + `]}`))
	if err != nil || len(r.Nodes) != 1 || len(r.Skipped) != 0 {
		t.Fatalf("hysteria import: %+v %v", r, err)
	}
	p := r.Nodes[0].Params
	if p["password"] != "auth:literal" || p["obfs_password"] != "obfs" || p["up_mbps"] != 30 || p["down_mbps"] != 80 || p["alpn"] != "h3" {
		t.Fatalf("hysteria fields lost: %#v", p)
	}
}

func TestNormalizeExternalProtocolAliases(t *testing.T) {
	for input, want := range map[string]string{" HTTPS ": "http", "socks5": "socks", "ss": "shadowsocks", "hy1": "hysteria", "hysteria1": "hysteria", "mixed": "mixed", "unknown": "unknown"} {
		if got := NormalizeProtocol(input); got != want {
			t.Fatalf("%q => %q, want %q", input, got, want)
		}
	}
	for _, raw := range []string{`proxies: [{name: unsupported, type: unknown, server: proxy.example.com, port: 443}]`, `{"outbounds":[{"type":"unknown","server":"proxy.example.com","server_port":443}]}`} {
		r, err := ParseSubscription([]byte(raw))
		if err == nil || len(r.Nodes) != 0 || len(r.Skipped) != 1 {
			t.Fatalf("unknown protocol accepted: %+v %v", r, err)
		}
	}
	mixed, err := BuildShareLink(ClientNode{Type: "mixed", Server: "proxy.example.com", ServerPort: 1080})
	if err != nil || !strings.HasPrefix(mixed, "socks5://") {
		t.Fatalf("mixed client changed: %s %v", mixed, err)
	}
}
