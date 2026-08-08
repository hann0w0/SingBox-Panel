package singbox

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// TestParseShareLinkRoundTrip builds a node, serializes it to a share link,
// parses it back, and verifies the identity/address survive.
func TestParseShareLinkRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		node ClientNode
	}{
		{
			name: "vless reality ws",
			node: ClientNode{
				Name: "HK 节点", Server: "1.2.3.4", ServerPort: 443, Type: "vless",
				Settings: InboundSettings{
					Flow:           "xtls-rprx-vision",
					PacketEncoding: "xudp",
					Transport:      TransportSettings{Type: "ws", Path: "/path", Headers: map[string]string{"Host": "example.com"}, MaxEarlyData: 2048, EarlyDataHeader: "Sec-WebSocket-Protocol"},
					TLS:            TLSSettings{Enabled: true, ServerName: "example.com", Fingerprint: "firefox", Reality: RealitySettings{Enabled: true, PublicKey: "abc123", ShortID: []string{"abcd"}}},
				},
				User: ProxyUser{UUID: "a2b0c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"},
			},
		},
		{
			name: "vless tcp none",
			node: ClientNode{
				Name: "US", Server: "8.8.8.8", ServerPort: 8443, Type: "vless",
				User: ProxyUser{UUID: "a2b0c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"},
			},
		},
		{
			name: "shadowsocks",
			node: ClientNode{
				Name: "SS", Server: "1.1.1.1", ServerPort: 10009, Type: "shadowsocks",
				Settings: InboundSettings{Method: "aes-256-gcm", SingleUser: true, SSServerPSK: "secret"},
			},
		},
		{
			name: "trojan",
			node: ClientNode{
				Name: "Trojan", Server: "tro.example.com", ServerPort: 443, Type: "trojan",
				Settings: InboundSettings{TLS: TLSSettings{Enabled: true, ServerName: "tro.example.com", Fingerprint: "safari"}},
				User:     ProxyUser{Password: "mypass"},
			},
		},
		{
			name: "trojan httpupgrade",
			node: ClientNode{
				Name: "TrojanUp", Server: "tro2.example.com", ServerPort: 443, Type: "trojan",
				Settings: InboundSettings{
					Transport: TransportSettings{Type: "httpupgrade", Path: "/up", Headers: map[string]string{"Host": "cdn.example.com"}},
					TLS:       TLSSettings{Enabled: true, ServerName: "cdn.example.com", Fingerprint: "random", Insecure: true},
				},
				User: ProxyUser{Password: "pass2"},
			},
		},
		{
			name: "hysteria2",
			node: ClientNode{
				Name: "Hy2", Server: "hy.example.com", ServerPort: 443, Type: "hysteria2",
				Settings: InboundSettings{TLS: TLSSettings{Enabled: true, ServerName: "hy.example.com", ALPN: []string{"h3"}}, ObfsPassword: "obfspw", UpMbps: 50, DownMbps: 100},
				User:     ProxyUser{Password: "hypw"},
			},
		},
		{
			name: "tuic",
			node: ClientNode{
				Name: "TUIC", Server: "tuic.example.com", ServerPort: 443, Type: "tuic",
				Settings: InboundSettings{TLS: TLSSettings{Enabled: true, ServerName: "tuic.example.com", ALPN: []string{"h3"}}, CongestionControl: "bbr", TUICUDPRelayMode: "stable"},
				User:     ProxyUser{UUID: "a2b0c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d", Password: "tuicpw"},
			},
		},
		{
			name: "anytls",
			node: ClientNode{
				Name: "AnyTLS", Server: "any.example.com", ServerPort: 8443, Type: "anytls",
				Settings: InboundSettings{TLS: TLSSettings{Enabled: true, ServerName: "any.example.com", Insecure: true, ALPN: []string{"h2"}, Fingerprint: "firefox"}, AnyTLSUDPOverStream: true},
				User:     ProxyUser{Password: "anypw"},
			},
		},
		{
			name: "vmess ws tls",
			node: ClientNode{
				Name: "Vmess", Server: "vm.example.com", ServerPort: 443, Type: "vmess",
				Settings: InboundSettings{
					VMessSecurity: "auto",
					Transport:     TransportSettings{Type: "ws", Path: "/v2", Headers: map[string]string{"Host": "vm.example.com"}},
					TLS:           TLSSettings{Enabled: true, ServerName: "vm.example.com", Fingerprint: "chrome"},
				},
				User: ProxyUser{UUID: "a2b0c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			link, err := BuildShareLink(tc.node)
			if err != nil {
				t.Fatalf("BuildShareLink: %v", err)
			}
			got, err := ParseShareLink(link)
			if err != nil {
				t.Fatalf("ParseShareLink(%q): %v", link, err)
			}
			if got.Type != tc.node.Type {
				t.Errorf("type = %q, want %q", got.Type, tc.node.Type)
			}
			if got.Server != tc.node.Server {
				t.Errorf("server = %q, want %q", got.Server, tc.node.Server)
			}
			if got.ServerPort != tc.node.ServerPort {
				t.Errorf("port = %d, want %d", got.ServerPort, tc.node.ServerPort)
			}
			if got.Name != tc.node.Name {
				t.Errorf("name = %q, want %q", got.Name, tc.node.Name)
			}
			if got.User.UUID != tc.node.User.UUID {
				t.Errorf("uuid = %q, want %q", got.User.UUID, tc.node.User.UUID)
			}
			if got.User.Password != tc.node.User.Password {
				t.Errorf("password mismatch: got %q want %q", got.User.Password, tc.node.User.Password)
			}
			// shadowsocks: the credential round-trips through SSServerPSK.
			if tc.node.Type == "shadowsocks" && got.Settings.SSServerPSK != tc.node.Settings.SSServerPSK {
				t.Errorf("ss psk = %q, want %q", got.Settings.SSServerPSK, tc.node.Settings.SSServerPSK)
			}
			if tc.node.Settings.TLS.Reality.Enabled && got.Settings.TLS.Reality.PublicKey != tc.node.Settings.TLS.Reality.PublicKey {
				t.Errorf("reality pbk = %q, want %q", got.Settings.TLS.Reality.PublicKey, tc.node.Settings.TLS.Reality.PublicKey)
			}
			if tc.node.Settings.Transport.Type == "ws" && got.Settings.Transport.Path != tc.node.Settings.Transport.Path {
				t.Errorf("ws path = %q, want %q", got.Settings.Transport.Path, tc.node.Settings.Transport.Path)
			}
			if got.Settings.Transport.Type != tc.node.Settings.Transport.Type {
				t.Errorf("transport = %q, want %q", got.Settings.Transport.Type, tc.node.Settings.Transport.Type)
			}
			if got.Settings.Transport.MaxEarlyData != tc.node.Settings.Transport.MaxEarlyData {
				t.Errorf("max early data = %d, want %d", got.Settings.Transport.MaxEarlyData, tc.node.Settings.Transport.MaxEarlyData)
			}
			if got.Settings.Transport.EarlyDataHeader != tc.node.Settings.Transport.EarlyDataHeader {
				t.Errorf("early data header = %q, want %q", got.Settings.Transport.EarlyDataHeader, tc.node.Settings.Transport.EarlyDataHeader)
			}
			if got.Settings.TLS.Fingerprint != tc.node.Settings.TLS.Fingerprint {
				t.Errorf("fingerprint = %q, want %q", got.Settings.TLS.Fingerprint, tc.node.Settings.TLS.Fingerprint)
			}
			if got.Settings.PacketEncoding != tc.node.Settings.PacketEncoding {
				// BuildShareLink defaults vless to xudp; an empty original means
				// "use the default", so the round trip legitimately yields xudp.
				if !(tc.node.Type == "vless" && tc.node.Settings.PacketEncoding == "" && got.Settings.PacketEncoding == "xudp") {
					t.Errorf("packet encoding = %q, want %q", got.Settings.PacketEncoding, tc.node.Settings.PacketEncoding)
				}
			}
			if tc.node.Settings.TLS.Insecure && !got.Settings.TLS.Insecure {
				t.Errorf("insecure should survive round trip")
			}
		})
	}
}

func TestParseShareLinkErrors(t *testing.T) {
	for _, uri := range []string{"", "http://example.com", "vless://", "vmess://not-base64", "ss://bad"} {
		if _, err := ParseShareLink(uri); err == nil {
			t.Errorf("ParseShareLink(%q) should fail", uri)
		}
	}
}

// TestParseShareLinkInsecure verifies the skip-cert-verify flag is recognized
// regardless of the spelling or letter case different clients export, and that
// an explicit "off" value is not misread as on.
func TestParseShareLinkInsecure(t *testing.T) {
	uuid := "a2b0c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
	cases := []struct {
		name string
		uri  string
		want bool
	}{
		{"vless allowInsecure=1", "vless://" + uuid + "@1.2.3.4:443?security=tls&sni=a.com&allowInsecure=1#n", true},
		{"vless lowercase allowinsecure", "vless://" + uuid + "@1.2.3.4:443?security=tls&sni=a.com&allowinsecure=1#n", true},
		{"vless insecure=true", "vless://" + uuid + "@1.2.3.4:443?security=tls&sni=a.com&insecure=true#n", true},
		{"vless skip-cert-verify", "vless://" + uuid + "@1.2.3.4:443?security=tls&sni=a.com&skip-cert-verify=1#n", true},
		{"vless Insecure mixed case", "vless://" + uuid + "@1.2.3.4:443?security=tls&sni=a.com&Insecure=true#n", true},
		{"vless insecure=0 stays off", "vless://" + uuid + "@1.2.3.4:443?security=tls&sni=a.com&insecure=0#n", false},
		{"vless no flag stays off", "vless://" + uuid + "@1.2.3.4:443?security=tls&sni=a.com#n", false},
		{"trojan allowInsecure=true", "trojan://pw@t.com:443?security=tls&sni=t.com&allowInsecure=true#n", true},
		{"trojan allow_insecure", "trojan://pw@t.com:443?allow_insecure=1&sni=t.com#n", true},
		{"hysteria2 insecure=1", "hysteria2://pw@hy.com:443?sni=hy.com&insecure=1#n", true},
		{"hysteria2 skipcertverify", "hysteria2://pw@hy.com:443?sni=hy.com&skip-cert-verify=yes#n", true},
		{"tuic allow_insecure=1", "tuic://" + uuid + ":pw@tu.com:443?sni=tu.com&allow_insecure=1#n", true},
		{"tuic insecure alias", "tuic://" + uuid + ":pw@tu.com:443?sni=tu.com&insecure=on#n", true},
		{"anytls insecure=1", "anytls://pw@a.com:8443?sni=a.com&insecure=1#n", true},
		{"hysteria v1 insecure=1", "hysteria://h.com:443?auth=pw&peer=h.com&insecure=1#n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseShareLink(tc.uri)
			if err != nil {
				t.Fatalf("ParseShareLink(%q): %v", tc.uri, err)
			}
			if got.Settings.TLS.Insecure != tc.want {
				t.Errorf("insecure = %v, want %v", got.Settings.TLS.Insecure, tc.want)
			}
		})
	}
}

// TestParseVMessInsecure covers the base64-JSON vmess payload separately since
// its flag lives in a JSON object rather than the URL query.
func TestParseVMessInsecure(t *testing.T) {
	uuid := "a2b0c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
	cases := []struct {
		name string
		obj  map[string]any
		want bool
	}{
		{"allowInsecure string 1", map[string]any{"add": "v.com", "port": "443", "id": uuid, "tls": "tls", "allowInsecure": "1"}, true},
		{"skip-cert-verify string", map[string]any{"add": "v.com", "port": "443", "id": uuid, "tls": "tls", "skip-cert-verify": "1"}, true},
		{"insecure number 1", map[string]any{"add": "v.com", "port": "443", "id": uuid, "tls": "tls", "insecure": float64(1)}, true},
		{"insecure bool true", map[string]any{"add": "v.com", "port": "443", "id": uuid, "tls": "tls", "insecure": true}, true},
		{"insecure 0 stays off", map[string]any{"add": "v.com", "port": "443", "id": uuid, "tls": "tls", "insecure": "0"}, false},
		{"no flag stays off", map[string]any{"add": "v.com", "port": "443", "id": uuid, "tls": "tls"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.obj)
			uri := "vmess://" + base64.StdEncoding.EncodeToString(raw)
			got, err := ParseShareLink(uri)
			if err != nil {
				t.Fatalf("ParseShareLink: %v", err)
			}
			if got.Settings.TLS.Insecure != tc.want {
				t.Errorf("insecure = %v, want %v", got.Settings.TLS.Insecure, tc.want)
			}
		})
	}
}
