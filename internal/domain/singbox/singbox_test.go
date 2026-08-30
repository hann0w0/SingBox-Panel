package singbox

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func sampleUsers() []ProxyUser {
	return []ProxyUser{
		{Name: "u1", UUID: "bf000d23-0752-40b4-affe-68f7707a9661", Password: "pw-user-1"},
		{Name: "u2", UUID: "cf000d23-0752-40b4-affe-68f7707a9662", Password: "pw-user-2"},
	}
}

func TestBuildServerConfig_VLESSReality(t *testing.T) {
	in := ServerConfigInput{
		Inbounds: []InboundInput{{
			Tag:        "vless-in",
			Type:       "vless",
			ListenPort: 443,
			Settings: InboundSettings{
				Flow: "xtls-rprx-vision",
				TLS: TLSSettings{
					Reality: RealitySettings{
						Enabled:         true,
						HandshakeServer: "www.microsoft.com",
						PrivateKey:      "UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc",
						PublicKey:       "some-public-key",
						ShortID:         []string{"0123456789abcdef"},
					},
				},
			},
			Users: sampleUsers(),
		}},
	}
	raw, err := BuildServerConfig(in)
	if err != nil {
		t.Fatalf("BuildServerConfig: %v", err)
	}

	// Must be valid JSON.
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("generated config is not valid JSON: %v\n%s", err, raw)
	}

	// Outbounds must be direct only — no removed special outbounds.
	obs, _ := cfg["outbounds"].([]any)
	if len(obs) != 1 {
		t.Fatalf("want 1 outbound, got %d", len(obs))
	}
	if typ := obs[0].(map[string]any)["type"]; typ != "direct" {
		t.Fatalf("want direct outbound, got %v", typ)
	}
	s := string(raw)
	for _, forbidden := range []string{`"type":"block"`, `"type": "block"`, `"outbound":"dns"`, `"outbound": "dns"`} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("config contains removed legacy construct %q", forbidden)
		}
	}

	// The panel does not do traffic accounting, so no experimental/clash_api
	// block should ever be emitted.
	if _, ok := cfg["experimental"]; ok {
		t.Fatal("config must not contain an experimental block")
	}

	// Inbound users must carry the stable names u1, u2.
	ibs := cfg["inbounds"].([]any)
	users := ibs[0].(map[string]any)["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("want 2 users, got %d", len(users))
	}
	if users[0].(map[string]any)["name"] != "u1" {
		t.Fatalf("want user name u1, got %v", users[0].(map[string]any)["name"])
	}
	if users[0].(map[string]any)["flow"] != "xtls-rprx-vision" {
		t.Fatalf("want flow xtls-rprx-vision, got %v", users[0].(map[string]any)["flow"])
	}
}

func TestBuildInbound_TLSRequired(t *testing.T) {
	// hysteria2 without TLS must be rejected.
	_, err := BuildInbound(InboundInput{
		Tag: "hy2", Type: "hysteria2", ListenPort: 8443,
		Settings: InboundSettings{},
		Users:    sampleUsers(),
	})
	if err == nil {
		t.Fatal("expected error for hysteria2 without TLS")
	}
}

func TestBuildInbound_Shadowsocks2022MultiUser(t *testing.T) {
	raw, err := BuildInbound(InboundInput{
		Tag: "ss", Type: "shadowsocks", ListenPort: 8388,
		Settings: InboundSettings{Method: "2022-blake3-aes-128-gcm", SSServerPSK: "c2VydmVyLXBzay0xNmJ5"},
		Users:    sampleUsers(),
	})
	if err != nil {
		t.Fatalf("BuildInbound ss: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	users := m["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("want 2 ss users, got %d", len(users))
	}
	// Per-user derived key must be deterministic and 16-byte base64 (24 chars).
	pw := users[0].(map[string]any)["password"].(string)
	if pw != DeriveSSUserKey("pw-user-1", "2022-blake3-aes-128-gcm") {
		t.Fatal("ss user key not deterministic")
	}
	if len(pw) != 24 {
		t.Fatalf("want 16-byte base64 key (24 chars), got %d: %q", len(pw), pw)
	}
}

func TestBuildShadowsocksAES128GCMInboundAndOutbound(t *testing.T) {
	settings := InboundSettings{
		Method:      "aes-128-gcm",
		SSServerPSK: "legacy-secret",
		SingleUser:  true,
	}
	raw, err := BuildInbound(InboundInput{
		Tag: "ss-aes128", Type: "shadowsocks", ListenPort: 8388, Settings: settings,
	})
	if err != nil {
		t.Fatalf("BuildInbound aes-128-gcm: %v", err)
	}
	var inbound map[string]any
	if err := json.Unmarshal(raw, &inbound); err != nil {
		t.Fatal(err)
	}
	if inbound["method"] != "aes-128-gcm" || inbound["password"] != "legacy-secret" {
		t.Fatalf("unexpected aes-128-gcm inbound: %#v", inbound)
	}
	if _, ok := inbound["users"]; ok {
		t.Fatalf("legacy Shadowsocks method must use one shared password: %#v", inbound)
	}

	out, err := BuildClientOutbound(ClientNode{
		Name: "ss-aes128", Server: "ss.example.com", ServerPort: 8388,
		Type: "shadowsocks", Settings: settings,
	})
	if err != nil {
		t.Fatalf("BuildClientOutbound aes-128-gcm: %v", err)
	}
	var outbound map[string]any
	if err := json.Unmarshal(out, &outbound); err != nil {
		t.Fatal(err)
	}
	if outbound["method"] != "aes-128-gcm" || outbound["password"] != "legacy-secret" {
		t.Fatalf("unexpected aes-128-gcm outbound: %#v", outbound)
	}
}

func TestBuildInbound_SnellAlwaysUsesSharedPSK(t *testing.T) {
	raw, err := BuildInbound(InboundInput{
		Tag: "snell", Type: "snell", ListenPort: 443,
		Settings: InboundSettings{SnellVersion: 5, SnellPSK: "shared-psk", SnellObfsMode: "tls", MultiUser: true},
		Users:    sampleUsers(),
	})
	if err != nil {
		t.Fatalf("BuildInbound snell: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["psk"] != "shared-psk" || got["obfs_mode"] != "tls" {
		t.Fatalf("Snell shared settings = %#v", got)
	}
	if _, ok := got["users"]; ok {
		t.Fatalf("Snell must not emit users: %#v", got)
	}
}

func TestBuildHysteria2GeckoParameters(t *testing.T) {
	settings := InboundSettings{
		TLS:      TLSSettings{Enabled: true, CertificatePath: "cert.pem", KeyPath: "key.pem"},
		ObfsType: "gecko", ObfsPassword: "gecko-secret", GeckoMinPacketSize: 512, GeckoMaxPacketSize: 1200,
	}
	raw, err := BuildInbound(InboundInput{Tag: "hy2", Type: "hysteria2", ListenPort: 443, Settings: settings, Users: sampleUsers()})
	if err != nil {
		t.Fatalf("BuildInbound hysteria2 gecko: %v", err)
	}
	var inbound map[string]any
	if err := json.Unmarshal(raw, &inbound); err != nil {
		t.Fatal(err)
	}
	obfs := inbound["obfs"].(map[string]any)
	if obfs["type"] != "gecko" || obfs["password"] != "gecko-secret" || obfs["min_packet_size"] != float64(512) || obfs["max_packet_size"] != float64(1200) {
		t.Fatalf("Gecko inbound = %#v", obfs)
	}
	out, err := BuildClientOutbound(ClientNode{Name: "hy2", Server: "hy.example.com", ServerPort: 443, Type: "hysteria2", Settings: settings, User: ProxyUser{Password: "gecko-secret"}})
	if err != nil {
		t.Fatalf("BuildClientOutbound hysteria2 gecko: %v", err)
	}
	var outbound map[string]any
	if err := json.Unmarshal(out, &outbound); err != nil {
		t.Fatal(err)
	}
	clientObfs := outbound["obfs"].(map[string]any)
	if clientObfs["type"] != "gecko" || clientObfs["min_packet_size"] != float64(512) || clientObfs["max_packet_size"] != float64(1200) {
		t.Fatalf("Gecko outbound = %#v", clientObfs)
	}
}

func TestBuildShareLinks(t *testing.T) {
	nodes := []ClientNode{
		{Name: "n-vless", Server: "1.2.3.4", ServerPort: 443, Type: "vless",
			Settings: InboundSettings{Flow: "xtls-rprx-vision", TLS: TLSSettings{Reality: RealitySettings{Enabled: true, HandshakeServer: "www.microsoft.com", PublicKey: "pbk", ShortID: []string{"abcd"}}}},
			User:     ProxyUser{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"}},
		{Name: "n-trojan", Server: "1.2.3.4", ServerPort: 443, Type: "trojan",
			Settings: InboundSettings{TLS: TLSSettings{Enabled: true, ServerName: "example.com"}},
			User:     ProxyUser{Password: "pw"}},
		{Name: "n-ss", Server: "1.2.3.4", ServerPort: 8388, Type: "shadowsocks",
			Settings: InboundSettings{Method: "2022-blake3-aes-128-gcm", SSServerPSK: "c2VydmVyLXBzay0xNmJ5"},
			User:     ProxyUser{Password: "pw"}},
		{Name: "n-hy2", Server: "1.2.3.4", ServerPort: 8443, Type: "hysteria2",
			Settings: InboundSettings{ObfsPassword: "obfs", TLS: TLSSettings{Enabled: true, ServerName: "example.com", Insecure: true}},
			User:     ProxyUser{Password: "pw"}},
		{Name: "n-tuic", Server: "1.2.3.4", ServerPort: 8444, Type: "tuic",
			Settings: InboundSettings{TLS: TLSSettings{Enabled: true, ServerName: "example.com"}},
			User:     ProxyUser{UUID: "bf000d23", Password: "pw"}},
	}
	prefixes := map[string]string{
		"n-vless": "vless://", "n-trojan": "trojan://", "n-ss": "ss://",
		"n-hy2": "hysteria2://", "n-tuic": "tuic://",
	}
	for _, n := range nodes {
		link, err := BuildShareLink(n)
		if err != nil {
			t.Fatalf("BuildShareLink %s: %v", n.Name, err)
		}
		if !strings.HasPrefix(link, prefixes[n.Name]) {
			t.Fatalf("%s: want prefix %s, got %s", n.Name, prefixes[n.Name], link)
		}
		// URI schemes (not the base64 vmess form) must parse.
		if _, err := url.Parse(link); err != nil {
			t.Fatalf("%s: unparseable link %q: %v", n.Name, link, err)
		}
	}

	// Client outbound must marshal for every type.
	for _, n := range nodes {
		if _, err := BuildClientOutbound(n); err != nil {
			t.Fatalf("BuildClientOutbound %s: %v", n.Name, err)
		}
	}
}

func TestBuildShareLinkBracketsIPv6(t *testing.T) {
	link, err := BuildShareLink(ClientNode{
		Name: "ipv6", Server: "2001:db8::10", ServerPort: 443, Type: "vless",
		Settings: InboundSettings{TLS: TLSSettings{Enabled: true, ServerName: "example.com", CertificatePath: "cert.pem", KeyPath: "key.pem"}},
		User:     ProxyUser{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"},
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("unparseable IPv6 link %q: %v", link, err)
	}
	if u.Hostname() != "2001:db8::10" || u.Port() != "443" {
		t.Fatalf("authority = %q; want bracketed IPv6:443", u.Host)
	}
}

func TestVMessRealityShareLinkCarriesClientKeys(t *testing.T) {
	link, err := BuildShareLink(ClientNode{
		Name: "vmess-reality", Server: "1.2.3.4", ServerPort: 443, Type: "vmess",
		Settings: InboundSettings{TLS: TLSSettings{Reality: RealitySettings{
			Enabled: true, HandshakeServer: "www.microsoft.com", PublicKey: "public-key", ShortID: []string{"abcd"},
		}}},
		User: ProxyUser{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"tls": "reality", "pbk": "public-key", "sid": "abcd", "fp": "chrome"} {
		if got[key] != want {
			t.Errorf("%s = %v, want %q", key, got[key], want)
		}
	}
}

func TestVMessParametersStayAligned(t *testing.T) {
	st := InboundSettings{
		VMessSecurity: "chacha20-poly1305",
		VMessAlterID:  1,
		TLS:           TLSSettings{Enabled: true, ServerName: "example.com", ALPN: []string{"h2", "http/1.1"}, CertificatePath: "cert.pem", KeyPath: "key.pem"},
	}
	user := ProxyUser{Name: "user", UUID: "bf000d23-0752-40b4-affe-68f7707a9661"}

	inRaw, err := BuildInbound(InboundInput{Tag: "vmess", Type: "vmess", ListenPort: 443, Settings: st, Users: []ProxyUser{user}})
	if err != nil {
		t.Fatal(err)
	}
	var inbound map[string]any
	_ = json.Unmarshal(inRaw, &inbound)
	gotUser := inbound["users"].([]any)[0].(map[string]any)
	if gotUser["alterId"] != float64(1) {
		t.Fatalf("inbound alterId = %v, want 1", gotUser["alterId"])
	}

	outRaw, err := BuildClientOutbound(ClientNode{
		Name: "vmess", Server: "1.2.3.4", ServerPort: 443, Type: "vmess", Settings: st, User: user,
	})
	if err != nil {
		t.Fatal(err)
	}
	var outbound map[string]any
	_ = json.Unmarshal(outRaw, &outbound)
	if outbound["security"] != "chacha20-poly1305" || outbound["alter_id"] != float64(1) {
		t.Fatalf("client VMess parameters not aligned: %v", outbound)
	}

	link, err := BuildShareLink(ClientNode{
		Name: "vmess", Server: "1.2.3.4", ServerPort: 443, Type: "vmess", Settings: st, User: user,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
	var share map[string]any
	_ = json.Unmarshal(decoded, &share)
	if share["aid"] != "1" || share["scy"] != "chacha20-poly1305" || share["alpn"] != "h2,http/1.1" {
		t.Fatalf("VMess share parameters not aligned: %v", share)
	}
}

func TestTUICOfficialParametersStayAligned(t *testing.T) {
	st := InboundSettings{
		CongestionControl: "bbr",
		AuthTimeout:       "5s",
		ZeroRTTHandshake:  true,
		Heartbeat:         "15s",
		TLS:               TLSSettings{Enabled: true, ServerName: "example.com", ALPN: []string{"h3"}, CertificatePath: "cert.pem", KeyPath: "key.pem"},
	}
	user := ProxyUser{Name: "user", UUID: "bf000d23-0752-40b4-affe-68f7707a9661", Password: "secret"}

	inRaw, err := BuildInbound(InboundInput{Tag: "tuic", Type: "tuic", ListenPort: 443, Settings: st, Users: []ProxyUser{user}})
	if err != nil {
		t.Fatal(err)
	}
	var inbound map[string]any
	_ = json.Unmarshal(inRaw, &inbound)
	for key, want := range map[string]any{
		"congestion_control": "bbr",
		"auth_timeout":       "5s",
		"zero_rtt_handshake": true,
		"heartbeat":          "15s",
	} {
		if inbound[key] != want {
			t.Errorf("TUIC inbound %s = %v, want %v", key, inbound[key], want)
		}
	}

	outRaw, err := BuildClientOutbound(ClientNode{
		Name: "tuic", Server: "1.2.3.4", ServerPort: 443, Type: "tuic", Settings: st, User: user,
	})
	if err != nil {
		t.Fatal(err)
	}
	var outbound map[string]any
	_ = json.Unmarshal(outRaw, &outbound)
	if outbound["zero_rtt_handshake"] != true || outbound["heartbeat"] != "15s" {
		t.Fatalf("TUIC client parameters not aligned: %v", outbound)
	}
}

func TestTrojanFallbackAndALPN(t *testing.T) {
	st := InboundSettings{
		TLS:            TLSSettings{Enabled: true, ServerName: "example.com", ALPN: []string{"h2", "http/1.1"}, CertificatePath: "cert.pem", KeyPath: "key.pem"},
		TrojanFallback: &FallbackSettings{Server: "127.0.0.1", ServerPort: 8080},
	}
	user := ProxyUser{Name: "user", Password: "secret"}
	raw, err := BuildInbound(InboundInput{Tag: "trojan", Type: "trojan", ListenPort: 443, Settings: st, Users: []ProxyUser{user}})
	if err != nil {
		t.Fatal(err)
	}
	var inbound map[string]any
	_ = json.Unmarshal(raw, &inbound)
	fallback := inbound["fallback"].(map[string]any)
	if fallback["server"] != "127.0.0.1" || fallback["server_port"] != float64(8080) {
		t.Fatalf("Trojan fallback = %v", fallback)
	}
	tls := inbound["tls"].(map[string]any)
	if got := tls["alpn"].([]any); len(got) != 2 || got[0] != "h2" || got[1] != "http/1.1" {
		t.Fatalf("Trojan ALPN = %v", got)
	}
}

func TestProtocolParameterValidation(t *testing.T) {
	tests := []struct {
		name string
		typ  string
		st   InboundSettings
	}{
		{name: "vmess security", typ: "vmess", st: InboundSettings{VMessSecurity: "invalid"}},
		{name: "tuic duration", typ: "tuic", st: InboundSettings{TLS: TLSSettings{Enabled: true}, AuthTimeout: "soon"}},
		{name: "trojan fallback", typ: "trojan", st: InboundSettings{TLS: TLSSettings{Enabled: true}, TrojanFallback: &FallbackSettings{Server: "127.0.0.1"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.st.Validate(tt.typ); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestSnellOutboundVersionMapping(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   int
		want float64
	}{
		{name: "v4", in: 4, want: 4},
		{name: "v5 uses v4 wire value", in: 5, want: 4},
		{name: "v6", in: 6, want: 6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := BuildClientOutbound(ClientNode{
				Name: "snell", Server: "snell.example.com", ServerPort: 443,
				Type: "snell", Settings: InboundSettings{SnellVersion: tt.in},
				User: ProxyUser{Password: "snell-psk"},
			})
			if err != nil {
				t.Fatal(err)
			}
			var outbound map[string]any
			if err := json.Unmarshal(raw, &outbound); err != nil {
				t.Fatal(err)
			}
			if got := outbound["version"]; got != tt.want {
				t.Fatalf("version = %v, want %v; JSON = %s", got, tt.want, raw)
			}
		})
	}
}

func TestSnellInboundAndOutboundVersionsAreValidatedSeparately(t *testing.T) {
	inboundPSK := "123456789012"
	for _, version := range []int{5, 6} {
		if err := (InboundSettings{SnellVersion: version, SnellPSK: inboundPSK}).Validate("snell"); err != nil {
			t.Fatalf("inbound v%d rejected: %v", version, err)
		}
	}
	if err := (InboundSettings{SnellVersion: 4, SnellPSK: inboundPSK}).Validate("snell"); err == nil {
		t.Fatal("inbound v4 was accepted")
	}
	for _, version := range []int{4, 6} {
		if err := (InboundSettings{SnellVersion: version, SnellPSK: inboundPSK}).ValidateClientOutbound("snell"); err != nil {
			t.Fatalf("outbound v%d rejected: %v", version, err)
		}
	}
	for _, version := range []int{3, 5} {
		if err := (InboundSettings{SnellVersion: version, SnellPSK: inboundPSK}).ValidateClientOutbound("snell"); err == nil {
			t.Fatalf("outbound v%d was accepted", version)
		}
	}
}

func TestSnellOutboundOfficialFields(t *testing.T) {
	raw, err := BuildClientOutbound(ClientNode{
		Name: "snell", Server: "snell.example.com", ServerPort: 443, Type: "snell",
		Settings: InboundSettings{
			SnellVersion: 6, SnellPSK: "123456789012",
			SnellReuse: true, SnellNetwork: "udp", SnellMode: "unshaped",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"psk": "123456789012", "reuse": true,
		"network": "udp", "version": float64(6), "mode": "unshaped",
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	if _, ok := got["obfs_mode"]; ok {
		t.Error("v6 outbound must not contain obfs_mode")
	}

	legacy, err := BuildClientOutbound(ClientNode{
		Name: "snell-v5", Server: "snell.example.com", ServerPort: 443, Type: "snell",
		Settings: InboundSettings{SnellVersion: 5, SnellPSK: "server-psk", SnellObfsMode: "http", SnellObfsHost: "cdn.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var legacyMap map[string]any
	if err := json.Unmarshal(legacy, &legacyMap); err != nil {
		t.Fatal(err)
	}
	if legacyMap["version"] != float64(4) || legacyMap["obfs_mode"] != "http" || legacyMap["obfs_host"] != "cdn.example.com" {
		t.Fatalf("legacy v5 outbound mapping = %#v", legacyMap)
	}
}

func TestSnellClientPSKFallsBackToImportedServerPSK(t *testing.T) {
	got := SnellClientPSK(InboundSettings{SingleUser: true, SnellPSK: "server-psk"}, "")
	if got != "server-psk" {
		t.Fatalf("PSK = %q, want imported server PSK", got)
	}
	got = SnellClientPSK(InboundSettings{SnellPSK: "server-psk"}, "")
	if got != "server-psk" {
		t.Fatalf("empty user fallback PSK = %q, want imported server PSK", got)
	}
}

func TestSelfSignedTLSForcesClientSkipVerification(t *testing.T) {
	tls := TLSSettings{SelfSigned: true, ServerName: "example.com"}
	nodes := []ClientNode{
		{Name: "vless", Server: "1.2.3.4", ServerPort: 443, Type: "vless", Settings: InboundSettings{TLS: tls}, User: ProxyUser{UUID: "u"}},
		{Name: "vmess", Server: "1.2.3.4", ServerPort: 443, Type: "vmess", Settings: InboundSettings{TLS: tls}, User: ProxyUser{UUID: "u"}},
		{Name: "trojan", Server: "1.2.3.4", ServerPort: 443, Type: "trojan", Settings: InboundSettings{TLS: tls}, User: ProxyUser{Password: "p"}},
		{Name: "hy2", Server: "1.2.3.4", ServerPort: 443, Type: "hysteria2", Settings: InboundSettings{TLS: tls}, User: ProxyUser{Password: "p"}},
		{Name: "tuic", Server: "1.2.3.4", ServerPort: 443, Type: "tuic", Settings: InboundSettings{TLS: tls}, User: ProxyUser{UUID: "u", Password: "p"}},
		{Name: "anytls", Server: "1.2.3.4", ServerPort: 443, Type: "anytls", Settings: InboundSettings{TLS: tls}, User: ProxyUser{Password: "p"}},
	}
	wantQuery := map[string]string{
		"vless":  "allowInsecure",
		"trojan": "allowInsecure",
		"hy2":    "insecure",
		"tuic":   "allow_insecure",
		"anytls": "insecure",
	}

	for _, n := range nodes {
		t.Run(n.Name, func(t *testing.T) {
			outRaw, err := BuildClientOutbound(n)
			if err != nil {
				t.Fatal(err)
			}
			var outbound map[string]any
			_ = json.Unmarshal(outRaw, &outbound)
			clientTLS, ok := outbound["tls"].(map[string]any)
			if !ok || clientTLS["insecure"] != true {
				t.Fatalf("sing-box client TLS = %v, want insecure=true", outbound["tls"])
			}

			link, err := BuildShareLink(n)
			if err != nil {
				t.Fatal(err)
			}
			if n.Type == "vmess" {
				decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
				if err != nil {
					t.Fatal(err)
				}
				var share map[string]any
				_ = json.Unmarshal(decoded, &share)
				if share["allowInsecure"] != "1" {
					t.Fatalf("VMess share allowInsecure = %v", share["allowInsecure"])
				}
				return
			}
			u, err := url.Parse(link)
			if err != nil {
				t.Fatal(err)
			}
			key := wantQuery[n.Name]
			if u.Query().Get(key) != "1" {
				t.Fatalf("%s share query %s = %q; link=%s", n.Type, key, u.Query().Get(key), link)
			}
		})
	}
}

func TestTrustedTLSKeepsCertificateVerificationEnabled(t *testing.T) {
	n := ClientNode{
		Name:       "trojan",
		Server:     "example.com",
		ServerPort: 443,
		Type:       "trojan",
		Settings: InboundSettings{TLS: TLSSettings{
			Enabled:    true,
			ServerName: "example.com",
		}},
		User: ProxyUser{Password: "p"},
	}

	outRaw, err := BuildClientOutbound(n)
	if err != nil {
		t.Fatal(err)
	}
	var outbound map[string]any
	_ = json.Unmarshal(outRaw, &outbound)
	clientTLS, ok := outbound["tls"].(map[string]any)
	if !ok {
		t.Fatalf("sing-box client TLS = %v", outbound["tls"])
	}
	if _, exists := clientTLS["insecure"]; exists {
		t.Fatalf("trusted TLS unexpectedly disables certificate verification: %v", clientTLS)
	}

	link, err := BuildShareLink(n)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("allowInsecure"); got != "" {
		t.Fatalf("trusted TLS allowInsecure = %q; link=%s", got, link)
	}
}

func TestBuildInboundSocksAuthenticationModes(t *testing.T) {
	tests := []struct {
		name     string
		settings InboundSettings
		wantUser bool
	}{
		{name: "no authentication", settings: InboundSettings{SingleUser: true}},
		{name: "username and password", settings: InboundSettings{SingleUser: true, Username: "alice", Password: "secret"}, wantUser: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := BuildInbound(InboundInput{Tag: "socks-in", Type: "socks", ListenPort: 1080, Settings: tc.settings})
			if err != nil {
				t.Fatal(err)
			}
			var inbound map[string]any
			if err := json.Unmarshal(raw, &inbound); err != nil {
				t.Fatal(err)
			}
			users, exists := inbound["users"]
			if !tc.wantUser {
				if exists {
					t.Fatalf("no-auth SOCKS must omit users, got %v", users)
				}
				return
			}
			list, ok := users.([]any)
			if !ok || len(list) != 1 {
				t.Fatalf("authenticated SOCKS users = %v", users)
			}
			user := list[0].(map[string]any)
			if user["username"] != "alice" || user["password"] != "secret" {
				t.Fatalf("authenticated SOCKS user = %v", user)
			}
		})
	}
}

func TestBuildSocksOutboundAndShareLink(t *testing.T) {
	raw, err := buildOutbound(OutboundInput{
		Tag: "socks-out", Type: "socks", Server: "proxy.example.com", ServerPort: 1080,
		Username: "alice", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	var outbound map[string]any
	if err := json.Unmarshal(raw, &outbound); err != nil {
		t.Fatal(err)
	}
	if outbound["username"] != "alice" || outbound["password"] != "secret" {
		t.Fatalf("SOCKS outbound credentials were lost: %v", outbound)
	}

	link, err := BuildShareLink(ClientNode{
		Name: "SOCKS node", Server: "proxy.example.com", ServerPort: 1080, Type: "socks",
		Settings: InboundSettings{Username: "alice", Password: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.Scheme != "socks5" || u.User.Username() != "alice" || password != "secret" {
		t.Fatalf("SOCKS share link = %s", link)
	}
}

func TestBuildRouteActionsAndRuleSets(t *testing.T) {
	route := buildRouteRule(RuleInput{Action: "route", Inbound: []string{"in"}, Outbound: "direct"})
	if route["action"] != "route" || route["outbound"] != "direct" {
		t.Fatalf("route action must be explicit and keep outbound: %v", route)
	}
	if !ruleNeedsSniff(RuleInput{Protocol: []string{"tls"}}) {
		t.Fatal("protocol matching must enable sniffing")
	}
	reject := buildRouteRule(RuleInput{Action: "reject", Method: "drop", Outbound: "block"})
	if reject["action"] != "reject" || reject["method"] != "drop" {
		t.Fatalf("reject action lost its method: %v", reject)
	}
	sniff := buildRouteRule(RuleInput{
		Action: "sniff", Inbound: []string{"in"}, Sniffer: []string{"tls"},
		DomainSuffix: []string{"stale.example"}, Protocol: []string{"tls"}, Outbound: "sniff",
	})
	if sniff["action"] != "sniff" || sniff["sniffer"] == nil || sniff["inbound"] == nil {
		t.Fatalf("sniff action = %v", sniff)
	}
	if _, exists := sniff["domain_suffix"]; exists {
		t.Fatalf("sniff action retained a hidden route condition: %v", sniff)
	}
	if _, exists := sniff["protocol"]; exists {
		t.Fatalf("sniff action must not duplicate sniffer into protocol: %v", sniff)
	}
	dns := buildRouteRule(RuleInput{Action: "hijack-dns", Protocol: []string{"tls"}, Outbound: "hijack-dns"})
	if dns["action"] != "hijack-dns" || len(dns["protocol"].([]string)) != 1 || dns["protocol"].([]string)[0] != "dns" {
		t.Fatalf("DNS hijack must always match only DNS: %v", dns)
	}

	local := buildRuleSet(RuleSetInput{Tag: "local", Type: "local", Format: "source", Path: "/etc/sing-box/local.json"})
	if local["type"] != "local" || local["path"] == "" || local["url"] != nil {
		t.Fatalf("local rule-set = %v", local)
	}
	remote := buildRuleSet(RuleSetInput{
		Tag: "remote", Type: "remote", Format: "binary", URL: "https://example.com/rules.srs",
		DownloadDetour: "direct", UpdateInterval: "1d",
	})
	if remote["url"] == "" || remote["download_detour"] != "direct" || remote["update_interval"] != "1d" {
		t.Fatalf("remote rule-set = %v", remote)
	}
}

func TestBuildServerConfigDoesNotDuplicateGlobalSniff(t *testing.T) {
	raw, err := BuildServerConfig(ServerConfigInput{
		Rules: []RuleInput{
			{Action: "sniff", Outbound: "sniff"},
			{DomainSuffix: []string{"example.com"}, Outbound: "direct"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Route.Rules) != 2 || config.Route.Rules[0]["action"] != "sniff" {
		t.Fatalf("global sniff was duplicated or reordered: %s", raw)
	}
}

// QUIC outbounds (hysteria2/tuic) must NOT carry a utls block: sing-box's QUIC
// dialer rejects uTLS at runtime with "unsupported usage for uTLS", so the node
// passes `check` but never connects. TCP protocols must still get utls.
func TestQUICOutboundsOmitUTLS(t *testing.T) {
	tlsOn := TLSSettings{Enabled: true, ServerName: "example.com"}
	user := ProxyUser{Name: "u", UUID: "bf000d23-0752-40b4-affe-68f7707a9661", Password: "pw"}

	for _, typ := range []string{"hysteria2", "tuic"} {
		raw, err := BuildClientOutbound(ClientNode{
			Name: typ, Server: "1.2.3.4", ServerPort: 443, Type: typ, Settings: InboundSettings{TLS: tlsOn}, User: user,
		})
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		tls, _ := m["tls"].(map[string]any)
		if tls == nil {
			t.Fatalf("%s: expected tls block", typ)
		}
		if _, hasUTLS := tls["utls"]; hasUTLS {
			t.Errorf("%s outbound must not carry utls (QUIC rejects it at runtime)", typ)
		}
	}

	// TCP protocol keeps utls.
	raw, _ := BuildClientOutbound(ClientNode{
		Name: "vless", Server: "1.2.3.4", ServerPort: 443, Type: "vless",
		Settings: InboundSettings{TLS: tlsOn}, User: user,
	})
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	tls, _ := m["tls"].(map[string]any)
	if _, hasUTLS := tls["utls"]; !hasUTLS {
		t.Error("vless outbound must keep utls")
	}
}

// hysteria2 is QUIC and has no v2ray transport; a ws transport on it (or on any
// non vless/vmess/trojan type) must be rejected before it reaches the node.
func TestWSTransportRejectedOnNonV2Ray(t *testing.T) {
	err := InboundSettings{
		TLS:       TLSSettings{Enabled: true, CertificatePath: "cert.pem", KeyPath: "key.pem"},
		Transport: TransportSettings{Type: "ws", Path: "/x"},
	}.Validate("hysteria2")
	if err == nil {
		t.Fatal("expected ws transport on hysteria2 to be rejected")
	}
	// ws on vmess is fine.
	if err := (InboundSettings{Transport: TransportSettings{Type: "ws", Path: "/x"}}).Validate("vmess"); err != nil {
		t.Fatalf("ws on vmess should be allowed: %v", err)
	}
}

// VLESS flow requires TLS/REALITY; plain-TCP + flow fails at runtime, so reject it.
func TestVLESSFlowRequiresTLS(t *testing.T) {
	if err := (InboundSettings{Flow: "xtls-rprx-vision"}).Validate("vless"); err == nil {
		t.Fatal("expected vless flow without TLS to be rejected")
	}
	ok := InboundSettings{Flow: "xtls-rprx-vision", TLS: TLSSettings{Reality: RealitySettings{Enabled: true, HandshakeServer: "a.com", PrivateKey: "k"}}}
	if err := ok.Validate("vless"); err != nil {
		t.Fatalf("vless flow + REALITY should be allowed: %v", err)
	}
}

// A trojan node on REALITY must produce a reality share link, not a plain-TLS
// one (which would fail the handshake against a REALITY-only server).
func TestTrojanRealityShareLink(t *testing.T) {
	link, err := BuildShareLink(ClientNode{
		Name: "t", Server: "1.2.3.4", ServerPort: 443, Type: "trojan",
		Settings: InboundSettings{TLS: TLSSettings{Reality: RealitySettings{
			Enabled: true, HandshakeServer: "www.microsoft.com", PublicKey: "PUBKEY", ShortID: []string{"abcd"},
		}}},
		User: ProxyUser{Password: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "security=reality") || !strings.Contains(link, "pbk=PUBKEY") {
		t.Errorf("trojan+reality link missing reality params: %s", link)
	}
	if strings.Contains(link, "security=tls") {
		t.Errorf("trojan+reality link must not claim plain tls: %s", link)
	}
}

func TestTransportUsesOfficialHTTPUpgradeShape(t *testing.T) {
	raw, err := BuildClientOutbound(ClientNode{
		Name: "upgrade", Server: "example.com", ServerPort: 443, Type: "vless",
		Settings: InboundSettings{Transport: TransportSettings{
			Type: "httpupgrade", Path: "/upgrade",
			Headers:      map[string]string{"Host": "cdn.example.com", "X-Test": "yes"},
			MaxEarlyData: 1024, EarlyDataHeader: "Sec-WebSocket-Protocol",
		}},
		User: ProxyUser{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	transport := got["transport"].(map[string]any)
	if transport["type"] != "httpupgrade" || transport["host"] != "cdn.example.com" {
		t.Fatalf("HTTPUpgrade transport = %#v", transport)
	}
	if _, ok := transport["max_early_data"]; ok {
		t.Fatal("HTTPUpgrade must not contain WebSocket early data")
	}
	if _, ok := transport["early_data_header_name"]; ok {
		t.Fatal("HTTPUpgrade must not contain WebSocket early data header")
	}
	headers, _ := transport["headers"].(map[string]any)
	if headers["Host"] != nil || headers["X-Test"] != "yes" {
		t.Fatalf("HTTPUpgrade headers = %#v", headers)
	}
}

func TestTransportPreservesRepeatedHeaderValues(t *testing.T) {
	transport := TransportSettings{Type: "ws", Path: "/ws", HeaderValues: map[string][]string{
		"Accept": {"text/html", "application/json"},
	}}
	raw, err := BuildClientOutbound(ClientNode{
		Name: "multi-header", Server: "example.com", ServerPort: 443, Type: "vless",
		Settings: InboundSettings{Transport: transport},
		User:     ProxyUser{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	headers := got["transport"].(map[string]any)["headers"].(map[string]any)
	values, ok := headers["Accept"].([]any)
	if !ok || len(values) != 2 || values[0] != "text/html" || values[1] != "application/json" {
		t.Fatalf("repeated headers = %#v", headers["Accept"])
	}
}

func TestShadowsocksPluginIsRenderedForBeta(t *testing.T) {
	settings := InboundSettings{Method: "aes-256-gcm", SSPlugin: "v2ray-plugin;mode=websocket;host=cdn.example.com"}
	raw, err := BuildClientOutbound(ClientNode{
		Name: "ss", Server: "example.com", ServerPort: 443, Type: "shadowsocks", Settings: settings,
		User: ProxyUser{Password: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["plugin"] != "v2ray-plugin" || got["plugin_opts"] != "mode=websocket;host=cdn.example.com" {
		t.Fatalf("Shadowsocks plugin fields = %#v", got)
	}
}

func TestBetaProtocolValidation(t *testing.T) {
	if err := (InboundSettings{TLS: TLSSettings{Enabled: true}}).Validate("trojan"); err == nil {
		t.Fatal("TLS without certificate material must be rejected")
	}
	if err := (InboundSettings{TLS: TLSSettings{Enabled: true, CertificatePath: "cert.pem", KeyPath: "key.pem"}}).Validate("trojan"); err != nil {
		t.Fatalf("valid certificate paths rejected: %v", err)
	}
	if err := (InboundSettings{Flow: "unsupported"}).ValidateClientOutbound("vless"); err == nil {
		t.Fatal("unsupported VLESS flow was accepted")
	}
	if err := (InboundSettings{
		TLS:                   TLSSettings{Enabled: true, CertificatePath: "cert.pem", KeyPath: "key.pem"},
		UpMbps:                100,
		IgnoreClientBandwidth: true,
	}).Validate("hysteria2"); err == nil {
		t.Fatal("conflicting Hysteria2 bandwidth settings were accepted")
	}
	for _, security := range []string{"auto", "none", "zero", "aes-128-gcm", "chacha20-poly1305", "aes-128-cfb"} {
		if err := (InboundSettings{VMessSecurity: security}).Validate("vmess"); err != nil {
			t.Fatalf("VMess security %q rejected: %v", security, err)
		}
	}
}
