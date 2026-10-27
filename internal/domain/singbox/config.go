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
	if err := ValidateRealityGuardLayout(in.Inbounds); err != nil {
		return nil, err
	}
	inbounds := make([]json.RawMessage, 0, len(in.Inbounds))
	var guardRules []map[string]any
	for _, ib := range in.Inbounds {
		if ib.Settings.RealityGuardEnabled() {
			for _, address := range []string{in.StatsController, in.StatsAPI} {
				_, portText, _ := net.SplitHostPort(address)
				port, _ := strconv.Atoi(portText)
				if port == ib.Settings.TLS.Reality.Guard.InternalPort || port == ib.ListenPort {
					return nil, fmt.Errorf("REALITY 入站 %q 与统计服务端口冲突", ib.Tag)
				}
			}
		}
		raw, err := BuildInbound(ib)
		if err != nil {
			return nil, fmt.Errorf("inbound %q: %w", ib.Tag, err)
		}
		if ib.Settings.RealityGuardEnabled() {
			guard, err := json.Marshal(buildRealityGuardInbound(ib))
			if err != nil {
				return nil, err
			}
			inbounds = append(inbounds, guard)
			guardRules = append(guardRules, buildRealityGuardRules(ib)...)
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
	// Gate traffic must reach only its own local VLESS listener before the
	// general loopback protection. The terminal reject isolates all other
	// gate traffic from user rules and the global sniff fallback.
	rules := append([]map[string]any{}, guardRules...)
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

// LoopbackGuardRule rejects proxied connections to the node itself, so proxy
// users cannot reach node-local services (including the stats endpoints)
// through the proxy. Besides loopback it covers:
//   - 0.0.0.0/8 and ::, which Linux delivers to the local host, so connecting
//     to 0.0.0.0:<port> would otherwise bypass the loopback range;
//   - link-local addresses and the cloud metadata endpoints (169.254.169.254,
//     Alibaba's 100.100.100.200, AWS's fd00:ec2::254), which hand out instance
//     credentials to anything that can reach them from the host.
//
// Private (RFC 1918) networks are deliberately left reachable: some operators
// route proxy users into an intranet on purpose and can add their own rule.
// Destinations given as a domain that resolves to these addresses are not
// covered; the stats secret is the real barrier for the stats endpoints.
func LoopbackGuardRule() map[string]any {
	return map[string]any{
		"domain":        []string{"localhost"},
		"domain_suffix": []string{".localhost"},
		"ip_cidr": []string{
			"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "100.100.100.200/32",
			"::/128", "::1/128", "fe80::/10", "fd00:ec2::254/128",
		},
		"action": "reject",
	}
}

// legacyLoopbackGuardRule is the guard emitted before link-local and
// unspecified addresses were added. Imports still recognise it so a config
// generated by an older panel stays managed instead of gaining a user rule.
func legacyLoopbackGuardRule() map[string]any {
	return map[string]any{
		"domain":        []string{"localhost"},
		"domain_suffix": []string{".localhost"},
		"ip_cidr":       []string{"127.0.0.0/8", "::1/128"},
		"action":        "reject",
	}
}

// IsLoopbackGuardRule reports whether a raw route rule is exactly a guard
// emitted by BuildServerConfig (current or legacy form), so importing a
// panel-generated config does not turn it into a user-visible rule.
func IsLoopbackGuardRule(rule map[string]any) bool {
	got := canonicalJSON(rule)
	return got == canonicalJSON(LoopbackGuardRule()) || got == canonicalJSON(legacyLoopbackGuardRule())
}

// UpgradeLegacyLoopbackGuardJSON returns raw with every legacy guard rule in
// route.rules replaced by the current one. Import uses it before comparing a
// node's config with the regenerated managed config, so a config written by an
// older panel is still recognised as panel-managed. raw is returned unchanged
// when it holds no legacy guard or cannot be parsed.
func UpgradeLegacyLoopbackGuardJSON(raw []byte) []byte {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return raw
	}
	route, _ := root["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	legacy := canonicalJSON(legacyLoopbackGuardRule())
	changed := false
	for i, rule := range rules {
		if m, ok := rule.(map[string]any); ok && canonicalJSON(m) == legacy {
			rules[i] = LoopbackGuardRule()
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return out
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
