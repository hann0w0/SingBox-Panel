package singbox

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ParsedInbound is one inbound recovered from an existing config.json.
type ParsedInbound struct {
	Tag        string
	Type       string
	ListenPort int
	Settings   InboundSettings
	// UserCount is the users[] length in the source config (0 when the protocol
	// carries a single top-level credential instead, e.g. shadowsocks).
	UserCount int
}

// ParsedOutbound is one non-builtin outbound recovered from a config.
type ParsedOutbound struct {
	Tag        string
	Type       string
	Server     string
	ServerPort int
	Username   string
	UUID       string
	Password   string
	Settings   InboundSettings
}

// ParsedRule is one route rule recovered from a config.
type ParsedRule struct {
	Match    RuleInput
	Outbound string
}

// ParsedConfig is everything the panel can adopt from an existing config.json.
type ParsedConfig struct {
	Inbounds  []ParsedInbound
	Outbounds []ParsedOutbound
	Rules     []ParsedRule
	RuleSets  []RuleSetInput
	Final     string
	// Skipped lists items the panel cannot model (kind: reason).
	Skipped []string
}

// ---- small JSON access helpers (config.json is decoded into map[string]any) ----

func mMap(m map[string]any, k string) map[string]any {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return nil
}

func mStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func mInt(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func mBool(m map[string]any, k string) bool {
	v, _ := m[k].(bool)
	return v
}

func mSlice(m map[string]any, k string) []any {
	if v, ok := m[k].([]any); ok {
		return v
	}
	return nil
}

// mStrSlice reads a string array, tolerating a bare string (sing-box accepts both).
func mStrSlice(m map[string]any, k string) []string {
	switch v := m[k].(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func mIntSlice(m map[string]any, k string) []int {
	switch v := m[k].(type) {
	case float64:
		return []int{int(v)}
	case []any:
		out := make([]int, 0, len(v))
		for _, e := range v {
			if f, ok := e.(float64); ok {
				out = append(out, int(f))
			}
		}
		return out
	}
	return nil
}

// firstUser returns the first entry of an inbound's users[] array, or nil.
func firstUser(m map[string]any) map[string]any {
	us := mSlice(m, "users")
	if len(us) == 0 {
		return nil
	}
	u, _ := us[0].(map[string]any)
	return u
}

// parseTLS recovers TLSSettings from an inbound/outbound tls object.
func parseTLS(t map[string]any) TLSSettings {
	if t == nil {
		return TLSSettings{}
	}
	out := TLSSettings{
		Enabled:         mBool(t, "enabled"),
		ServerName:      mStr(t, "server_name"),
		ALPN:            mStrSlice(t, "alpn"),
		CertificatePath: mStr(t, "certificate_path"),
		KeyPath:         mStr(t, "key_path"),
		Insecure:        mBool(t, "insecure"),
	}
	if utls := mMap(t, "utls"); utls != nil {
		out.Fingerprint = mStr(utls, "fingerprint")
	}
	// Inline PEM material is emitted as an array of lines (see buildTLS/pemLines)
	// but may also appear as a single string. Recover either form, otherwise a
	// switch to managed config would regenerate TLS with no certificate and fail
	// `sing-box check`.
	if cert := mStrSlice(t, "certificate"); len(cert) > 0 {
		out.Certificate = strings.Join(cert, "\n")
	}
	if key := mStrSlice(t, "key"); len(key) > 0 {
		out.Key = strings.Join(key, "\n")
	}
	// ACME: the 1.14+ certificate_provider object (shared string tags are
	// resolved to objects by ParseServerConfig) or the legacy inline tls.acme.
	acme := mMap(t, "acme")
	if cp := mMap(t, "certificate_provider"); cp != nil && mStr(cp, "type") == "acme" {
		acme = cp
	}
	if acme != nil {
		if d := mStrSlice(acme, "domain"); len(d) > 0 {
			out.ACMEDomain = d[0]
		}
		out.ACMEEmail = mStr(acme, "email")
	}
	if r := mMap(t, "reality"); r != nil && mBool(r, "enabled") {
		privateKey := mStr(r, "private_key")
		publicKey := mStr(r, "public_key")
		if publicKey == "" && privateKey != "" {
			if raw, err := base64.RawURLEncoding.DecodeString(privateKey); err == nil {
				if key, err := ecdh.X25519().NewPrivateKey(raw); err == nil {
					publicKey = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
				}
			}
		}
		out.Reality = RealitySettings{
			Enabled:    true,
			PrivateKey: privateKey,
			PublicKey:  publicKey,
			ShortID:    mStrSlice(r, "short_id"),
		}
		if h := mMap(r, "handshake"); h != nil {
			out.Reality.HandshakeServer = mStr(h, "server")
			out.Reality.HandshakeServerPort = mInt(h, "server_port")
		}
	}
	return out
}

func parseTransport(t map[string]any) TransportSettings {
	if t == nil {
		return TransportSettings{}
	}
	out := TransportSettings{Type: mStr(t, "type"), Path: mStr(t, "path")}
	if out.Type == "ws" {
		out.MaxEarlyData = mInt(t, "max_early_data")
		out.EarlyDataHeader = mStr(t, "early_data_header_name")
		if out.EarlyDataHeader == "" {
			// Accept the panel's historical field name as well as sing-box's
			// official wire name so older raw configs round-trip losslessly.
			out.EarlyDataHeader = mStr(t, "early_data_header")
		}
	}
	if h := mMap(t, "headers"); h != nil {
		for k, v := range h {
			if s, ok := v.(string); ok {
				out.SetHeaderValues(k, []string{s})
				continue
			}
			if values, ok := v.([]any); ok {
				stringsValues := make([]string, 0, len(values))
				for _, value := range values {
					if s, ok := value.(string); ok {
						stringsValues = append(stringsValues, s)
					}
				}
				out.SetHeaderValues(k, stringsValues)
			}
		}
	}
	if out.Type == "httpupgrade" {
		if host := mStr(t, "host"); host != "" {
			out.SetHeaderValues("Host", []string{host})
		}
	}
	return out
}

// parseInbound maps one raw inbound object to the panel's representation.
// ok is false for inbound types the panel does not model (e.g. mixed/tun).
func parseInbound(m map[string]any) (ParsedInbound, bool) {
	typ := mStr(m, "type")
	in := ParsedInbound{
		Tag:        mStr(m, "tag"),
		Type:       typ,
		ListenPort: mInt(m, "listen_port"),
		UserCount:  len(mSlice(m, "users")),
	}
	s := InboundSettings{
		TLS:       parseTLS(mMap(m, "tls")),
		Transport: parseTransport(mMap(m, "transport")),
	}
	u := firstUser(m)
	// A single (or absent) users[] entry means one fixed credential — exactly the
	// panel's single-user mode. Multiple entries means panel-managed users.
	s.SingleUser = in.UserCount <= 1

	switch typ {
	case "vless":
		if u != nil {
			s.UUID = mStr(u, "uuid")
			s.Flow = mStr(u, "flow")
		}
		if s.Flow == "" {
			s.Flow = mStr(m, "flow")
		}
	case "vmess":
		if u != nil {
			s.UUID = mStr(u, "uuid")
			s.VMessAlterID = mInt(u, "alterId")
		}
		s.VMessSecurity = "auto"
	case "trojan":
		if u != nil {
			s.Password = mStr(u, "password")
		}
		if f := mMap(m, "fallback"); f != nil {
			s.TrojanFallback = &FallbackSettings{Server: mStr(f, "server"), ServerPort: mInt(f, "server_port")}
		}
	case "anytls":
		if u != nil {
			s.Password = mStr(u, "password")
		}
	case "shadowsocks":
		s.Method = mStr(m, "method")
		s.SSServerPSK = mStr(m, "password")
		// No users[] at all => a single shared credential.
		s.SingleUser = in.UserCount == 0
	case "hysteria2":
		if u != nil {
			s.Password = mStr(u, "password")
		}
		s.UpMbps = mInt(m, "up_mbps")
		s.DownMbps = mInt(m, "down_mbps")
		s.IgnoreClientBandwidth = mBool(m, "ignore_client_bandwidth")
		if o := mMap(m, "obfs"); o != nil {
			s.ObfsType = mStr(o, "type")
			s.ObfsPassword = mStr(o, "password")
			s.GeckoMinPacketSize = mInt(o, "min_packet_size")
			s.GeckoMaxPacketSize = mInt(o, "max_packet_size")
		}
	case "tuic":
		if u != nil {
			s.UUID = mStr(u, "uuid")
			s.Password = mStr(u, "password")
		}
		s.CongestionControl = mStr(m, "congestion_control")
		s.AuthTimeout = mStr(m, "auth_timeout")
		s.ZeroRTTHandshake = mBool(m, "zero_rtt_handshake")
		s.Heartbeat = mStr(m, "heartbeat")
	case "snell":
		s.SnellVersion = mInt(m, "version")
		s.SnellPSK = mStr(m, "psk")
		s.SnellObfsMode = mStr(m, "obfs_mode")
		s.SnellMode = mStr(m, "mode")
		// The panel manages Snell as one shared PSK, regardless of legacy
		// users[].userkey entries in an imported config.
		s.SingleUser = true
	case "socks":
		if u != nil {
			s.Username = mStr(u, "username")
			s.Password = mStr(u, "password")
		}
	default:
		return ParsedInbound{}, false
	}
	in.Settings = s
	return in, true
}

// builtinOutbounds are outbound types/tags the panel manages itself and never
// imports as landing targets.
func isBuiltinOutbound(typ, tag string) bool {
	switch typ {
	case "direct", "block", "dns", "selector", "urltest":
		return true
	}
	return tag == "direct"
}

func parseOutbound(m map[string]any) (ParsedOutbound, bool) {
	typ := mStr(m, "type")
	tag := mStr(m, "tag")
	if isBuiltinOutbound(typ, tag) {
		return ParsedOutbound{}, false
	}
	switch typ {
	case "vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic", "anytls", "snell", "socks":
	default:
		return ParsedOutbound{}, false
	}
	out := ParsedOutbound{
		Tag:        tag,
		Type:       typ,
		Server:     mStr(m, "server"),
		ServerPort: mInt(m, "server_port"),
		Username:   mStr(m, "username"),
		UUID:       mStr(m, "uuid"),
		Password:   mStr(m, "password"),
	}
	s := InboundSettings{TLS: parseTLS(mMap(m, "tls")), Transport: parseTransport(mMap(m, "transport"))}
	switch typ {
	case "shadowsocks":
		s.Method = mStr(m, "method")
		// The landing credential is carried verbatim in the outbound password.
		s.SSServerPSK = out.Password
		s.SSPlugin = JoinShadowsocksPluginFields(mStr(m, "plugin"), mStr(m, "plugin_opts"))
	case "vless":
		s.Flow = mStr(m, "flow")
	case "vmess":
		s.VMessSecurity = mStr(m, "security")
		s.VMessAlterID = mInt(m, "alter_id")
	case "tuic":
		s.CongestionControl = mStr(m, "congestion_control")
		s.TUICUDPRelayMode = mStr(m, "udp_relay_mode")
		s.ZeroRTTHandshake = mBool(m, "zero_rtt_handshake")
		s.Heartbeat = mStr(m, "heartbeat")
	case "hysteria2":
		s.UpMbps = mInt(m, "up_mbps")
		s.DownMbps = mInt(m, "down_mbps")
		if obfs := mMap(m, "obfs"); obfs != nil {
			s.ObfsType = mStr(obfs, "type")
			s.ObfsPassword = mStr(obfs, "password")
			s.GeckoMinPacketSize = mInt(obfs, "min_packet_size")
			s.GeckoMaxPacketSize = mInt(obfs, "max_packet_size")
		}
	case "snell":
		out.Password = mStr(m, "psk")
		s.SnellVersion = mInt(m, "version")
		s.SnellPSK = mStr(m, "psk")
		s.SnellReuse = mBool(m, "reuse")
		s.SnellNetwork = strings.ToLower(mStr(m, "network"))
		s.SnellObfsMode = mStr(m, "obfs_mode")
		s.SnellObfsHost = mStr(m, "obfs_host")
		s.SnellMode = mStr(m, "mode")
		s.SingleUser = true
	case "socks":
		s.Username = out.Username
		s.Password = out.Password
	}
	out.Settings = s
	return out, true
}

func parseRule(m map[string]any) (ParsedRule, bool) {
	action := mStr(m, "action")
	if action == "" {
		action = "route"
	}
	r := ParsedRule{
		Match: RuleInput{
			Action:        action,
			Method:        mStr(m, "method"),
			Inbound:       mStrSlice(m, "inbound"),
			Domain:        mStrSlice(m, "domain"),
			DomainSuffix:  mStrSlice(m, "domain_suffix"),
			DomainKeyword: mStrSlice(m, "domain_keyword"),
			IPCIDR:        mStrSlice(m, "ip_cidr"),
			SourceIPCIDR:  mStrSlice(m, "source_ip_cidr"),
			Port:          mIntSlice(m, "port"),
			Protocol:      mStrSlice(m, "protocol"),
			Sniffer:       mStrSlice(m, "sniffer"),
			Network:       mStr(m, "network"),
			RuleSet:       mStrSlice(m, "rule_set"),
		},
		Outbound: mStr(m, "outbound"),
	}
	switch action {
	case "route":
		if r.Outbound == "" {
			return ParsedRule{}, false
		}
		if r.Outbound == "block" || r.Outbound == "reject" {
			r.Match.Action = "reject"
			r.Outbound = "block"
		}
	case "reject":
		r.Outbound = "block"
	case "sniff":
		r.Outbound = "sniff"
	case "hijack-dns":
		r.Outbound = "hijack-dns"
	default:
		return ParsedRule{}, false
	}
	return r, true
}

// ParseServerConfig recovers the panel's model from an existing official
// sing-box config.json, so a server configured by hand can be adopted by the
// panel without retyping everything. Unmodellable entries are reported in
// ParsedConfig.Skipped rather than failing the whole import.
func ParseServerConfig(raw []byte) (*ParsedConfig, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("配置不是合法 JSON: %w", err)
	}
	out := &ParsedConfig{Final: "direct"}

	providers := taggedObjects(root, "certificate_providers")
	for _, e := range mSlice(root, "inbounds") {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if note := resolveCertificateProvider(m, providers); note != "" {
			out.Skipped = append(out.Skipped, note)
		}
		in, ok := parseInbound(m)
		if !ok {
			out.Skipped = append(out.Skipped, fmt.Sprintf("入站 %s: 面板不支持的类型 %s", mStr(m, "tag"), mStr(m, "type")))
			continue
		}
		if in.ListenPort == 0 {
			out.Skipped = append(out.Skipped, fmt.Sprintf("入站 %s: 缺少 listen_port", in.Tag))
			continue
		}
		out.Inbounds = append(out.Inbounds, in)
	}

	for _, e := range mSlice(root, "outbounds") {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		ob, ok := parseOutbound(m)
		if !ok {
			continue // builtin (direct/block/...) or unsupported: silently ignored
		}
		out.Outbounds = append(out.Outbounds, ob)
	}

	if route := mMap(root, "route"); route != nil {
		if f := mStr(route, "final"); f != "" {
			out.Final = f
		}
		for _, e := range mSlice(route, "rules") {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if IsLoopbackGuardRule(m) {
				continue // regenerated by BuildServerConfig for managed configs
			}
			if r, ok := parseRule(m); ok {
				out.Rules = append(out.Rules, r)
			}
		}
		httpClients := taggedObjects(root, "http_clients")
		defaultHTTPClient := mStr(route, "default_http_client")
		if defaultHTTPClient == "" {
			if list := mSlice(root, "http_clients"); len(list) > 0 {
				if first, ok := list[0].(map[string]any); ok {
					defaultHTTPClient = mStr(first, "tag")
				}
			}
		}
		for _, e := range mSlice(route, "rule_set") {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			typ := mStr(m, "type")
			if typ != "remote" && typ != "local" {
				out.Skipped = append(out.Skipped, fmt.Sprintf("规则集 %s: 仅支持 remote/local 类型", mStr(m, "tag")))
				continue
			}
			out.RuleSets = append(out.RuleSets, RuleSetInput{
				Tag: mStr(m, "tag"), Type: typ, URL: mStr(m, "url"), Path: mStr(m, "path"),
				Format: mStr(m, "format"), DownloadDetour: ruleSetDownloadDetour(m, httpClients, defaultHTTPClient),
				UpdateInterval: mStr(m, "update_interval"),
			})
		}
	}

	// Cross-check route targets against what was actually imported. Outbounds the
	// panel cannot model (selector/urltest/dns/...) are skipped, so a rule or a
	// final that points at one would leave a dangling tag — and every config the
	// panel later pushes would fail `sing-box check` and roll back.
	known := map[string]bool{
		"direct": true, "block": true, "reject": true,
		"sniff": true, "hijack-dns": true,
	}
	finalKnown := out.Final == "direct"
	for _, ob := range out.Outbounds {
		known[ob.Tag] = true
		if ob.Tag == out.Final {
			finalKnown = true
		}
	}
	if !finalKnown {
		out.Skipped = append(out.Skipped,
			fmt.Sprintf("默认出站 final=%s 未被导入，已回退为 direct", out.Final))
		out.Final = "direct"
	}
	kept := out.Rules[:0]
	for _, r := range out.Rules {
		if !known[r.Outbound] {
			out.Skipped = append(out.Skipped,
				fmt.Sprintf("规则 → %s：该出站未被导入，规则已跳过", r.Outbound))
			continue
		}
		kept = append(kept, r)
	}
	out.Rules = kept

	return out, nil
}

// taggedObjects indexes a top-level array of tagged objects (http_clients,
// certificate_providers) by tag.
func taggedObjects(root map[string]any, key string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, e := range mSlice(root, key) {
		if m, ok := e.(map[string]any); ok {
			if tag := mStr(m, "tag"); tag != "" {
				out[tag] = m
			}
		}
	}
	return out
}

// resolveCertificateProvider replaces a shared tls.certificate_provider tag on
// an inbound with the referenced top-level object so parseTLS sees one shape.
// It returns a note when the provider cannot be represented by the panel.
func resolveCertificateProvider(inbound map[string]any, providers map[string]map[string]any) string {
	tls := mMap(inbound, "tls")
	if tls == nil {
		return ""
	}
	var provider map[string]any
	switch v := tls["certificate_provider"].(type) {
	case nil:
		return ""
	case string:
		provider = providers[v]
		if provider == nil {
			return fmt.Sprintf("入站 %s: 证书提供者 %s 不存在", mStr(inbound, "tag"), v)
		}
		tls["certificate_provider"] = provider
	case map[string]any:
		provider = v
	default:
		return ""
	}
	if typ := mStr(provider, "type"); typ != "acme" {
		return fmt.Sprintf("入站 %s: 面板仅支持 ACME 证书提供者，%s 未导入", mStr(inbound, "tag"), typ)
	}
	return ""
}

// ruleSetDownloadDetour recovers the outbound a remote rule-set downloads
// through. sing-box 1.14+ expresses it as an http_client (a tag or an inline
// object) whose dial detour is the outbound; a client without detour dials
// directly. The legacy download_detour is read when no client is set, and an
// empty result keeps the implicit "default outbound" meaning.
func ruleSetDownloadDetour(rs map[string]any, clients map[string]map[string]any, defaultClient string) string {
	var client map[string]any
	switch v := rs["http_client"].(type) {
	case string:
		client = clients[v]
	case map[string]any:
		client = v
	}
	if client == nil && rs["http_client"] == nil {
		if legacy := mStr(rs, "download_detour"); legacy != "" {
			return legacy
		}
		if defaultClient != "" {
			client = clients[defaultClient]
		}
	}
	if client == nil {
		return ""
	}
	if detour := mStr(client, "detour"); detour != "" {
		return detour
	}
	return "direct"
}
