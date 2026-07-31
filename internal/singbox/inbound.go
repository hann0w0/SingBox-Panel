package singbox

import (
	"encoding/json"
	"fmt"
	"strings"
)

// InboundInput describes one inbound to render into a server config.
type InboundInput struct {
	Tag        string
	Type       string // vless | vmess | trojan | shadowsocks | hysteria2 | tuic
	ListenPort int
	Settings   InboundSettings
	Users      []ProxyUser
}

// ---- sing-box wire structs (subset we generate) ----

type acmeInbound struct {
	Domain []string `json:"domain"`
	Email  string   `json:"email,omitempty"`
}

type realityHandshake struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
}

type realityInbound struct {
	Enabled    bool             `json:"enabled"`
	Handshake  realityHandshake `json:"handshake"`
	PrivateKey string           `json:"private_key"`
	ShortID    []string         `json:"short_id,omitempty"`
}

type tlsInbound struct {
	Enabled         bool            `json:"enabled"`
	ServerName      string          `json:"server_name,omitempty"`
	ALPN            []string        `json:"alpn,omitempty"`
	Certificate     []string        `json:"certificate,omitempty"` // inline PEM lines
	Key             []string        `json:"key,omitempty"`         // inline PEM lines
	CertificatePath string          `json:"certificate_path,omitempty"`
	KeyPath         string          `json:"key_path,omitempty"`
	ACME            *acmeInbound    `json:"acme,omitempty"`
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

type wsTransport struct {
	Type    string            `json:"type"` // "ws"
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
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
			PrivateKey: t.Reality.PrivateKey,
			ShortID:    t.Reality.ShortID,
		}
		if out.ServerName == "" {
			out.ServerName = t.Reality.HandshakeServer
		}
	case t.Certificate != "" && t.Key != "":
		// Inline PEM: no files to create or keep in sync on the node.
		out.Certificate = pemLines(t.Certificate)
		out.Key = pemLines(t.Key)
	case t.ACMEDomain != "":
		out.ACME = &acmeInbound{Domain: []string{t.ACMEDomain}, Email: t.ACMEEmail}
		if out.ServerName == "" {
			out.ServerName = t.ACMEDomain
		}
	default:
		out.CertificatePath = t.CertificatePath
		out.KeyPath = t.KeyPath
	}
	return out
}

// buildTransport renders the ws transport clause, or nil for tcp.
func buildTransport(t TransportSettings) *wsTransport {
	if t.normalized() == nil {
		return nil
	}
	return &wsTransport{Type: "ws", Path: t.Path, Headers: t.Headers}
}

// BuildInbound renders a single inbound object as official config JSON.
func BuildInbound(in InboundInput) (json.RawMessage, error) {
	if err := in.Settings.Validate(in.Type); err != nil {
		return nil, err
	}
	m := map[string]any{
		"type":        in.Type,
		"tag":         in.Tag,
		"listen":      "::",
		"listen_port": in.ListenPort,
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
		m["tls"] = tls // required (validate enforced)
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
		} else if IsSS2022(method) {
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
			// Legacy methods: single shared password, no per-user accounting.
			m["password"] = in.Settings.SSServerPSK
		}

	case "hysteria2":
		users := make([]map[string]any, 0, len(eff))
		for _, u := range eff {
			users = append(users, map[string]any{"name": u.Name, "password": u.Password})
		}
		m["users"] = users
		m["tls"] = tls // required
		if in.Settings.UpMbps > 0 {
			m["up_mbps"] = in.Settings.UpMbps
		}
		if in.Settings.DownMbps > 0 {
			m["down_mbps"] = in.Settings.DownMbps
		}
		if in.Settings.ObfsPassword != "" {
			m["obfs"] = map[string]any{"type": "salamander", "password": in.Settings.ObfsPassword}
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
		m["tls"] = tls // required
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
		m["tls"] = tls // required

	case "hysteria": // Hysteria v1
		users := make([]map[string]any, 0, len(eff))
		for _, u := range eff {
			users = append(users, map[string]any{"name": u.Name, "auth_str": u.Password})
		}
		m["users"] = users
		m["tls"] = tls // required
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
		m["tls"] = tls // required

	case "snell": // requires a sing-box 1.14 beta binary on the node
		m["version"] = in.Settings.SnellVersion
		m["psk"] = in.Settings.SnellPSK
		// Single-credential Snell is just the PSK — no users[] / userkey at all.
		if !in.Settings.SingleUser {
			users := make([]map[string]any, 0, len(eff))
			for _, u := range eff {
				users = append(users, map[string]any{"name": u.Name, "userkey": u.Password})
			}
			m["users"] = users
		}
		if in.Settings.SnellVersion == 5 && in.Settings.SnellObfsMode != "" && in.Settings.SnellObfsMode != "none" {
			m["obfs_mode"] = in.Settings.SnellObfsMode
		}
		if in.Settings.SnellVersion == 6 && in.Settings.SnellMode != "" {
			m["mode"] = in.Settings.SnellMode
		}

	case "socks":
		if in.Settings.UseMultiUser(in.Type) {
			users := make([]map[string]any, 0, len(eff))
			for _, u := range eff {
				users = append(users, map[string]any{"username": u.Username, "password": u.Password})
			}
			if len(users) == 0 {
				if in.Settings.Username == "" || in.Settings.Password == "" {
					return nil, fmt.Errorf("socks: multi-user mode requires a fallback credential when no users are active")
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

	default:
		return nil, fmt.Errorf("unsupported inbound type %q", in.Type)
	}

	return json.Marshal(m)
}
