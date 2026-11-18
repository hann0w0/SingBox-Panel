package singbox

import (
	"encoding/json"
	"testing"
)

func TestLoopbackGuardCoversUnspecifiedAndMetadataAddresses(t *testing.T) {
	cidrs, _ := LoopbackGuardRule()["ip_cidr"].([]string)
	have := map[string]bool{}
	for _, c := range cidrs {
		have[c] = true
	}
	// 0.0.0.0 and :: reach the local host on Linux; link-local and the metadata
	// endpoints hand out cloud instance credentials.
	for _, want := range []string{"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "100.100.100.200/32", "::/128", "::1/128", "fe80::/10", "fd00:ec2::254/128"} {
		if !have[want] {
			t.Errorf("loopback guard missing %s", want)
		}
	}
	// Intranet routing is an operator choice and must stay possible.
	for _, private := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		if have[private] {
			t.Errorf("loopback guard unexpectedly blocks %s", private)
		}
	}
}

func TestLegacyLoopbackGuardIsRecognisedAndUpgraded(t *testing.T) {
	legacy := legacyLoopbackGuardRule()
	raw, _ := json.Marshal(legacy)
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if !IsLoopbackGuardRule(generic) {
		t.Fatal("legacy guard not recognised")
	}
	config, _ := json.Marshal(map[string]any{
		"route": map[string]any{"rules": []any{legacy, map[string]any{"action": "sniff"}}, "final": "direct"},
	})
	upgraded := UpgradeLegacyLoopbackGuardJSON(config)
	var root struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(upgraded, &root); err != nil || len(root.Route.Rules) != 2 {
		t.Fatalf("upgraded = %s, err = %v", upgraded, err)
	}
	if canonicalJSON(root.Route.Rules[0]) != canonicalJSON(LoopbackGuardRule()) {
		t.Fatalf("legacy guard not replaced: %v", root.Route.Rules[0])
	}
	if canonicalJSON(root.Route.Rules[1]) != canonicalJSON(map[string]any{"action": "sniff"}) {
		t.Fatalf("other rule changed: %v", root.Route.Rules[1])
	}
	if string(UpgradeLegacyLoopbackGuardJSON([]byte(`{"route":{"rules":[]}}`))) != `{"route":{"rules":[]}}` {
		t.Fatal("config without a legacy guard was rewritten")
	}
}

func TestBuildServerConfigOrdersResolveAndNarrowLoopbackExceptionsBeforeGuard(t *testing.T) {
	raw, err := BuildServerConfig(ServerConfigInput{
		StatsController: "127.0.0.1:29091",
		StatsAPI:        "127.0.0.1:29092",
		StatsSecret:     "secret",
		Rules: []RuleInput{
			{Action: "resolve", Extra: map[string]any{"strategy": "prefer_ipv4"}},
			{IPCIDR: []string{"127.0.0.1/32"}, Port: []int{9928}, Outbound: "direct"},
			// Broad loopback or stats-port exceptions must stay behind LoopbackGuardRule.
			{IPCIDR: []string{"127.0.0.0/8"}, Port: []int{9928}, Outbound: "direct"},
			{IPCIDR: []string{"127.0.0.1/32"}, Port: []int{29091}, Outbound: "direct"},
			{DomainSuffix: []string{"example.com"}, Outbound: "direct"},
		},
	})
	if err != nil {
		t.Fatalf("BuildServerConfig: %v", err)
	}
	var root struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	// Expected order:
	// 0: resolve (prefer_ipv4)
	// 1: 127.0.0.1/32:9928 -> direct
	// 2: LoopbackGuardRule
	// 3: auto sniff (because example.com domain_suffix is present)
	// 4: broad 127.0.0.0/8:9928 (blocked by guard above)
	// 5: stats port 127.0.0.1/32:29091 (blocked by guard above)
	// 6: example.com -> direct
	if len(root.Route.Rules) != 7 {
		t.Fatalf("expected 7 rules, got %d: %s", len(root.Route.Rules), raw)
	}
	if root.Route.Rules[0]["action"] != "resolve" || root.Route.Rules[0]["strategy"] != "prefer_ipv4" {
		t.Fatalf("rule[0] should be resolve, got %v", root.Route.Rules[0])
	}
	if root.Route.Rules[1]["outbound"] != "direct" {
		t.Fatalf("rule[1] should be 127.0.0.1/32:9928 -> direct, got %v", root.Route.Rules[1])
	}
	if !IsLoopbackGuardRule(root.Route.Rules[2]) {
		t.Fatalf("rule[2] should be LoopbackGuardRule, got %v", root.Route.Rules[2])
	}
	if root.Route.Rules[3]["action"] != "sniff" {
		t.Fatalf("rule[3] should be auto sniff, got %v", root.Route.Rules[3])
	}
	if root.Route.Rules[4]["outbound"] != "direct" || root.Route.Rules[5]["outbound"] != "direct" {
		t.Fatalf("unsafe loopback rules must remain after LoopbackGuardRule: %v", root.Route.Rules[4:6])
	}
}
