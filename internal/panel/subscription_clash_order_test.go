package panel

import (
	"strings"
	"testing"
)

func TestMarshalClashProxiesYAMLOrdersFields(t *testing.T) {
	proxies := []map[string]any{
		{
			"client-fingerprint": "chrome",
			"name":               "🇺🇸 AnyTLS",
			"password":           "secret",
			"port":               8443,
			"server":             "edge.example.com",
			"skip-cert-verify":   true,
			"sni":                "cdn.example.com",
			"type":               "anytls",
			"udp":                true,
		},
		{
			"name":    "Snell",
			"port":    12814,
			"psk":     "secret-psk",
			"server":  "snell.example.com",
			"type":    "snell",
			"udp":     true,
			"version": 5,
		},
	}

	data, err := marshalClashProxiesYAML(proxies)
	if err != nil {
		t.Fatalf("marshal Clash proxies: %v", err)
	}
	got := string(data)
	want := `proxies:
  - type: anytls
    name: 🇺🇸 AnyTLS
    server: edge.example.com
    port: 8443
    password: secret
    sni: cdn.example.com
    client-fingerprint: chrome
    skip-cert-verify: true
    udp: true
  - type: snell
    name: Snell
    server: snell.example.com
    port: 12814
    psk: secret-psk
    version: 5
    udp: true
`
	if got != want {
		t.Fatalf("unexpected Clash YAML order:\n%s", got)
	}
}

func TestMarshalClashProxiesYAMLEndsWithSkipVerifyThenUDP(t *testing.T) {
	data, err := marshalClashProxiesYAML([]map[string]any{{
		"type":               "vless",
		"name":               "🇹🇼 VLESS",
		"server":             "edge.example.com",
		"port":               443,
		"uuid":               "00000000-0000-0000-0000-000000000000",
		"client-fingerprint": "chrome",
		"flow":               "xtls-rprx-vision",
		"network":            "tcp",
		"servername":         "cdn.example.com",
		"udp":                true,
		"skip-cert-verify":   true,
		"tls":                true,
	}})
	if err != nil {
		t.Fatalf("marshal Clash proxy: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "name: 🇹🇼 VLESS") {
		t.Fatalf("Clash proxy name does not preserve flag emoji:\n%s", got)
	}
	if strings.Contains(got, `\U0001`) {
		t.Fatalf("Clash proxy name contains escaped flag emoji:\n%s", got)
	}
	want := `proxies:
  - type: vless
    name: 🇹🇼 VLESS
    server: edge.example.com
    port: 443
    uuid: 00000000-0000-0000-0000-000000000000
    flow: xtls-rprx-vision
    network: tcp
    servername: cdn.example.com
    client-fingerprint: chrome
    tls: true
    skip-cert-verify: true
    udp: true
`
	if got != want {
		t.Fatalf("unexpected VLESS Clash YAML order:\n%s", got)
	}
}
