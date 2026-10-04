package singbox

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
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
	// collect node and inbound-port traffic counters. A panel-managed config
	// (StatsController set) also gets a loopback guard route rule.
	StatsController string
	// StatsSecret authenticates both loopback stats endpoints. Without it any
	// proxy user could reach them through the node itself.
	StatsSecret string
	// StatsAPI, when set together with StatsSecret, enables the sing-box 1.14+
	// API service on this loopback address. Its connection stream carries the
	// authenticated user, which powers per-user traffic accounting.
	StatsAPI string
}

// StatsServiceTag is the tag of the panel-owned sing-box API service.
const StatsServiceTag = "panel-stats"

// routeOut is the route block with a fixed key order: rules, then rule_set,
// then the "direct" fallback (final) at the very bottom.
type routeOut struct {
	Rules             []map[string]any `json:"rules,omitempty"`
	RuleSet           []map[string]any `json:"rule_set,omitempty"`
	Final             string           `json:"final"`
	DefaultHTTPClient string           `json:"default_http_client,omitempty"`
}

// configOut is the whole config.json with a fixed, human-friendly key order:
// log → http_clients → inbounds → outbounds → route (experimental last, since
// it's the panel's own stats hook, not part of the proxy config). Marshaling a
// struct preserves field order, unlike a map (which sorts keys alphabetically).
type configOut struct {
	Log          map[string]any    `json:"log"`
	HTTPClients  []map[string]any  `json:"http_clients,omitempty"`
	Inbounds     []json.RawMessage `json:"inbounds"`
	Outbounds    []json.RawMessage `json:"outbounds"`
	Route        routeOut          `json:"route"`
	Services     []map[string]any  `json:"services,omitempty"`
	Experimental *experimentalOut  `json:"experimental,omitempty"`
}

type experimentalOut struct {
	ClashAPI map[string]any `json:"clash_api"`
}

// BuildServerConfig renders a complete official sing-box config.json using the
// project's sing-box 1.14 baseline (also accepted by 1.15). The output is validated on the VPS by `sing-box check`
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
	if in.StatsController != "" {
		rules = append(rules, LoopbackGuardRule())
	}
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
	var httpClients []map[string]any
	if len(in.RuleSets) > 0 {
		directTags := map[string]bool{"direct": true}
		for _, o := range in.Outbounds {
			if o.Type == "" || o.Type == "direct" {
				directTags[o.Tag] = true
			}
		}
		clients := newRuleSetHTTPClients(final, directTags)
		rs := make([]map[string]any, 0, len(in.RuleSets))
		for _, s := range in.RuleSets {
			m := buildRuleSet(s)
			if m["type"] == "remote" {
				m["http_client"] = clients.tagFor(s.DownloadDetour)
			}
			rs = append(rs, m)
		}
		route.RuleSet = rs
		if len(clients.list) > 0 {
			httpClients = clients.list
			route.DefaultHTTPClient = clients.list[0]["tag"].(string)
		}
	}

	cfg := configOut{
		Log:         map[string]any{"level": logLevel, "timestamp": true},
		HTTPClients: httpClients,
		Inbounds:    inbounds,
		Outbounds:   outbounds,
		Route:       route,
	}
	if in.StatsController != "" {
		clash := map[string]any{"external_controller": in.StatsController}
		if in.StatsSecret != "" {
			clash["secret"] = in.StatsSecret
		}
		cfg.Experimental = &experimentalOut{ClashAPI: clash}
	}
	if in.StatsAPI != "" && in.StatsSecret != "" {
		service, err := statsService(in.StatsAPI, in.StatsSecret)
		if err != nil {
			return nil, err
		}
		cfg.Services = []map[string]any{service}
	}

	return json.MarshalIndent(cfg, "", "  ")
}

// statsService renders the loopback API service. The dashboard is never
// enabled, so sing-box never downloads anything for it.
func statsService(address, secret string) (map[string]any, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("stats api address %q: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("stats api address %q: invalid port", address)
	}
	return map[string]any{
		"type": "api", "tag": StatsServiceTag,
		"listen": host, "listen_port": port, "secret": secret,
	}, nil
}

// LoopbackGuardRule rejects proxied connections to the node's own loopback
// interface, so proxy users cannot reach node-local services (including the
// stats endpoints) through the proxy. Destinations given as a domain that
// resolves to loopback are not covered; the stats secret is the real barrier.
func LoopbackGuardRule() map[string]any {
	return map[string]any{
		"domain":        []string{"localhost"},
		"domain_suffix": []string{".localhost"},
		"ip_cidr":       []string{"127.0.0.0/8", "::1/128"},
		"action":        "reject",
	}
}

// IsLoopbackGuardRule reports whether a raw route rule is exactly the guard
// emitted by BuildServerConfig, so importing a panel-generated config does not
// turn it into a user-visible rule.
func IsLoopbackGuardRule(rule map[string]any) bool {
	return canonicalJSON(LoopbackGuardRule()) == canonicalJSON(rule)
}

func canonicalJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	var generic any
	if json.Unmarshal(raw, &generic) != nil {
		return ""
	}
	out, _ := json.Marshal(generic)
	return string(out)
}
