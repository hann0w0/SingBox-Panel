package singbox

import (
	"encoding/json"
	"fmt"
)

// Residual captures the parts of a hand-written config.json that the panel does
// not model, so they survive every panel push instead of forcing the whole
// server into raw mode.
//
// The panel owns a known set of keys per object (see ownedKeys). Everything
// else -- smux, cache_file, a rule's invert flag, an unknown inbound type, an
// entire experimental block -- is stored here verbatim and merged back when the
// config is rendered. That keeps the panel editable on configs it only
// partially understands, without pretending to understand the rest.
type Residual struct {
	// TopLevel holds config keys the panel does not render at all.
	TopLevel map[string]json.RawMessage `json:"top_level,omitempty"`
	// Log holds extra log.* keys beyond the level the panel sets.
	Log map[string]json.RawMessage `json:"log,omitempty"`
	// Inbounds maps an inbound tag to keys the panel does not own.
	Inbounds map[string]map[string]json.RawMessage `json:"inbounds,omitempty"`
	// Outbounds maps an outbound tag to keys the panel does not own.
	Outbounds map[string]map[string]json.RawMessage `json:"outbounds,omitempty"`
	// Route holds extra route.* keys beyond rules/rule_set/final.
	Route map[string]json.RawMessage `json:"route,omitempty"`
	// RuleFields maps a rule index (as a decimal string) to keys the panel does
	// not own. Rules have no tag, so position is the only stable identity.
	RuleFields map[string]map[string]json.RawMessage `json:"rule_fields,omitempty"`
	// RuleSets maps a rule_set tag to keys the panel does not own.
	RuleSets map[string]map[string]json.RawMessage `json:"rule_sets,omitempty"`
	// PassThroughInbounds holds whole inbound objects of a type the panel has no
	// editor for. They are emitted unchanged on every push.
	PassThroughInbounds []json.RawMessage `json:"pass_through_inbounds,omitempty"`
	// PassThroughOutbounds is the outbound equivalent.
	PassThroughOutbounds []json.RawMessage `json:"pass_through_outbounds,omitempty"`
	// Experimental and the other top-level sing-box blocks are preserved whole
	// because the panel only owns experimental.clash_api.
	Experimental map[string]json.RawMessage `json:"experimental,omitempty"`
}

// Empty reports whether the residual carries nothing worth persisting.
func (r Residual) Empty() bool {
	return len(r.TopLevel) == 0 && len(r.Log) == 0 && len(r.Inbounds) == 0 &&
		len(r.Outbounds) == 0 && len(r.Route) == 0 && len(r.RuleFields) == 0 &&
		len(r.RuleSets) == 0 && len(r.PassThroughInbounds) == 0 &&
		len(r.PassThroughOutbounds) == 0 && len(r.Experimental) == 0
}

// ownedConfigKeys are top-level config.json keys the panel renders itself.
var ownedConfigKeys = map[string]bool{
	"log": true, "inbounds": true, "outbounds": true, "route": true,
	"experimental": true, "services": true, "http_clients": true,
}

// ownedLogKeys are log.* keys BuildServerConfig sets.
var ownedLogKeys = map[string]bool{"level": true, "timestamp": true}

// ownedRouteKeys are route.* keys BuildServerConfig sets.
var ownedRouteKeys = map[string]bool{
	"rules": true, "rule_set": true, "final": true, "default_http_client": true,
}

// ownedInboundKeys are inbound.* keys BuildInbound always writes. users[] is
// owned too: the panel names single-user credentials "user" and regenerates the
// list from its own model, so a hand-written user name is not a lost field.
var ownedInboundKeys = map[string]bool{
	"type": true, "tag": true, "listen": true, "listen_port": true, "users": true,
}

// ownedOutboundKeys are outbound.* keys the panel writes when rendering.
var ownedOutboundKeys = map[string]bool{
	"type": true, "tag": true, "server": true, "server_port": true,
	"uuid": true, "username": true, "password": true, "method": true,
}

// ownedRuleKeys are route rule keys derived from RuleInput plus "outbound".
var ownedRuleKeys = map[string]bool{
	"action": true, "method": true, "auth_user": true, "inbound": true,
	"domain": true, "domain_suffix": true, "domain_keyword": true,
	"ip_cidr": true, "source_ip_cidr": true, "port": true, "protocol": true,
	"network": true, "rule_set": true, "rule_set_ip_cidr_match_source": true,
	"sniffer": true, "outbound": true,
}

// ownedRuleSetKeys are rule_set entry keys the panel writes.
var ownedRuleSetKeys = map[string]bool{
	"tag": true, "type": true, "url": true, "path": true, "format": true,
	"download_detour": true, "update_interval": true,
}

// experimental is preserved whole, including clash_api: the panel overwrites
// clash_api only when it renders one (mergeInto keeps existing keys), so a
// hand-written clash_api survives whenever the panel does not own it.
var ownedExperimentalKeys = map[string]bool{}

func unknownKeys(m map[string]any, owned map[string]bool) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	for key, value := range m {
		if owned[key] {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			continue
		}
		if out == nil {
			out = make(map[string]json.RawMessage, len(m))
		}
		out[key] = raw
	}
	return out
}

// ExtractResidual builds the residual from a hand-written config plus the set
// of tags the panel successfully parsed. Inbounds/outbounds it could not parse
// are kept whole so a config with an unsupported protocol still round-trips.
func ExtractResidual(raw []byte, parsed *ParsedConfig) (Residual, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return Residual{}, fmt.Errorf("配置不是合法 JSON: %w", err)
	}
	res := Residual{TopLevel: unknownKeys(root, ownedConfigKeys)}

	if log, ok := root["log"].(map[string]any); ok {
		res.Log = unknownKeys(log, ownedLogKeys)
	}
	if route, ok := root["route"].(map[string]any); ok {
		res.Route = unknownKeys(route, ownedRouteKeys)
	}
	if exp, ok := root["experimental"].(map[string]any); ok {
		res.Experimental = unknownKeys(exp, ownedExperimentalKeys)
	}

	knownInbound := make(map[string]bool, len(parsed.Inbounds))
	for _, in := range parsed.Inbounds {
		knownInbound[in.Tag] = true
	}
	// REALITY gate inbounds are panel-generated (see findRealityGuardImports), so
	// they are neither unknown nor user-owned: skip them entirely.
	guardInboundIndex := make(map[int]bool)
	for _, guard := range findRealityGuardImports(root) {
		guardInboundIndex[guard.directIndex] = true
		guardInboundIndex[guard.targetIndex] = true
	}
	for index, entry := range mSlice(root, "inbounds") {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if guardInboundIndex[index] {
			continue
		}
		tag := mStr(m, "tag")
		if !knownInbound[tag] {
			// No panel editor for this type: keep the whole object verbatim.
			rawEntry, err := json.Marshal(m)
			if err == nil {
				res.PassThroughInbounds = append(res.PassThroughInbounds, rawEntry)
			}
			continue
		}
		if extra := unknownKeys(m, ownedInboundKeys); extra != nil {
			if res.Inbounds == nil {
				res.Inbounds = make(map[string]map[string]json.RawMessage)
			}
			res.Inbounds[tag] = extra
		}
	}

	knownOutbound := make(map[string]bool, len(parsed.Outbounds))
	for _, ob := range parsed.Outbounds {
		knownOutbound[ob.Tag] = true
	}
	for _, entry := range mSlice(root, "outbounds") {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		tag := mStr(m, "tag")
		if !knownOutbound[tag] {
			// direct/block are panel-built, so only keep genuinely unsupported
			// types; skipping the builtins avoids duplicating them.
			if typ := mStr(m, "type"); typ != "direct" && typ != "block" && typ != "dns" {
				rawEntry, err := json.Marshal(m)
				if err == nil {
					res.PassThroughOutbounds = append(res.PassThroughOutbounds, rawEntry)
				}
			}
			continue
		}
		if extra := unknownKeys(m, ownedOutboundKeys); extra != nil {
			if res.Outbounds == nil {
				res.Outbounds = make(map[string]map[string]json.RawMessage)
			}
			res.Outbounds[tag] = extra
		}
	}

	// Rules keep their extra keys by position so an imported config's unknown
	// match fields (invert, clash_mode, rule_set_ip_is_private, ...) survive.
	if route, ok := root["route"].(map[string]any); ok {
		for index, entry := range mSlice(route, "rules") {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if extra := unknownKeys(m, ownedRuleKeys); extra != nil {
				if res.RuleFields == nil {
					res.RuleFields = make(map[string]map[string]json.RawMessage)
				}
				res.RuleFields[fmt.Sprintf("%d", index)] = extra
			}
		}
		for _, entry := range mSlice(route, "rule_set") {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if extra := unknownKeys(m, ownedRuleSetKeys); extra != nil {
				if res.RuleSets == nil {
					res.RuleSets = make(map[string]map[string]json.RawMessage)
				}
				res.RuleSets[mStr(m, "tag")] = extra
			}
		}
	}

	return res, nil
}

// NormalizeSingleUserName makes single-credential inbound user names comparable.
// The panel always renders that one user as "user" (see
// InboundSettings.SingleUserIdentity), so a hand-written name is a naming
// difference, not a field the panel cannot model.
func NormalizeSingleUserName(raw []byte) ([]byte, bool) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, false
	}
	// A REALITY gate's inner inbound carries an identity the panel does not
	// regenerate, so its user name is a real difference, not a naming variant.
	guardInboundIndex := make(map[int]bool)
	for _, guard := range findRealityGuardImports(root) {
		guardInboundIndex[guard.directIndex] = true
		guardInboundIndex[guard.targetIndex] = true
	}
	changed := false
	for index, entry := range mSlice(root, "inbounds") {
		in, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if guardInboundIndex[index] {
			continue
		}
		users := mSlice(in, "users")
		if len(users) != 1 {
			continue
		}
		user, ok := users[0].(map[string]any)
		if !ok {
			continue
		}
		if name := mStr(user, "name"); name != "" && name != "user" {
			user["name"] = "user"
			changed = true
		}
	}
	if !changed {
		return raw, true
	}
	normalized, err := json.Marshal(root)
	if err != nil {
		return nil, false
	}
	return normalized, true
}

// mergeInto adds residual keys to a rendered object, leaving the panel's own
// keys untouched: unknown fields are restored recursively, while known fields
// keep the panel's value so the model stays authoritative.
func mergeInto(target map[string]any, extra map[string]json.RawMessage) {
	for key, raw := range extra {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		if existingMap, ok := target[key].(map[string]any); ok {
			if extraMap, ok := value.(map[string]any); ok {
				mergeMissingMap(existingMap, extraMap)
				continue
			}
		}
		if _, taken := target[key]; taken {
			continue
		}
		target[key] = value
	}
}

func mergeMissingMap(target, extra map[string]any) {
	for key, val := range extra {
		if existingMap, ok := target[key].(map[string]any); ok {
			if extraMap, ok := val.(map[string]any); ok {
				mergeMissingMap(existingMap, extraMap)
				continue
			}
		}
		if _, taken := target[key]; taken {
			continue
		}
		target[key] = val
	}
}

// ApplyResidual merges the preserved fields back into a freshly rendered
// config. The panel's own keys always win; every other key is restored
// verbatim, which is what keeps a partially understood config editable.
func ApplyResidual(rendered []byte, res Residual) ([]byte, error) {
	if res.Empty() {
		return rendered, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(rendered, &cfg); err != nil {
		return nil, fmt.Errorf("生成的配置不是合法 JSON: %w", err)
	}
	mergeInto(cfg, res.TopLevel)

	if log, ok := cfg["log"].(map[string]any); ok {
		mergeInto(log, res.Log)
	}
	if route, ok := cfg["route"].(map[string]any); ok {
		mergeInto(route, res.Route)
		if rules, ok := route["rules"].([]any); ok {
			for index := range rules {
				rule, ok := rules[index].(map[string]any)
				if !ok || IsLoopbackGuardRule(rule) {
					continue
				}
				mergeInto(rule, res.RuleFields[fmt.Sprintf("%d", index)])
			}
		}
		if sets, ok := route["rule_set"].([]any); ok {
			for _, entry := range sets {
				set, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				mergeInto(set, res.RuleSets[mStr(set, "tag")])
			}
		}
	}

	// experimental is rebuilt from the panel's clash_api plus preserved keys.
	if len(res.Experimental) > 0 {
		exp, ok := cfg["experimental"].(map[string]any)
		if !ok {
			exp = make(map[string]any, len(res.Experimental))
			cfg["experimental"] = exp
		}
		mergeInto(exp, res.Experimental)
	}

	applyResidualInbounds(cfg, res)
	applyResidualOutbounds(cfg, res)

	return json.MarshalIndent(cfg, "", "  ")
}

// applyResidualInbounds restores per-tag extras and re-appends whole pass-through
// inbound objects that have no panel editor.
func applyResidualInbounds(cfg map[string]any, res Residual) {
	inbounds, _ := cfg["inbounds"].([]any)
	for _, entry := range inbounds {
		in, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if extra := res.Inbounds[mStr(in, "tag")]; extra != nil {
			mergeInto(in, extra)
		}
	}
	for _, raw := range res.PassThroughInbounds {
		var value any
		if json.Unmarshal(raw, &value) == nil {
			inbounds = append(inbounds, value)
		}
	}
	if len(res.PassThroughInbounds) > 0 {
		cfg["inbounds"] = inbounds
	}
}

// applyResidualOutbounds restores per-tag extras and appends pass-through
// outbound objects that the panel does not render itself.
func applyResidualOutbounds(cfg map[string]any, res Residual) {
	outbounds, _ := cfg["outbounds"].([]any)
	for _, entry := range outbounds {
		ob, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if extra := res.Outbounds[mStr(ob, "tag")]; extra != nil {
			mergeInto(ob, extra)
		}
	}
	for _, raw := range res.PassThroughOutbounds {
		var value any
		if json.Unmarshal(raw, &value) == nil {
			outbounds = append(outbounds, value)
		}
	}
	if len(res.PassThroughOutbounds) > 0 {
		cfg["outbounds"] = outbounds
	}
}
