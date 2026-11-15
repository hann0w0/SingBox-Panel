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

func specificIPv6(raw string) bool {
	address, err := netip.ParseAddr(raw)
	return err == nil && address.Is6() && !address.Is4In6() && !address.IsUnspecified() && !address.IsLoopback()
}
