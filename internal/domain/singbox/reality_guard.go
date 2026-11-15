package singbox

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// RealityGuardSettings describes the public TLS/SNI gate for a loopback-only
// VLESS inbound. InboundInput.ListenPort remains the public subscription port.
// These are panel settings, never fields in a sing-box TLS object.
type RealityGuardSettings struct {
	Enabled      bool     `json:"enabled,omitempty"`
	InternalPort int      `json:"internal_port,omitempty"`
	SniffTimeout string   `json:"sniff_timeout,omitempty"`
	ServerNames  []string `json:"server_names,omitempty"`
	Tag          string   `json:"tag,omitempty"`
}

func (s InboundSettings) RealityGuardEnabled() bool {
	return s.TLS.Reality.Guard != nil && s.TLS.Reality.Guard.Enabled
}

func (s InboundSettings) RealityGuardTag(inboundTag string) string {
	if guard := s.TLS.Reality.Guard; guard != nil && guard.Tag != "" {
		return guard.Tag
	}
	return inboundTag + "-guard"
}

func (s InboundSettings) RealityGuardServerNames() []string {
	if guard := s.TLS.Reality.Guard; guard != nil && len(guard.ServerNames) > 0 {
		names := make([]string, len(guard.ServerNames))
		for i, name := range guard.ServerNames {
			names[i] = strings.ToLower(name)
		}
		return names
	}
	name := s.TLS.ServerName
	if name == "" {
		name = s.TLS.Reality.HandshakeServer
	}
	return []string{strings.ToLower(name)}
}

func (s InboundSettings) validateRealityGuard(typ string) error {
	if !s.RealityGuardEnabled() {
		return nil
	}
	if typ != "vless" || !s.TLS.Reality.Enabled || (s.Transport.Type != "" && !strings.EqualFold(s.Transport.Type, "tcp")) {
		return fmt.Errorf("前置 SNI 过滤仅支持 VLESS + REALITY + TCP")
	}
	g := s.TLS.Reality.Guard
	if g.InternalPort < 1 || g.InternalPort > 65535 {
		return fmt.Errorf("REALITY 内部端口必须在 1-65535 之间")
	}
	if g.Tag != "" && (strings.TrimSpace(g.Tag) != g.Tag || strings.ContainsAny(g.Tag, "\r\n\t")) {
		return fmt.Errorf("REALITY 前置入站标签不能包含首尾空白或控制字符")
	}
	if g.SniffTimeout != "" {
		if d, err := time.ParseDuration(g.SniffTimeout); err != nil || d <= 0 {
			return fmt.Errorf("SNI 嗅探超时必须为正时长，例如 300ms")
		}
	}
	clientName := s.TLS.ServerName
	if clientName == "" {
		clientName = s.TLS.Reality.HandshakeServer
	}
	clientAllowed := false
	for _, name := range s.RealityGuardServerNames() {
		if !validGuardServerName(name) {
			return fmt.Errorf("SNI 白名单必须使用完整域名，不能包含通配符、端口或 IP: %q", name)
		}
		if strings.EqualFold(name, clientName) {
			clientAllowed = true
		}
	}
	if !clientAllowed {
		return fmt.Errorf("SNI 白名单必须包含订阅使用的域名 %q", clientName)
	}
	return nil
}

func validGuardServerName(name string) bool {
	if name == "" || len(name) > 253 || net.ParseIP(name) != nil {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c != '-' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
				return false
			}
		}
	}
	return true
}

// ValidateRealityGuardLayout checks collisions across public/internal ports
// and generated/explicit inbound tags. The panel also calls it before saving,
// including disabled rows so enabling an existing row remains safe.
func ValidateRealityGuardLayout(inbounds []InboundInput) error {
	for i, in := range inbounds {
		if !in.Settings.RealityGuardEnabled() {
			continue
		}
		if err := in.Settings.validateRealityGuard(in.Type); err != nil {
			return err
		}
		g := in.Settings.TLS.Reality.Guard
		guardTag := in.Settings.RealityGuardTag(in.Tag)
		if in.ListenPort < 1 || in.ListenPort > 65535 || in.ListenPort == g.InternalPort {
			return fmt.Errorf("REALITY 公网与内部端口必须有效且不同")
		}
		if guardTag == in.Tag {
			return fmt.Errorf("REALITY 前置入站与内部入站不能使用相同标签")
		}
		for j, other := range inbounds {
			if i == j {
				continue
			}
			if guardTag == other.Tag || in.Tag == other.Tag {
				return fmt.Errorf("REALITY 入站标签与 %q 冲突", other.Tag)
			}
			if other.ListenPort == g.InternalPort || other.ListenPort == in.ListenPort {
				return fmt.Errorf("REALITY 端口与入站 %q 冲突", other.Tag)
			}
			if other.Settings.RealityGuardEnabled() {
				otherGuard := other.Settings.TLS.Reality.Guard
				if otherGuard.InternalPort == g.InternalPort || otherGuard.InternalPort == in.ListenPort {
					return fmt.Errorf("REALITY 内部端口与入站 %q 冲突", other.Tag)
				}
				if guardTag == other.Settings.RealityGuardTag(other.Tag) || in.Tag == other.Settings.RealityGuardTag(other.Tag) {
					return fmt.Errorf("REALITY 前置入站标签与 %q 冲突", other.Tag)
				}
			}
		}
	}
	return nil
}

func buildRealityGuardInbound(in InboundInput) map[string]any {
	return map[string]any{
		"type": "direct", "tag": in.Settings.RealityGuardTag(in.Tag),
		"listen": in.Settings.ListenAddress(), "listen_port": in.ListenPort, "network": "tcp",
		"override_address": "127.0.0.1", "override_port": in.Settings.TLS.Reality.Guard.InternalPort,
	}
}

func buildRealityGuardRules(in InboundInput) []map[string]any {
	g := in.Settings.TLS.Reality.Guard
	tag := in.Settings.RealityGuardTag(in.Tag)
	timeout := g.SniffTimeout
	if timeout == "" {
		timeout = "300ms"
	}
	return []map[string]any{
		{"inbound": []string{tag}, "action": "sniff", "sniffer": []string{"tls"}, "timeout": timeout},
		// A default rule ORs domain with ip_cidr! Logical AND is essential:
		// the loopback destination must never itself satisfy the SNI whitelist.
		{"type": "logical", "mode": "and", "rules": []map[string]any{
			{"inbound": []string{tag}, "domain": in.Settings.RealityGuardServerNames()},
			{"ip_cidr": []string{"127.0.0.1/32"}, "port": []int{g.InternalPort}},
		}, "action": "route", "outbound": "direct"},
		{"inbound": []string{tag}, "action": "reject", "method": "drop"},
	}
}
