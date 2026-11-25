package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
	"gorm.io/gorm"
)

// node bundles a server+inbound for a subscriber.
type node struct {
	orderType string
	orderID   uint
	tag       string
	name      string
	server    string
	port      int
	typ       string
	region    string // ISO-ish region code (US/HK/JP…) for the dashboard map; may be empty
	settings  singbox.InboundSettings
	user      singbox.ProxyUser
	udp       *bool // nil keeps the historical default: UDP enabled
}

func (n node) udpEnabled() bool {
	if n.typ == "http" {
		return false
	}
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
	defer func() { a.recordSubscriptionFetch(c, &user, subFormat(c), c.Writer.Status()) }()
	// A disabled or expired account must stop receiving nodes. Report the expiry
	// so the client can show why, but hand out nothing.
	if !userActive(&user) {
		setSubscriptionUserinfo(c, &user)
		c.String(http.StatusForbidden, "account disabled or expired")
		return
	}

	nodes, err := a.gatherNodesWithError(&user)
	if err != nil {
		log.Printf("subscription: failed to build nodes for user %d: %v", user.ID, err)
		c.String(http.StatusServiceUnavailable, "subscription temporarily unavailable")
		return
	}

	// Usage header consumed by many clients to display quota.
	setSubscriptionUserinfo(c, &user)

	switch subFormat(c) {
	case "sing-box", "singbox":
		a.writeSingbox(c, nodes)
	case "clash", "clash-meta", "clashmeta":
		a.writeClash(c, nodes)
	case "shadowrocket":
		a.writeShadowrocket(c, nodes)
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
	case strings.Contains(ua, "shadowrocket"):
		return "shadowrocket"
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
// inbounds the user is granted, plus the custom (external) nodes scoped to them
// or to everyone that no subscription rule hides.
func (a *App) gatherNodes(user *model.User) []node {
	nodes, _ := a.gatherNodesWithError(user)
	return nodes
}

// gatherNodesWithError returns a complete node set or an error.  Subscription
// responses must never turn a transient database failure into a valid-looking
// empty subscription, which clients could cache as a node deletion.
func (a *App) gatherNodesWithError(user *model.User) ([]node, error) {
	var out []node
	var firstInboundErr error
	recordInboundErr := func(srv *model.Server, tag string, err error) {
		log.Printf("subscription: skipping broken inbound %q on server %d (%s) for user %d: %v", tag, srv.ID, srv.Name, user.ID, err)
		if firstInboundErr == nil {
			firstInboundErr = err
		}
	}
	if len(user.ServerIDs) > 0 {
		var servers []model.Server
		if err := a.db.Where("id IN ?", user.ServerIDs).Order("id").Find(&servers).Error; err != nil {
			return nil, err
		}
		applyServerOrder(a.db, servers)

		for i := range servers {
			srv := &servers[i]
			var inbounds []model.Inbound
			if err := a.db.Where("server_id = ? AND enabled = ?", srv.ID, true).Order("id").Find(&inbounds).Error; err != nil {
				return nil, err
			}
			first := len(out)
			for _, ib := range inbounds {
				// Respect per-inbound access: a protocol the user isn't granted must
				// not appear in their subscription.
				if !user.HasInbound(srv.ID, ib.ID) {
					continue
				}
				var st singbox.InboundSettings
				if len(ib.Settings) > 0 {
					if err := json.Unmarshal(ib.Settings, &st); err != nil {
						recordInboundErr(srv, ib.Tag, fmt.Errorf("inbound %q settings: %w", ib.Tag, err))
						continue
					}
				}
				host, err := inboundNodeAddress(srv, &st)
				if errors.Is(err, errLocalInbound) {
					continue
				}
				if err != nil {
					recordInboundErr(srv, ib.Tag, fmt.Errorf("入站 %q: %w", ib.Tag, err))
					continue
				}
				if err := st.ValidateMultiUser(string(ib.Type)); err != nil {
					recordInboundErr(srv, ib.Tag, fmt.Errorf("inbound %q settings: %w", ib.Tag, err))
					continue
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
					orderType: userNodeTypeManaged,
					orderID:   ib.ID,
					tag:       ib.Tag,
					server:    host,
					port:      st.EffectiveClientPort(ib.ListenPort),
					typ:       string(ib.Type),
					region:    srv.Region,
					settings:  st,
					user:      identity,
				})
			}
			nameServerNodes(srv.Name, out[first:])

			// Each forward is a separate node with its own credential. The
			// original inbound node above stays untouched. Forwarding is an
			// explicit capability: without it, stale forward records must not
			// appear in a subscription after an administrator revokes access.
			var forwards []model.UserForward
			if user.ForwardingEnabled {
				if err := a.db.Where("user_id = ? AND server_id = ?", user.ID, srv.ID).Order("id").Find(&forwards).Error; err != nil {
					return nil, err
				}
			}
			inboundByID := make(map[uint]model.Inbound, len(inbounds))
			for _, ib := range inbounds {
				inboundByID[ib.ID] = ib
			}
			outboundByID := make(map[uint]model.Outbound, len(forwards))
			if len(forwards) > 0 {
				outboundIDs := make([]uint, 0, len(forwards))
				for _, forward := range forwards {
					outboundIDs = append(outboundIDs, forward.OutboundID)
				}
				if ids := normalizedIDs(outboundIDs); len(ids) > 0 {
					var outbounds []model.Outbound
					if err := a.db.Where("id IN ? AND owner_user_id = ?", ids, user.ID).Find(&outbounds).Error; err != nil {
						return nil, err
					}
					for _, ob := range outbounds {
						outboundByID[ob.ID] = ob
					}
				}
			}
			for _, forward := range forwards {
				ib, ok := inboundByID[forward.InboundID]
				if !ok {
					continue
				}
				if !user.HasInbound(srv.ID, ib.ID) {
					continue
				}
				outbound, ok := outboundByID[forward.OutboundID]
				if !ok {
					continue
				}
				var st singbox.InboundSettings
				if len(ib.Settings) > 0 {
					if err := json.Unmarshal(ib.Settings, &st); err != nil {
						continue
					}
				}
				if !st.UseMultiUser(string(ib.Type)) {
					continue
				}
				host, err := inboundNodeAddress(srv, &st)
				if errors.Is(err, errLocalInbound) {
					continue
				}
				if err != nil {
					recordInboundErr(srv, ib.Tag, fmt.Errorf("转发入站 %q: %w", ib.Tag, err))
					continue
				}
				st.SingleUser = false
				st.MultiUser = true
				name := forwardNodeName(srv.Name, ib.Tag, outbound.Tag, forward.Remark)
				out = append(out, node{
					orderType: userNodeTypeForward,
					orderID:   forward.ID,
					tag:       ib.Tag,
					name:      name,
					server:    host,
					port:      st.EffectiveClientPort(ib.ListenPort),
					typ:       string(ib.Type),
					region:    srv.Region,
					settings:  st,
					user:      forwardProxyIdentity(user, forward.ID, ib.ID),
				})
			}
		}
	}

	// Custom external nodes carry their own credentials (share link or the
	// structured protocol fields), so no per-user derivation happens here.
	var customs []model.CustomNode
	if err := a.db.Where("hidden_by_subscription_rule = ?", false).Order("sort_order, id").Find(&customs).Error; err != nil {
		return nil, err
	}
	for i := range customs {
		c := &customs[i]
		if !c.HasUser(user.ID) {
			continue
		}
		if n, ok := a.customNodeToNode(c); ok {
			n.orderType = userNodeTypeCustom
			n.orderID = c.ID
			out = append(out, n)
		}
	}
	if len(out) == 0 && firstInboundErr != nil {
		return nil, firstInboundErr
	}
	applyUserNodeOrder(a.db, user.ID, out)
	deduplicateNodeNames(out)
	return out, nil
}

func deduplicateNodeNames(nodes []node) {
	seen := make(map[string]int, len(nodes))
	for i := range nodes {
		nodes[i].name = uniqueName(seen, nodes[i].name)
	}
}

// applyUserNodeOrder applies per-user custom node display names and performs a
// stable mixed sort. Nodes absent from a saved order (for example, a newly
// assigned node) stay at the end in their existing canonical order until the
// administrator saves a new personal order.
func applyUserNodeOrder(db *gorm.DB, userID uint, nodes []node) {
	if len(nodes) == 0 {
		return
	}
	var rows []model.UserNodeOrder
	if err := db.Where("user_id = ?", userID).Order("sort_order, id").Find(&rows).Error; err != nil || len(rows) == 0 {
		return
	}
	positions := make(map[string]int, len(rows))
	customNames := make(map[string]string, len(rows))
	for i, row := range rows {
		key := row.NodeType + ":" + strconv.FormatUint(uint64(row.NodeID), 10)
		positions[key] = i
		if custom := strings.TrimSpace(row.CustomName); custom != "" {
			customNames[key] = custom
		}
	}
	for i := range nodes {
		key := nodes[i].orderType + ":" + strconv.FormatUint(uint64(nodes[i].orderID), 10)
		if custom := customNames[key]; custom != "" {
			nodes[i].name = custom
			if flagRegion := regionFromName(custom); flagRegion != "" {
				nodes[i].region = flagRegion
			}
		}
	}
	unknown := len(rows)
	if len(nodes) < 2 {
		return
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		pi, ok := positions[nodes[i].orderType+":"+strconv.FormatUint(uint64(nodes[i].orderID), 10)]
		if !ok {
			pi = unknown
		}
		pj, ok := positions[nodes[j].orderType+":"+strconv.FormatUint(uint64(nodes[j].orderID), 10)]
		if !ok {
			pj = unknown
		}
		return pi < pj
	})
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
		UUID                  string         `json:"uuid"`
		Password              string         `json:"password"`
		Username              string         `json:"username"`
		HTTPPath              string         `json:"http_path"`
		HTTPHeaders           map[string]any `json:"http_headers"`
		Method                string         `json:"method"`
		SSPlugin              string         `json:"ss_plugin"`
		Plugin                string         `json:"plugin"`
		SSPluginOpts          map[string]any `json:"ss_plugin_opts"`
		Flow                  string         `json:"flow"`
		PacketEncoding        string         `json:"packet_encoding"`
		VMessSecurity         string         `json:"security"`
		VMessAlterID          int            `json:"alter_id"`
		TLS                   string         `json:"tls"` // none | tls | reality
		SNI                   string         `json:"sni"`
		PBK                   string         `json:"pbk"`
		SID                   string         `json:"sid"`
		Fingerprint           string         `json:"fingerprint"`
		Insecure              bool           `json:"insecure"`
		SkipCertVerify        bool           `json:"skip_cert_verify"`
		Transport             string         `json:"transport"` // tcp | ws | httpupgrade
		Network               string         `json:"network"`
		Path                  string         `json:"path"`
		Host                  string         `json:"host"`
		Headers               map[string]any `json:"headers"`
		MaxEarlyData          int            `json:"max_early_data"`
		EarlyDataHeaderName   string         `json:"early_data_header_name"`
		ALPN                  string         `json:"alpn"`
		Congestion            string         `json:"congestion_control"`
		CongestionController  string         `json:"congestion_controller"`
		UDPRelayMode          string         `json:"udp_relay_mode"`
		UDP                   *bool          `json:"udp"`
		UDPOverStream         bool           `json:"udp_over_stream"`
		Obfs                  string         `json:"obfs"`
		ObfsType              string         `json:"obfs_type"`
		ObfsPassword          string         `json:"obfs_password"`
		GeckoMinPacketSize    int            `json:"gecko_min_packet_size"`
		GeckoMaxPacketSize    int            `json:"gecko_max_packet_size"`
		UpMbps                int            `json:"up_mbps"`
		DownMbps              int            `json:"down_mbps"`
		IgnoreClientBandwidth bool           `json:"ignore_client_bandwidth"`
		ZeroRTTHandshake      bool           `json:"zero_rtt_handshake"`
		Heartbeat             string         `json:"heartbeat"`
		PSK                   string         `json:"psk"`
		Reuse                 bool           `json:"reuse"`
		Version               int            `json:"version"`
		ObfsMode              string         `json:"obfs_mode"`
		ObfsHost              string         `json:"obfs_host"`
		Mode                  string         `json:"mode"`
	}
	if len(c.Params) > 0 {
		if err := json.Unmarshal(c.Params, &p); err != nil {
			return node{}, false
		}
	}
	protocol := singbox.NormalizeProtocol(c.Protocol)
	if strings.EqualFold(strings.TrimSpace(c.Protocol), "https") {
		p.TLS = "tls"
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
		// Keep unsupported types visible to ValidateClientOutbound. Dropping
		// them here would silently turn a gRPC/HTTP/QUIC node into plain TCP.
		st.Transport.Type = transport
		if transport == "ws" || transport == "httpupgrade" {
			st.Transport.Path = p.Path
			st.Transport.MaxEarlyData = p.MaxEarlyData
			st.Transport.EarlyDataHeader = p.EarlyDataHeaderName
			if len(p.Headers) > 0 {
				for key, value := range p.Headers {
					st.Transport.SetHeaderValues(key, headerValues(value))
				}
			}
			if host := strings.TrimSpace(p.Host); host != "" {
				st.Transport.SetHeaderValues("Host", []string{host})
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
		st.GeckoMinPacketSize = p.GeckoMinPacketSize
		st.GeckoMaxPacketSize = p.GeckoMaxPacketSize
		st.UpMbps = p.UpMbps
		st.DownMbps = p.DownMbps
		st.IgnoreClientBandwidth = p.IgnoreClientBandwidth
		buildTLS()
	case "snell":
		if p.Version == 0 {
			p.Version = 5
		}
		st.SnellVersion = p.Version
		st.SnellPSK = p.PSK
		st.SnellReuse = p.Reuse
		st.SnellNetwork = strings.ToLower(strings.TrimSpace(p.Network))
		st.SnellObfsMode = p.ObfsMode
		if st.SnellObfsMode == "" {
			st.SnellObfsMode = p.Obfs
		}
		st.SnellObfsHost = strings.TrimSpace(p.ObfsHost)
		st.SnellMode = p.Mode
	case "http":
		st.Username, st.Password = p.Username, p.Password
		st.HTTPPath = p.HTTPPath
		if len(p.HTTPHeaders) > 0 {
			st.HTTPHeaders = make(map[string][]string, len(p.HTTPHeaders))
			for key, value := range p.HTTPHeaders {
				st.HTTPHeaders[key] = headerValues(value)
			}
		}
		buildTLS()
	case "socks", "mixed":
		st.Username = p.Username
		st.Password = p.Password
	default:
		return node{}, false
	}
	return node{
		name:     name,
		server:   strings.Trim(strings.TrimSpace(c.Address), "[]"),
		port:     c.Port,
		typ:      protocol,
		region:   regionFromName(name),
		settings: st,
		user:     u,
		udp:      p.UDP,
	}, true
}

func headerValues(value any) []string {
	switch values := value.(type) {
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			out = append(out, fmt.Sprint(item))
		}
		return out
	case []string:
		return values
	case string:
		return []string{values}
	default:
		return []string{fmt.Sprint(value)}
	}
}

// mergeSSPluginOptions keeps Clash's split plugin/plugin-opts representation
// in the SIP002 string used by share links and the internal node model.
func mergeSSPluginOptions(plugin string, opts map[string]any) string {
	plugin = strings.TrimSpace(plugin)
	rawParts := strings.Split(plugin, ";")
	pluginName := strings.TrimSpace(rawParts[0])
	if pluginName == "" {
		return ""
	}
	if pluginName == "obfs" {
		pluginName = "obfs-local"
	}
	parts := []string{pluginName}
	seenKeys := make(map[string]bool, len(opts)+len(rawParts))
	for _, existing := range rawParts[1:] {
		existing = strings.TrimSpace(existing)
		if existing == "" {
			continue
		}
		k, _, _ := strings.Cut(existing, "=")
		seenKeys[strings.TrimSpace(k)] = true
		parts = append(parts, existing)
	}
	if len(opts) == 0 {
		return strings.Join(parts, ";")
	}
	keys := make([]string, 0, len(opts))
	for key := range opts {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		rawVal := opts[key]
		if rawVal == nil {
			continue
		}
		mappedKey := key
		if pluginName == "obfs-local" {
			switch key {
			case "mode":
				mappedKey = "obfs"
			case "host":
				mappedKey = "obfs-host"
			}
		}
		if seenKeys[mappedKey] {
			continue
		}
		if pluginName == "v2ray-plugin" {
			switch strings.ToLower(key) {
			case "tls":
				switch v := rawVal.(type) {
				case bool:
					if v {
						parts = append(parts, "tls")
						seenKeys["tls"] = true
					}
				default:
					s := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
					if s == "true" || s == "1" {
						parts = append(parts, "tls")
						seenKeys["tls"] = true
					}
				}
				continue
			case "mux":
				switch v := rawVal.(type) {
				case bool:
					if !v {
						parts = append(parts, "mux=0")
						seenKeys["mux"] = true
					}
				default:
					s := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
					if s == "false" || s == "0" {
						parts = append(parts, "mux=0")
						seenKeys["mux"] = true
					}
				}
				continue
			}
		}
		value := strings.TrimSpace(fmt.Sprint(rawVal))
		if value != "" {
			parts = append(parts, mappedKey+"="+value)
			seenKeys[mappedKey] = true
		}
	}
	return strings.Join(parts, ";")
}

func splitSSPluginOptions(plugin string) (string, map[string]any) {
	parts := strings.Split(plugin, ";")
	name := strings.TrimSpace(parts[0])
	isObfs := name == "obfs" || name == "obfs-local"
	opts := map[string]any{}
	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if !ok {
			opts[key] = true
			continue
		}
		value = strings.TrimSpace(value)
		if isObfs {
			switch key {
			case "obfs":
				key = "mode"
			case "obfs-host":
				key = "host"
			}
		}
		if name == "v2ray-plugin" && key == "mux" {
			switch strings.ToLower(value) {
			case "0", "false":
				opts["mux"] = false
			default:
				opts["mux"] = true
			}
			continue
		}
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
