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

// ParseShareLink parses a standard share URI (vless://, vmess://, ss://,
// trojan://, hysteria2://, hysteria://, tuic://, anytls://, socks5://) into a
// ClientNode. It is the inverse of BuildShareLink and lets the panel adopt a
// hand-shared external node (e.g. a friend's server the admin does not control)
// into a subscription without touching the managed sing-box configuration.
func ParseShareLink(uri string) (ClientNode, error) {
	uri = strings.TrimSpace(uri)
	lower := strings.ToLower(uri)
	switch {
	case strings.HasPrefix(lower, "vmess://"):
		return parseVMess(uri)
	case strings.HasPrefix(lower, "vless://"):
		return parseVLESS(uri)
	case strings.HasPrefix(lower, "ss://"):
		return parseShadowsocks(uri)
	case strings.HasPrefix(lower, "trojan://"):
		return parseTrojan(uri)
	case strings.HasPrefix(lower, "hysteria2://"), strings.HasPrefix(lower, "hy2://"):
		return parseHysteria2(uri)
	case strings.HasPrefix(lower, "hysteria://"), strings.HasPrefix(lower, "hy1://"):
		return parseHysteria(uri)
	case strings.HasPrefix(lower, "tuic://"):
		return parseTUIC(uri)
	case strings.HasPrefix(lower, "anytls://"):
		return parseAnyTLS(uri)
	case strings.HasPrefix(lower, "socks5://"), strings.HasPrefix(lower, "socks://"):
		return parseSocks(uri)
	default:
		return ClientNode{}, fmt.Errorf("unsupported link scheme")
	}
}

// shareHostPort returns the hostname and port (defaulting to 443) from a
// parsed URL.
func shareHostPort(u *url.URL) (string, int, error) {
	host := u.Hostname()
	if host == "" {
		return "", 0, fmt.Errorf("missing host")
	}
	port := 443
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return "", 0, fmt.Errorf("invalid port %q", p)
		}
		port = n
	}
	return host, port, nil
}

// shareName extracts the URL fragment as the node display name.
func shareName(u *url.URL) string {
	return strings.TrimSpace(u.Fragment)
}

func splitOrNil(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []string{s}
}

// parseWSQuery fills TransportSettings from the shared type/path/host query
// params (used by vless / trojan links).
func parseWSQuery(q url.Values, t *TransportSettings) {
	switch q.Get("type") {
	case "ws":
		*t = TransportSettings{Type: "ws", Path: q.Get("path"), Headers: map[string]string{}}
	case "httpupgrade":
		*t = TransportSettings{Type: "httpupgrade", Path: q.Get("path"), Headers: map[string]string{}}
	default:
		return
	}
	if h := q.Get("host"); h != "" {
		t.Headers["Host"] = h
	}
	if v := q.Get("maxEarlyData"); v != "" {
		t.MaxEarlyData = atoiSafe(v)
	}
	if h := q.Get("earlyDataHeaderName"); h != "" {
		t.EarlyDataHeader = h
	}
}

// parseTLSSecurity maps the security/sni/pbk/sid/fp query params of a share
// link onto TLSSettings. insecure is the already-resolved skip-verify flag
// (see insecureFromQuery), so every spelling is honored by the caller.
func parseTLSSecurity(security, sni, pbk, sid string, insecure bool, fingerprint string, t *TLSSettings) error {
	t.Fingerprint = fingerprint
	switch security {
	case "reality":
		if pbk == "" {
			return fmt.Errorf("reality link missing public key")
		}
		t.Enabled = true
		t.ServerName = sni
		t.Reality = RealitySettings{Enabled: true, PublicKey: pbk, ShortID: splitOrNil(sid)}
	case "tls", "xtls":
		t.Enabled = true
		t.ServerName = sni
		if insecure {
			t.Insecure = true
		}
	case "none", "":
		// plaintext
	default:
		return fmt.Errorf("unsupported security %q", security)
	}
	return nil
}

func parseVLESS(uri string) (ClientNode, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return ClientNode{}, fmt.Errorf("invalid vless link: %w", err)
	}
	if u.User == nil || u.User.Username() == "" {
		return ClientNode{}, fmt.Errorf("vless link missing uuid")
	}
	host, port, err := shareHostPort(u)
	if err != nil {
		return ClientNode{}, err
	}
	q := u.Query()
	cn := ClientNode{Name: shareName(u), Server: host, ServerPort: port, Type: "vless"}
	cn.User.UUID = u.User.Username()
	cn.Settings.Flow = q.Get("flow")
	cn.Settings.PacketEncoding = q.Get("packetEncoding")
	parseWSQuery(q, &cn.Settings.Transport)
	if err := parseTLSSecurity(q.Get("security"), q.Get("sni"), q.Get("pbk"), q.Get("sid"), insecureFromQuery(q), q.Get("fp"), &cn.Settings.TLS); err != nil {
		return ClientNode{}, err
	}
	return cn, nil
}

func parseVMess(uri string) (ClientNode, error) {
	raw := strings.TrimPrefix(uri, "vmess://")
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		data, err = base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return ClientNode{}, fmt.Errorf("vmess link is not valid base64")
		}
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return ClientNode{}, fmt.Errorf("vmess link payload is not valid JSON: %w", err)
	}
	host := strOf(obj, "add")
	port := intOf(obj, "port")
	if host == "" || port <= 0 {
		return ClientNode{}, fmt.Errorf("vmess link missing server/port")
	}
	cn := ClientNode{Name: strOf(obj, "ps"), Server: host, ServerPort: port, Type: "vmess"}
	cn.User.UUID = strOf(obj, "id")
	cn.Settings.VMessAlterID = intOf(obj, "aid")
	cn.Settings.VMessSecurity = strOf(obj, "scy")
	if cn.Settings.VMessSecurity == "" {
		cn.Settings.VMessSecurity = "auto"
	}
	if strOf(obj, "net") == "ws" || strOf(obj, "net") == "httpupgrade" {
		cn.Settings.Transport = TransportSettings{Type: strOf(obj, "net"), Path: strOf(obj, "path"), Headers: map[string]string{}}
		if h := strOf(obj, "host"); h != "" {
			cn.Settings.Transport.Headers["Host"] = h
		}
	}
	switch strOf(obj, "tls") {
	case "reality":
		cn.Settings.TLS.Enabled = true
		cn.Settings.TLS.ServerName = strOf(obj, "sni")
		cn.Settings.TLS.Fingerprint = strOf(obj, "fp")
		cn.Settings.TLS.Reality = RealitySettings{Enabled: true, PublicKey: strOf(obj, "pbk"), ShortID: splitOrNil(strOf(obj, "sid"))}
	case "tls":
		cn.Settings.TLS.Enabled = true
		cn.Settings.TLS.ServerName = strOf(obj, "sni")
		cn.Settings.TLS.Fingerprint = strOf(obj, "fp")
		if insecureFromMap(obj) {
			cn.Settings.TLS.Insecure = true
		}
	}
	if a := strOf(obj, "alpn"); a != "" {
		cn.Settings.TLS.ALPN = strings.Split(a, ",")
	}
	return cn, nil
}

func parseShadowsocks(uri string) (ClientNode, error) {
	rest := strings.TrimPrefix(uri, "ss://")
	name := ""
	if i := strings.LastIndex(rest, "#"); i >= 0 {
		name = strings.TrimSpace(rest[i+1:])
		rest = rest[:i]
	}
	// SIP002 plugin parameter: ss://base64(method:pass)@host:port/?plugin=…
	plugin := ""
	if i := strings.Index(rest, "?"); i >= 0 {
		if q, err := url.ParseQuery(rest[i+1:]); err == nil {
			plugin = q.Get("plugin")
		}
		rest = rest[:i]
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return ClientNode{}, fmt.Errorf("ss link missing @")
	}
	userinfo, hostport := rest[:at], rest[at+1:]
	host, port, err := splitHostPort(hostport)
	if err != nil {
		return ClientNode{}, err
	}
	// userinfo is base64(method:password) or plain method:password
	decoded := userinfo
	if d, err := base64.RawURLEncoding.DecodeString(userinfo); err == nil {
		decoded = string(d)
	} else if d, err := base64.StdEncoding.DecodeString(userinfo); err == nil {
		decoded = string(d)
	}
	method, password, ok := strings.Cut(decoded, ":")
	if !ok || method == "" || password == "" {
		return ClientNode{}, fmt.Errorf("ss link missing method/password")
	}
	cn := ClientNode{Name: name, Server: host, ServerPort: port, Type: "shadowsocks"}
	cn.Settings.Method = method
	cn.Settings.SSPlugin = plugin
	// Present the credential as a fixed single-user PSK so every writer
	// (links/clash/sing-box/surge) emits it verbatim via SSClientPassword.
	cn.Settings.SingleUser = true
	cn.Settings.SSServerPSK = password
	return cn, nil
}

func splitHostPort(s string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, fmt.Errorf("invalid host:port %q", s)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port %q", portStr)
	}
	return host, port, nil
}

func parseTrojan(uri string) (ClientNode, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return ClientNode{}, fmt.Errorf("invalid trojan link: %w", err)
	}
	if u.User == nil || u.User.Username() == "" {
		return ClientNode{}, fmt.Errorf("trojan link missing password")
	}
	host, port, err := shareHostPort(u)
	if err != nil {
		return ClientNode{}, err
	}
	q := u.Query()
	cn := ClientNode{Name: shareName(u), Server: host, ServerPort: port, Type: "trojan"}
	cn.User.Password = u.User.Username()
	parseWSQuery(q, &cn.Settings.Transport)
	if err := parseTLSSecurity(q.Get("security"), q.Get("sni"), q.Get("pbk"), q.Get("sid"), insecureFromQuery(q), q.Get("fp"), &cn.Settings.TLS); err != nil {
		return ClientNode{}, err
	}
	// trojan is TLS by nature; a link without a security param still means TLS.
	if !cn.Settings.TLS.Enabled && !cn.Settings.TLS.Reality.Enabled {
		cn.Settings.TLS.Enabled = true
		cn.Settings.TLS.ServerName = q.Get("sni")
		// insecure was already resolved by parseTLSSecurity above if security was set;
		// for a link with no security param at all, check again here.
		if insecureFromQuery(q) {
			cn.Settings.TLS.Insecure = true
		}
	}
	if a := q.Get("alpn"); a != "" {
		cn.Settings.TLS.ALPN = strings.Split(a, ",")
	}
	return cn, nil
}

func parseHysteria2(uri string) (ClientNode, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return ClientNode{}, fmt.Errorf("invalid hysteria2 link: %w", err)
	}
	host, port, err := shareHostPort(u)
	if err != nil {
		return ClientNode{}, err
	}
	q := u.Query()
	cn := ClientNode{Name: shareName(u), Server: host, ServerPort: port, Type: "hysteria2"}
	if u.User != nil {
		cn.User.Password = u.User.Username()
	}
	cn.Settings.TLS.Enabled = true
	cn.Settings.TLS.ServerName = q.Get("sni")
	if insecureFromQuery(q) {
		cn.Settings.TLS.Insecure = true
	}
	if q.Get("obfs") == "salamander" {
		cn.Settings.ObfsPassword = q.Get("obfs-password")
	}
	if v := q.Get("up"); v != "" {
		cn.Settings.UpMbps = atoiSafe(v)
	}
	if v := q.Get("down"); v != "" {
		cn.Settings.DownMbps = atoiSafe(v)
	}
	if a := q.Get("alpn"); a != "" {
		cn.Settings.TLS.ALPN = strings.Split(a, ",")
	}
	return cn, nil
}

func parseHysteria(uri string) (ClientNode, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return ClientNode{}, fmt.Errorf("invalid hysteria link: %w", err)
	}
	host, port, err := shareHostPort(u)
	if err != nil {
		return ClientNode{}, err
	}
	q := u.Query()
	cn := ClientNode{Name: shareName(u), Server: host, ServerPort: port, Type: "hysteria"}
	cn.User.Password = q.Get("auth")
	cn.Settings.TLS.Enabled = true
	cn.Settings.TLS.ServerName = q.Get("peer")
	if insecureFromQuery(q) {
		cn.Settings.TLS.Insecure = true
	}
	cn.Settings.UpMbps = atoiSafe(q.Get("upmbps"))
	cn.Settings.DownMbps = atoiSafe(q.Get("downmbps"))
	if v := q.Get("obfs"); v != "" {
		cn.Settings.ObfsPassword = v
	}
	return cn, nil
}

func parseTUIC(uri string) (ClientNode, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return ClientNode{}, fmt.Errorf("invalid tuic link: %w", err)
	}
	if u.User == nil || u.User.Username() == "" {
		return ClientNode{}, fmt.Errorf("tuic link missing uuid")
	}
	host, port, err := shareHostPort(u)
	if err != nil {
		return ClientNode{}, err
	}
	q := u.Query()
	cn := ClientNode{Name: shareName(u), Server: host, ServerPort: port, Type: "tuic"}
	cn.User.UUID = u.User.Username()
	if pw, ok := u.User.Password(); ok {
		cn.User.Password = pw
	}
	cn.Settings.TLS.Enabled = true
	cn.Settings.TLS.ServerName = q.Get("sni")
	if insecureFromQuery(q) {
		cn.Settings.TLS.Insecure = true
	}
	if cc := q.Get("congestion_control"); cc != "" {
		cn.Settings.CongestionControl = cc
	}
	if v := q.Get("udp_relay_mode"); v != "" {
		cn.Settings.TUICUDPRelayMode = v
	}
	if a := q.Get("alpn"); a != "" {
		cn.Settings.TLS.ALPN = strings.Split(a, ",")
	}
	return cn, nil
}

func parseAnyTLS(uri string) (ClientNode, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return ClientNode{}, fmt.Errorf("invalid anytls link: %w", err)
	}
	if u.User == nil || u.User.Username() == "" {
		return ClientNode{}, fmt.Errorf("anytls link missing password")
	}
	host, port, err := shareHostPort(u)
	if err != nil {
		return ClientNode{}, err
	}
	q := u.Query()
	cn := ClientNode{Name: shareName(u), Server: host, ServerPort: port, Type: "anytls"}
	cn.User.Password = u.User.Username()
	cn.Settings.TLS.Enabled = true
	cn.Settings.TLS.ServerName = q.Get("sni")
	if insecureFromQuery(q) {
		cn.Settings.TLS.Insecure = true
	}
	if a := q.Get("alpn"); a != "" {
		cn.Settings.TLS.ALPN = strings.Split(a, ",")
	}
	if fp := q.Get("fp"); fp != "" {
		cn.Settings.TLS.Fingerprint = fp
	}
	if v := q.Get("udp_over_stream"); v == "1" || strings.EqualFold(v, "true") {
		cn.Settings.AnyTLSUDPOverStream = true
	}
	return cn, nil
}

func parseSocks(uri string) (ClientNode, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return ClientNode{}, fmt.Errorf("invalid socks link: %w", err)
	}
	host, port, err := shareHostPort(u)
	if err != nil {
		return ClientNode{}, err
	}
	cn := ClientNode{Name: shareName(u), Server: host, ServerPort: port, Type: "socks"}
	if u.User != nil {
		cn.Settings.Username = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			cn.Settings.Password = pw
		}
	}
	return cn, nil
}

func strOf(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func intOf(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case string:
		return atoiSafe(v)
	}
	return 0
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// isTruthy reports whether a share-link value means "on". Clients disagree on
// spelling (1 / true / yes / on), so treat any of them — case-insensitively —
// as true, and everything else (including "0" and "false") as false.
func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// normalizeKey lowercases a query/JSON key and drops '-'/'_' so that
// allowInsecure, allow_insecure and skip-cert-verify all collapse onto a
// single canonical form for matching.
func normalizeKey(k string) string {
	k = strings.ToLower(k)
	k = strings.ReplaceAll(k, "-", "")
	k = strings.ReplaceAll(k, "_", "")
	return k
}

// insecureKeys is the set of normalized keys under which the various clients
// export a "skip TLS certificate verification" flag.
var insecureKeys = map[string]bool{
	"allowinsecure":    true,
	"insecure":         true,
	"allowinsecuretls": true,
	"skipcertverify":   true,
	"tlsinsecure":      true,
}

// insecureFromQuery reports whether a share-link query carries a truthy
// "skip TLS verification" flag under ANY of its common spellings, matched
// case-insensitively and ignoring '-'/'_' separators. Different clients emit
// allowInsecure / allowinsecure / insecure / allow_insecure / skip-cert-verify,
// so a single fixed key (as before) silently dropped most of them.
func insecureFromQuery(q url.Values) bool {
	for k, vs := range q {
		if !insecureKeys[normalizeKey(k)] {
			continue
		}
		for _, v := range vs {
			if isTruthy(v) {
				return true
			}
		}
	}
	return false
}

// insecureFromMap is the same check for the vmess JSON payload map, whose values
// may be strings or numbers depending on the exporter.
func insecureFromMap(m map[string]any) bool {
	for k, v := range m {
		if !insecureKeys[normalizeKey(k)] {
			continue
		}
		switch val := v.(type) {
		case string:
			if isTruthy(val) {
				return true
			}
		case bool:
			if val {
				return true
			}
		case float64:
			if val != 0 {
				return true
			}
		}
	}
	return false
}
