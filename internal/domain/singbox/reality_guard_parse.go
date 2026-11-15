package singbox

import "encoding/json"

// Recognition is intentionally narrower than sing-box's grammar. A raw gate
// can become one managed inbound only when every field and reference in its
// direct -> loopback VLESS chain has an unambiguous panel representation.
type realityGuardImport struct {
	directIndex, targetIndex, ruleIndex int
	publicPort, internalPort            int
	tag, timeout                        string
	serverNames                         []string
}

func guardOnlyKeys(object map[string]any, keys ...string) bool {
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return false
		}
	}
	return object != nil
}

func guardPort(object map[string]any, key string) int {
	number, ok := object[key].(float64)
	if !ok || number < 1 || number > 65535 || number != float64(int(number)) {
		return 0
	}
	return int(number)
}

func guardStringArray(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok || len(values) == 0 {
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok || text == "" {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}

func guardSame(value any, expected any) bool {
	return canonicalJSON(value) == canonicalJSON(expected)
}

func guardReferences(value any, tags map[string]bool) bool {
	switch value := value.(type) {
	case string:
		return tags[value]
	case []any:
		for _, child := range value {
			if guardReferences(child, tags) {
				return true
			}
		}
	case map[string]any:
		for _, child := range value {
			if guardReferences(child, tags) {
				return true
			}
		}
	}
	return false
}

func guardTargetSupported(target map[string]any) bool {
	if !guardOnlyKeys(target, "type", "tag", "listen", "listen_port", "users", "tls") ||
		mStr(target, "type") != "vless" || mStr(target, "listen") != "127.0.0.1" || mStr(target, "tag") == "" {
		return false
	}
	users := mSlice(target, "users")
	if len(users) != 1 {
		return false
	}
	user, _ := users[0].(map[string]any)
	if !guardOnlyKeys(user, "name", "uuid", "flow") || mStr(user, "uuid") == "" {
		return false
	}
	for _, key := range []string{"name", "flow"} {
		if value, exists := user[key]; exists {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	tls := mMap(target, "tls")
	if !guardOnlyKeys(tls, "enabled", "server_name", "alpn", "reality") || !mBool(tls, "enabled") {
		return false
	}
	if value, exists := tls["server_name"]; exists {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	if value, exists := tls["alpn"]; exists {
		if _, ok := guardStringArray(value); !ok {
			return false
		}
	}
	reality := mMap(tls, "reality")
	if !guardOnlyKeys(reality, "enabled", "handshake", "private_key", "short_id", "max_time_difference") ||
		!mBool(reality, "enabled") || mStr(reality, "private_key") == "" {
		return false
	}
	if value, exists := reality["short_id"]; exists {
		if _, ok := guardStringArray(value); !ok {
			return false
		}
	}
	if value, exists := reality["max_time_difference"]; exists {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	handshake := mMap(reality, "handshake")
	return guardOnlyKeys(handshake, "server", "server_port") && mStr(handshake, "server") != "" && guardPort(handshake, "server_port") != 0
}

func guardAllowNames(rule map[string]any, tag string, port int) ([]string, bool) {
	if mStr(rule, "action") != "route" || mStr(rule, "outbound") != "direct" {
		return nil, false
	}
	match := rule
	if mStr(rule, "type") == "logical" {
		if !guardOnlyKeys(rule, "type", "mode", "rules", "action", "outbound") || mStr(rule, "mode") != "and" {
			return nil, false
		}
		children := mSlice(rule, "rules")
		if len(children) != 2 {
			return nil, false
		}
		match, _ = children[0].(map[string]any)
		if !guardOnlyKeys(match, "inbound", "domain") || !guardSame(children[1], map[string]any{"ip_cidr": []string{"127.0.0.1/32"}, "port": []int{port}}) {
			return nil, false
		}
	} else if !guardOnlyKeys(rule, "inbound", "domain", "action", "outbound") {
		return nil, false
	}
	if !guardSame(match["inbound"], []string{tag}) {
		return nil, false
	}
	return guardStringArray(match["domain"])
}

func findRealityGuardImports(root map[string]any) []realityGuardImport {
	inbounds := mSlice(root, "inbounds")
	rules := mSlice(mMap(root, "route"), "rules")
	tags := map[string]int{}
	ports := map[int]int{}
	for _, value := range inbounds {
		inbound, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		tags[mStr(inbound, "tag")]++
		if port := guardPort(inbound, "listen_port"); port != 0 {
			ports[port]++
		}
	}
	var found []realityGuardImport
	used := map[int]bool{}
	for start := 0; start+2 < len(rules); start += 3 {
		sniff, _ := rules[start].(map[string]any)
		inboundTags, ok := guardStringArray(sniff["inbound"])
		if !ok || len(inboundTags) != 1 {
			break
		}
		tag := inboundTags[0]
		directIndex := -1
		for index, value := range inbounds {
			inbound := value.(map[string]any)
			if mStr(inbound, "tag") == tag && mStr(inbound, "type") == "direct" {
				directIndex = index
			}
		}
		if directIndex < 0 {
			break
		}
		direct := inbounds[directIndex].(map[string]any)
		publicPort, internalPort := guardPort(direct, "listen_port"), guardPort(direct, "override_port")
		if tag == "" || tags[tag] != 1 || publicPort == 0 || internalPort == 0 || publicPort == internalPort || ports[publicPort] != 1 || ports[internalPort] != 1 ||
			!guardOnlyKeys(direct, "type", "tag", "listen", "listen_port", "network", "override_address", "override_port") ||
			(InboundSettings{Listen: mStr(direct, "listen")}).validateListen() != nil || mStr(direct, "network") != "tcp" || mStr(direct, "override_address") != "127.0.0.1" {
			return nil
		}
		targetIndex := -1
		for index, value := range inbounds {
			inbound := value.(map[string]any)
			if guardPort(inbound, "listen_port") == internalPort && mStr(inbound, "listen") == "127.0.0.1" {
				targetIndex = index
			}
		}
		if targetIndex < 0 || used[targetIndex] || !guardTargetSupported(inbounds[targetIndex].(map[string]any)) {
			return nil
		}
		target := inbounds[targetIndex].(map[string]any)
		if tags[mStr(target, "tag")] != 1 || used[directIndex] ||
			!guardOnlyKeys(sniff, "inbound", "action", "sniffer", "timeout") || mStr(sniff, "action") != "sniff" ||
			!guardSame(sniff["sniffer"], []string{"tls"}) || mStr(sniff, "timeout") == "" {
			return nil
		}
		allow, _ := rules[start+1].(map[string]any)
		names, ok := guardAllowNames(allow, tag, internalPort)
		if !ok || !guardSame(rules[start+2], map[string]any{"inbound": []string{tag}, "action": "reject", "method": "drop"}) {
			return nil
		}
		parsed, _ := parseInbound(target)
		parsed.Settings.TLS.Reality.Guard = &RealityGuardSettings{Enabled: true, InternalPort: internalPort, Tag: tag, SniffTimeout: mStr(sniff, "timeout"), ServerNames: names}
		if parsed.Settings.TLS.Reality.PublicKey == "" || parsed.Settings.Validate("vless") != nil {
			return nil
		}
		found = append(found, realityGuardImport{directIndex: directIndex, targetIndex: targetIndex, ruleIndex: start,
			publicPort: publicPort, internalPort: internalPort, tag: tag, timeout: mStr(sniff, "timeout"), serverNames: names})
		used[directIndex], used[targetIndex] = true, true
	}
	for _, guard := range found {
		protectedTags := map[string]bool{guard.tag: true, mStr(inbounds[guard.targetIndex].(map[string]any), "tag"): true}
		for index, rule := range rules {
			if index >= guard.ruleIndex && index < guard.ruleIndex+3 {
				continue
			}
			// The original VLESS tag survives the merge and remains a valid
			// target of ordinary user routing. The public gate tag disappears
			// from the panel's rule model and must have no extra references.
			if guardReferences(rule, map[string]bool{guard.tag: true}) {
				return nil
			}
		}
		// Other raw inbound/dial settings can reference tags as well. Do not
		// silently redirect such a reference when collapsing this pair.
		for index, inbound := range inbounds {
			if index != guard.directIndex && index != guard.targetIndex && guardReferences(inbound, protectedTags) {
				return nil
			}
			other := inbound.(map[string]any)
			if index != guard.directIndex && guardPort(other, "override_port") == guard.internalPort {
				return nil
			}
		}
		for key, value := range root {
			if key != "inbounds" && key != "route" && guardReferences(value, protectedTags) {
				return nil
			}
		}
		for key, value := range mMap(root, "route") {
			if key != "rules" && guardReferences(value, protectedTags) {
				return nil
			}
		}
	}
	return found
}

// NormalizeRealityGuardImportJSON permits only documented differences for a
// recognized gate: a stricter logical AND allow rule, the panel's synthetic
// single-user name and the default log timestamp. Unknown data stays intact
// so the caller's normal JSON equality check still selects raw mode.
func NormalizeRealityGuardImportJSON(raw []byte) ([]byte, bool) {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return nil, false
	}
	guards := findRealityGuardImports(root)
	if len(guards) == 0 {
		return nil, false
	}
	rules := mSlice(mMap(root, "route"), "rules")
	for _, guard := range guards {
		rules[guard.ruleIndex+1] = map[string]any{
			"type": "logical", "mode": "and", "action": "route", "outbound": "direct",
			"rules": []map[string]any{
				{"inbound": []string{guard.tag}, "domain": guard.serverNames},
				{"ip_cidr": []string{"127.0.0.1/32"}, "port": []int{guard.internalPort}},
			},
		}
		target := mSlice(root, "inbounds")[guard.targetIndex].(map[string]any)
		user := mSlice(target, "users")[0].(map[string]any)
		if mStr(user, "name") == "" {
			user["name"] = "user"
		}
	}
	if log := mMap(root, "log"); log != nil {
		if _, exists := log["timestamp"]; !exists {
			log["timestamp"] = true
		}
	}
	normalized, err := json.Marshal(root)
	return normalized, err == nil
}
