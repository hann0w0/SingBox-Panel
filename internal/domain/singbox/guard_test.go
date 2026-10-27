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
