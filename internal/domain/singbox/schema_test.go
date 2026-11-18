package singbox

import "testing"

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

	for _, key := range []string{"domain_regex", "port_range", "process_name", "clash_mode", "ip_is_private", "invert"} {
		if _, ok := bundle.RouteRule.Properties[key]; !ok {
			t.Errorf("missing route_rule schema property %q", key)
		}
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
