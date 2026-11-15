package singbox

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestServerRuleRoundTripPreservesAuthenticationScope(t *testing.T) {
	for _, names := range []any{"alice", []string{"alice", "bob"}} {
		raw, err := json.Marshal(map[string]any{"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}}, "route": map[string]any{"rules": []any{map[string]any{"auth_user": names, "domain_suffix": []string{"example.org"}, "outbound": "direct"}}}})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseServerConfig(raw)
		if err != nil || len(parsed.Rules) != 1 {
			t.Fatalf("parse: %+v %v", parsed, err)
		}
		rule := parsed.Rules[0].Match
		rule.Outbound = parsed.Rules[0].Outbound
		built, err := BuildServerConfig(ServerConfigInput{Rules: []RuleInput{rule}})
		if err != nil {
			t.Fatal(err)
		}
		again, err := ParseServerConfig(built)
		if err != nil || len(again.Rules) < 1 {
			t.Fatalf("reparse: %+v %v", again, err)
		}
		found := false
		for _, candidate := range again.Rules {
			found = found || len(candidate.Match.AuthUser) > 0 && reflect.DeepEqual(candidate.Match.AuthUser, rule.AuthUser)
		}
		if !found {
			t.Fatalf("authentication scope lost: %s", built)
		}
	}
}
