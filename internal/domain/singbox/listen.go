package singbox

import (
	"fmt"
	"net/netip"
)

func (s InboundSettings) ListenAddress() string {
	if s.Listen == "" {
		return "::"
	}
	return s.Listen
}

func (s InboundSettings) validateListen() error {
	address, err := netip.ParseAddr(s.ListenAddress())
	if err != nil || address.Zone() != "" {
		return fmt.Errorf("监听地址必须是有效的 IPv4 或 IPv6 地址")
	}
	if s.IPv6Only && (!address.Is6() || address.Is4In6()) {
		return fmt.Errorf("仅 IPv6 模式必须使用 IPv6 监听地址")
	}
	return nil
}

func ipv6OnlyRule(tag string) map[string]any {
	return map[string]any{
		"inbound": []string{tag}, "source_ip_cidr": []string{"0.0.0.0/0", "::ffff:0:0/96"},
		"action": "reject", "method": "drop",
	}
}

func inboundListenTag(in InboundInput) string {
	if in.Settings.RealityGuardEnabled() {
		return in.Settings.RealityGuardTag(in.Tag)
	}
	return in.Tag
}

func extractIPv6OnlyRules(root map[string]any) map[string]bool {
	inbounds := make(map[string]map[string]any)
	for _, value := range mSlice(root, "inbounds") {
		inbound, ok := value.(map[string]any)
		if ok && mStr(inbound, "tag") != "" {
			inbounds[mStr(inbound, "tag")] = inbound
		}
	}
	result := make(map[string]bool)
	route := mMap(root, "route")
	rules := mSlice(route, "rules")
	count := 0
	for _, value := range rules {
		rule, ok := value.(map[string]any)
		if !ok {
			break
		}
		tags := mStrSlice(rule, "inbound")
		if len(tags) != 1 || !guardSame(rule, ipv6OnlyRule(tags[0])) {
			break
		}
		inbound := inbounds[tags[0]]
		address, err := netip.ParseAddr(mStr(inbound, "listen"))
		if err != nil || !address.Is6() || address.Is4In6() {
			break
		}
		result[tags[0]] = true
		count++
	}
	if count > 0 {
		route["rules"] = rules[count:]
	}
	return result
}
