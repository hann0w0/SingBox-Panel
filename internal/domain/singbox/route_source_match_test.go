package singbox

import (
	"encoding/json"
	"testing"
)

func TestRuleSetIPSourceMatchRoundTrip(t *testing.T) {
	for _, source := range []bool{false, true} {
		for _, action := range []string{"route", "reject"} {
			t.Run(action+map[bool]string{false: "/target", true: "/source"}[source], func(t *testing.T) {
				outbound := "direct"
				if action == "reject" {
					outbound = "block"
				}
				rule := RuleInput{
					Action: action, Inbound: []string{"dmit-in"}, RuleSet: []string{"geoip-cn"},
					RuleSetIPCIDRMatchSource: source, Outbound: outbound,
				}
				raw, err := BuildServerConfig(ServerConfigInput{Rules: []RuleInput{rule}})
				if err != nil {
					t.Fatal(err)
				}
				var config struct {
					Route struct {
						Rules []map[string]any `json:"rules"`
					} `json:"route"`
				}
				if err := json.Unmarshal(raw, &config); err != nil {
					t.Fatal(err)
				}
				got, exists := config.Route.Rules[0]["rule_set_ip_cidr_match_source"]
				if source && got != true || !source && exists {
					t.Fatalf("source match flag = %v, exists %v, want source %v", got, exists, source)
				}
				parsed, err := ParseServerConfig(raw)
				if err != nil {
					t.Fatal(err)
				}
				if len(parsed.Rules) != 1 || parsed.Rules[0].Match.RuleSetIPCIDRMatchSource != source {
					t.Fatalf("source match lost during import: %+v", parsed.Rules)
				}
				parsedRule := parsed.Rules[0].Match
				parsedRule.Outbound = parsed.Rules[0].Outbound
				rebuilt, err := BuildServerConfig(ServerConfigInput{Rules: []RuleInput{parsedRule}})
				if err != nil {
					t.Fatal(err)
				}
				if string(rebuilt) != string(raw) {
					t.Fatalf("import changed source matching:\n%s\n%s", raw, rebuilt)
				}
			})
		}
	}
}

func TestActionRulesDropHiddenRuleSetSourceMatch(t *testing.T) {
	for _, action := range []string{"sniff", "hijack-dns"} {
		rule := buildRouteRule(RuleInput{Action: action, RuleSet: []string{"geoip-cn"}, RuleSetIPCIDRMatchSource: true})
		if _, exists := rule["rule_set_ip_cidr_match_source"]; exists {
			t.Fatalf("%s retained hidden source match: %v", action, rule)
		}
	}
}
