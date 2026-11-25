package panel

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"
)

const privateOutboundDNSValidationTimeout = 2 * time.Second

var lookupPrivateOutboundIPAddrs = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	if isDocumentationDomain(host) {
		return nil, &net.DNSError{Err: "documentation domain", Name: host, IsNotFound: true}
	}
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

func isDocumentationDomain(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	for _, reserved := range []string{"example", "example.com", "example.net", "example.org", "test", "invalid"} {
		if host == reserved || strings.HasSuffix(host, "."+reserved) {
			return true
		}
	}
	return false
}

func forbiddenPrivateOutboundHost(raw string) bool {
	if isLoopbackOrUnspecifiedHost(raw) {
		return true
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.Contains(trimmed, "%") {
		return true
	}
	host := strings.Trim(trimmed, "[]")
	if isAmbiguousNumericHost(strings.TrimSuffix(strings.ToLower(host), ".")) {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return requirePublicSubscriptionIPs([]net.IP{addr.AsSlice()}) != nil
	}
	if address := net.ParseIP(host); address != nil {
		return requirePublicSubscriptionIPs([]net.IP{address}) != nil
	}
	normalized, err := normalizeNodeAddress(trimmed)
	if err != nil || normalized == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), privateOutboundDNSValidationTimeout)
	defer cancel()
	if addrs, lookupErr := lookupPrivateOutboundIPAddrs(ctx, normalized); lookupErr == nil && len(addrs) > 0 {
		ips := make([]net.IP, 0, len(addrs))
		for _, addr := range addrs {
			ips = append(ips, addr.IP)
		}
		if requirePublicSubscriptionIPs(ips) != nil {
			return true
		}
	}
	return false
}
