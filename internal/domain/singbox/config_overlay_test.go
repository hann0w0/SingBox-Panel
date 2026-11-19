package singbox

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestConfigOverlayUsesCurrentRendererForBothBaselines(t *testing.T) {
	// A renderer may acquire fields after this snapshot was saved. Its new
	// defaults appear on both projections and must not overwrite a custom value.
	p := ConfigProjection{Input: ServerConfigInput{Inbounds: []InboundInput{{Tag: "main", Type: "vless", ListenPort: 443, Settings: InboundSettings{SingleUser: true, UUID: "11111111-2222-3333-4444-555555555555", TLS: TLSSettings{Enabled: true}}}}}, IDs: map[string][]string{"inbounds": {"1"}}}
	raw := []byte(`{"inbounds":[{"type":"vless","tag":"main","listen_port":443,"tls":{"enabled":true,"server_name":"custom.example","future_tls":true},"users":[{"name":"manual","uuid":"11111111-2222-3333-4444-555555555555","extra":true}]}],"outbounds":[{"type":"direct","tag":"direct"}]}`)
	overlay, err := NewConfigOverlay(raw, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(overlay)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the defaults applied by a newer renderer, then edit another field.
	current := p
	current.Input.Inbounds = append([]InboundInput(nil), p.Input.Inbounds...)
	current.Input.Inbounds[0].ListenPort = 8443
	got, err := ApplyConfigOverlay(encoded, current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"server_name": "custom.example"`)) || !bytes.Contains(got, []byte(`"future_tls": true`)) {
		t.Fatalf("new defaults changed raw values: %s", got)
	}
	// A later explicit edit must take ownership of precisely that field.
	current.Input.Inbounds[0].Settings.TLS.ServerName = "edited.example"
	current.Input.Inbounds[0].Settings.UUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	got, err = ApplyConfigOverlay(encoded, current)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"server_name": "edited.example"`, `"future_tls": true`, `"extra": true`, `"name": "manual"`, `aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee`} {
		if !bytes.Contains(got, []byte(part)) {
			t.Fatalf("explicit edit lost %s: %s", part, got)
		}
	}
	// A disabled entry must disappear without being resurrected by preservation.
	current.Input.Inbounds = nil
	current.IDs = map[string][]string{}
	got, err = ApplyConfigOverlay(encoded, current)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte(`"tag": "main"`)) {
		t.Fatalf("disabled inbound resurrected: %s", got)
	}
}

func TestMergeConfigJSONNewObjectFieldKeepsUnknownChildren(t *testing.T) {
	got, err := mergeConfigJSON([]byte(`{"tls":{"future":{"custom":true}}}`), []byte(`{"tls":{}}`), []byte(`{"tls":{"future":{"enabled":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"tls":{"future":{"custom":true,"enabled":true}}}` {
		t.Fatalf("newly modelled object lost unknown child: %s", got)
	}
}

func TestConfigOverlayPreservesAbsentAndEmptySections(t *testing.T) {
	p := ConfigProjection{Input: ServerConfigInput{Inbounds: []InboundInput{{Tag: "main", Type: "socks", ListenPort: 1080, Settings: InboundSettings{SingleUser: true}}}}, IDs: map[string][]string{"inbounds": {"1"}}}
	overlay, err := NewConfigOverlay([]byte(`{"inbounds":[{"type":"socks","tag":"main","listen_port":1080}],"outbounds":[]}`), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(overlay)
	p.Input.Inbounds[0].ListenPort = 1081
	got, err := ApplyConfigOverlay(encoded, p)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatal(err)
	}
	if _, added := root["route"]; added || string(root["outbounds"]) != "[]" {
		t.Fatalf("absent/empty sections changed: %s", got)
	}
	overlay.Original = json.RawMessage(`null`)
	encoded, _ = json.Marshal(overlay)
	if _, err := ApplyConfigOverlay(encoded, p); err == nil {
		t.Fatal("corrupt snapshot silently accepted")
	}
}

func TestConfigOverlayRuleEditsKeepIdentity(t *testing.T) {
	p := ConfigProjection{
		Input: ServerConfigInput{
			StatsController: "127.0.0.1:29091",
			Rules: []RuleInput{
				{Port: []int{80}, Outbound: "direct"},
				{Action: "resolve", Extra: map[string]any{"strategy": "prefer_ipv4"}},
				{DomainSuffix: []string{"example.com"}, Outbound: "direct"},
			},
		},
		IDs:         map[string][]string{"rules": {"a", "b", "c"}},
		RuleTargets: []string{"direct", "", "direct"},
	}
	raw, _ := json.Marshal(map[string]any{"route": map[string]any{"rules": []any{
		map[string]any{"port": []int{80}, "outbound": "direct", "marker": "A"},
		map[string]any{"action": "resolve", "strategy": "prefer_ipv4", "marker": "B"},
		map[string]any{"domain_suffix": []string{"example.com"}, "outbound": "direct", "marker": "C"},
		LoopbackGuardRule(),
	}}})
	overlay, err := NewConfigOverlay(raw, p, map[int]string{0: "a", 1: "b", 2: "c"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(overlay)
	current := p
	current.Input.Rules = append([]RuleInput(nil), p.Input.Rules...)
	current.Input.Rules[0].Port = []int{81}
	for n := 0; n < 3; n++ {
		got, err := ApplyConfigOverlay(encoded, current)
		if err != nil {
			t.Fatal(err)
		}
		var root struct {
			Route struct {
				Rules []map[string]any `json:"rules"`
			} `json:"route"`
		}
		if err := json.Unmarshal(got, &root); err != nil {
			t.Fatal(err)
		}
		rules := root.Route.Rules
		if len(rules) != 4 {
			t.Fatalf("edit duplicated or added rules: %s", got)
		}
		if rules[0]["marker"] != "A" || canonicalJSON(rules[0]["port"]) != `[81]` || rules[0]["action"] == "resolve" {
			t.Fatalf("A edit changed another rule: %s", got)
		}
		if rules[1]["marker"] != "B" || rules[1]["action"] != "resolve" || rules[1]["port"] != nil || rules[1]["strategy"] != "prefer_ipv4" {
			t.Fatalf("B inherited A fields: %s", got)
		}
		if rules[2]["marker"] != "C" || canonicalJSON(rules[2]["domain_suffix"]) != `["example.com"]` || !IsLoopbackGuardRule(rules[3]) {
			t.Fatalf("other rules changed: %s", got)
		}
	}
}

func TestConfigOverlayRuleOrderAndOpaqueEntries(t *testing.T) {
	p := ConfigProjection{
		Input: ServerConfigInput{StatsController: "127.0.0.1:29091", Rules: []RuleInput{
			{Port: []int{80}, Outbound: "direct"},
			{Action: "resolve", Extra: map[string]any{"strategy": "prefer_ipv4"}},
		}},
		IDs:         map[string][]string{"rules": {"a", "b"}},
		RuleTargets: []string{"direct", ""},
	}
	raw, _ := json.Marshal(map[string]any{"route": map[string]any{"rules": []any{
		map[string]any{"action": "future-a", "large": json.Number("9007199254740993")},
		LoopbackGuardRule(),
		map[string]any{"port": []int{80}, "outbound": "direct", "marker": "A"},
		map[string]any{"action": "future-b", "nested": map[string]any{"keep": true}},
		map[string]any{"action": "resolve", "strategy": "prefer_ipv4", "marker": "B"},
	}}})
	overlay, err := NewConfigOverlay(raw, p, map[int]string{2: "a", 4: "b"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(overlay)
	got, err := ApplyConfigOverlay(encoded, p)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("no-op changed source: %s, %v", got, err)
	}
	unrelated := p
	unrelated.Input.LogLevel = "debug"
	got, err = ApplyConfigOverlay(encoded, unrelated)
	if err != nil {
		t.Fatal(err)
	}
	var source, updated map[string]json.RawMessage
	_ = json.Unmarshal(raw, &source)
	_ = json.Unmarshal(got, &updated)
	if canonicalRaw(source["route"]) != canonicalRaw(updated["route"]) {
		t.Fatalf("unrelated edit changed route order: %s", got)
	}
	current := p
	current.Input.Rules = []RuleInput{p.Input.Rules[1], p.Input.Rules[0]}
	current.IDs = map[string][]string{"rules": {"b", "a"}}
	current.RuleTargets = []string{"", "direct"}
	current.Input.Rules[1].Port = []int{81}
	got, err = ApplyConfigOverlay(encoded, current)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatal(err)
	}
	rules := root.Route.Rules
	if len(rules) != 5 || rules[0]["marker"] != "B" || rules[1]["marker"] != "A" || canonicalJSON(rules[1]["port"]) != `[81]` || rules[2]["action"] != "future-a" || rules[3]["action"] != "future-b" || !IsLoopbackGuardRule(rules[4]) || !bytes.Contains(got, []byte(`9007199254740993`)) {
		t.Fatalf("form order or opaque rules changed: %s", got)
	}
	// Deleting/disabling one form row must not reattach its raw fields elsewhere.
	current.Input.Rules = current.Input.Rules[:1]
	current.IDs["rules"] = current.IDs["rules"][:1]
	current.RuleTargets = current.RuleTargets[:1]
	got, err = ApplyConfigOverlay(encoded, current)
	if err != nil || bytes.Contains(got, []byte(`"marker": "A"`)) || bytes.Contains(got, []byte(`"port"`)) {
		t.Fatalf("deleted form rule restored or leaked: %s, %v", got, err)
	}
}

func TestConfigOverlayWithoutCompatibleRulesKeepsSourceOrder(t *testing.T) {
	raw := []byte(`{"log":{"level":"info"},"route":{"rules":[{"action":"future-a","data":1},{"action":"future-b","data":2}]}}`)
	p := ConfigProjection{Input: ServerConfigInput{LogLevel: "info"}}
	overlay, err := NewConfigOverlay(raw, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(overlay)
	p.Input.LogLevel = "debug"
	got, err := ApplyConfigOverlay(encoded, p)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(raw, &before)
	_ = json.Unmarshal(got, &after)
	if canonicalRaw(before["route"]) != canonicalRaw(after["route"]) {
		t.Fatalf("opaque-only routes changed: %s", got)
	}
}

func TestConfigOverlayRepairsLegacyGeneratedRuleBindings(t *testing.T) {
	p := ConfigProjection{
		Input: ServerConfigInput{StatsController: "127.0.0.1:29091", Rules: []RuleInput{
			{Action: "resolve", Extra: map[string]any{"strategy": "prefer_ipv4"}},
			{Domain: []string{"example.com"}, Outbound: "direct"},
		}},
		IDs: map[string][]string{"rules": {"a", "b"}}, RuleTargets: []string{"", "direct"},
	}
	raw, _ := json.Marshal(map[string]any{"route": map[string]any{"rules": []any{
		map[string]any{"action": "resolve", "strategy": "prefer_ipv4"}, LoopbackGuardRule(),
		map[string]any{"action": "sniff"}, map[string]any{"domain": []string{"example.com"}, "outbound": "direct"},
	}}})
	overlay, err := NewConfigOverlay(raw, p, map[int]string{0: "a", 3: "b"})
	if err != nil {
		t.Fatal(err)
	}
	// Imported snapshots mislabelled the guard; rendered migrations also
	// assigned generated prefix IDs to user rules and vice versa.
	for _, bindings := range [][]string{{"a", "auto:sniff", "", "b"}, {"stats:loopback", "auto:sniff", "a", "b"}} {
		overlay.Bindings["rules"] = bindings
		encoded, _ := json.Marshal(overlay)
		current := p
		current.Input.Rules = append([]RuleInput(nil), p.Input.Rules...)
		current.Input.Rules[1].Domain = []string{"edited.example"}
		got, err := ApplyConfigOverlay(encoded, current)
		if err != nil {
			t.Fatal(err)
		}
		var root struct {
			Route struct {
				Rules []map[string]any `json:"rules"`
			} `json:"route"`
		}
		if err := json.Unmarshal(got, &root); err != nil {
			t.Fatal(err)
		}
		rules := root.Route.Rules
		if len(rules) != 4 || rules[0]["action"] != "sniff" || rules[1]["action"] != "resolve" || canonicalJSON(rules[2]["domain"]) != `["edited.example"]` || !IsLoopbackGuardRule(rules[3]) {
			t.Fatalf("legacy bindings %v mixed or duplicated generated rules: %s", bindings, got)
		}
	}
}

func TestConfigOverlayRepairsLegacyRenderedRuleBindings(t *testing.T) {
	p := ConfigProjection{
		Input: ServerConfigInput{StatsController: "127.0.0.1:29091", Rules: []RuleInput{
			{Action: "resolve", Extra: map[string]any{"strategy": "prefer_ipv4"}},
			{Port: []int{443}, Outbound: "direct"},
		}},
		IDs: map[string][]string{"rules": {"a", "b"}}, RuleTargets: []string{"", "direct"},
	}
	raw, _ := json.Marshal(map[string]any{"route": map[string]any{"rules": []any{
		map[string]any{"action": "resolve", "strategy": "prefer_ipv4"}, LoopbackGuardRule(),
		map[string]any{"port": []int{443}, "outbound": "direct"},
	}}})
	overlay, err := NewConfigOverlay(raw, p, map[int]string{0: "a", 2: "b"})
	if err != nil {
		t.Fatal(err)
	}
	// Legacy residual migration assigned every rendered item the old ID order.
	overlay.Bindings["rules"] = []string{"stats:loopback", "a", "b"}
	encoded, _ := json.Marshal(overlay)
	p.Input.Rules[0].Extra = map[string]any{"strategy": "prefer_ipv6"}
	got, err := ApplyConfigOverlay(encoded, p)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatal(err)
	}
	rules := root.Route.Rules
	if len(rules) != 3 || rules[0]["action"] != "resolve" || rules[0]["strategy"] != "prefer_ipv6" || rules[0]["ip_cidr"] != nil || canonicalJSON(rules[1]["port"]) != `[443]` || !IsLoopbackGuardRule(rules[2]) {
		t.Fatalf("legacy migration mixed or duplicated rules: %s", got)
	}
}

func TestConfigOverlayRepairsLegacyRenderedCatchAllWithoutStealingLocalRule(t *testing.T) {
	p := ConfigProjection{
		Input: ServerConfigInput{StatsController: "127.0.0.1:29091", Rules: []RuleInput{
			{Outbound: "direct"},
			{IPCIDR: []string{"127.0.0.1/32"}, Port: []int{9928}, Outbound: "direct"},
			{Action: "resolve", Extra: map[string]any{"strategy": "prefer_ipv4"}},
		}},
		IDs: map[string][]string{"rules": {"a", "b", "c"}}, RuleTargets: []string{"direct", "direct", ""},
	}
	raw, _ := json.Marshal(map[string]any{"route": map[string]any{"rules": []any{
		map[string]any{"ip_cidr": []string{"127.0.0.1/32"}, "port": []int{9928}, "outbound": "direct", "extra": "local"},
		map[string]any{"action": "resolve", "strategy": "prefer_ipv4"}, LoopbackGuardRule(),
		map[string]any{"outbound": "direct", "extra": "catch-all"},
	}}})
	overlay, err := NewConfigOverlay(raw, p, map[int]string{0: "b", 1: "c", 3: "a"})
	if err != nil {
		t.Fatal(err)
	}
	overlay.Bindings["rules"] = []string{"stats:loopback", "a", "b", "c"}
	encoded, _ := json.Marshal(overlay)
	p.Input.Rules[0].Port = []int{81}
	got, err := ApplyConfigOverlay(encoded, p)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatal(err)
	}
	rules := root.Route.Rules
	if len(rules) != 4 || rules[0]["extra"] != "catch-all" || canonicalJSON(rules[0]["port"]) != `[81]` || rules[0]["ip_cidr"] != nil || rules[1]["extra"] != "local" || canonicalJSON(rules[1]["port"]) != `[9928]` || rules[2]["action"] != "resolve" || !IsLoopbackGuardRule(rules[3]) {
		t.Fatalf("legacy catch-all stole another rule's fields: %s", got)
	}
}
