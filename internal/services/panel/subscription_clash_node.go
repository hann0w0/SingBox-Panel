package panel

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func clashProxy(n node, seen map[string]int) map[string]any {
	name := uniqueName(seen, n.name)
	st := n.settings
	u := n.getUserIdentity()
	tlsOn := nodeTLSOn(st)
	serverHost := strings.Trim(strings.TrimSpace(n.server), "[]")
	sni := sniOf(st, strings.Trim(strings.TrimSpace(n.sniHost()), "[]"))
	net := "tcp"
	switch st.Transport.Type {
	case "ws":
		net = "ws"
	case "httpupgrade":
		net = "http-upgrade"
	}
	trOpts := func() map[string]any {
		o := map[string]any{}
		if st.Transport.Path != "" {
			o["path"] = st.Transport.Path
		}
		headers := map[string]any{}
		for key, values := range st.Transport.HeaderValuesMap() {
			if len(values) > 0 && !strings.EqualFold(key, "host") {
				headers[key] = values[0]
			}
		}
		if h := st.Transport.Host(); h != "" {
			headers["Host"] = h
		}
		if len(headers) > 0 {
			o["headers"] = headers
		}
		if st.Transport.MaxEarlyData > 0 {
			o["max-early-data"] = st.Transport.MaxEarlyData
			if st.Transport.EarlyDataHeader != "" {
				o["early-data-header-name"] = st.Transport.EarlyDataHeader
			}
		}
		return o
	}
	fp := st.TLS.Fingerprint
	if fp == "" {
		fp = "chrome"
	}

	base := map[string]any{"name": name, "server": serverHost, "port": n.port}
	switch n.typ {
	case "vless":
		base["type"] = "vless"
		base["uuid"] = u.UUID
		base["network"] = net
		base["udp"] = n.udpEnabled()
		if st.Flow != "" {
			base["flow"] = st.Flow
		}
		if tlsOn {
			base["tls"] = true
			base["servername"] = sni
			base["client-fingerprint"] = fp
			if len(st.TLS.ALPN) > 0 {
				base["alpn"] = st.TLS.ALPN
			}
			if st.TLS.Reality.Enabled {
				base["reality-opts"] = map[string]any{"public-key": st.TLS.Reality.PublicKey, "short-id": firstOf(st.TLS.Reality.ShortID)}
			}
			if st.TLS.ClientInsecure() {
				base["skip-cert-verify"] = true
			}
		}
		if net == "ws" {
			base["ws-opts"] = trOpts()
		} else if net == "http-upgrade" {
			base["http-upgrade-opts"] = trOpts()
		}
	case "vmess":
		base["type"] = "vmess"
		base["uuid"] = u.UUID
		base["alterId"] = st.VMessAlterID
		base["cipher"] = st.VMessSecurityValue()
		base["network"] = net
		base["udp"] = n.udpEnabled()
		if tlsOn {
			base["tls"] = true
			base["servername"] = sni
			base["client-fingerprint"] = fp
			if len(st.TLS.ALPN) > 0 {
				base["alpn"] = st.TLS.ALPN
			}
			if st.TLS.Reality.Enabled {
				base["reality-opts"] = map[string]any{"public-key": st.TLS.Reality.PublicKey, "short-id": firstOf(st.TLS.Reality.ShortID)}
			}
			if st.TLS.ClientInsecure() {
				base["skip-cert-verify"] = true
			}
		}
		if net == "ws" {
			base["ws-opts"] = trOpts()
		} else if net == "http-upgrade" {
			base["http-upgrade-opts"] = trOpts()
		}
	case "trojan":
		base["type"] = "trojan"
		base["password"] = u.Password
		base["sni"] = sni
		base["udp"] = n.udpEnabled()
		// Trojan always runs TLS on TCP, so carry the uTLS fingerprint like the
		// other TCP protocols (vless/vmess/anytls).
		base["client-fingerprint"] = fp
		if len(st.TLS.ALPN) > 0 {
			base["alpn"] = st.TLS.ALPN
		}
		if st.TLS.ClientInsecure() {
			base["skip-cert-verify"] = true
		}
		if st.TLS.Reality.Enabled {
			realityOpts := map[string]any{"public-key": st.TLS.Reality.PublicKey}
			if sid := firstOf(st.TLS.Reality.ShortID); sid != "" {
				realityOpts["short-id"] = sid
			}
			base["reality-opts"] = realityOpts
		}
		if net == "ws" {
			base["network"] = "ws"
			base["ws-opts"] = trOpts()
		} else if net == "http-upgrade" {
			base["network"] = "http-upgrade"
			base["http-upgrade-opts"] = trOpts()
		}
	case "shadowsocks":
		base["type"] = "ss"
		base["cipher"] = st.Method
		base["password"] = singbox.SSClientPassword(st, u.Password)
		if st.SSPlugin != "" {
			plugin, opts := splitSSPluginOptions(st.SSPlugin)
			if plugin == "obfs-local" {
				plugin = "obfs"
			}
			base["plugin"] = plugin
			if len(opts) > 0 {
				base["plugin-opts"] = opts
			}
		}
		base["udp"] = n.udpEnabled()
	case "socks":
		base["type"] = "socks5"
		if st.Username != "" {
			base["username"] = st.Username
		}
		if st.Password != "" {
			base["password"] = st.Password
		}
		base["udp"] = n.udpEnabled()
	case "http":
		base["type"] = "http"
		if st.Username != "" {
			base["username"] = st.Username
		}
		if st.Password != "" {
			base["password"] = st.Password
		}
		if st.HTTPPath != "" {
			// Clash HTTP proxies have no CONNECT path option.
			return nil
		}
		headers := map[string]any{}
		hasAuthorization := false
		for key, values := range st.HTTPHeaders {
			if len(values) == 1 {
				headers[key] = values[0]
				hasAuthorization = hasAuthorization || strings.EqualFold(key, "Proxy-Authorization")
			} else if len(values) > 1 {
				// Mihomo HTTP headers are map[string]string. Keep repeated
				// headers in sing-box instead of emitting an invalid YAML proxy.
				return nil
			}
		}
		if !hasAuthorization && (st.Username == "") != (st.Password == "") {
			// Mihomo only creates Basic auth when both fields are nonempty.
			// An explicit header keeps one-sided credentials from becoming anonymous.
			headers["Proxy-Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(st.Username+":"+st.Password))
		}
		if len(headers) > 0 {
			base["headers"] = headers
		}
		if tlsOn {
			base["tls"] = true
			base["sni"] = sni
			if st.TLS.ClientInsecure() {
				base["skip-cert-verify"] = true
			}
			if len(st.TLS.ALPN) > 0 {
				base["alpn"] = st.TLS.ALPN
			}
			if st.TLS.Fingerprint != "" {
				base["client-fingerprint"] = st.TLS.Fingerprint
			}
		}
	case "mixed": // Clash has no mixed type; clients dial it as SOCKS5
		base["type"] = "socks5"
		if u.Username == "" {
			u.Username = st.Username
		}
		if u.Password == "" {
			u.Password = st.Password
		}
		if u.Username != "" {
			base["username"] = u.Username
		}
		if u.Password != "" {
			base["password"] = u.Password
		}
		base["udp"] = n.udpEnabled()
	case "hysteria2":
		base["type"] = "hysteria2"
		base["password"] = u.Password
		base["sni"] = sni
		if len(st.TLS.ALPN) > 0 {
			base["alpn"] = st.TLS.ALPN
		}
		if st.ObfsPassword != "" {
			obfsType := st.ObfsType
			if obfsType == "" {
				obfsType = "salamander"
			}
			base["obfs"] = obfsType
			base["obfs-password"] = st.ObfsPassword
			if obfsType == "gecko" {
				options := map[string]any{}
				if st.GeckoMinPacketSize > 0 {
					options["min-packet-size"] = st.GeckoMinPacketSize
				}
				if st.GeckoMaxPacketSize > 0 {
					options["max-packet-size"] = st.GeckoMaxPacketSize
				}
				if len(options) > 0 {
					base["obfs-opts"] = options
				}
			}
		}
		if st.UpMbps > 0 {
			base["up"] = fmt.Sprintf("%d Mbps", st.UpMbps)
		}
		if st.DownMbps > 0 {
			base["down"] = fmt.Sprintf("%d Mbps", st.DownMbps)
		}
		if st.TLS.ClientInsecure() {
			base["skip-cert-verify"] = true
		}
	case "snell":
		// Snell is Surge's native protocol; mihomo supports it too.
		base["type"] = "snell"
		base["psk"] = singbox.SnellClientPSK(st, u.Password)
		if st.SnellVersion > 0 {
			base["version"] = st.SnellVersion
		}
		if (st.SnellVersion == 4 || st.SnellVersion == 5) && st.SnellObfsMode != "" && st.SnellObfsMode != "none" {
			obfsOpts := map[string]any{"mode": st.SnellObfsMode}
			if st.SnellObfsHost != "" {
				obfsOpts["host"] = st.SnellObfsHost
			}
			base["obfs-opts"] = obfsOpts
		}
		base["udp"] = n.udpEnabled()
	case "anytls":
		base["type"] = "anytls"
		base["password"] = u.Password
		base["sni"] = sni
		base["udp"] = n.udpEnabled()
		// AnyTLS runs TLS on TCP, so mirror vless/vmess and carry the uTLS
		// fingerprint; mihomo honours client-fingerprint for anytls too.
		base["client-fingerprint"] = fp
		if len(st.TLS.ALPN) > 0 {
			base["alpn"] = st.TLS.ALPN
		}
		if st.TLS.ClientInsecure() {
			base["skip-cert-verify"] = true
		}
	case "hysteria":
		base["type"] = "hysteria"
		base["auth-str"] = u.Password
		base["sni"] = sni
		if st.UpMbps > 0 {
			base["up"] = fmt.Sprintf("%d Mbps", st.UpMbps)
		}
		if st.DownMbps > 0 {
			base["down"] = fmt.Sprintf("%d Mbps", st.DownMbps)
		}
		if st.ObfsPassword != "" {
			base["obfs"] = st.ObfsPassword
		}
		if st.TLS.ClientInsecure() {
			base["skip-cert-verify"] = true
		}
	case "tuic":
		base["type"] = "tuic"
		base["uuid"] = u.UUID
		base["password"] = u.Password
		cc := st.CongestionControl
		if cc == "" {
			cc = "cubic"
		}
		base["congestion-controller"] = cc
		base["udp-relay-mode"] = st.TUICRelayModeValue()
		if base["udp-relay-mode"] == "" {
			base["udp-relay-mode"] = "native"
		}
		base["sni"] = sni
		base["reduce-rtt"] = st.ZeroRTTHandshake
		if d, err := time.ParseDuration(st.TUICHeartbeatValue()); err == nil {
			base["heartbeat-interval"] = d.Milliseconds()
		}
		if len(st.TLS.ALPN) > 0 {
			base["alpn"] = st.TLS.ALPN
		}
		if st.TLS.ClientInsecure() {
			base["skip-cert-verify"] = true
		}
	default:
		return nil
	}
	return base
}

func uniqueName(seen map[string]int, name string) string {
	if seen[name] == 0 {
		seen[name] = 1
		return name
	}
	for n := seen[name] + 1; ; n++ {
		candidate := fmt.Sprintf("%s-%d", name, n)
		if seen[candidate] == 0 {
			seen[name] = n
			seen[candidate] = 1
			return candidate
		}
	}
}

func firstOf(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func nodeTLSOn(st singbox.InboundSettings) bool {
	return st.TLS.Enabled || st.TLS.SelfSigned || st.TLS.ACMEDomain != "" || st.TLS.Reality.Enabled
}

func sniOf(st singbox.InboundSettings, server string) string {
	t := st.TLS
	if t.ServerName != "" {
		return t.ServerName
	}
	if t.Reality.Enabled {
		return t.Reality.HandshakeServer
	}
	if t.ACMEDomain != "" {
		return t.ACMEDomain
	}
	return server
}

// NodeDetail is a per-user node with its share link and human-readable params.
