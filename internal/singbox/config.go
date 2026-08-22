package singbox

import (
	"encoding/json"
	"fmt"
)

// ServerConfigInput is everything needed to render a server's full config.json.
type ServerConfigInput struct {
	// LogLevel defaults to "warn".
	LogLevel string
	Inbounds []InboundInput

	// Outbounds/Rules/RuleSets/Final drive routing (transit / landing). A
	// "direct" outbound is always included. Final is the default outbound tag.
	Outbounds []OutboundInput
	Rules     []RuleInput
	RuleSets  []RuleSetInput
	Final     string

	// StatsController enables the loopback-only Clash API used by the Agent to
	// collect node and inbound-port traffic counters.
	StatsController string
}

// routeOut is the route block with a fixed key order: rules, then rule_set,
// then the "direct" fallback (final) at the very bottom.
type routeOut struct {
	Rules   []map[string]any `json:"rules,omitempty"`
	RuleSet []map[string]any `json:"rule_set,omitempty"`
	Final   string           `json:"final"`
}

// configOut is the whole config.json with a fixed, human-friendly key order:
// log → inbounds → outbounds → route (experimental last, since it's the panel's
// own stats hook, not part of the proxy config). Marshaling a struct preserves
// field order, unlike a map (which sorts keys alphabetically).
type configOut struct {
	Log          map[string]any    `json:"log"`
	Inbounds     []json.RawMessage `json:"inbounds"`
	Outbounds    []json.RawMessage `json:"outbounds"`
	Route        routeOut          `json:"route"`
	Experimental *experimentalOut  `json:"experimental,omitempty"`
}

type experimentalOut struct {
	ClashAPI map[string]any `json:"clash_api"`
}

// BuildServerConfig renders a complete official sing-box config.json using the
// project's sing-box 1.14 beta baseline. The output is validated on the VPS by `sing-box check`
// before being applied.
func BuildServerConfig(in ServerConfigInput) ([]byte, error) {
	inbounds := make([]json.RawMessage, 0, len(in.Inbounds))
	for _, ib := range in.Inbounds {
		raw, err := BuildInbound(ib)
		if err != nil {
			return nil, fmt.Errorf("inbound %q: %w", ib.Tag, err)
		}
		inbounds = append(inbounds, raw)
	}
	if len(inbounds) == 0 {
		// A valid config still needs the inbounds key; an empty list is fine.
		inbounds = []json.RawMessage{}
	}

	logLevel := in.LogLevel
	if logLevel == "" {
		logLevel = "info"
	}

	// Outbounds: always include a "direct" outbound, then the admin's.
	outbounds := make([]json.RawMessage, 0, len(in.Outbounds)+1)
	haveDirect := false
	for _, o := range in.Outbounds {
		raw, err := buildOutbound(o)
		if err != nil {
			return nil, fmt.Errorf("outbound %q: %w", o.Tag, err)
		}
		outbounds = append(outbounds, raw)
		if o.Tag == "direct" {
			haveDirect = true
		}
	}
	if !haveDirect {
		d, _ := json.Marshal(map[string]any{"type": "direct", "tag": "direct"})
		outbounds = append([]json.RawMessage{d}, outbounds...)
	}

	// Route rules: keep it minimal. Only add a sniff rule when some rule matches
	// on a domain (which requires sniffing); a plain inbound/ip/port relay needs
	// none of it.
	needSniff := false
	hasGlobalSniff := false
	for _, r := range in.Rules {
		if ruleNeedsSniff(r) {
			needSniff = true
		}
		if routeRuleAction(r) == "sniff" && len(r.Inbound) == 0 {
			hasGlobalSniff = true
		}
	}
	rules := []map[string]any{}
	if needSniff && !hasGlobalSniff {
		rules = append(rules, map[string]any{"action": "sniff"})
	}
	for _, r := range in.Rules {
		rules = append(rules, buildRouteRule(r))
	}
	final := in.Final
	if final == "" {
		final = "direct"
	}
	route := routeOut{Final: final}
	if len(rules) > 0 {
		route.Rules = rules
	}
	if len(in.RuleSets) > 0 {
		rs := make([]map[string]any, 0, len(in.RuleSets))
		for _, s := range in.RuleSets {
			rs = append(rs, buildRuleSet(s))
		}
		route.RuleSet = rs
	}

	cfg := configOut{
		Log:       map[string]any{"level": logLevel, "timestamp": true},
		Inbounds:  inbounds,
		Outbounds: outbounds,
		Route:     route,
	}
	if in.StatsController != "" {
		cfg.Experimental = &experimentalOut{ClashAPI: map[string]any{
			"external_controller": in.StatsController,
		}}
	}

	return json.MarshalIndent(cfg, "", "  ")
}
