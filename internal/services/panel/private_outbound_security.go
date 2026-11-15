package panel

import (
	"net"
	"strings"
)

func forbiddenPrivateOutboundHost(raw string) bool {
	if isLoopbackOrUnspecifiedHost(raw) {
		return true
	}
	host := strings.Trim(strings.TrimSpace(raw), "[]")
	if strings.Contains(host, "%") {
		return true
	}
	if address := net.ParseIP(host); address != nil {
		return requirePublicSubscriptionIPs([]net.IP{address}) != nil
	}
	normalized, err := normalizeNodeAddress(host)
	return err != nil || normalized == ""
}
