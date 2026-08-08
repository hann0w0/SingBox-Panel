package singbox

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ClientNode is a single (server-inbound, user) pair to render for a client.
type ClientNode struct {
	Name       string // display name shown in the client
	Server     string // public host clients dial
	ServerPort int
	Type       string
	Settings   InboundSettings
	User       ProxyUser
}

func firstShortID(ids []string) string {
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// fingerprintOf resolves the uTLS fingerprint clients present. chrome is the
// documented default; an explicit setting (e.g. safari, firefox, random) is
// mirrored verbatim into share links so all clients fingerprint identically.
func fingerprintOf(t TLSSettings) string {
	if t.Fingerprint != "" {
		return t.Fingerprint
	}
	return "chrome"
}

// clientSNI resolves the server_name used by the client.
func clientSNI(t TLSSettings, server string) string {
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

// tlsEnabled reports whether the inbound carries TLS/REALITY/ACME.
func (t TLSSettings) tlsEnabled() bool {
	return t.Enabled || t.SelfSigned || t.ACMEDomain != "" || t.Reality.Enabled
}

func networkOf(s TransportSettings) string {
	if s.normalized() == nil {
		return "tcp"
	}
	return s.Type
}

// buildClientTLS renders a client-side tls object, or nil for no TLS. nodeType
// gates uTLS: QUIC-based protocols (hysteria2/tuic/hysteria) must NOT carry a
// utls block — sing-box's QUIC dialer rejects a uTLS config at runtime with
// "unsupported usage for uTLS", so the node passes `check` but never connects.
// uTLS stays on for the TCP protocols (vless/vmess/trojan/anytls) and is
// mandatory for REALITY (TCP-only, vless).
func buildClientTLS(t TLSSettings, server, nodeType string) map[string]any {
	if !t.tlsEnabled() {
		return nil
	}
	tls := map[string]any{
		"enabled":     true,
		"server_name": clientSNI(t, server),
	}
	switch nodeType {
	case "hysteria2", "tuic", "hysteria":
		// QUIC: no uTLS (see doc comment).
	default:
		tp := t.Fingerprint
		if tp == "" {
			tp = "chrome"
		}
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": tp}
	}
	if len(t.ALPN) > 0 {
		tls["alpn"] = t.ALPN
	}
	if t.ClientInsecure() {
		tls["insecure"] = true
	}
	if t.Reality.Enabled {
		tls["reality"] = map[string]any{
			"enabled":    true,
			"public_key": t.Reality.PublicKey,
			"short_id":   firstShortID(t.Reality.ShortID),
		}
	}
	return tls
}

// BuildClientOutbound renders a sing-box client outbound for the given node.
func BuildClientOutbound(n ClientNode) (json.RawMessage, error) {
	base := map[string]any{
		"type":        n.Type,
		"tag":         n.Name,
		"server":      n.Server,
		"server_port": n.ServerPort,
	}
	tls := buildClientTLS(n.Settings.TLS, n.Server, n.Type)
	tr := buildTransport(n.Settings.Transport)

	switch n.Type {
	case "vless":
		base["uuid"] = n.User.UUID
		if n.Settings.Flow != "" {
			base["flow"] = n.Settings.Flow
		}
		pe := n.Settings.PacketEncoding
		if pe == "" {
			pe = "xudp"
		}
		base["packet_encoding"] = pe
		if tls != nil {
			base["tls"] = tls
		}
		if tr != nil {
			base["transport"] = tr
		}
	case "vmess":
		base["uuid"] = n.User.UUID
		base["security"] = n.Settings.VMessSecurityValue()
		base["alter_id"] = n.Settings.VMessAlterID
		if tls != nil {
			base["tls"] = tls
		}
		if tr != nil {
			base["transport"] = tr
		}
	case "trojan":
		base["password"] = n.User.Password
		// sing-box's trojan outbound has no packet_encoding field (UDP is
		// carried natively); a vless-only option must not leak into it.
		if tls != nil {
			base["tls"] = tls
		}
		if tr != nil {
			base["transport"] = tr
		}
	case "shadowsocks":
		base["method"] = n.Settings.Method
		base["password"] = SSClientPassword(n.Settings, n.User.Password)
		// sing-box's shadowsocks outbound has no packet_encoding field either.
	case "hysteria2":
		base["password"] = n.User.Password
		if tls != nil {
			base["tls"] = tls
		}
		if n.Settings.ObfsPassword != "" {
			base["obfs"] = map[string]any{"type": "salamander", "password": n.Settings.ObfsPassword}
		}
		if n.Settings.UpMbps > 0 {
			base["up_mbps"] = n.Settings.UpMbps
		}
		if n.Settings.DownMbps > 0 {
			base["down_mbps"] = n.Settings.DownMbps
		}
	case "tuic":
		base["uuid"] = n.User.UUID
		base["password"] = n.User.Password
		if tls != nil {
			base["tls"] = tls
		}
		cc := n.Settings.CongestionControl
		if cc == "" {
			cc = "cubic"
		}
		base["congestion_control"] = cc
		if v := n.Settings.TUICRelayModeValue(); v != "" {
			base["udp_relay_mode"] = v
		}
		base["zero_rtt_handshake"] = n.Settings.ZeroRTTHandshake
		base["heartbeat"] = n.Settings.TUICHeartbeatValue()
	case "hysteria":
		base["auth_str"] = n.User.Password
		up, down := HysteriaBandwidth(n.Settings)
		base["up_mbps"] = up
		base["down_mbps"] = down
		if n.Settings.ObfsPassword != "" {
			base["obfs"] = n.Settings.ObfsPassword
		}
		if tls != nil {
			base["tls"] = tls
		}
	case "anytls":
		base["password"] = n.User.Password
		// AnyTLS always relays UDP over the TLS stream (UOT); the official
		// outbound schema has no udp_over_stream switch, so nothing to emit.
		if tls != nil {
			base["tls"] = tls
		}
	case "snell":
		base["psk"] = SnellClientPSK(n.Settings, n.User.Password)
		if n.Settings.SnellVersion > 0 {
			base["version"] = n.Settings.SnellVersion
		}
	case "socks":
		user := n.User.Username
		if user == "" {
			user = n.Settings.Username
		}
		pw := n.User.Password
		if pw == "" {
			pw = n.Settings.Password
		}
		if user != "" {
			base["username"] = user
		}
		if pw != "" {
			base["password"] = pw
		}
	case "mixed": // client dials it as SOCKS5 (HTTP is a server-side convenience)
		base["type"] = "socks"
		user := n.User.Username
		if user == "" {
			user = n.Settings.Username
		}
		pw := n.User.Password
		if pw == "" {
			pw = n.Settings.Password
		}
		if user != "" {
			base["username"] = user
		}
		if pw != "" {
			base["password"] = pw
		}
	default:
		// naive and shadowtls have no sing-box OUTBOUND type (naive is
		// inbound-only). Returning an error makes the caller skip just this node
		// instead of emitting a type sing-box rejects, which would invalidate the
		// subscriber's whole profile.
		return nil, fmt.Errorf("unsupported node type %q", n.Type)
	}
	return json.Marshal(base)
}

// BuildShareLink renders a scheme URI (vless://, vmess://, trojan://, ss://,
// hysteria2://, tuic://) for plain-text share-link subscriptions.
func BuildShareLink(n ClientNode) (string, error) {
	host := strings.Trim(n.Server, "[]")
	port := strconv.Itoa(n.ServerPort)
	hostPort := net.JoinHostPort(host, port)
	name := url.PathEscape(n.Name)
	t := n.Settings.TLS

	switch n.Type {
	case "vless":
		q := url.Values{}
		q.Set("encryption", "none")
		q.Set("type", networkOf(n.Settings.Transport))
		switch {
		case t.Reality.Enabled:
			q.Set("security", "reality")
			q.Set("pbk", t.Reality.PublicKey)
			if sid := firstShortID(t.Reality.ShortID); sid != "" {
				q.Set("sid", sid)
			}
			q.Set("fp", fingerprintOf(t))
			q.Set("sni", clientSNI(t, host))
		case t.tlsEnabled():
			q.Set("security", "tls")
			q.Set("fp", fingerprintOf(t))
			q.Set("sni", clientSNI(t, host))
		default:
			q.Set("security", "none")
		}
		if n.Settings.Flow != "" {
			q.Set("flow", n.Settings.Flow)
		}
		if networkOf(n.Settings.Transport) == "ws" || networkOf(n.Settings.Transport) == "httpupgrade" {
			if n.Settings.Transport.Path != "" {
				q.Set("path", n.Settings.Transport.Path)
			}
			if h := n.Settings.Transport.Headers["Host"]; h != "" {
				q.Set("host", h)
			}
			if n.Settings.Transport.MaxEarlyData > 0 {
				q.Set("maxEarlyData", strconv.Itoa(n.Settings.Transport.MaxEarlyData))
				if n.Settings.Transport.EarlyDataHeader != "" {
					q.Set("earlyDataHeaderName", n.Settings.Transport.EarlyDataHeader)
				}
			}
		}
		if t.tlsEnabled() && len(t.ALPN) > 0 {
			q.Set("alpn", strings.Join(t.ALPN, ","))
		}
		if t.tlsEnabled() && t.ClientInsecure() && !t.Reality.Enabled {
			q.Set("allowInsecure", "1")
		}
		pe := n.Settings.PacketEncoding
		if pe == "" {
			pe = "xudp"
		}
		q.Set("packetEncoding", pe)
		return fmt.Sprintf("vless://%s@%s?%s#%s", n.User.UUID, hostPort, q.Encode(), name), nil

	case "vmess":
		net := networkOf(n.Settings.Transport)
		obj := map[string]any{
			"v": "2", "ps": n.Name, "add": host, "port": port,
			"id": n.User.UUID, "aid": strconv.Itoa(n.Settings.VMessAlterID), "scy": n.Settings.VMessSecurityValue(),
			"net": net, "type": "none", "host": "", "path": "",
			"tls": "", "sni": "",
		}
		if net == "ws" || net == "httpupgrade" {
			obj["path"] = n.Settings.Transport.Path
			obj["host"] = n.Settings.Transport.Headers["Host"]
		}
		if t.Reality.Enabled {
			obj["tls"] = "reality"
			obj["sni"] = clientSNI(t, host)
			obj["fp"] = fingerprintOf(t)
			obj["pbk"] = t.Reality.PublicKey
			obj["sid"] = firstShortID(t.Reality.ShortID)
		} else if t.tlsEnabled() {
			obj["tls"] = "tls"
			obj["sni"] = clientSNI(t, host)
			obj["fp"] = fingerprintOf(t)
		}
		if len(t.ALPN) > 0 {
			obj["alpn"] = strings.Join(t.ALPN, ",")
		}
		if t.tlsEnabled() && t.ClientInsecure() && !t.Reality.Enabled {
			obj["allowInsecure"] = "1"
		}
		raw, _ := json.Marshal(obj)
		return "vmess://" + base64.StdEncoding.EncodeToString(raw), nil

	case "trojan":
		q := url.Values{}
		q.Set("type", networkOf(n.Settings.Transport))
		// Trojan can run over REALITY too (reality is generic across inbound
		// types). Mirror the VLESS branch so the client is told to do REALITY,
		// not plain TLS against a REALITY-only server (which fails the handshake).
		if t.Reality.Enabled {
			q.Set("security", "reality")
			q.Set("pbk", t.Reality.PublicKey)
			if sid := firstShortID(t.Reality.ShortID); sid != "" {
				q.Set("sid", sid)
			}
			q.Set("fp", fingerprintOf(t))
		} else {
			q.Set("security", "tls")
			q.Set("fp", fingerprintOf(t))
			if t.ClientInsecure() {
				q.Set("allowInsecure", "1")
			}
		}
		q.Set("sni", clientSNI(t, host))
		if len(t.ALPN) > 0 {
			q.Set("alpn", strings.Join(t.ALPN, ","))
		}
		if networkOf(n.Settings.Transport) == "ws" || networkOf(n.Settings.Transport) == "httpupgrade" {
			if n.Settings.Transport.Path != "" {
				q.Set("path", n.Settings.Transport.Path)
			}
			if h := n.Settings.Transport.Headers["Host"]; h != "" {
				q.Set("host", h)
			}
			if n.Settings.Transport.MaxEarlyData > 0 {
				q.Set("maxEarlyData", strconv.Itoa(n.Settings.Transport.MaxEarlyData))
				if n.Settings.Transport.EarlyDataHeader != "" {
					q.Set("earlyDataHeaderName", n.Settings.Transport.EarlyDataHeader)
				}
			}
		}
		return fmt.Sprintf("trojan://%s@%s?%s#%s", url.QueryEscape(n.User.Password), hostPort, q.Encode(), name), nil

	case "shadowsocks":
		password := SSClientPassword(n.Settings, n.User.Password)
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(n.Settings.Method + ":" + password))
		q := url.Values{}
		if n.Settings.SSPlugin != "" {
			q.Set("plugin", n.Settings.SSPlugin)
		}
		if len(q) > 0 {
			return fmt.Sprintf("ss://%s@%s/?%s#%s", userinfo, hostPort, q.Encode(), name), nil
		}
		return fmt.Sprintf("ss://%s@%s#%s", userinfo, hostPort, name), nil

	case "socks":
		username := n.User.Username
		if username == "" {
			username = n.Settings.Username
		}
		password := n.User.Password
		if password == "" {
			password = n.Settings.Password
		}
		authority := hostPort
		if username != "" || password != "" {
			authority = url.UserPassword(username, password).String() + "@" + hostPort
		}
		return fmt.Sprintf("socks5://%s#%s", authority, name), nil

	case "mixed": // one port speaks HTTP + SOCKS5; clients dial it as SOCKS5
		username := n.User.Username
		if username == "" {
			username = n.Settings.Username
		}
		password := n.User.Password
		if password == "" {
			password = n.Settings.Password
		}
		authority := hostPort
		if username != "" || password != "" {
			authority = url.UserPassword(username, password).String() + "@" + hostPort
		}
		return fmt.Sprintf("socks5://%s#%s", authority, name), nil

	case "hysteria2":
		q := url.Values{}
		q.Set("sni", clientSNI(t, host))
		if n.Settings.ObfsPassword != "" {
			q.Set("obfs", "salamander")
			q.Set("obfs-password", n.Settings.ObfsPassword)
		}
		if n.Settings.UpMbps > 0 {
			q.Set("up", strconv.Itoa(n.Settings.UpMbps))
		}
		if n.Settings.DownMbps > 0 {
			q.Set("down", strconv.Itoa(n.Settings.DownMbps))
		}
		if len(t.ALPN) > 0 {
			q.Set("alpn", strings.Join(t.ALPN, ","))
		}
		if t.ClientInsecure() {
			q.Set("insecure", "1")
		}
		return fmt.Sprintf("hysteria2://%s@%s?%s#%s", url.QueryEscape(n.User.Password), hostPort, q.Encode(), name), nil

	case "tuic":
		q := url.Values{}
		cc := n.Settings.CongestionControl
		if cc == "" {
			cc = "cubic"
		}
		q.Set("congestion_control", cc)
		if v := n.Settings.TUICRelayModeValue(); v != "" {
			q.Set("udp_relay_mode", v)
		}
		q.Set("sni", clientSNI(t, host))
		if len(t.ALPN) > 0 {
			q.Set("alpn", strings.Join(t.ALPN, ","))
		}
		if t.ClientInsecure() {
			q.Set("allow_insecure", "1")
		}
		return fmt.Sprintf("tuic://%s:%s@%s?%s#%s",
			n.User.UUID, url.QueryEscape(n.User.Password), hostPort, q.Encode(), name), nil

	case "anytls":
		q := url.Values{}
		q.Set("sni", clientSNI(t, host))
		if len(t.ALPN) > 0 {
			q.Set("alpn", strings.Join(t.ALPN, ","))
		}
		// Mirror the other TCP protocols: always advertise the uTLS fingerprint
		// (chrome unless overridden) so every client handshakes identically.
		q.Set("fp", fingerprintOf(t))
		if n.Settings.AnyTLSUDPOverStream {
			q.Set("udp_over_stream", "1")
		}
		if t.ClientInsecure() {
			q.Set("insecure", "1")
		}
		return fmt.Sprintf("anytls://%s@%s?%s#%s",
			url.QueryEscape(n.User.Password), hostPort, q.Encode(), name), nil

	case "hysteria": // v1
		q := url.Values{}
		q.Set("protocol", "udp")
		q.Set("auth", n.User.Password)
		q.Set("peer", clientSNI(t, host))
		up, down := HysteriaBandwidth(n.Settings)
		q.Set("upmbps", strconv.Itoa(up))
		q.Set("downmbps", strconv.Itoa(down))
		if n.Settings.ObfsPassword != "" {
			q.Set("obfs", n.Settings.ObfsPassword)
		}
		if t.ClientInsecure() {
			q.Set("insecure", "1")
		}
		return fmt.Sprintf("hysteria://%s?%s#%s", hostPort, q.Encode(), name), nil

	case "naive":
		return fmt.Sprintf("naive+https://%s:%s@%s#%s",
			url.QueryEscape(n.User.Name), url.QueryEscape(n.User.Password), hostPort, name), nil

	default:
		// snell and shadowtls have no widely-supported share-link scheme; the
		// panel shows their parameters instead of inventing a URI.
		return "", fmt.Errorf("unsupported node type %q", n.Type)
	}
}
