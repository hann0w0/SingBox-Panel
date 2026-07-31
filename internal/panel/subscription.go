package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

// node bundles a server+inbound for a subscriber.
type node struct {
	tag      string
	name     string
	server   string
	port     int
	typ      string
	settings singbox.InboundSettings
}

// userActive reports whether an account may still pull config from the panel.
// Expiry is checked directly rather than trusting Enabled, which the reconciler
// only flips on its next tick.
func userActive(u *model.User) bool {
	return u.Enabled && !u.Expired(time.Now())
}

// handleSubscription serves a user's subscription in the requested format.
func (a *App) handleSubscription(c *gin.Context) {
	token := c.Param("token")
	var user model.User
	if err := a.db.Where("sub_token = ?", token).First(&user).Error; err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	// A disabled or expired account must stop receiving nodes. Report the expiry
	// so the client can show why, but hand out nothing.
	if !userActive(&user) {
		setSubscriptionUserinfo(c, &user)
		c.String(http.StatusForbidden, "account disabled or expired")
		return
	}

	nodes := a.gatherNodes(&user)

	// Usage header consumed by many clients to display quota.
	setSubscriptionUserinfo(c, &user)

	switch subFormat(c) {
	case "sing-box", "singbox":
		a.writeSingbox(c, nodes)
	case "clash", "clash-meta", "clashmeta":
		a.writeClash(c, nodes)
	case "surge":
		a.writeSurge(c, nodes)
	default:
		a.writeLinks(c, nodes)
	}
}

func subFormat(c *gin.Context) string {
	if f := strings.ToLower(c.Query("target")); f != "" {
		return f
	}
	if f := strings.ToLower(c.Query("flag")); f != "" {
		return f
	}
	ua := strings.ToLower(c.GetHeader("User-Agent"))
	switch {
	case strings.Contains(ua, "sing-box"), strings.Contains(ua, "singbox"):
		return "sing-box"
	case strings.Contains(ua, "clash"), strings.Contains(ua, "mihomo"), strings.Contains(ua, "stash"):
		return "clash"
	case strings.Contains(ua, "surge"):
		return "surge"
	default:
		return "links"
	}
}

func setSubscriptionUserinfo(c *gin.Context, u *model.User) {
	// Only expiry is reported: single-credential inbounds make per-user byte
	// accounting impossible, and emitting upload/download/total=0 would show
	// clients a bogus "0 B of 0 B used".
	if u.ExpireAt != nil {
		c.Header("Subscription-Userinfo", fmt.Sprintf("expire=%d", u.ExpireAt.Unix()))
	}
	c.Header("Profile-Update-Interval", "12")
}

// gatherNodes returns the subscribable nodes for a user (its group's servers
// with enabled inbounds).
func (a *App) gatherNodes(user *model.User) []node {
	if len(user.ServerIDs) == 0 {
		return nil
	}
	var servers []model.Server
	a.db.Where("id IN ?", user.ServerIDs).Order("id").Find(&servers)
	applyServerOrder(a.db, servers)

	var out []node
	for i := range servers {
		srv := &servers[i]
		host := srv.Address
		if host == "" {
			host = srv.PublicIP
		}
		host, err := normalizeNodeAddress(host)
		if err != nil || host == "" {
			continue
		}
		var inbounds []model.Inbound
		a.db.Where("server_id = ? AND enabled = ?", srv.ID, true).Find(&inbounds)
		for _, ib := range inbounds {
			// Respect per-inbound access: a protocol the user isn't granted must
			// not appear in their subscription.
			if !user.HasInbound(srv.ID, ib.ID) {
				continue
			}
			var st singbox.InboundSettings
			if len(ib.Settings) > 0 {
				_ = json.Unmarshal(ib.Settings, &st)
			}
			// Match the generated server config, which is always single-credential.
			st.SingleUser = true
			out = append(out, node{
				tag:      ib.Tag,
				name:     fmt.Sprintf("%s-%s", srv.Name, ib.Tag),
				server:   host,
				port:     ib.ListenPort,
				typ:      string(ib.Type),
				settings: st,
			})
		}
	}
	return out
}

func (n node) clientNode() singbox.ClientNode {
	return singbox.ClientNode{
		Name:       n.name,
		Server:     n.server,
		ServerPort: n.port,
		Type:       n.typ,
		Settings:   n.settings,
		User:       n.settings.SingleUserIdentity(),
	}
}

// writeLinks emits one share URI per line (Shadowrocket and friends accept
// plain text; base64 only makes the output unreadable).
func (a *App) writeLinks(c *gin.Context, nodes []node) {
	var lines []string
	for _, n := range nodes {
		link, err := singbox.BuildShareLink(n.clientNode())
		if err == nil {
			lines = append(lines, link)
		}
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(strings.Join(lines, "\n")+"\n"))
}

func (a *App) writeSingbox(c *gin.Context, nodes []node) {
	var outbounds []json.RawMessage
	var tags []string
	seen := map[string]int{}
	for _, n := range nodes {
		cn := n.clientNode()
		cn.Name = uniqueName(seen, cn.Name)
		ob, err := singbox.BuildClientOutbound(cn)
		if err != nil {
			continue
		}
		outbounds = append(outbounds, ob)
		tags = append(tags, cn.Name)
	}

	// With no renderable node, emitting the selector/urltest groups would give
	// sing-box an empty (null) member list and the whole profile fails to load.
	all := []any{}
	final := "direct"
	if len(tags) > 0 {
		all = append(all,
			map[string]any{"type": "selector", "tag": "proxy", "outbounds": append([]string{"auto"}, tags...)},
			map[string]any{"type": "urltest", "tag": "auto", "outbounds": tags,
				"url": "https://www.gstatic.com/generate_204", "interval": "5m"},
		)
		final = "proxy"
	}
	for _, ob := range outbounds {
		all = append(all, ob)
	}
	all = append(all, map[string]any{"type": "direct", "tag": "direct"})

	cfg := map[string]any{
		"log": map[string]any{"level": "info"},
		"inbounds": []map[string]any{
			{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080},
		},
		"outbounds": all,
		"route": map[string]any{
			"final":                 final,
			"auto_detect_interface": true,
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	c.Data(http.StatusOK, "application/json; charset=utf-8", data)
}

func (a *App) writeClash(c *gin.Context, nodes []node) {
	proxies, _ := clashProxies(nodes)
	// Only the proxies array: rules/groups belong to the user's own config.
	doc := map[string]any{"proxies": proxies}
	data, _ := yaml.Marshal(doc)
	c.Data(http.StatusOK, "text/yaml; charset=utf-8", data)
}

func clashProxies(nodes []node) (proxies []map[string]any, names []string) {
	seen := map[string]int{}
	for _, n := range nodes {
		p := clashProxy(n, seen)
		if p == nil {
			continue
		}
		proxies = append(proxies, p)
		names = append(names, p["name"].(string))
	}
	return proxies, names
}

func clashProxy(n node, seen map[string]int) map[string]any {
	name := uniqueName(seen, n.name)
	st := n.settings
	u := st.SingleUserIdentity()
	tlsOn := st.TLS.Enabled || st.TLS.SelfSigned || st.TLS.ACMEDomain != "" || st.TLS.Reality.Enabled
	sni := st.TLS.ServerName
	if sni == "" {
		if st.TLS.Reality.Enabled {
			sni = st.TLS.Reality.HandshakeServer
		} else if st.TLS.ACMEDomain != "" {
			sni = st.TLS.ACMEDomain
		} else {
			sni = n.server
		}
	}
	net := "tcp"
	if st.Transport.Type == "ws" {
		net = "ws"
	}
	wsOpts := func() map[string]any {
		o := map[string]any{}
		if st.Transport.Path != "" {
			o["path"] = st.Transport.Path
		}
		if h := st.Transport.Headers["Host"]; h != "" {
			o["headers"] = map[string]any{"Host": h}
		}
		return o
	}

	base := map[string]any{"name": name, "server": n.server, "port": n.port}
	switch n.typ {
	case "vless":
		base["type"] = "vless"
		base["uuid"] = u.UUID
		base["network"] = net
		base["udp"] = true
		if st.Flow != "" {
			base["flow"] = st.Flow
		}
		if tlsOn {
			base["tls"] = true
			base["servername"] = sni
			base["client-fingerprint"] = "chrome"
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
			base["ws-opts"] = wsOpts()
		}
	case "vmess":
		base["type"] = "vmess"
		base["uuid"] = u.UUID
		base["alterId"] = st.VMessAlterID
		base["cipher"] = st.VMessSecurityValue()
		base["network"] = net
		base["udp"] = true
		if tlsOn {
			base["tls"] = true
			base["servername"] = sni
			base["client-fingerprint"] = "chrome"
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
			base["ws-opts"] = wsOpts()
		}
	case "trojan":
		base["type"] = "trojan"
		base["password"] = u.Password
		base["sni"] = sni
		base["udp"] = true
		if len(st.TLS.ALPN) > 0 {
			base["alpn"] = st.TLS.ALPN
		}
		if st.TLS.ClientInsecure() {
			base["skip-cert-verify"] = true
		}
		if net == "ws" {
			base["network"] = "ws"
			base["ws-opts"] = wsOpts()
		}
	case "shadowsocks":
		base["type"] = "ss"
		base["cipher"] = st.Method
		base["password"] = singbox.SSClientPassword(st, u.Password)
		base["udp"] = true
	case "socks":
		base["type"] = "socks5"
		if st.Username != "" {
			base["username"] = st.Username
		}
		if st.Password != "" {
			base["password"] = st.Password
		}
		base["udp"] = true
	case "hysteria2":
		base["type"] = "hysteria2"
		base["password"] = u.Password
		base["sni"] = sni
		if st.ObfsPassword != "" {
			base["obfs"] = "salamander"
			base["obfs-password"] = st.ObfsPassword
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
		if st.SnellVersion == 5 && st.SnellObfsMode != "" && st.SnellObfsMode != "none" {
			base["obfs-opts"] = map[string]any{"mode": st.SnellObfsMode}
		}
		base["udp"] = true
	case "anytls":
		base["type"] = "anytls"
		base["password"] = u.Password
		base["sni"] = sni
		base["udp"] = true
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
		base["sni"] = sni
		base["udp-relay-mode"] = "native"
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
	seen[name]++
	if n := seen[name]; n > 1 {
		return fmt.Sprintf("%s-%d", name, n)
	}
	return name
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
type NodeDetail struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Server string            `json:"server"`
	Port   int               `json:"port"`
	Link   string            `json:"link"`
	Params map[string]string `json:"params"`
}

func (a *App) userNodeDetails(user *model.User) []NodeDetail {
	nodes := a.gatherNodes(user)
	out := make([]NodeDetail, 0, len(nodes))
	for _, n := range nodes {
		link, _ := singbox.BuildShareLink(n.clientNode())
		out = append(out, NodeDetail{
			Name:   n.name,
			Type:   n.typ,
			Server: n.server,
			Port:   n.port,
			Link:   link,
			Params: nodeParams(n),
		})
	}
	return out
}

func nodeParams(n node) map[string]string {
	st := n.settings
	u := st.SingleUserIdentity()
	p := map[string]string{"服务器": n.server, "端口": strconv.Itoa(n.port), "协议": n.typ}
	net := "tcp"
	if st.Transport.Type == "ws" {
		net = "ws"
	}
	sni := sniOf(st, n.server)
	if nodeTLSOn(st) && st.TLS.ClientInsecure() {
		p["跳过证书验证"] = "true"
	}
	switch n.typ {
	case "vless":
		p["UUID"] = u.UUID
		if st.Flow != "" {
			p["Flow"] = st.Flow
		}
		p["传输"] = net
		switch {
		case st.TLS.Reality.Enabled:
			p["安全"] = "reality"
			p["SNI"] = sni
			p["PublicKey"] = st.TLS.Reality.PublicKey
			p["ShortID"] = firstOf(st.TLS.Reality.ShortID)
			p["指纹"] = "chrome"
		case nodeTLSOn(st):
			p["安全"] = "tls"
			p["SNI"] = sni
		default:
			p["安全"] = "none"
		}
		if net == "ws" {
			p["Path"] = st.Transport.Path
			if h := st.Transport.Headers["Host"]; h != "" {
				p["Host"] = h
			}
		}
	case "vmess":
		p["UUID"] = u.UUID
		p["加密"] = st.VMessSecurityValue()
		p["AlterId"] = strconv.Itoa(st.VMessAlterID)
		p["传输"] = net
		if nodeTLSOn(st) {
			p["安全"] = "tls"
			p["SNI"] = sni
		}
		if len(st.TLS.ALPN) > 0 {
			p["ALPN"] = strings.Join(st.TLS.ALPN, ",")
		}
		if net == "ws" {
			p["Path"] = st.Transport.Path
			if h := st.Transport.Headers["Host"]; h != "" {
				p["Host"] = h
			}
		}
	case "trojan":
		p["密码"] = u.Password
		p["SNI"] = sni
		p["传输"] = net
		if len(st.TLS.ALPN) > 0 {
			p["ALPN"] = strings.Join(st.TLS.ALPN, ",")
		}
	case "shadowsocks":
		p["加密方式"] = st.Method
		p["密码"] = singbox.SSClientPassword(st, u.Password)
	case "socks":
		if st.Username != "" {
			p["用户名"] = st.Username
		}
		if st.Password != "" {
			p["密码"] = st.Password
		}
		if st.Username == "" && st.Password == "" {
			p["认证"] = "无需认证"
		}
	case "hysteria2":
		p["密码"] = u.Password
		p["SNI"] = sni
		if st.ObfsPassword != "" {
			p["Obfs"] = "salamander"
			p["Obfs密码"] = st.ObfsPassword
		}
	case "tuic":
		p["UUID"] = u.UUID
		p["密码"] = u.Password
		cc := st.CongestionControl
		if cc == "" {
			cc = "cubic"
		}
		p["拥塞控制"] = cc
		p["认证超时"] = st.TUICAuthTimeoutValue()
		p["0-RTT"] = strconv.FormatBool(st.ZeroRTTHandshake)
		p["心跳间隔"] = st.TUICHeartbeatValue()
		p["SNI"] = sni
	case "anytls":
		p["密码"] = u.Password
		p["SNI"] = sni
	case "naive":
		p["用户名"] = u.Name
		p["密码"] = u.Password
		p["SNI"] = sni
	case "hysteria":
		p["认证"] = u.Password
		p["SNI"] = sni
		if st.UpMbps > 0 {
			p["上行"] = strconv.Itoa(st.UpMbps) + " Mbps"
		}
		if st.DownMbps > 0 {
			p["下行"] = strconv.Itoa(st.DownMbps) + " Mbps"
		}
		if st.ObfsPassword != "" {
			p["Obfs"] = st.ObfsPassword
		}
	case "shadowtls":
		ver := st.ShadowTLSVersion
		if ver == 0 {
			ver = 3
		}
		p["版本"] = "v" + strconv.Itoa(ver)
		if ver == 2 {
			p["密码"] = st.ShadowTLSPassword
		} else {
			p["密码"] = u.Password
		}
		p["握手域名"] = st.ShadowTLSHandshake
		if st.ShadowTLSHandshakePort > 0 {
			p["握手端口"] = strconv.Itoa(st.ShadowTLSHandshakePort)
		}
	case "snell":
		if st.SnellVersion > 0 {
			p["版本"] = "v" + strconv.Itoa(st.SnellVersion)
		}
		// Single-credential Snell authenticates with the PSK itself.
		p["PSK"] = singbox.SnellClientPSK(st, u.Password)
		if st.SnellVersion == 5 && st.SnellObfsMode != "" && st.SnellObfsMode != "none" {
			p["混淆"] = st.SnellObfsMode
		}
		if st.SnellVersion == 6 && st.SnellMode != "" {
			p["模式"] = st.SnellMode
		}
	}
	return p
}

// writeSurge emits just the [Proxy] section; the user pastes/includes it into
// their own Surge profile.
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

func surgeProxies(nodes []node) (lines, names, skipped []string) {
	seen := map[string]int{}
	for _, n := range nodes {
		name := uniqueName(seen, n.name)
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

func surgeProxy(n node, name string) string {
	st := n.settings
	u := st.SingleUserIdentity()
	sni := sniOf(st, n.server)
	switch n.typ {
	case "shadowsocks":
		pass := singbox.SSClientPassword(st, u.Password)
		return fmt.Sprintf("%s = ss, %s, %d, encrypt-method=%s, password=%s, udp-relay=true",
			name, n.server, n.port, st.Method, pass)
	case "socks":
		line := fmt.Sprintf("%s = socks5, %s, %d", name, n.server, n.port)
		if st.Username != "" {
			line += ", username=" + st.Username
		}
		if st.Password != "" {
			line += ", password=" + st.Password
		}
		return line + ", udp-relay=true"
	case "trojan":
		return fmt.Sprintf("%s = trojan, %s, %d, password=%s, sni=%s, skip-cert-verify=%t",
			name, n.server, n.port, u.Password, sni, st.TLS.ClientInsecure())
	case "anytls":
		// Surge added AnyTLS support in iOS 5.17.0 / macOS 6.4.3.
		return fmt.Sprintf("%s = anytls, %s, %d, password=%s, sni=%s, skip-cert-verify=%t",
			name, n.server, n.port, u.Password, sni, st.TLS.ClientInsecure())
	case "vmess":
		if st.TLS.Reality.Enabled {
			return ""
		}
		line := fmt.Sprintf("%s = vmess, %s, %d, username=%s, vmess-aead=%t", name, n.server, n.port, u.UUID, st.VMessAlterID == 0)
		if nodeTLSOn(st) {
			line += fmt.Sprintf(", tls=true, sni=%s", sni)
			if st.TLS.ClientInsecure() {
				line += ", skip-cert-verify=true"
			}
		}
		if st.Transport.Type == "ws" {
			line += ", ws=true, ws-path=" + st.Transport.Path
			if h := st.Transport.Headers["Host"]; h != "" {
				line += ", ws-headers=Host:" + h
			}
		}
		return line
	case "hysteria2":
		line := fmt.Sprintf("%s = hysteria2, %s, %d, password=%s, sni=%s", name, n.server, n.port, u.Password, sni)
		if st.DownMbps > 0 {
			line += fmt.Sprintf(", download-bandwidth=%d", st.DownMbps)
		}
		if st.TLS.ClientInsecure() {
			line += ", skip-cert-verify=true"
		}
		return line
	case "snell":
		// Surge's native protocol: name = snell, host, port, psk=…, version=…
		line := fmt.Sprintf("%s = snell, %s, %d, psk=%s", name, n.server, n.port,
			singbox.SnellClientPSK(st, u.Password))
		if st.SnellVersion > 0 {
			line += fmt.Sprintf(", version=%d", st.SnellVersion)
		}
		if st.SnellVersion == 5 && st.SnellObfsMode != "" && st.SnellObfsMode != "none" {
			line += ", obfs=" + st.SnellObfsMode
		}
		return line + ", udp-relay=true"
	case "tuic":
		alpn := firstOf(st.TLS.ALPN)
		if alpn == "" {
			alpn = "h3"
		}
		line := fmt.Sprintf("%s = tuic-v5, %s, %d, uuid=%s, password=%s, sni=%s, alpn=%s",
			name, n.server, n.port, u.UUID, u.Password, sni, alpn)
		if st.TLS.ClientInsecure() {
			line += ", skip-cert-verify=true"
		}
		return line
	default: // vless and anything Surge can't express
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
			doc := map[string]any{"proxies": []any{p}}
			d, _ := yaml.Marshal(doc)
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
	doc := map[string]any{"proxies": proxies}
	data, _ := yaml.Marshal(doc)
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
