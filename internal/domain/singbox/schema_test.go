package singbox

import (
	"encoding/json"
	"testing"
)

func TestSchemaBundleCoversProtocolsAndComplexFields(t *testing.T) {
	bundle := GetSchemaBundle()
	if bundle.Version == "" {
		t.Fatal("schema version must not be empty")
	}
	if bundle.Inbound == nil || bundle.Outbound == nil || bundle.RouteRule == nil {
		t.Fatal("inbound, outbound, and route_rule schemas must be populated")
	}

	for _, typ := range []string{"vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic", "anytls", "snell", "socks", "mixed", "http", "tun", "direct", "redirect", "tproxy"} {
		if _, ok := bundle.Inbound.Variants[typ]; !ok {
			t.Errorf("missing inbound schema variant %q", typ)
		}
	}

	for _, typ := range []string{"shadowsocks", "snell", "socks", "http", "tuic", "trojan", "anytls", "vless", "vmess", "hysteria2"} {
		if _, ok := bundle.Outbound.Variants[typ]; !ok {
			t.Errorf("missing outbound schema variant %q", typ)
		}
	}

	// Verify server-side inbound schema excludes client-only TLS fields and TUN listen fields.
	vlessTLS := bundle.Inbound.Variants["vless"].Properties["tls"].Properties
	if _, hasInsecure := vlessTLS["insecure"]; hasInsecure {
		t.Error("server inbound TLS schema must not include client-only field insecure")
	}
	if _, hasUTLS := vlessTLS["utls"]; hasUTLS {
		t.Error("server inbound TLS schema must not include client-only field utls")
	}
	if _, hasListen := bundle.Inbound.Variants["tun"].Properties["listen"]; hasListen {
		t.Error("tun inbound schema must not include listen")
	}
	if _, hasMultiplex := bundle.Inbound.Variants["hysteria2"].Properties["multiplex"]; hasMultiplex {
		t.Error("hysteria2 inbound schema must not include multiplex")
	}

	for _, key := range []string{
		"domain_regex", "port_range", "process_name", "process_path_regex", "package_name_regex",
		"clash_mode", "ip_is_private", "source_ip_is_private", "invert", "no_drop",
		"disable_optimistic_cache", "rewrite_ttl", "client_subnet",
		"network_strategy", "network_type", "fallback_network_type", "fallback_delay",
		"tls_fragment", "tls_fragment_fallback_delay", "tls_record_fragment",
		"tls_spoof", "tls_spoof_method",
	} {
		if _, ok := bundle.RouteRule.Properties[key]; !ok {
			t.Errorf("missing route_rule schema property %q", key)
		}
	}

	if enum := bundle.RouteRule.Properties["action"].Enum; len(enum) != 7 {
		t.Errorf("route action enum = %v, want route/reject/sniff/hijack-dns/resolve/route-options/empty", enum)
	}
}

func TestValidateJSONAgainstSchemaAcceptsComplexAndUnknownFields(t *testing.T) {
	bundle := GetSchemaBundle()

	err := ValidateJSONAgainstSchema(bundle.Inbound, map[string]any{
		"type":          "vless",
		"tag":           "vless-in",
		"listen":        "::",
		"listen_port":   float64(443),
		"tcp_fast_open": true,
		"future_option": "kept-verbatim",
		"tls": map[string]any{
			"enabled":     true,
			"min_version": "1.2",
			"ech": map[string]any{
				"enabled": true,
			},
		},
		"transport": map[string]any{
			"type":         "grpc",
			"service_name": "TunService",
		},
	})
	if err != nil {
		t.Fatalf("valid complex inbound rejected: %v", err)
	}

	err = ValidateJSONAgainstSchema(bundle.Inbound, map[string]any{
		"type":        "vless",
		"listen_port": float64(70000),
	})
	if err == nil {
		t.Fatal("expected out-of-range listen_port to fail schema validation")
	}

	err = ValidateJSONAgainstSchema(bundle.RouteRule, map[string]any{
		"action":       "route",
		"domain_regex": []any{"^ad\\."},
		"port_range":   []any{"1000:2000"},
		"invert":       true,
		"custom_flag":  42,
	})
	if err != nil {
		t.Fatalf("valid complex route rule rejected: %v", err)
	}

	err = ValidateJSONAgainstSchema(bundle.RouteRule, map[string]any{
		"action": "route",
		"port":   []any{float64(0)},
	})
	if err == nil {
		t.Fatal("expected invalid port 0 in route rule to fail schema validation")
	}

	err = ValidateJSONAgainstSchema(bundle.RouteRule, map[string]any{
		"action":      "resolve",
		"rewrite_ttl": float64(-1),
	})
	if err == nil {
		t.Fatal("expected negative rewrite_ttl in route rule to fail schema validation")
	}

	err = ValidateJSONAgainstSchema(bundle.RouteRule, map[string]any{
		"action":      "resolve",
		"rewrite_ttl": float64(0),
	})
	if err != nil {
		t.Fatalf("zero rewrite_ttl rejected: %v", err)
	}

	err = ValidateJSONAgainstSchema(bundle.Outbound, map[string]any{
		"type":            "vless",
		"tag":             "relay-out",
		"server":          "edge.example.com",
		"server_port":     float64(443),
		"detour":          "upstream",
		"connect_timeout": "5s",
		"transport":       map[string]any{"type": "grpc", "service_name": "GunService"},
		"multiplex":       map[string]any{"enabled": true, "protocol": "h2mux", "max_connections": float64(4)},
		"tls":             map[string]any{"enabled": true, "insecure": false, "utls": map[string]any{"enabled": true, "fingerprint": "chrome"}},
	})
	if err != nil {
		t.Fatalf("valid complex outbound rejected: %v", err)
	}

	// Typed Go slices (such as []string written into s.Raw by syncSecretsToRaw)
	// must also satisfy array schema fields like tls.reality.short_id.
	err = ValidateJSONAgainstSchema(bundle.Inbound, map[string]any{
		"type":        "vless",
		"tag":         "vless-reality",
		"listen_port": float64(443),
		"tls": map[string]any{
			"enabled": true,
			"reality": map[string]any{
				"enabled":     true,
				"private_key": "example-priv-key",
				"short_id":    []string{"dff29bbb"},
				"handshake":   map[string]any{"server": "www.apple.com", "server_port": float64(443)},
			},
		},
	})
	if err != nil {
		t.Fatalf("typed string slice short_id rejected: %v", err)
	}
}

func TestBuildInboundStripsRawVLESSFlowAndSnellUsers(t *testing.T) {
	vlessBytes, err := BuildInbound(InboundInput{
		Tag:        "vless-raw",
		Type:       "vless",
		ListenPort: 443,
		Settings: InboundSettings{
			Flow: "xtls-rprx-vision",
			TLS: TLSSettings{
				Enabled:    true,
				ServerName: "www.apple.com",
				Reality: RealitySettings{
					Enabled:             true,
					HandshakeServer:     "www.apple.com",
					HandshakeServerPort: 443,
					PrivateKey:          "priv",
					ShortID:             []string{"abcd"},
				},
			},
			Raw: map[string]any{
				"type":          "vless",
				"tag":           "vless-raw",
				"flow":          "xtls-rprx-vision",
				"tcp_fast_open": true,
			},
		},
		Users: []ProxyUser{{Name: "u1", UUID: "11111111-1111-1111-1111-111111111111"}},
	})
	if err != nil {
		t.Fatalf("BuildInbound vless: %v", err)
	}
	var vlessMap map[string]any
	if err := json.Unmarshal(vlessBytes, &vlessMap); err != nil {
		t.Fatalf("unmarshal vless: %v", err)
	}
	if _, leaked := vlessMap["flow"]; leaked {
		t.Fatalf("top-level flow must not leak onto vless inbound: %v", vlessMap)
	}

	snellBytes, err := BuildInbound(InboundInput{
		Tag:        "snell-raw",
		Type:       "snell",
		ListenPort: 10021,
		Settings: InboundSettings{
			SingleUser:   true,
			SnellVersion: 6,
			SnellPSK:     "8aV2P5T/MQuv8MJ077rHzKA5CvZwOq9+xnk/Y726PNo=",
			Raw: map[string]any{
				"type":  "snell",
				"users": []any{map[string]any{"name": "legacy"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildInbound snell: %v", err)
	}
	var snellMap map[string]any
	if err := json.Unmarshal(snellBytes, &snellMap); err != nil {
		t.Fatalf("unmarshal snell: %v", err)
	}
	if _, leaked := snellMap["users"]; leaked {
		t.Fatalf("users must not leak onto snell inbound: %v", snellMap)
	}
}

func TestSyncFromRawUpdatesIPv6OnlyAndTLSMode(t *testing.T) {
	s := InboundSettings{
		Listen:   "2001:db8::1",
		IPv6Only: true,
		TLS:      TLSSettings{Enabled: true, SelfSigned: true},
		Raw: map[string]any{
			"listen": "::",
			"tls": map[string]any{
				"enabled":     true,
				"server_name": "www.apple.com",
				"reality": map[string]any{
					"enabled":     true,
					"private_key": "priv",
					"short_id":    []any{"abcd"},
					"handshake":   map[string]any{"server": "www.apple.com", "server_port": 443},
				},
			},
			"packet_encoding": "",
		},
	}
	s.SyncFromRaw("vless")
	if s.IPv6Only {
		t.Fatal("expected IPv6Only to be cleared when listen changes to ::\n")
	}
	if s.TLS.SelfSigned {
		t.Fatal("expected SelfSigned to be cleared when switching to reality")
	}
	if s.PacketEncoding != "none" {
		t.Fatalf("expected empty packet_encoding in raw to set PacketEncoding=none, got %q", s.PacketEncoding)
	}

	// Preserving ipv6_only when listen in raw is unchanged (e.g. form edits with
	// listen_mode=ipv6 and existing raw fields).
	ipv6Form := InboundSettings{
		Listen:   "::",
		IPv6Only: true,
		Raw:      map[string]any{"listen": "::", "type": "shadowsocks"},
	}
	ipv6Form.SyncFromRaw("shadowsocks")
	if !ipv6Form.IPv6Only {
		t.Fatal("expected IPv6Only to be preserved when raw listen matches existing ::")
	}
}

func TestValidateInboundRawEnforcesActualType(t *testing.T) {
	// A mismatched "type" inside Raw must not bypass validation against the
	// actual inbound protocol type (e.g. trojan-only fields on a vless inbound).
	s := InboundSettings{
		Raw: map[string]any{
			"type": "trojan",
			"flow": "invalid-flow-value",
		},
	}
	if err := s.Validate("vless"); err == nil {
		t.Fatal("expected Raw validation to enforce actual inbound type vless")
	}
}
