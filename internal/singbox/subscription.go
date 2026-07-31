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
	return "ws"
}

// buildClientTLS renders a client-side tls object, or nil for no TLS.
func buildClientTLS(t TLSSettings, server string) map[string]any {
	if !t.tlsEnabled() {
		return nil
	}
	tls := map[string]any{
		"enabled":     true,
		"server_name": clientSNI(t, server),
		// uTLS improves camouflage and is required alongside REALITY.
		"utls": map[string]any{"enabled": true, "fingerprint": "chrome"},
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
	tls := buildClientTLS(n.Settings.TLS, n.Server)
	tr := buildTransport(n.Settings.Transport)

	switch n.Type {
	case "vless":
		base["uuid"] = n.User.UUID
		if n.Settings.Flow != "" {
			base["flow"] = n.Settings.Flow
		}
		base["packet_encoding"] = "xudp"
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
		if tls != nil {
			base["tls"] = tls
		}
		if tr != nil {
			base["transport"] = tr
		}
	case "shadowsocks":
		base["method"] = n.Settings.Method
		base["password"] = SSClientPassword(n.Settings, n.User.Password)
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
			q.Set("fp", "chrome")
			q.Set("sni", clientSNI(t, host))
		case t.tlsEnabled():
			q.Set("security", "tls")
			q.Set("fp", "chrome")
			q.Set("sni", clientSNI(t, host))
		default:
			q.Set("security", "none")
		}
		if n.Settings.Flow != "" {
			q.Set("flow", n.Settings.Flow)
		}
		if networkOf(n.Settings.Transport) == "ws" {
			if n.Settings.Transport.Path != "" {
				q.Set("path", n.Settings.Transport.Path)
			}
			if h := n.Settings.Transport.Headers["Host"]; h != "" {
				q.Set("host", h)
			}
		}
		if t.tlsEnabled() && t.ClientInsecure() && !t.Reality.Enabled {
			q.Set("allowInsecure", "1")
		}
		return fmt.Sprintf("vless://%s@%s?%s#%s", n.User.UUID, hostPort, q.Encode(), name), nil

	case "vmess":
		net := networkOf(n.Settings.Transport)
		obj := map[string]any{
			"v": "2", "ps": n.Name, "add": host, "port": port,
			"id": n.User.UUID, "aid": strconv.Itoa(n.Settings.VMessAlterID), "scy": n.Settings.VMessSecurityValue(),
			"net": net, "type": "none", "host": "", "path": "",
			"tls": "", "sni": "",
		}
		if net == "ws" {
			obj["path"] = n.Settings.Transport.Path
			obj["host"] = n.Settings.Transport.Headers["Host"]
		}
		if t.Reality.Enabled {
			obj["tls"] = "reality"
			obj["sni"] = clientSNI(t, host)
			obj["fp"] = "chrome"
			obj["pbk"] = t.Reality.PublicKey
			obj["sid"] = firstShortID(t.Reality.ShortID)
		} else if t.tlsEnabled() {
			obj["tls"] = "tls"
			obj["sni"] = clientSNI(t, host)
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
		q.Set("security", "tls")
		q.Set("sni", clientSNI(t, host))
		if len(t.ALPN) > 0 {
			q.Set("alpn", strings.Join(t.ALPN, ","))
		}
		if t.ClientInsecure() {
			q.Set("allowInsecure", "1")
		}
		if networkOf(n.Settings.Transport) == "ws" {
			if n.Settings.Transport.Path != "" {
				q.Set("path", n.Settings.Transport.Path)
			}
			if h := n.Settings.Transport.Headers["Host"]; h != "" {
				q.Set("host", h)
			}
		}
		return fmt.Sprintf("trojan://%s@%s?%s#%s", url.QueryEscape(n.User.Password), hostPort, q.Encode(), name), nil

	case "shadowsocks":
		password := SSClientPassword(n.Settings, n.User.Password)
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(n.Settings.Method + ":" + password))
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

	case "hysteria2":
		q := url.Values{}
		q.Set("sni", clientSNI(t, host))
		if n.Settings.ObfsPassword != "" {
			q.Set("obfs", "salamander")
			q.Set("obfs-password", n.Settings.ObfsPassword)
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
