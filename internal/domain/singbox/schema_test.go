package singbox

import "testing"

func TestSchemaBundleCoversProtocolsAndComplexFields(t *testing.T) {
	bundle := GetSchemaBundle()
	if bundle.Version == "" {
		t.Fatal("schema version must not be empty")
	}
	if bundle.Inbound == nil || bundle.RouteRule == nil {
		t.Fatal("inbound and route_rule schemas must be populated")
	}

	for _, typ := range []string{"vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic", "anytls", "snell", "socks", "mixed", "http", "tun", "direct", "redirect", "tproxy"} {
		if _, ok := bundle.Inbound.Variants[typ]; !ok {
			t.Errorf("missing inbound schema variant %q", typ)
		}
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
}
