package panel

import (
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestOutboundTestCommandUsesQUICProbeForUDPProtocols(t *testing.T) {
	base := outboundStoredSettings{Server: "land.example.com", ServerPort: 443}
	obfs := base
	obfs.Settings = singbox.InboundSettings{ObfsType: "salamander", ObfsPassword: "k"}
	legacyObfs := base
	legacyObfs.Settings = singbox.InboundSettings{ObfsPassword: "k"}

	cases := []struct {
		typ            string
		st             outboundStoredSettings
		wantNetwork    string
		wantObfuscated bool
	}{
		{"vless", base, "", false},
		{"trojan", base, "", false},
		{"shadowsocks", base, "", false},
		{"hysteria2", base, "quic", false},
		{"hysteria2", obfs, "quic", true},
		{"hysteria2", legacyObfs, "quic", true},
		{"tuic", base, "quic", false},
	}
	for _, tc := range cases {
		cmd := outboundTestCommand(tc.typ, tc.st)
		if cmd.Host != "land.example.com" || cmd.Port != 443 {
			t.Fatalf("%s: target = %s:%d", tc.typ, cmd.Host, cmd.Port)
		}
		if cmd.Network != tc.wantNetwork || cmd.Obfuscated != tc.wantObfuscated {
			t.Fatalf("%s %+v: network=%q obfuscated=%v", tc.typ, tc.st.Settings, cmd.Network, cmd.Obfuscated)
		}
	}
}

func TestValidateOutboundRejectsLoopbackAndUnspecifiedServers(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	for _, host := range []string{"127.0.0.1", "localhost", "api.localhost", "::1", "[::1]", "0.0.0.0", "::"} {
		raw := []byte(`{"server":"` + host + `","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}`)
		if err := app.validateOutbound(db, 1, 0, "land", "vless", 0, raw); err == nil {
			t.Fatalf("validateOutbound accepted loopback/unspecified server %q", host)
		}
	}
}
