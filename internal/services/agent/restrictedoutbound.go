package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"
)

const restrictedOutboundLookupTimeout = 8 * time.Second

var restrictedOutboundReservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

type restrictedOutboundResolver func(context.Context, string) ([]netip.Addr, error)

func resolveRestrictedOutbound(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func preferIPv6RestrictedOutbound() bool {
	// Zero-packet UDP sockets consult only the kernel routing table.
	if conn4, err := net.Dial("udp4", "1.1.1.1:53"); err == nil {
		_ = conn4.Close()
		return false
	}
	if conn6, err := net.Dial("udp6", "[2606:4700:4700::1111]:53"); err == nil {
		_ = conn6.Close()
		return true
	}
	return false
}

func prepareRestrictedOutbounds(ctx context.Context, raw []byte, tags []string, resolve restrictedOutboundResolver) ([]byte, error) {
	return prepareRestrictedOutboundsWithPreference(ctx, raw, tags, resolve, false)
}

func prepareRestrictedOutboundsWithPreference(ctx context.Context, raw []byte, tags []string, resolve restrictedOutboundResolver, preferIPv6 bool) ([]byte, error) {
	if len(tags) == 0 {
		return raw, nil
	}
	if len(raw) > maxConfigSize {
		return nil, fmt.Errorf("restricted outbound validation: config exceeds %d-byte limit", maxConfigSize)
	}
	ctx, cancel := context.WithTimeout(ctx, restrictedOutboundLookupTimeout)
	defer cancel()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return nil, fmt.Errorf("restricted outbound validation requires a JSON config object")
	}
	var outbounds []json.RawMessage
	if err := json.Unmarshal(root["outbounds"], &outbounds); err != nil {
		return nil, fmt.Errorf("restricted outbound validation requires an outbound array: %w", err)
	}
	indexes := make(map[string]int, len(outbounds))
	for index, outbound := range outbounds {
		var header struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal(outbound, &header); err != nil {
			return nil, fmt.Errorf("restricted outbound validation: invalid outbound at index %d: %w", index, err)
		}
		if header.Tag == "" {
			continue
		}
		if _, duplicate := indexes[header.Tag]; duplicate {
			return nil, fmt.Errorf("restricted outbound validation: duplicate outbound tag %q; assign unique tags", header.Tag)
		}
		indexes[header.Tag] = index
	}
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("restricted outbound %q validation canceled: %w", tag, err)
		}
		if strings.TrimSpace(tag) == "" || seen[tag] {
			return nil, fmt.Errorf("restricted outbound validation: empty or duplicate restricted tag %q", tag)
		}
		seen[tag] = true
		index, found := indexes[tag]
		if !found {
			return nil, fmt.Errorf("restricted outbound %q is missing from config; regenerate the configuration", tag)
		}
		prepared, err := pinRestrictedOutbound(ctx, outbounds[index], tag, resolve, preferIPv6)
		if err != nil {
			return nil, err
		}
		outbounds[index] = prepared
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("restricted outbound validation canceled: %w", err)
	}
	root["outbounds"], _ = json.Marshal(outbounds)
	return json.Marshal(root)
}

func pinRestrictedOutbound(ctx context.Context, raw json.RawMessage, tag string, resolve restrictedOutboundResolver, preferIPv6 bool) (json.RawMessage, error) {
	var outbound map[string]json.RawMessage
	if err := json.Unmarshal(raw, &outbound); err != nil {
		return nil, fmt.Errorf("restricted outbound %q has invalid settings: %w", tag, err)
	}
	var typ, server string
	if err := json.Unmarshal(outbound["type"], &typ); err != nil {
		return nil, fmt.Errorf("restricted outbound %q requires a supported proxy type", tag)
	}
	switch typ {
	case "vless", "vmess", "trojan", "shadowsocks", "ss", "hysteria2", "tuic", "socks", "http", "anytls", "snell":
	default:
		return nil, fmt.Errorf("restricted outbound %q has unsupported type %q", tag, typ)
	}
	if err := json.Unmarshal(outbound["server"], &server); err != nil || strings.TrimSpace(server) == "" {
		return nil, fmt.Errorf("restricted outbound %q requires a server hostname or public IP", tag)
	}
	host := strings.TrimSpace(server)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if strings.ContainsAny(host, "%*/\\@# \t\r\n") {
		return nil, fmt.Errorf("restricted outbound %q has invalid server %q; use a hostname or public IP without a zone or wildcard", tag, server)
	}
	address, literalErr := netip.ParseAddr(host)
	var addresses []netip.Addr
	if literalErr == nil {
		addresses = []netip.Addr{address}
	} else {
		normalized := strings.TrimSuffix(strings.ToLower(host), ".")
		if normalized == "localhost" || strings.HasSuffix(normalized, ".localhost") {
			return nil, fmt.Errorf("restricted outbound %q server %q points to localhost; use a public proxy server", tag, server)
		}
		var err error
		addresses, err = resolve(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("restricted outbound %q: resolve server %q on this node failed; check its DNS records: %w", tag, server, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("restricted outbound %q: resolve server %q canceled: %w", tag, server, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("restricted outbound %q server %q has no IP addresses; check its DNS records", tag, server)
	}
	validated := make([]netip.Addr, 0, len(addresses))
	for _, candidate := range addresses {
		if !publicRestrictedOutboundAddress(candidate) {
			return nil, fmt.Errorf("restricted outbound %q server %q resolves to unsafe address %s; every address must be public", tag, server, candidate)
		}
		validated = append(validated, candidate.Unmap())
	}
	slices.SortFunc(validated, func(left, right netip.Addr) int {
		if left.Is4() != right.Is4() {
			if preferIPv6 {
				if left.Is6() {
					return -1
				}
				return 1
			}
			if left.Is4() {
				return -1
			}
			return 1
		}
		return left.Compare(right)
	})
	outbound["server"], _ = json.Marshal(validated[0].String())
	if literalErr != nil && len(outbound["tls"]) > 0 {
		var tls map[string]json.RawMessage
		if err := json.Unmarshal(outbound["tls"], &tls); err != nil {
			return nil, fmt.Errorf("restricted outbound %q has invalid TLS settings: %w", tag, err)
		}
		var enabled bool
		if len(tls["enabled"]) > 0 {
			if err := json.Unmarshal(tls["enabled"], &enabled); err != nil {
				return nil, fmt.Errorf("restricted outbound %q has invalid TLS enabled setting: %w", tag, err)
			}
		}
		var serverName string
		if len(tls["server_name"]) > 0 {
			if err := json.Unmarshal(tls["server_name"], &serverName); err != nil {
				return nil, fmt.Errorf("restricted outbound %q has invalid TLS server_name: %w", tag, err)
			}
		}
		if enabled && serverName == "" {
			tls["server_name"], _ = json.Marshal(host)
			outbound["tls"], _ = json.Marshal(tls)
		}
	}
	if literalErr != nil && len(outbound["transport"]) > 0 {
		var transport map[string]json.RawMessage
		if err := json.Unmarshal(outbound["transport"], &transport); err != nil {
			return nil, fmt.Errorf("restricted outbound %q has invalid transport settings: %w", tag, err)
		}
		var trType string
		if len(transport["type"]) > 0 {
			_ = json.Unmarshal(transport["type"], &trType)
		}
		switch trType {
		case "ws":
			var headers map[string]any
			if len(transport["headers"]) > 0 {
				if err := json.Unmarshal(transport["headers"], &headers); err != nil {
					return nil, fmt.Errorf("restricted outbound %q has invalid transport headers: %w", tag, err)
				}
			}
			if headers == nil {
				headers = map[string]any{}
			}
			hasHost := false
			for k := range headers {
				if strings.EqualFold(k, "host") {
					hasHost = true
					break
				}
			}
			if !hasHost {
				headers["Host"] = host
				transport["headers"], _ = json.Marshal(headers)
				outbound["transport"], _ = json.Marshal(transport)
			}
		case "httpupgrade":
			var trHost string
			if len(transport["host"]) > 0 {
				_ = json.Unmarshal(transport["host"], &trHost)
			}
			if strings.TrimSpace(trHost) == "" {
				transport["host"], _ = json.Marshal(host)
				outbound["transport"], _ = json.Marshal(transport)
			}
		case "http":
			if !hasNonEmptyTransportHost(transport["host"]) {
				transport["host"], _ = json.Marshal([]string{host})
				outbound["transport"], _ = json.Marshal(transport)
			}
		}
	}
	return json.Marshal(outbound)
}

func hasNonEmptyTransportHost(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return strings.TrimSpace(single) != ""
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		for _, item := range list {
			if strings.TrimSpace(item) != "" {
				return true
			}
		}
	}
	return false
}

func publicRestrictedOutboundAddress(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if address.IsUnspecified() || address.IsLoopback() || address.IsPrivate() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() {
		return false
	}
	for _, prefix := range restrictedOutboundReservedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
