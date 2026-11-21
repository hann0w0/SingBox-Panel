package panel

import (
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestSurgeVLESSExport(t *testing.T) {
	const uuid = "2cae91b3-f85c-433b-88ee-a002b9ed6d61"

	t.Run("raw-reality-vision", func(t *testing.T) {
		st := singbox.InboundSettings{
			SingleUser: true,
			Flow:       "xtls-rprx-vision",
			TLS: singbox.TLSSettings{
				Enabled:    true,
				ServerName: "www.microsoft.com",
				Insecure:   true, // REALITY ignores skip-cert-verify
				Reality: singbox.RealitySettings{
					Enabled:   true,
					PublicKey: "dwrjY6KiAMPxKdlF7T4AWFXXBtsxeHCXtBQYeCOdZV4",
					ShortID:   []string{"0123456789abcdef"},
				},
			},
		}
		n := node{
			name: "Proxy-REALITY", server: "1.2.3.4", port: 443, typ: "vless",
			settings: st, user: singbox.ProxyUser{UUID: uuid},
		}
		got := surgeProxy(n, n.name)
		want := "Proxy-REALITY = vless, 1.2.3.4, 443, username=" + uuid +
			", flow=xtls-rprx-vision, sni=www.microsoft.com" +
			", reality-public-key=dwrjY6KiAMPxKdlF7T4AWFXXBtsxeHCXtBQYeCOdZV4" +
			", reality-short-id=0123456789abcdef"
		if got != want {
			t.Fatalf("got:\n  %s\nwant:\n  %s", got, want)
		}
		if strings.Contains(got, "tls=true") || strings.Contains(got, "skip-cert-verify") || strings.Contains(got, "udp-relay") {
			t.Fatalf("unexpected parameter in REALITY line: %s", got)
		}
	})

	t.Run("raw-reality-no-short-id-handshake-sni", func(t *testing.T) {
		st := singbox.InboundSettings{
			SingleUser: true,
			TLS: singbox.TLSSettings{
				Reality: singbox.RealitySettings{
					Enabled:         true,
					HandshakeServer: "www.apple.com",
					PublicKey:       "dwrjY6KiAMPxKdlF7T4AWFXXBtsxeHCXtBQYeCOdZV4",
				},
			},
		}
		n := node{
			name: "REALITY-Min", server: "1.2.3.4", port: 443, typ: "vless",
			settings: st, user: singbox.ProxyUser{UUID: uuid},
		}
		got := surgeProxy(n, n.name)
		want := "REALITY-Min = vless, 1.2.3.4, 443, username=" + uuid +
			", sni=www.apple.com, reality-public-key=dwrjY6KiAMPxKdlF7T4AWFXXBtsxeHCXtBQYeCOdZV4"
		if got != want {
			t.Fatalf("got:\n  %s\nwant:\n  %s", got, want)
		}
	})

	t.Run("raw-tls-vision-alpn-insecure", func(t *testing.T) {
		st := singbox.InboundSettings{
			SingleUser: true,
			Flow:       "xtls-rprx-vision",
			TLS: singbox.TLSSettings{
				Enabled:    true,
				ServerName: "example.com",
				ALPN:       []string{"h2", "http/1.1"},
				Insecure:   true,
			},
		}
		n := node{
			name: "Proxy-TLS", server: "example.com", port: 443, typ: "vless",
			settings: st, user: singbox.ProxyUser{UUID: uuid},
		}
		got := surgeProxy(n, n.name)
		want := `Proxy-TLS = vless, example.com, 443, username=` + uuid +
			`, flow=xtls-rprx-vision, tls=true, sni=example.com, alpn="h2,http/1.1", skip-cert-verify=true`
		if got != want {
			t.Fatalf("got:\n  %s\nwant:\n  %s", got, want)
		}
	})

	t.Run("ws-tls-with-headers", func(t *testing.T) {
		tr := singbox.TransportSettings{Type: "ws", Path: "/vless"}
		tr.SetHeaderValues("Host", []string{"cdn.example.com"})
		tr.SetHeaderValues("X-Auth", []string{"token"})
		st := singbox.InboundSettings{
			SingleUser: true,
			TLS:        singbox.TLSSettings{Enabled: true, ServerName: "example.com"},
			Transport:  tr,
		}
		n := node{
			name: "Proxy-WS", server: "example.com", port: 443, typ: "vless",
			settings: st, user: singbox.ProxyUser{UUID: uuid},
		}
		got := surgeProxy(n, n.name)
		want := "Proxy-WS = vless, example.com, 443, username=" + uuid +
			", tls=true, sni=example.com, ws=true, ws-path=/vless, ws-headers=Host:cdn.example.com|X-Auth:token"
		if got != want {
			t.Fatalf("got:\n  %s\nwant:\n  %s", got, want)
		}
	})

	t.Run("httpupgrade-tls", func(t *testing.T) {
		tr := singbox.TransportSettings{Type: "httpupgrade", Path: "/up"}
		tr.SetHeaderValues("Host", []string{"cdn.example.com"})
		st := singbox.InboundSettings{
			SingleUser: true,
			TLS:        singbox.TLSSettings{Enabled: true, ServerName: "cdn.example.com"},
			Transport:  tr,
		}
		n := node{
			name: "Proxy-HTTPUpgrade", server: "cdn.example.com", port: 443, typ: "vless",
			settings: st, user: singbox.ProxyUser{UUID: uuid},
		}
		got := surgeProxy(n, n.name)
		want := "Proxy-HTTPUpgrade = vless, cdn.example.com, 443, username=" + uuid +
			", tls=true, sni=cdn.example.com, ws=true, ws-http-upgrade=true, ws-path=/up, ws-headers=Host:cdn.example.com"
		if got != want {
			t.Fatalf("got:\n  %s\nwant:\n  %s", got, want)
		}
	})

	t.Run("raw-plain", func(t *testing.T) {
		n := node{
			name: "Proxy-Plain", server: "1.2.3.4", port: 80, typ: "vless",
			settings: singbox.InboundSettings{SingleUser: true},
			user:     singbox.ProxyUser{UUID: uuid},
		}
		got := surgeProxy(n, n.name)
		want := "Proxy-Plain = vless, 1.2.3.4, 80, username=" + uuid
		if got != want {
			t.Fatalf("got:\n  %s\nwant:\n  %s", got, want)
		}
	})

	t.Run("roundtrip-surge-import", func(t *testing.T) {
		nodes := []node{
			{
				name: "VLESS-REALITY", server: "1.2.3.4", port: 443, typ: "vless",
				user: singbox.ProxyUser{UUID: uuid},
				settings: singbox.InboundSettings{
					SingleUser: true,
					Flow:       "xtls-rprx-vision",
					TLS: singbox.TLSSettings{
						Enabled:    true,
						ServerName: "www.microsoft.com",
						Reality: singbox.RealitySettings{
							Enabled:   true,
							PublicKey: "dwrjY6KiAMPxKdlF7T4AWFXXBtsxeHCXtBQYeCOdZV4",
							ShortID:   []string{"0123456789abcdef"},
						},
					},
				},
			},
			{
				name: "VLESS-HTTPUpgrade", server: "cdn.example.com", port: 443, typ: "vless",
				user: singbox.ProxyUser{UUID: uuid},
				settings: singbox.InboundSettings{
					SingleUser: true,
					TLS:        singbox.TLSSettings{Enabled: true, ServerName: "cdn.example.com"},
					Transport:  singbox.TransportSettings{Type: "httpupgrade", Path: "/up", Headers: map[string]string{"Host": "cdn.example.com"}},
				},
			},
		}
		exported := surgeLines(nodes)
		parsed, err := singbox.ParseSubscription([]byte(exported))
		if err != nil {
			t.Fatalf("ParseSubscription failed on exported Surge VLESS:\n%s\nerr: %v", exported, err)
		}
		if len(parsed.Nodes) != 2 || len(parsed.Skipped) != 0 {
			t.Fatalf("unexpected parse result: nodes=%d skipped=%+v\nexported:\n%s", len(parsed.Nodes), parsed.Skipped, exported)
		}
		if parsed.Nodes[0].Params["tls"] != "reality" || parsed.Nodes[0].Params["pbk"] != "dwrjY6KiAMPxKdlF7T4AWFXXBtsxeHCXtBQYeCOdZV4" || parsed.Nodes[0].Params["sid"] != "0123456789abcdef" || parsed.Nodes[0].Params["flow"] != "xtls-rprx-vision" {
			t.Fatalf("REALITY roundtrip params = %#v", parsed.Nodes[0].Params)
		}
		if parsed.Nodes[1].Params["transport"] != "httpupgrade" || parsed.Nodes[1].Params["path"] != "/up" || parsed.Nodes[1].Params["host"] != "cdn.example.com" {
			t.Fatalf("HTTPUpgrade roundtrip params = %#v", parsed.Nodes[1].Params)
		}
	})
}

func TestSurgeVLESSSkipsUnsupportedCombinations(t *testing.T) {
	const uuid = "2cae91b3-f85c-433b-88ee-a002b9ed6d61"

	cases := []struct {
		name string
		node node
	}{
		{
			name: "missing-uuid",
			node: node{name: "no-uuid", server: "1.2.3.4", port: 443, typ: "vless"},
		},
		{
			name: "reality-missing-pbk",
			node: node{
				name: "no-pbk", server: "1.2.3.4", port: 443, typ: "vless",
				user:     singbox.ProxyUser{UUID: uuid},
				settings: singbox.InboundSettings{TLS: singbox.TLSSettings{Reality: singbox.RealitySettings{Enabled: true}}},
			},
		},
		{
			name: "reality-with-ws",
			node: node{
				name: "reality-ws", server: "1.2.3.4", port: 443, typ: "vless",
				user: singbox.ProxyUser{UUID: uuid},
				settings: singbox.InboundSettings{
					Transport: singbox.TransportSettings{Type: "ws", Path: "/ws"},
					TLS:       singbox.TLSSettings{Reality: singbox.RealitySettings{Enabled: true, PublicKey: "pbk"}},
				},
			},
		},
		{
			name: "vision-with-ws",
			node: node{
				name: "vision-ws", server: "1.2.3.4", port: 443, typ: "vless",
				user: singbox.ProxyUser{UUID: uuid},
				settings: singbox.InboundSettings{
					Flow:      "xtls-rprx-vision",
					Transport: singbox.TransportSettings{Type: "ws", Path: "/ws"},
					TLS:       singbox.TLSSettings{Enabled: true, ServerName: "example.com"},
				},
			},
		},
		{
			name: "vision-without-tls",
			node: node{
				name: "vision-plain", server: "1.2.3.4", port: 80, typ: "vless",
				user:     singbox.ProxyUser{UUID: uuid},
				settings: singbox.InboundSettings{Flow: "xtls-rprx-vision"},
			},
		},
		{
			name: "unsupported-transport-grpc",
			node: node{
				name: "grpc", server: "1.2.3.4", port: 443, typ: "vless",
				user: singbox.ProxyUser{UUID: uuid},
				settings: singbox.InboundSettings{
					Transport: singbox.TransportSettings{Type: "grpc"},
					TLS:       singbox.TLSSettings{Enabled: true, ServerName: "example.com"},
				},
			},
		},
		{
			name: "unquotable-double-quote-in-pbk",
			node: node{
				name: "bad-pbk", server: "1.2.3.4", port: 443, typ: "vless",
				user: singbox.ProxyUser{UUID: uuid},
				settings: singbox.InboundSettings{
					TLS: singbox.TLSSettings{Reality: singbox.RealitySettings{Enabled: true, PublicKey: `bad"key`}},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := surgeProxy(tc.node, tc.node.name); got != "" {
				t.Fatalf("expected %s to be skipped, got: %s", tc.name, got)
			}
		})
	}
}
