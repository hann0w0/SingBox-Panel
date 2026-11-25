package singbox

import (
	"encoding/json"
	"fmt"
	"strings"
)

// InboundInput describes one inbound to render into a server config.
type InboundInput struct {
	ProjectionOnly bool
	Tag            string
	Type           string // vless | vmess | trojan | shadowsocks | hysteria2 | tuic
	ListenPort     int
	Settings       InboundSettings
	Users          []ProxyUser
}

// ---- sing-box wire structs (subset we generate) ----

// acmeProvider is an inline ACME certificate provider (sing-box 1.14+). It
// replaces the inline tls.acme object, which 1.14 deprecated and 1.15 rejects.
type acmeProvider struct {
	Type   string   `json:"type"` // always "acme"
	Domain []string `json:"domain"`
	Email  string   `json:"email,omitempty"`
}

type realityHandshake struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
}

type realityInbound struct {
	Enabled           bool             `json:"enabled"`
	Handshake         realityHandshake `json:"handshake"`
	PrivateKey        string           `json:"private_key"`
	ShortID           []string         `json:"short_id,omitempty"`
	MaxTimeDifference string           `json:"max_time_difference,omitempty"`
}

type tlsInbound struct {
	Enabled         bool            `json:"enabled"`
	ServerName      string          `json:"server_name,omitempty"`
	ALPN            []string        `json:"alpn,omitempty"`
	Certificate     []string        `json:"certificate,omitempty"` // inline PEM lines
	Key             []string        `json:"key,omitempty"`         // inline PEM lines
	CertificatePath string          `json:"certificate_path,omitempty"`
	KeyPath         string          `json:"key_path,omitempty"`
	CertProvider    *acmeProvider   `json:"certificate_provider,omitempty"`
	Reality         *realityInbound `json:"reality,omitempty"`
}

// pemLines splits a PEM blob into the array-of-lines form sing-box expects for
// an inline certificate/key.
func pemLines(pem string) []string {
	pem = strings.TrimSpace(pem)
	if pem == "" {
		return nil
	}
	return strings.Split(pem, "\n")
}

type v2rayTransport struct {
	Type            string         `json:"type"`
	Host            any            `json:"host,omitempty"`
	Path            string         `json:"path,omitempty"`
	Method          string         `json:"method,omitempty"`
	ServiceName     string         `json:"service_name,omitempty"`
	Headers         map[string]any `json:"headers,omitempty"`
	MaxEarlyData    int            `json:"max_early_data,omitempty"`
	EarlyDataHeader string         `json:"early_data_header_name,omitempty"`
}

func orDefaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// buildTLS renders the inbound tls clause, or nil when the inbound has no TLS.
func buildTLS(t TLSSettings) *tlsInbound {
	if !t.Enabled && t.ACMEDomain == "" && !t.Reality.Enabled {
		return nil
	}
	out := &tlsInbound{Enabled: true, ServerName: t.ServerName, ALPN: t.ALPN}
	switch {
	case t.Reality.Enabled:
		out.Reality = &realityInbound{
			Enabled: true,
			Handshake: realityHandshake{
				Server:     t.Reality.HandshakeServer,
				ServerPort: orDefaultInt(t.Reality.HandshakeServerPort, 443),
			},
			PrivateKey:        t.Reality.PrivateKey,
			ShortID:           t.Reality.ShortID,
			MaxTimeDifference: t.Reality.MaxTimeDifference,
		}
		if out.ServerName == "" {
			out.ServerName = t.Reality.HandshakeServer
		}
	case t.Certificate != "" && t.Key != "":
		// Inline PEM: no files to create or keep in sync on the node.
		out.Certificate = pemLines(t.Certificate)
		out.Key = pemLines(t.Key)
	case t.ACMEDomain != "":
		out.CertProvider = &acmeProvider{Type: "acme", Domain: []string{t.ACMEDomain}, Email: t.ACMEEmail}
		if out.ServerName == "" {
			out.ServerName = t.ACMEDomain
		}
	default:
		out.CertificatePath = t.CertificatePath
		out.KeyPath = t.KeyPath
	}
	return out
}

func transportHeaders(t TransportSettings, omitHost bool) map[string]any {
	values := t.HeaderValuesMap()
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, headerValues := range values {
		if !omitHost || !strings.EqualFold(key, "host") {
			if len(headerValues) == 1 {
				out[key] = headerValues[0]
			} else if len(headerValues) > 1 {
				out[key] = headerValues
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildTransport renders a supported v2ray transport using only the wire fields
// legal for its type, or nil for TCP.
func buildTransport(t TransportSettings) *v2rayTransport {
	if t.normalized() == nil {
		return nil
	}
	typ := strings.ToLower(t.Type)
	out := &v2rayTransport{Type: typ}
	switch typ {
	case "ws":
		out.Path = t.Path
		out.Headers = transportHeaders(t, false)
		out.MaxEarlyData = t.MaxEarlyData
		out.EarlyDataHeader = t.EarlyDataHeader
	case "httpupgrade":
		out.Path = t.Path
		if host := t.Host(); host != "" {
			out.Host = host
		}
		out.Headers = transportHeaders(t, true)
	case "http":
		if len(t.HostList) > 0 {
			out.Host = t.HostList
		} else if host := t.Host(); host != "" {
			out.Host = []string{host}
		}
		out.Path = t.Path
		out.Method = t.Method
		out.Headers = transportHeaders(t, true)
	case "grpc":
		out.ServiceName = t.ServiceName
	case "quic":
		// QUIC transport only accepts type.
	default:
		out.Path = t.Path
		out.Headers = transportHeaders(t, false)
	}
	return out
}

func buildHysteria2Obfs(s InboundSettings) map[string]any {
	if s.ObfsPassword == "" || (s.ObfsType != "" && s.ObfsType != "salamander" && s.ObfsType != "gecko") {
		return nil
	}
	obfs := map[string]any{"type": s.ObfsType, "password": s.ObfsPassword}
	if obfs["type"] == "" {
		obfs["type"] = "salamander"
	}
	if s.ObfsType == "gecko" {
		if s.GeckoMinPacketSize > 0 {
			obfs["min_packet_size"] = s.GeckoMinPacketSize
		}
		if s.GeckoMaxPacketSize > 0 {
			obfs["max_packet_size"] = s.GeckoMaxPacketSize
		}
	}
	return obfs
}

// BuildInbound renders a single inbound object as official config JSON.
func BuildInbound(in InboundInput) (json.RawMessage, error) {
	if err := in.Settings.Validate(in.Type); err != nil && !in.ProjectionOnly {
		return nil, err
	}
	if in.Settings.IPv6Only && in.Settings.ListenAddress() == "::" {
		return nil, fmt.Errorf("IPv6 入站必须绑定具体的本机 IPv6 地址")
	}
	if !in.Settings.SingleUser && len(in.Users) > 0 {
		// Imported wire-mode inputs can carry users without the panel opt-in.
		multiSettings := in.Settings
		multiSettings.MultiUser = true
		if err := multiSettings.ValidateMultiUser(in.Type); err != nil {
			return nil, err
		}
	}
	m := map[string]any{
		"type": in.Type,
		"tag":  in.Tag,
	}
	if in.Type != "tun" {
		m["listen"] = in.Settings.ListenAddress()
		if in.ListenPort > 0 {
			m["listen_port"] = in.ListenPort
		}
	}
	tls := buildTLS(in.Settings.TLS)
	tr := buildTransport(in.Settings.Transport)

	// Effective users: single-credential inbounds present one fixed identity;
	// otherwise the panel's per-user list is provisioned into users[].
	eff := in.Users
	if in.Settings.SingleUser {
		eff = []ProxyUser{in.Settings.SingleUserIdentity()}
	}

	switch in.Type {
	case "vless":
		users := make([]map[string]any, 0, len(eff))
		for _, u := range eff {
			e := map[string]any{"name": u.Name, "uuid": u.UUID}
			if in.Settings.Flow != "" {
				e["flow"] = in.Settings.Flow
			}
			users = append(users, e)
		}
		m["users"] = users
		if tls != nil {
			m["tls"] = tls
		}
		if tr != nil {
			m["transport"] = tr
		}

	case "vmess":
		users := make([]map[string]any, 0, len(eff))
		for _, u := range eff {
			users = append(users, map[string]any{"name": u.Name, "uuid": u.UUID, "alterId": in.Settings.VMessAlterID})
		}
		m["users"] = users
		if tls != nil {
			m["tls"] = tls
		}
		if tr != nil {
			m["transport"] = tr
		}

	case "trojan":
		users := make([]map[string]any, 0, len(eff))
		for _, u := range eff {
			users = append(users, map[string]any{"name": u.Name, "password": u.Password})
		}
		m["users"] = users
		if tls != nil {
			m["tls"] = tls
		}
		if in.Settings.TrojanFallback != nil {
			m["fallback"] = map[string]any{
				"server":      in.Settings.TrojanFallback.Server,
				"server_port": in.Settings.TrojanFallback.ServerPort,
			}
		}
		if tr != nil {
			m["transport"] = tr
		}

	case "shadowsocks":
		method := in.Settings.Method
		m["method"] = method
		if in.Settings.SingleUser {
			// Single credential: the server PSK IS the password, no users[].
			m["password"] = in.Settings.SSServerPSK
		} else if SupportsMultiUser(in.Type, in.Settings) {
			m["password"] = in.Settings.SSServerPSK
			us := make([]map[string]any, 0, len(eff))
			for _, u := range eff {
				us = append(us, map[string]any{
					"name":     u.Name,
					"password": DeriveSSUserKey(u.Password, method),
				})
			}
			m["users"] = us
		} else {
			// Legacy and SS2022 ChaCha methods use one shared password.
			m["password"] = in.Settings.SSServerPSK
		}

	case "hysteria2":
		users := make([]map[string]any, 0, len(eff))
		for _, u := range eff {
			users = append(users, map[string]any{"name": u.Name, "password": u.Password})
		}
		m["users"] = users
		if tls != nil {
			m["tls"] = tls
		}
		if in.Settings.UpMbps > 0 {
			m["up_mbps"] = in.Settings.UpMbps
		}
		if in.Settings.DownMbps > 0 {
			m["down_mbps"] = in.Settings.DownMbps
		}
		if in.Settings.IgnoreClientBandwidth {
			m["ignore_client_bandwidth"] = true
		}
		// Keep legacy rows with only obfs_password as salamander. Gecko also
		// carries its optional packet-size bounds in the same object.
		if obfs := buildHysteria2Obfs(in.Settings); obfs != nil {
			m["obfs"] = obfs
		}
		// NOTE: hysteria2 is QUIC and has no v2ray transport field. Emitting
		// "transport" here makes `sing-box check` fail with an unknown-field
		// error and the config is rolled back.

	case "tuic":
		users := make([]map[string]any, 0, len(eff))
		for _, u := range eff {
			users = append(users, map[string]any{"name": u.Name, "uuid": u.UUID, "password": u.Password})
		}
		m["users"] = users
		if tls != nil {
			m["tls"] = tls
		}
		cc := in.Settings.CongestionControl
		if cc == "" {
			cc = "cubic"
		}
		m["congestion_control"] = cc
		m["auth_timeout"] = in.Settings.TUICAuthTimeoutValue()
		m["zero_rtt_handshake"] = in.Settings.ZeroRTTHandshake
		m["heartbeat"] = in.Settings.TUICHeartbeatValue()

	case "naive":
		users := make([]map[string]any, 0, len(eff))
		for _, u := range eff {
			users = append(users, map[string]any{"username": u.Name, "password": u.Password})
		}
		m["users"] = users
		if tls != nil {
			m["tls"] = tls
		}

	case "hysteria": // Hysteria v1
		users := make([]map[string]any, 0, len(eff))
		for _, u := range eff {
			users = append(users, map[string]any{"name": u.Name, "auth_str": u.Password})
		}
		m["users"] = users
		if tls != nil {
			m["tls"] = tls
		}
		up, down := HysteriaBandwidth(in.Settings)
		m["up_mbps"] = up
		m["down_mbps"] = down
		if in.Settings.ObfsPassword != "" {
			m["obfs"] = in.Settings.ObfsPassword // v1 obfs is a plain string
		}

	case "shadowtls":
		ver := in.Settings.ShadowTLSVersion
		if ver == 0 {
			ver = 3
		}
		m["version"] = ver
		m["handshake"] = map[string]any{
			"server":      in.Settings.ShadowTLSHandshake,
			"server_port": orDefaultInt(in.Settings.ShadowTLSHandshakePort, 443),
		}
		if ver >= 3 {
			users := make([]map[string]any, 0, len(eff))
			for _, u := range eff {
				users = append(users, map[string]any{"name": u.Name, "password": u.Password})
			}
			m["users"] = users
		} else if ver == 2 {
			m["password"] = in.Settings.ShadowTLSPassword
		}

	case "anytls":
		users := make([]map[string]any, 0, len(eff))
		for _, u := range eff {
			users = append(users, map[string]any{"name": u.Name, "password": u.Password})
		}
		m["users"] = users
		if tls != nil {
			m["tls"] = tls
		}

	case "snell": // requires sing-box 1.14+ on the node
		m["version"] = in.Settings.SnellVersion
		m["psk"] = in.Settings.SnellPSK
		// Snell is intentionally fixed to one PSK in this panel. Do not emit
		// users[] even when an old row still contains a multi-user flag.
		if in.Settings.SnellVersion == 5 && in.Settings.SnellObfsMode != "" && in.Settings.SnellObfsMode != "none" {
			m["obfs_mode"] = in.Settings.SnellObfsMode
		}
		// v6 mode: only emit official values. The legacy "default" placeholder
		// (never a valid sing-box mode) is dropped instead of breaking check.
		if in.Settings.SnellVersion == 6 && in.Settings.SnellMode != "" && in.Settings.SnellMode != "default" {
			m["mode"] = in.Settings.SnellMode
		}

	case "socks":
		// SOCKS uses one shared login, independent of panel user assignments.
		if in.Settings.Username != "" || in.Settings.Password != "" {
			m["users"] = []map[string]any{
				{"username": in.Settings.Username, "password": in.Settings.Password},
			}
		}

	case "mixed": // HTTP + SOCKS5 on one port
		if in.Settings.UseMultiUser(in.Type) {
			users := make([]map[string]any, 0, len(eff))
			for _, u := range eff {
				users = append(users, map[string]any{"username": u.Username, "password": u.Password})
			}
			if len(users) == 0 {
				if in.Settings.Username == "" || in.Settings.Password == "" {
					return nil, fmt.Errorf("mixed: multi-user mode requires a fallback credential when no users are active")
				}
				users = append(users, map[string]any{
					"username": in.Settings.Username,
					"password": in.Settings.Password,
				})
			}
			m["users"] = users
		} else if in.Settings.Username != "" || in.Settings.Password != "" {
			m["users"] = []map[string]any{
				{"username": in.Settings.Username, "password": in.Settings.Password},
			}
		}

	case "http":
		if in.Settings.Username != "" || in.Settings.Password != "" {
			m["users"] = []map[string]any{
				{"username": in.Settings.Username, "password": in.Settings.Password},
			}
		}
		if tls != nil {
			m["tls"] = tls
		}

	case "tun", "direct", "redirect", "tproxy":
		// Schema-driven inbounds rely on Settings.Raw for protocol-specific fields.

	default:
		return nil, fmt.Errorf("unsupported inbound type %q", in.Type)
	}

	if len(in.Settings.Raw) > 0 {
		merged, err := mergeInboundWithRaw(in, m)
		if err != nil {
			return nil, err
		}
		return json.Marshal(merged)
	}
	return json.Marshal(m)
}

func mergeInboundWithRaw(in InboundInput, overlay map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(overlay)
	if err != nil {
		return nil, err
	}
	var normalizedOverlay map[string]any
	if err := json.Unmarshal(encoded, &normalizedOverlay); err != nil {
		return nil, err
	}
	base := deepCloneMap(in.Settings.Raw)
	cleanStaleRawInboundUsers(base, in)
	cleanStaleRawInboundProtocolFields(base, in)
	cleanStaleRawInboundTLS(base, in.Settings.TLS, normalizedOverlay)
	cleanStaleRawInboundTransport(base, in.Settings.Transport)
	return deepMergeMaps(base, normalizedOverlay), nil
}

func cleanStaleRawInboundUsers(base map[string]any, in InboundInput) {
	if !in.Settings.SingleUser || in.Type == "snell" {
		delete(base, "users")
		return
	}
	switch in.Type {
	case "socks", "http":
		if in.Settings.Username == "" && in.Settings.Password == "" {
			delete(base, "users")
		}
	case "mixed":
		if !in.Settings.UseMultiUser(in.Type) && in.Settings.Username == "" && in.Settings.Password == "" {
			delete(base, "users")
		}
	case "shadowsocks":
		delete(base, "users")
	case "shadowtls":
		if in.Settings.ShadowTLSVersion == 2 {
			delete(base, "users")
		}
	}
}

func cleanStaleRawInboundProtocolFields(base map[string]any, in InboundInput) {
	switch in.Type {
	case "vless":
		delete(base, "flow")
	case "trojan":
		if in.Settings.TrojanFallback == nil {
			delete(base, "fallback")
		}
	case "hysteria2":
		if in.Settings.UpMbps <= 0 {
			delete(base, "up_mbps")
		}
		if in.Settings.DownMbps <= 0 {
			delete(base, "down_mbps")
		}
		if !in.Settings.IgnoreClientBandwidth {
			delete(base, "ignore_client_bandwidth")
		}
		if buildHysteria2Obfs(in.Settings) == nil {
			delete(base, "obfs")
		} else if obfsMap, ok := base["obfs"].(map[string]any); ok {
			if in.Settings.ObfsType != "gecko" || in.Settings.GeckoMinPacketSize <= 0 {
				delete(obfsMap, "min_packet_size")
			}
			if in.Settings.ObfsType != "gecko" || in.Settings.GeckoMaxPacketSize <= 0 {
				delete(obfsMap, "max_packet_size")
			}
		}
	case "hysteria":
		if in.Settings.ObfsPassword == "" {
			delete(base, "obfs")
		}
	case "shadowtls":
		if in.Settings.ShadowTLSVersion == 0 || in.Settings.ShadowTLSVersion >= 3 {
			delete(base, "password")
		}
	case "snell":
		if in.Settings.SnellVersion != 5 || in.Settings.SnellObfsMode == "" || in.Settings.SnellObfsMode == "none" {
			delete(base, "obfs_mode")
		}
		if in.Settings.SnellVersion != 6 || in.Settings.SnellMode == "" || in.Settings.SnellMode == "default" {
			delete(base, "mode")
		}
	}
}

func cleanStaleRawInboundTLS(base map[string]any, t TLSSettings, normalizedOverlay map[string]any) {
	if !t.tlsEnabled() {
		delete(base, "tls")
		return
	}
	tlsMap, ok := base["tls"].(map[string]any)
	if !ok {
		return
	}
	overlayTLS, _ := normalizedOverlay["tls"].(map[string]any)
	if mStr(overlayTLS, "server_name") == "" {
		delete(tlsMap, "server_name")
	}
	if len(t.ALPN) == 0 {
		delete(tlsMap, "alpn")
	}
	switch {
	case t.Reality.Enabled:
		delete(tlsMap, "certificate")
		delete(tlsMap, "key")
		delete(tlsMap, "certificate_path")
		delete(tlsMap, "key_path")
		delete(tlsMap, "certificate_provider")
		delete(tlsMap, "acme")
		if realityMap, ok := tlsMap["reality"].(map[string]any); ok {
			if len(t.Reality.ShortID) == 0 {
				delete(realityMap, "short_id")
			}
			if t.Reality.MaxTimeDifference == "" {
				delete(realityMap, "max_time_difference")
			}
		}
	case t.Certificate != "" && t.Key != "":
		delete(tlsMap, "reality")
		delete(tlsMap, "certificate_path")
		delete(tlsMap, "key_path")
		delete(tlsMap, "certificate_provider")
		delete(tlsMap, "acme")
	case t.ACMEDomain != "":
		delete(tlsMap, "reality")
		delete(tlsMap, "certificate")
		delete(tlsMap, "key")
		delete(tlsMap, "certificate_path")
		delete(tlsMap, "key_path")
		delete(tlsMap, "acme")
		if cp, ok := tlsMap["certificate_provider"].(map[string]any); ok && mStr(cp, "type") == "acme" && t.ACMEEmail == "" {
			delete(cp, "email")
		}
	default:
		delete(tlsMap, "reality")
		delete(tlsMap, "certificate")
		delete(tlsMap, "key")
		delete(tlsMap, "acme")
		if cp, ok := tlsMap["certificate_provider"].(map[string]any); ok && mStr(cp, "type") == "acme" {
			delete(tlsMap, "certificate_provider")
		}
		if t.CertificatePath == "" {
			delete(tlsMap, "certificate_path")
		}
		if t.KeyPath == "" {
			delete(tlsMap, "key_path")
		}
	}
}

func cleanStaleRawInboundTransport(base map[string]any, t TransportSettings) {
	tr := strings.ToLower(t.Type)
	if tr == "" || tr == "tcp" {
		delete(base, "transport")
		return
	}
	trMap, ok := base["transport"].(map[string]any)
	if !ok {
		return
	}
	if prevType := strings.ToLower(mStr(trMap, "type")); prevType != "" && prevType != tr {
		delete(base, "transport")
		return
	}
	if t.Path == "" {
		delete(trMap, "path")
	}
	switch tr {
	case "ws":
		delete(trMap, "host")
		delete(trMap, "method")
		delete(trMap, "service_name")
		delete(trMap, "early_data_header")
		if transportHeaders(t, false) == nil {
			delete(trMap, "headers")
		}
		if t.MaxEarlyData <= 0 {
			delete(trMap, "max_early_data")
		}
		if t.EarlyDataHeader == "" {
			delete(trMap, "early_data_header_name")
		}
	case "httpupgrade":
		delete(trMap, "method")
		delete(trMap, "service_name")
		delete(trMap, "max_early_data")
		delete(trMap, "early_data_header_name")
		delete(trMap, "early_data_header")
		if t.Host() == "" {
			delete(trMap, "host")
		}
		if transportHeaders(t, true) == nil {
			delete(trMap, "headers")
		}
	case "http":
		delete(trMap, "service_name")
		delete(trMap, "max_early_data")
		delete(trMap, "early_data_header_name")
		delete(trMap, "early_data_header")
		if len(t.HostList) == 0 && t.Host() == "" {
			delete(trMap, "host")
		}
		if t.Method == "" {
			delete(trMap, "method")
		}
		if transportHeaders(t, true) == nil {
			delete(trMap, "headers")
		}
	case "grpc":
		delete(trMap, "path")
		delete(trMap, "host")
		delete(trMap, "method")
		delete(trMap, "headers")
		delete(trMap, "max_early_data")
		delete(trMap, "early_data_header_name")
		delete(trMap, "early_data_header")
		if t.ServiceName == "" {
			delete(trMap, "service_name")
		}
	case "quic":
		delete(trMap, "path")
		delete(trMap, "host")
		delete(trMap, "method")
		delete(trMap, "headers")
		delete(trMap, "service_name")
		delete(trMap, "max_early_data")
		delete(trMap, "early_data_header_name")
		delete(trMap, "early_data_header")
	default:
		if transportHeaders(t, false) == nil {
			delete(trMap, "headers")
		}
	}
}
