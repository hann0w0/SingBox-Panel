package singbox

import (
	"encoding/json"
	"testing"
)

const residualSource = `{
  "log": {"level": "info", "timestamp": true, "output": "/var/log/sing-box.log"},
  "dns": {"servers": [{"address": "1.1.1.1"}]},
  "ntp": {"enabled": true, "server": "time.apple.com"},
  "inbounds": [
    {
      "type": "vless", "tag": "vl", "listen": "::", "listen_port": 443,
      "users": [{"name": "u", "uuid": "11111111-1111-1111-1111-111111111111"}],
      "tls": {"enabled": true, "server_name": "a.com"},
      "smux": {"enabled": true, "brutal": {"enabled": true, "up_mbps": 10}},
      "udp_timeout": "5m"
    },
    {
      "type": "wireguard", "tag": "wg-in", "listen_port": 51820,
      "private_key": "keep-me", "peers": [{"public_key": "peer"}]
    }
  ],
  "outbounds": [
    {"type": "direct", "tag": "direct"},
    {"type": "tor", "tag": "tor-out", "executable_path": "/usr/bin/tor"}
  ],
  "route": {
    "final": "direct",
    "default_domain_resolver": {"server": "local"},
    "rules": [
      {"action": "sniff"},
      {"domain_suffix": ["a.com"], "outbound": "direct", "invert": true, "clash_mode": "rule"}
    ],
    "rule_set": [
      {"type": "remote", "tag": "geoip-cn", "format": "binary", "url": "https://example.com/cn.srs", "download_detour": "direct", "http_client": "custom"}
    ]
  },
  "experimental": {
    "clash_api": {"external_controller": "127.0.0.1:9090"},
    "cache_file": {"enabled": true, "store_rdrc": true},
    "v2ray_api": {"listen": "127.0.0.1:8080"}
  }
}`

func TestExtractResidualKeepsEverythingThePanelCannotModel(t *testing.T) {
	parsed, err := ParseServerConfig([]byte(residualSource))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := ExtractResidual([]byte(residualSource), parsed)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	if got := len(res.PassThroughInbounds); got != 1 {
		t.Fatalf("pass-through inbounds = %d, want 1 (wireguard)", got)
	}
	if got := len(res.PassThroughOutbounds); got != 1 {
		t.Fatalf("pass-through outbounds = %d, want 1 (tor)", got)
	}
	for _, key := range []string{"dns", "ntp"} {
		if _, ok := res.TopLevel[key]; !ok {
			t.Errorf("top-level %q not preserved", key)
		}
	}
	if _, ok := res.Log["output"]; !ok {
		t.Error("log.output not preserved")
	}
	if _, ok := res.Inbounds["vl"]["smux"]; !ok {
		t.Error("inbound smux not preserved")
	}
	if _, ok := res.Inbounds["vl"]["udp_timeout"]; !ok {
		t.Error("inbound udp_timeout not preserved")
	}
	if _, ok := res.Route["default_domain_resolver"]; !ok {
		t.Error("route.default_domain_resolver not preserved")
	}
	if _, ok := res.RuleFields["1"]["invert"]; !ok {
		t.Error("rule invert not preserved")
	}
	if _, ok := res.RuleSets["geoip-cn"]["http_client"]; !ok {
		t.Error("rule_set http_client not preserved")
	}
	if _, ok := res.Experimental["cache_file"]; !ok {
		t.Error("experimental.cache_file not preserved")
	}
}

func TestApplyResidualRestoresUnknownFieldsAndKeepsPanelValues(t *testing.T) {
	parsed, err := ParseServerConfig([]byte(residualSource))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := ExtractResidual([]byte(residualSource), parsed)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	// Render as the panel would: same tag, but the panel owns the port.
	rendered, err := BuildServerConfig(ServerConfigInput{
		Inbounds: []InboundInput{{
			Tag: "vl", Type: "vless", ListenPort: 8443,
			Settings: InboundSettings{
				UUID: "11111111-1111-1111-1111-111111111111", SingleUser: true,
				TLS: TLSSettings{Enabled: true, ServerName: "a.com", CertificatePath: "/etc/ssl/a.pem", KeyPath: "/etc/ssl/a.key"},
			},
		}},
		Rules: []RuleInput{{Action: "sniff"}},
		Final: "direct",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	merged, err := ApplyResidual(rendered, res)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(merged, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["dns"]; !ok {
		t.Error("dns block lost after merge")
	}
	if _, ok := cfg["ntp"]; !ok {
		t.Error("ntp block lost after merge")
	}
	if log, ok := cfg["log"].(map[string]any); !ok || log["output"] == nil {
		t.Error("log.output lost after merge")
	}

	inbounds, _ := cfg["inbounds"].([]any)
	if len(inbounds) != 2 {
		t.Fatalf("inbounds = %d, want 2 (rendered vless + pass-through wireguard)", len(inbounds))
	}
	first, _ := inbounds[0].(map[string]any)
	if first["listen_port"] != float64(8443) {
		t.Errorf("panel port = %v, want 8443 to win over the stale 443", first["listen_port"])
	}
	if _, ok := first["smux"]; !ok {
		t.Error("smux lost after merge")
	}
	second, _ := inbounds[1].(map[string]any)
	if second["type"] != "wireguard" || second["private_key"] != "keep-me" {
		t.Errorf("pass-through inbound altered: %v", second)
	}

	outbounds, _ := cfg["outbounds"].([]any)
	foundTOR := false
	for _, entry := range outbounds {
		if ob, ok := entry.(map[string]any); ok && ob["type"] == "tor" {
			foundTOR = true
		}
	}
	if !foundTOR {
		t.Error("pass-through tor outbound lost")
	}

	exp, _ := cfg["experimental"].(map[string]any)
	if _, ok := exp["cache_file"]; !ok {
		t.Error("experimental.cache_file lost after merge")
	}
	if _, ok := exp["clash_api"]; !ok {
		t.Error("panel clash_api lost after merge")
	}
}
