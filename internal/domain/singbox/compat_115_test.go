package singbox

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// sing-box 1.15 refuses to start with options deprecated in 1.14: the inline
// tls.acme object, a remote rule-set download_detour, and remote rule-sets
// without an explicit HTTP client. These tests pin the replacement output.

func TestBuildServerConfigRuleSetsUseExplicitHTTPClients(t *testing.T) {
	raw, err := BuildServerConfig(ServerConfigInput{
		Outbounds: []OutboundInput{
			{Tag: "land", Type: "socks", Server: "1.2.3.4", ServerPort: 1080},
			{Tag: "direct-2", Type: "direct"},
		},
		Final: "land",
		RuleSets: []RuleSetInput{
			{Tag: "via-final", URL: "https://example.com/a.srs"},
			{Tag: "via-direct", URL: "https://example.com/b.srs", DownloadDetour: "direct"},
			{Tag: "via-land", URL: "https://example.com/c.srs", DownloadDetour: "land"},
			{Tag: "via-direct-2", URL: "https://example.com/d.srs", DownloadDetour: "direct-2"},
			{Tag: "local", Type: "local", Path: "/etc/sing-box/local.srs"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "download_detour") {
		t.Fatalf("config still emits download_detour:\n%s", raw)
	}
	var cfg struct {
		HTTPClients []map[string]any `json:"http_clients"`
		Route       struct {
			DefaultHTTPClient string           `json:"default_http_client"`
			RuleSet           []map[string]any `json:"rule_set"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	wantClients := []map[string]any{
		{"tag": "http-via-land", "detour": "land"},
		{"tag": "http-direct"},
	}
	if !reflect.DeepEqual(cfg.HTTPClients, wantClients) {
		t.Fatalf("http_clients = %v, want %v", cfg.HTTPClients, wantClients)
	}
	if cfg.Route.DefaultHTTPClient != "http-via-land" {
		t.Fatalf("default_http_client = %q", cfg.Route.DefaultHTTPClient)
	}
	want := map[string]any{
		"via-final":    "http-via-land", // empty detour keeps "default outbound" = final
		"via-direct":   "http-direct",   // never a detour to an empty direct outbound
		"via-land":     "http-via-land",
		"via-direct-2": "http-direct",
		"local":        nil,
	}
	for _, rs := range cfg.Route.RuleSet {
		if got := rs["http_client"]; got != want[rs["tag"].(string)] {
			t.Errorf("rule-set %v http_client = %v, want %v", rs["tag"], got, want[rs["tag"].(string)])
		}
	}
}

func TestBuildServerConfigWithoutRemoteRuleSetsHasNoHTTPClients(t *testing.T) {
	raw, err := BuildServerConfig(ServerConfigInput{
		RuleSets: []RuleSetInput{{Tag: "local", Type: "local", Path: "/etc/sing-box/local.srs"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "http_client") {
		t.Fatalf("local-only rule-sets must not emit HTTP clients:\n%s", raw)
	}
}

func TestBuildTLSUsesACMECertificateProvider(t *testing.T) {
	raw, err := json.Marshal(buildTLS(TLSSettings{Enabled: true, ACMEDomain: "example.com", ACMEEmail: "ops@example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	var tls map[string]any
	if err := json.Unmarshal(raw, &tls); err != nil {
		t.Fatal(err)
	}
	if _, exists := tls["acme"]; exists {
		t.Fatalf("tls still emits the deprecated inline acme: %s", raw)
	}
	cp, _ := tls["certificate_provider"].(map[string]any)
	if cp["type"] != "acme" || cp["email"] != "ops@example.com" ||
		!reflect.DeepEqual(cp["domain"], []any{"example.com"}) || tls["server_name"] != "example.com" {
		t.Fatalf("tls = %s", raw)
	}
}

func TestParseServerConfigRoundTripsNewSyntax(t *testing.T) {
	raw, err := BuildServerConfig(ServerConfigInput{
		Inbounds: []InboundInput{{
			Tag: "tr", Type: "trojan", ListenPort: 443,
			Settings: InboundSettings{TLS: TLSSettings{Enabled: true, ACMEDomain: "example.com", ACMEEmail: "ops@example.com"}},
			Users:    []ProxyUser{{Name: "u", Password: "p"}},
		}},
		Outbounds: []OutboundInput{{Tag: "land", Type: "socks", Server: "1.2.3.4", ServerPort: 1080}},
		RuleSets: []RuleSetInput{
			{Tag: "a", URL: "https://example.com/a.srs", DownloadDetour: "land"},
			{Tag: "b", URL: "https://example.com/b.srs", DownloadDetour: "direct"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Inbounds) != 1 || parsed.Inbounds[0].Settings.TLS.ACMEDomain != "example.com" ||
		parsed.Inbounds[0].Settings.TLS.ACMEEmail != "ops@example.com" {
		t.Fatalf("ACME not recovered: %+v", parsed.Inbounds)
	}
	got := map[string]string{}
	for _, rs := range parsed.RuleSets {
		got[rs.Tag] = rs.DownloadDetour
	}
	if !reflect.DeepEqual(got, map[string]string{"a": "land", "b": "direct"}) {
		t.Fatalf("download routes = %v", got)
	}
}

func TestParseServerConfigReadsSharedProvidersAndLegacySyntax(t *testing.T) {
	raw := []byte(`{
	  "certificate_providers": [
	    {"type": "acme", "tag": "cert", "domain": ["shared.example.com"], "email": "a@example.com"},
	    {"type": "cloudflare-origin-ca", "tag": "cf"}
	  ],
	  "http_clients": [{"tag": "hc", "detour": "land"}],
	  "inbounds": [
	    {"type": "trojan", "tag": "shared", "listen_port": 1, "users": [{"password": "p"}],
	     "tls": {"enabled": true, "certificate_provider": "cert"}},
	    {"type": "trojan", "tag": "legacy", "listen_port": 2, "users": [{"password": "p"}],
	     "tls": {"enabled": true, "acme": {"domain": ["legacy.example.com"]}}},
	    {"type": "trojan", "tag": "cloudflare", "listen_port": 3, "users": [{"password": "p"}],
	     "tls": {"enabled": true, "certificate_provider": "cf"}}
	  ],
	  "outbounds": [{"type": "socks", "tag": "land", "server": "1.2.3.4", "server_port": 1080}],
	  "route": {"rule_set": [
	    {"type": "remote", "tag": "legacy", "url": "https://example.com/l.srs", "download_detour": "land"},
	    {"type": "remote", "tag": "default", "url": "https://example.com/d.srs"},
	    {"type": "remote", "tag": "inline", "url": "https://example.com/i.srs", "http_client": {}}
	  ]}
	}`)
	parsed, err := ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	domains := map[string]string{}
	for _, in := range parsed.Inbounds {
		domains[in.Tag] = in.Settings.TLS.ACMEDomain
	}
	if domains["shared"] != "shared.example.com" || domains["legacy"] != "legacy.example.com" || domains["cloudflare"] != "" {
		t.Fatalf("ACME domains = %v", domains)
	}
	if !strings.Contains(strings.Join(parsed.Skipped, "\n"), "cloudflare-origin-ca") {
		t.Fatalf("unsupported certificate provider was not reported: %v", parsed.Skipped)
	}
	got := map[string]string{}
	for _, rs := range parsed.RuleSets {
		got[rs.Tag] = rs.DownloadDetour
	}
	// "default" falls back to the first http_clients entry, like sing-box does.
	want := map[string]string{"legacy": "land", "default": "land", "inline": "direct"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("download routes = %v, want %v", got, want)
	}
}
