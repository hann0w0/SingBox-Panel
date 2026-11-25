package panel

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func (a *App) writeSurge(c *gin.Context, nodes []node) {
	lines, _, skipped := surgeProxies(nodes)
	var b strings.Builder
	b.WriteString("[Proxy]\n")
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	if len(skipped) > 0 {
		b.WriteString("# Surge 不支持以下节点，已跳过: " + strings.Join(skipped, ", ") + "\n")
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(b.String()))
}

func sanitizeSurgeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' || r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	// Surge splits "name = type, ..." at the first '=', so an '=' inside the
	// name would shift the whole definition. Keep its look with U+FF1D.
	name = strings.ReplaceAll(name, "=", "＝")
	if name == "" {
		name = "node"
	}
	if strings.HasPrefix(name, "#") || strings.HasPrefix(name, ";") || strings.HasPrefix(name, "[") {
		name = "_" + name
	}
	return name
}

func surgeProxies(nodes []node) (lines, names, skipped []string) {
	seen := map[string]int{}
	for _, n := range nodes {
		name := uniqueName(seen, sanitizeSurgeName(n.name))
		line := surgeProxy(n, name)
		if line == "" {
			skipped = append(skipped, name)
			continue
		}
		lines = append(lines, line)
		names = append(names, name)
	}
	return lines, names, skipped
}

// surgeALPN renders the ALPN argument for Surge TLS policies. Returns an
// empty string when the node has no ALPN configured (Surge then uses its
// protocol default); multi-value lists are quoted per the Surge manual.
func surgeALPN(alpn []string) string {
	if len(alpn) == 0 {
		return ""
	}
	v := strings.Join(alpn, ",")
	if strings.Contains(v, ",") {
		v = `"` + v + `"`
	}
	return ", alpn=" + v
}

func surgeProxy(n node, name string) string {
	if n.typ == "shadowsocks" && n.settings.SSPlugin != "" {
		_, host, ok := surgeSSObfs(n.settings.SSPlugin)
		if !ok || strings.Contains(host, `"`) {
			return ""
		}
	}
	line := surgeProxyUnchecked(n, sanitizeSurgeName(name))
	if strings.IndexFunc(line, unicode.IsControl) >= 0 || surgeHasUnquotableValue(n) {
		return ""
	}
	return line
}

func surgeSSObfs(plugin string) (mode, host string, ok bool) {
	name, opts := singbox.ShadowsocksPluginFields(plugin)
	if name != "obfs-local" {
		return "", "", false
	}
	for _, part := range strings.Split(opts, ";") {
		key, value, found := strings.Cut(part, "=")
		if !found {
			return "", "", false
		}
		switch key {
		case "obfs":
			if mode != "" {
				return "", "", false
			}
			mode = value
		case "obfs-host":
			if host != "" {
				return "", "", false
			}
			host = value
		default:
			return "", "", false
		}
	}
	return mode, host, mode == "http" || mode == "tls"
}

// surgeValue renders one parameter value. Surge separates parameters with
// commas, so a value containing one (possible in third-party passwords or
// paths) is wrapped in double quotes, the form the manual uses for ALPN lists.
func surgeValue(v string) string {
	if strings.Contains(v, ",") {
		return `"` + v + `"`
	}
	return v
}

// surgeWSHeaders renders WebSocket/HTTPUpgrade headers in Surge's pipe-separated
// Key:Value format, placing Host first when present.
func surgeWSHeaders(tr singbox.TransportSettings) string {
	var parts []string
	if h := tr.Host(); h != "" {
		parts = append(parts, "Host:"+h)
	}
	values := tr.HeaderValuesMap()
	keys := make([]string, 0, len(values))
	for k := range values {
		if !strings.EqualFold(k, "host") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if vals := values[k]; len(vals) > 0 && vals[0] != "" {
			parts = append(parts, k+":"+vals[0])
		}
	}
	return strings.Join(parts, "|")
}

// surgeHasUnquotableValue reports a free-form value containing a double quote,
// which Surge's syntax cannot escape. Such a node is skipped (and reported)
// rather than emitted in a form Surge would mis-parse.
func surgeHasUnquotableValue(n node) bool {
	st := n.settings
	u := n.getUserIdentity()
	for _, v := range []string{
		n.server, st.Method, st.Username, st.Password, u.Password, u.UUID,
		st.Flow, st.TLS.Reality.PublicKey, firstOf(st.TLS.Reality.ShortID),
		st.ObfsPassword, st.SnellObfsHost, st.Transport.Path, st.Transport.Host(), sniOf(st, n.server),
		singbox.SSClientPassword(st, u.Password), singbox.SnellClientPSK(st, u.Password),
	} {
		if strings.Contains(v, `"`) {
			return true
		}
	}
	for k, vals := range st.Transport.HeaderValuesMap() {
		if strings.Contains(k, `"`) {
			return true
		}
		for _, v := range vals {
			if strings.Contains(v, `"`) {
				return true
			}
		}
	}
	return false
}

func surgeProxyUnchecked(n node, name string) string {
	n.server = strings.Trim(strings.TrimSpace(n.server), "[]")
	st := n.settings
	u := n.getUserIdentity()
	sni := sniOf(st, n.server)
	switch n.typ {
	case "shadowsocks":
		pass := singbox.SSClientPassword(st, u.Password)
		line := fmt.Sprintf("%s = ss, %s, %d, encrypt-method=%s, password=%s, udp-relay=%t",
			name, n.server, n.port, surgeValue(st.Method), surgeValue(pass), n.udpEnabled())
		if st.SSPlugin != "" {
			mode, host, _ := surgeSSObfs(st.SSPlugin)
			line += ", obfs=" + mode
			if host != "" {
				line += ", obfs-host=" + surgeValue(host)
			}
		}
		return line
	case "socks":
		line := fmt.Sprintf("%s = socks5, %s, %d", name, n.server, n.port)
		if st.Username != "" {
			line += ", username=" + surgeValue(st.Username)
		}
		if st.Password != "" {
			line += ", password=" + surgeValue(st.Password)
		}
		return fmt.Sprintf("%s, udp-relay=%t", line, n.udpEnabled())
	case "http":
		if st.HTTPPath != "" || len(st.HTTPHeaders) > 0 {
			return ""
		}
		typ := "http"
		if nodeTLSOn(st) {
			typ = "https"
		}
		line := fmt.Sprintf("%s = %s, %s, %d", name, typ, n.server, n.port)
		if st.Username != "" || st.Password != "" {
			username, password := surgeValue(st.Username), surgeValue(st.Password)
			if username == "" {
				username = `""`
			}
			if password == "" {
				password = `""`
			}
			line += ", username=" + username + ", password=" + password
		}
		if nodeTLSOn(st) {
			line += fmt.Sprintf(", sni=%s, skip-cert-verify=%t", surgeValue(sni), st.TLS.ClientInsecure())
			line += surgeALPN(st.TLS.ALPN)
		}
		return line
	case "trojan":
		if st.TLS.Reality.Enabled {
			return ""
		}
		tr := strings.ToLower(strings.TrimSpace(st.Transport.Type))
		switch tr {
		case "", "tcp", "ws":
		default:
			return ""
		}
		line := fmt.Sprintf("%s = trojan, %s, %d, password=%s, tls=true, sni=%s, skip-cert-verify=%t",
			name, n.server, n.port, surgeValue(u.Password), surgeValue(sni), st.TLS.ClientInsecure())
		line += surgeALPN(st.TLS.ALPN)
		if tr == "ws" {
			line += ", ws=true"
			if st.Transport.Path != "" {
				line += ", ws-path=" + surgeValue(st.Transport.Path)
			}
			if hdrs := surgeWSHeaders(st.Transport); hdrs != "" {
				line += ", ws-headers=" + surgeValue(hdrs)
			}
		}
		return fmt.Sprintf("%s, udp-relay=%t", line, n.udpEnabled())
	case "anytls":
		// Surge added AnyTLS support in iOS 5.17.0 / macOS 6.4.3; the shared TLS
		// parameters (sni, skip-cert-verify, alpn) apply from iOS 5.20.0+.
		line := fmt.Sprintf("%s = anytls, %s, %d, password=%s, sni=%s, skip-cert-verify=%t",
			name, n.server, n.port, surgeValue(u.Password), surgeValue(sni), st.TLS.ClientInsecure())
		line += surgeALPN(st.TLS.ALPN)
		return fmt.Sprintf("%s, udp-relay=%t", line, n.udpEnabled())
	case "vmess":
		if st.TLS.Reality.Enabled {
			return ""
		}
		line := fmt.Sprintf("%s = vmess, %s, %d, username=%s, vmess-aead=%t", name, n.server, n.port, surgeValue(u.UUID), st.VMessAlterID == 0)
		if nodeTLSOn(st) {
			line += fmt.Sprintf(", tls=true, sni=%s", surgeValue(sni))
			line += surgeALPN(st.TLS.ALPN)
			if st.TLS.ClientInsecure() {
				line += ", skip-cert-verify=true"
			}
		}
		if st.Transport.Type == "ws" {
			line += ", ws=true, ws-path=" + surgeValue(st.Transport.Path)
			if h := st.Transport.Host(); h != "" {
				line += ", ws-headers=" + surgeValue("Host:"+h)
			}
		}
		return fmt.Sprintf("%s, udp-relay=%t", line, n.udpEnabled())
	case "hysteria2":
		line := fmt.Sprintf("%s = hysteria2, %s, %d, password=%s, sni=%s", name, n.server, n.port, surgeValue(u.Password), surgeValue(sni))
		line += surgeALPN(st.TLS.ALPN)
		if st.DownMbps > 0 {
			line += fmt.Sprintf(", download-bandwidth=%d", st.DownMbps)
		}
		// Surge's Hysteria2 syntax has a dedicated Salamander field. Preserve
		// that field only for Salamander; Gecko is emitted by sing-box/Clash
		// formats and must not be mislabeled as Salamander here.
		if st.ObfsPassword != "" && (st.ObfsType == "" || st.ObfsType == "salamander") {
			line += ", salamander-password=" + surgeValue(st.ObfsPassword)
		}
		if st.TLS.ClientInsecure() {
			line += ", skip-cert-verify=true"
		}
		return line
	case "snell":
		// Surge's native protocol: name = snell, host, port, psk=…, version=…
		line := fmt.Sprintf("%s = snell, %s, %d, psk=%s", name, n.server, n.port,
			surgeValue(singbox.SnellClientPSK(st, u.Password)))
		if st.SnellVersion > 0 {
			line += fmt.Sprintf(", version=%d", st.SnellVersion)
		}
		if (st.SnellVersion == 4 || st.SnellVersion == 5) && st.SnellObfsMode != "" && st.SnellObfsMode != "none" {
			line += ", obfs=" + st.SnellObfsMode
			if st.SnellObfsHost != "" {
				line += ", obfs-host=" + surgeValue(st.SnellObfsHost)
			}
		}
		return fmt.Sprintf("%s, udp-relay=%t", line, n.udpEnabled())
	case "tuic":
		// sing-box implements TUIC v5 (uuid + password). Surge distinguishes the
		// two wire versions with different keywords — `tuic` = v4 (single token)
		// and `tuic-v5` = v5 (uuid + password) — and the manual states they are
		// NOT interchangeable. Our servers are sing-box TUIC v5, so emit tuic-v5
		// with the v5 credential pair or the policy cannot authenticate.
		alpn := firstOf(st.TLS.ALPN)
		if alpn == "" {
			alpn = "h3"
		}
		line := fmt.Sprintf("%s = tuic-v5, %s, %d, uuid=%s, password=%s, sni=%s, alpn=%s",
			name, n.server, n.port, surgeValue(u.UUID), surgeValue(u.Password), surgeValue(sni), alpn)
		if st.TLS.ClientInsecure() {
			line += ", skip-cert-verify=true"
		}
		return line
	case "vless":
		// Surge added native VLESS support in iOS 6.0.0 / macOS 6.12.0 (RAW/TCP,
		// WebSocket, HTTPUpgrade, REALITY, and XTLS Vision). UDP relay via XUDP
		// is built in with no extra parameter.
		if u.UUID == "" {
			return ""
		}
		tr := strings.ToLower(strings.TrimSpace(st.Transport.Type))
		switch tr {
		case "", "tcp", "ws", "httpupgrade":
		default:
			return ""
		}
		if st.TLS.Reality.Enabled {
			// Surge supports REALITY on RAW (TCP), gRPC, and XHTTP — not WS/HTTPUpgrade.
			if tr == "ws" || tr == "httpupgrade" || st.TLS.Reality.PublicKey == "" {
				return ""
			}
		}
		// Without VLESS Encryption, Vision flow requires RAW transport with TLS or REALITY.
		if st.Flow != "" && (tr == "ws" || tr == "httpupgrade" || (!st.TLS.Reality.Enabled && !nodeTLSOn(st))) {
			return ""
		}
		line := fmt.Sprintf("%s = vless, %s, %d, username=%s", name, n.server, n.port, surgeValue(u.UUID))
		if st.Flow != "" {
			line += ", flow=" + surgeValue(st.Flow)
		}
		if st.TLS.Reality.Enabled {
			// Setting reality-public-key enables REALITY and implies TLS;
			// skip-cert-verify does not apply to REALITY handshakes.
			if sni != "" {
				line += ", sni=" + surgeValue(sni)
			}
			line += ", reality-public-key=" + surgeValue(st.TLS.Reality.PublicKey)
			if sid := firstOf(st.TLS.Reality.ShortID); sid != "" {
				line += ", reality-short-id=" + surgeValue(sid)
			}
			line += surgeALPN(st.TLS.ALPN)
		} else if nodeTLSOn(st) {
			line += fmt.Sprintf(", tls=true, sni=%s", surgeValue(sni))
			line += surgeALPN(st.TLS.ALPN)
			if st.TLS.ClientInsecure() {
				line += ", skip-cert-verify=true"
			}
		}
		if tr == "ws" || tr == "httpupgrade" {
			line += ", ws=true"
			if tr == "httpupgrade" {
				line += ", ws-http-upgrade=true"
			}
			if st.Transport.Path != "" {
				line += ", ws-path=" + surgeValue(st.Transport.Path)
			}
			if hdrs := surgeWSHeaders(st.Transport); hdrs != "" {
				line += ", ws-headers=" + surgeValue(hdrs)
			}
		}
		return line
	default: // anything Surge can't express
		return ""
	}
}

// NodeFormatItem 代表单节点的三种配置格式，供前端逐行卡片渲染
type NodeFormatItem struct {
	Tag    string            `json:"tag"`
	Type   string            `json:"type"`
	Name   string            `json:"name"`
	Server string            `json:"server"`
	Port   int               `json:"port"`
	Params map[string]string `json:"params"`
	URI    string            `json:"uri"`
	Clash  string            `json:"clash"`
	Surge  string            `json:"surge"`
}

func buildNodeFormatItems(nodes []node) []NodeFormatItem {
	seenName := map[string]int{}
	seenClash := map[string]int{}

	var items []NodeFormatItem
	for _, n := range nodes {
		name := uniqueName(seenName, n.name)
		link, _ := singbox.BuildShareLink(n.clientNode())

		var clashStr string
		if p := clashProxy(n, seenClash); p != nil {
			d, _ := marshalClashProxiesYAML([]map[string]any{p})
			clashStr = string(d)
		}

		surgeStr := surgeProxy(n, name)

		items = append(items, NodeFormatItem{
			Tag:    n.tag,
			Type:   n.typ,
			Name:   name,
			Server: n.server,
			Port:   n.port,
			Params: nodeParams(n),
			URI:    link,
			Clash:  clashStr,
			Surge:  surgeStr,
		})
	}
	return items
}

// clashProxiesYAML 将 nodes 转为 Clash YAML proxies 块字符串，供管理员弹窗展示。
func clashProxiesYAML(nodes []node) string {
	proxies, _ := clashProxies(nodes)
	data, _ := marshalClashProxiesYAML(proxies)
	return string(data)
}

// surgeLines 将 nodes 转为 Surge [Proxy] 区块字符串，供管理员弹窗展示。
func surgeLines(nodes []node) string {
	lines, _, skipped := surgeProxies(nodes)
	var b strings.Builder
	b.WriteString("[Proxy]\n")
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	if len(skipped) > 0 {
		b.WriteString("# Surge 不支持以下节点，已跳过: " + strings.Join(skipped, ", ") + "\n")
	}
	return b.String()
}
