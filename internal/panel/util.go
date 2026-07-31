// Package panel implements the SingPanel control-plane server: the agent
// WebSocket gateway, admin/user REST APIs, and subscription output.
package panel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// randHex returns a cryptographically-random hex string of 2*n characters.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b)
}

// newUUID returns a random UUIDv4 string.
func newUUID() string { return uuid.NewString() }

func normalizedIPv4(raw string) (string, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil || !addr.Is4() {
		return "", false
	}
	return addr.String(), true
}

// normalizeNodeAddress accepts a host only, never a URL or host:port. IPv6 may
// be entered with brackets for convenience but is stored without them so every
// subscription formatter can add brackets according to its own syntax.
func normalizeNodeAddress(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if strings.TrimSpace(raw) != raw || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("不能包含空白字符")
	}
	if strings.HasPrefix(raw, "[") {
		if !strings.HasSuffix(raw, "]") {
			return "", fmt.Errorf("IPv6 方括号格式不完整")
		}
		addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]"))
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", fmt.Errorf("无效的 IPv6 地址")
		}
		return addr.String(), nil
	}
	if addr, err := netip.ParseAddr(raw); err == nil {
		if addr.Zone() != "" {
			return "", fmt.Errorf("不支持带网络区域的 IP 地址")
		}
		return addr.Unmap().String(), nil
	}
	if strings.ContainsAny(raw, "/?#@[]:") {
		return "", fmt.Errorf("只填写域名或 IP，不要包含协议、端口或路径")
	}

	host := strings.TrimSuffix(strings.ToLower(raw), ".")
	if host == "" || len(host) > 253 {
		return "", fmt.Errorf("无效的域名")
	}
	allDigitsAndDots := true
	for _, r := range host {
		if (r < '0' || r > '9') && r != '.' {
			allDigitsAndDots = false
			break
		}
	}
	if allDigitsAndDots {
		return "", fmt.Errorf("无效的 IPv4 地址")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("无效的域名")
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return "", fmt.Errorf("无效的域名")
			}
		}
	}
	return host, nil
}
