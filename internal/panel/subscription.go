package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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
	region   string // ISO-ish region code (US/HK/JP…) for the dashboard map; may be empty
	settings singbox.InboundSettings
	user     singbox.ProxyUser
	udp      *bool // nil keeps the historical default: UDP enabled
}

func (n node) udpEnabled() bool {
	return n.udp == nil || *n.udp
}

// userActive reports whether an account may still pull config from the panel.
// Expiry is checked directly rather than trusting Enabled, which the reconciler
// only flips on its next tick.
func userActive(u *model.User) bool {
	return u.Enabled && !u.Expired(time.Now())
}

// handleSubscription serves a user's subscription in the requested format.
func (a *App) handleSubscription(c *gin.Context) {
	// The subscription URL is a bearer credential and the response carries the
	// node secrets in plaintext — never let any intermediate cache store it.
	c.Header("Cache-Control", "no-store")
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
	// Only expiry is reported until sing-box exposes exact authenticated-user
	// byte counters. Emitting upload/download/total=0 would make clients display
	// a bogus quota, so unknown traffic fields are deliberately omitted.
	if u.ExpireAt != nil {
		c.Header("Subscription-Userinfo", fmt.Sprintf("expire=%d", u.ExpireAt.Unix()))
	}
	c.Header("Profile-Update-Interval", "12")
}

// gatherNodes returns the subscribable nodes for a user: the panel-managed
// inbounds the user is granted, plus any enabled custom (external) nodes
// scoped to them or to everyone.
func (a *App) gatherNodes(user *model.User) []node {
	var out []node
	if len(user.ServerIDs) > 0 {
		var servers []model.Server
		a.db.Where("id IN ?", user.ServerIDs).Order("id").Find(&servers)
		applyServerOrder(a.db, servers)

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
				var identity singbox.ProxyUser
				if st.UseMultiUser(string(ib.Type)) {
					st.SingleUser = false
					identity = proxyIdentity(user, ib.ID)
				} else {
					st.MultiUser = false
					st.SingleUser = true
					identity = st.SingleUserIdentity()
				}
				out = append(out, node{
					tag:      ib.Tag,
					name:     formatNodeDisplayName(srv.Name, ib.Tag, string(ib.Type)),
					server:   host,
					port:     ib.ListenPort,
					typ:      string(ib.Type),
					region:   srv.Region,
					settings: st,
					user:     identity,
				})
			}
		}
	}

	// Custom external nodes carry their own credentials (share link or the
	// structured protocol fields), so no per-user derivation happens here.
	var customs []model.CustomNode
	a.db.Where("enabled = ?", true).Order("sort_order, id").Find(&customs)
	for i := range customs {
		c := &customs[i]
		if !c.HasUser(user.ID) {
			continue
		}
		if n, ok := a.customNodeToNode(c); ok {
			out = append(out, n)
		}
	}
	return out
}

// customNodeToNode converts a hand-added external node into a subscription
// node. Link-based nodes are parsed; structured nodes (snell & friends without
// a widely-supported share-link scheme) render from their fields directly.
func (a *App) customNodeToNode(c *model.CustomNode) (node, bool) {
	if strings.TrimSpace(c.Link) != "" {
		cn, err := singbox.ParseShareLink(c.Link)
		if err != nil {
			return node{}, false
		}
		if c.Name != "" {
			cn.Name = c.Name
		}
		return node{
			name:     cn.Name,
			server:   cn.Server,
			port:     cn.ServerPort,
			typ:      cn.Type,
			region:   regionFromName(cn.Name),
			settings: cn.Settings,
			user:     cn.User,
		}, true
	}

	var p struct {
		UUID                 string            `json:"uuid"`
		Password             string            `json:"password"`
		Username             string            `json:"username"`
		Method               string            `json:"method"`
		SSPlugin             string            `json:"ss_plugin"`
		Plugin               string            `json:"plugin"`
		SSPluginOpts         map[string]any    `json:"ss_plugin_opts"`
		Flow                 string            `json:"flow"`
		PacketEncoding       string            `json:"packet_encoding"`
		VMessSecurity        string            `json:"security"`
		VMessAlterID         int               `json:"alter_id"`
		TLS                  string            `json:"tls"` // none | tls | reality
		SNI                  string            `json:"sni"`
		PBK                  string            `json:"pbk"`
		SID                  string            `json:"sid"`
		Fingerprint          string            `json:"fingerprint"`
		Insecure             bool              `json:"insecure"`
		SkipCertVerify       bool              `json:"skip_cert_verify"`
		Transport            string            `json:"transport"` // tcp | ws | httpupgrade
		Network              string            `json:"network"`
		Path                 string            `json:"path"`
		Host                 string            `json:"host"`
		Headers              map[string]string `json:"headers"`
		MaxEarlyData         int               `json:"max_early_data"`
		EarlyDataHeaderName  string            `json:"early_data_header_name"`
		ALPN                 string            `json:"alpn"`
		Congestion           string            `json:"congestion_control"`
		CongestionController string            `json:"congestion_controller"`
		UDPRelayMode         string            `json:"udp_relay_mode"`
		UDP                  *bool             `json:"udp"`
		UDPOverStream        bool              `json:"udp_over_stream"`
		Obfs                 string            `json:"obfs"`
		ObfsType             string            `json:"obfs_type"`
		ObfsPassword         string            `json:"obfs_password"`
		UpMbps               int               `json:"up_mbps"`
		DownMbps             int               `json:"down_mbps"`
		ZeroRTTHandshake     bool              `json:"zero_rtt_handshake"`
		Heartbeat            string            `json:"heartbeat"`
		PSK                  string            `json:"psk"`
		Version              int               `json:"version"`
		ObfsMode             string            `json:"obfs_mode"`
		Mode                 string            `json:"mode"`
	}
	if len(c.Params) > 0 {
		if err := json.Unmarshal(c.Params, &p); err != nil {
			return node{}, false
		}
	}
	protocol := strings.ToLower(strings.TrimSpace(c.Protocol))
	switch protocol {
	case "ss":
		protocol = "shadowsocks"
	case "socks5":
		protocol = "socks"
	case "hy2":
		protocol = "hysteria2"
	case "hy1":
		protocol = "hysteria"
	}
	name := c.Name
	if name == "" {
		name = protocol + " " + c.Address
	}
	st := singbox.InboundSettings{SingleUser: true}
	u := singbox.ProxyUser{Name: "user"}

	buildTLS := func() {
		mode := strings.ToLower(strings.TrimSpace(p.TLS))
		if mode == "" && p.PBK != "" {
			mode = "reality"
		}
		if mode == "" {
			switch protocol {
			case "trojan", "anytls", "hysteria2", "hysteria", "tuic":
				mode = "tls"
			}
		}
		st.TLS.ServerName = strings.TrimSpace(p.SNI)
		st.TLS.Fingerprint = strings.TrimSpace(p.Fingerprint)
		st.TLS.Insecure = p.Insecure || p.SkipCertVerify
		for _, value := range strings.Split(p.ALPN, ",") {
			if value = strings.TrimSpace(value); value != "" {
				st.TLS.ALPN = append(st.TLS.ALPN, value)
			}
		}
		switch mode {
		case "reality":
			st.TLS.Enabled = true
			st.TLS.Reality = singbox.RealitySettings{Enabled: true, PublicKey: p.PBK}
			for _, value := range strings.Split(p.SID, ",") {
				if value = strings.TrimSpace(value); value != "" {
					st.TLS.Reality.ShortID = append(st.TLS.Reality.ShortID, value)
				}
			}
		case "tls":
			st.TLS.Enabled = true
		}
	}
	buildTransport := func() {
		transport := strings.ToLower(strings.TrimSpace(p.Transport))
		if transport == "" {
			transport = strings.ToLower(strings.TrimSpace(p.Network))
		}
		switch transport {
		case "http-upgrade", "http_upgrade":
			transport = "httpupgrade"
		case "websocket":
			transport = "ws"
		}
		if transport == "ws" || transport == "httpupgrade" {
			st.Transport.Type = transport
			st.Transport.Path = p.Path
			st.Transport.MaxEarlyData = p.MaxEarlyData
			st.Transport.EarlyDataHeader = p.EarlyDataHeaderName
			if len(p.Headers) > 0 {
				st.Transport.Headers = make(map[string]string, len(p.Headers)+1)
				for key, value := range p.Headers {
					st.Transport.Headers[key] = value
					if strings.EqualFold(key, "Host") {
						st.Transport.Headers["Host"] = value
					}
				}
			}
			if host := strings.TrimSpace(p.Host); host != "" {
				if st.Transport.Headers == nil {
					st.Transport.Headers = map[string]string{}
				}
				st.Transport.Headers["Host"] = host
			}
		}
	}

	switch protocol {
	case "vless":
		u.UUID = p.UUID
		st.Flow = p.Flow
		st.PacketEncoding = p.PacketEncoding
		buildTLS()
		buildTransport()
	case "vmess":
		u.UUID = p.UUID
		st.VMessSecurity = p.VMessSecurity
		st.VMessAlterID = p.VMessAlterID
		buildTLS()
		buildTransport()
	case "trojan":
		u.Password = p.Password
		st.PacketEncoding = p.PacketEncoding
		buildTLS()
		buildTransport()
	case "anytls":
		u.Password = p.Password
		st.AnyTLSUDPOverStream = p.UDPOverStream
		buildTLS()
	case "shadowsocks":
		st.Method = p.Method
		if st.Method == "" {
			st.Method = "2022-blake3-aes-128-gcm"
		}
		st.SSServerPSK = p.Password
		st.SSPlugin = p.SSPlugin
		if st.SSPlugin == "" {
			st.SSPlugin = p.Plugin
		}
		st.SSPlugin = mergeSSPluginOptions(st.SSPlugin, p.SSPluginOpts)
	case "tuic":
		u.UUID = p.UUID
		u.Password = p.Password
		st.CongestionControl = p.Congestion
		if st.CongestionControl == "" {
			st.CongestionControl = p.CongestionController
		}
		st.TUICUDPRelayMode = p.UDPRelayMode
		if st.TUICUDPRelayMode != "" {
			st.TUICUDPRelayMode = st.TUICRelayModeValue()
		}
		st.ZeroRTTHandshake = p.ZeroRTTHandshake
		st.Heartbeat = strings.TrimSpace(p.Heartbeat)
		buildTLS()
	case "hysteria2", "hysteria":
		u.Password = p.Password
		st.ObfsType = p.ObfsType
		if st.ObfsType == "" && protocol == "hysteria2" {
			st.ObfsType = p.Obfs
		}
		st.ObfsPassword = p.ObfsPassword
		if st.ObfsPassword == "" && protocol == "hysteria" {
			st.ObfsPassword = p.Obfs
		}
		st.UpMbps = p.UpMbps
		st.DownMbps = p.DownMbps
		buildTLS()
	case "snell":
		if p.Version == 0 {
			p.Version = 5
		}
		st.SnellVersion = p.Version
		st.SnellPSK = p.PSK
		st.SnellObfsMode = p.ObfsMode
		if st.SnellObfsMode == "" {
			st.SnellObfsMode = p.Obfs
		}
		st.SnellMode = p.Mode
	case "socks", "mixed":
		st.Username = p.Username
		st.Password = p.Password
	default:
		return node{}, false
	}
	return node{
		name:     name,
		server:   c.Address,
		port:     c.Port,
		typ:      protocol,
		region:   regionFromName(name),
		settings: st,
		user:     u,
		udp:      p.UDP,
	}, true
}

// mergeSSPluginOptions keeps Clash's split plugin/plugin-opts representation
// in the SIP002 string used by share links and the internal node model.
func mergeSSPluginOptions(plugin string, opts map[string]any) string {
	plugin = strings.TrimSpace(plugin)
	if plugin == "" || len(opts) == 0 {
		return plugin
	}
	keys := make([]string, 0, len(opts))
	for key := range opts {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := []string{plugin}
	for _, key := range keys {
		value := strings.TrimSpace(fmt.Sprint(opts[key]))
		if value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, ";")
}

func splitSSPluginOptions(plugin string) (string, map[string]any) {
	parts := strings.Split(plugin, ";")
	name := strings.TrimSpace(parts[0])
	opts := map[string]any{}
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(value) {
		case "true":
			opts[key] = true
		case "false":
			opts[key] = false
		default:
			opts[key] = value
		}
	}
	return name, opts
}

// regionFromName extracts a two-letter region code from the first flag emoji in
// a node name (e.g. "HK AWS HKG" with a flag → "HK"). Flag emoji are two
// Regional Indicator Symbols (U+1F1E6..U+1F1FF), each mapping to a letter A-Z.
// Custom nodes have no server record, so their name's flag is the only region
// hint available.
func regionFromName(name string) string {
	const base = 0x1F1E6
	runes := []rune(name)
	for i := 0; i+1 < len(runes); i++ {
		a, b := runes[i], runes[i+1]
		if a >= base && a <= base+25 && b >= base && b <= base+25 {
			return string(rune('A'+(a-base))) + string(rune('A'+(b-base)))
		}
	}
	return ""
}

func formatNodeDisplayName(serverName, tag, typ string) string {
	prefix := typ + "-"
	if strings.HasPrefix(tag, prefix) && len(tag) == len(prefix)+6 {
		return fmt.Sprintf("%s - %s", serverName, formatProtocolDisplayName(typ))
	}
	return fmt.Sprintf("%s - %s", serverName, tag)
}

func formatProtocolDisplayName(typ string) string {
	switch typ {
	case "shadowsocks":
		return "Shadowsocks"
	case "socks":
		return "SOCKS5"
	case "vless":
		return "VLESS"
	case "vmess":
		return "VMess"
	case "trojan":
		return "Trojan"
	case "hysteria2":
		return "Hysteria2"
	case "hysteria":
		return "Hysteria"
	case "mixed":
		return "Mixed"
	case "tuic":
		return "TUIC"
	case "anytls":
		return "AnyTLS"
	case "snell":
		return "Snell"
	default:
		return strings.ToUpper(typ)
	}
}

func (n node) getUserIdentity() singbox.ProxyUser {
	identity := n.user
	if identity.Name == "" && identity.Username == "" && identity.UUID == "" && identity.Password == "" {
		identity = n.settings.SingleUserIdentity()
	}
	return identity
}

func (n node) clientNode() singbox.ClientNode {
	return singbox.ClientNode{
		Name:       n.name,
		Server:     n.server,
		ServerPort: n.port,
		Type:       n.typ,
		Settings:   n.settings,
		User:       n.getUserIdentity(),
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
	u := n.getUserIdentity()
	tlsOn := nodeTLSOn(st)
	sni := sniOf(st, n.server)
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
		if h := st.Transport.Headers["Host"]; h != "" {
			o["headers"] = map[string]any{"Host": h}
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

	base := map[string]any{"name": name, "server": n.server, "port": n.port}
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
	case "mixed": // Clash has no mixed type; clients dial it as SOCKS5
		base["type"] = "socks5"
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
	Region string            `json:"region"` // ISO-ish code (US/HK/JP…) for the dashboard map
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
			Region: n.region,
			Link:   link,
			Params: nodeParams(n),
		})
	}
	return out
}

func nodeParams(n node) map[string]string {
	st := n.settings
	u := n.getUserIdentity()
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
	st := n.settings
	u := n.getUserIdentity()
	sni := sniOf(st, n.server)
	switch n.typ {
	case "shadowsocks":
		pass := singbox.SSClientPassword(st, u.Password)
		return fmt.Sprintf("%s = ss, %s, %d, encrypt-method=%s, password=%s, udp-relay=%t",
			name, n.server, n.port, st.Method, pass, n.udpEnabled())
	case "socks":
		line := fmt.Sprintf("%s = socks5, %s, %d", name, n.server, n.port)
		if st.Username != "" {
			line += ", username=" + st.Username
		}
		if st.Password != "" {
			line += ", password=" + st.Password
		}
		return fmt.Sprintf("%s, udp-relay=%t", line, n.udpEnabled())
	case "trojan":
		line := fmt.Sprintf("%s = trojan, %s, %d, password=%s, tls=true, sni=%s, skip-cert-verify=%t",
			name, n.server, n.port, u.Password, sni, st.TLS.ClientInsecure())
		line += surgeALPN(st.TLS.ALPN)
		return fmt.Sprintf("%s, udp-relay=%t", line, n.udpEnabled())
	case "anytls":
		// Surge added AnyTLS support in iOS 5.17.0 / macOS 6.4.3; the shared TLS
		// parameters (sni, skip-cert-verify, alpn) apply from iOS 5.20.0+.
		line := fmt.Sprintf("%s = anytls, %s, %d, password=%s, sni=%s, skip-cert-verify=%t",
			name, n.server, n.port, u.Password, sni, st.TLS.ClientInsecure())
		line += surgeALPN(st.TLS.ALPN)
		return fmt.Sprintf("%s, udp-relay=%t", line, n.udpEnabled())
	case "vmess":
		if st.TLS.Reality.Enabled {
			return ""
		}
		line := fmt.Sprintf("%s = vmess, %s, %d, username=%s, vmess-aead=%t", name, n.server, n.port, u.UUID, st.VMessAlterID == 0)
		if nodeTLSOn(st) {
			line += fmt.Sprintf(", tls=true, sni=%s", sni)
			line += surgeALPN(st.TLS.ALPN)
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
		return fmt.Sprintf("%s, udp-relay=%t", line, n.udpEnabled())
	case "hysteria2":
		line := fmt.Sprintf("%s = hysteria2, %s, %d, password=%s, sni=%s", name, n.server, n.port, u.Password, sni)
		line += surgeALPN(st.TLS.ALPN)
		if st.DownMbps > 0 {
			line += fmt.Sprintf(", download-bandwidth=%d", st.DownMbps)
		}
		// Surge takes the Salamander obfs password in its own field.
		if st.ObfsPassword != "" {
			line += ", salamander-password=" + st.ObfsPassword
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
