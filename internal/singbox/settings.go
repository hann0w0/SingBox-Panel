// Package singbox generates official sing-box (>= 1.13 stable) server configs
// and client subscription artifacts. It is dependency-free (stdlib only) and
// decoupled from the panel's persistence types; the panel adapts its models to
// the input types here.
//
// The generated config strictly follows the official 1.13 schema:
//   - no removed special outbounds (block/dns) — DNS hijack via route action
//   - no removed inbound sniff/domain_strategy fields — sniff via route action
//   - new-format DNS servers ({"type":"local"|"udp"|"tls"|...})
//   - transports limited to tcp/ws (official builds omit with_grpc)
//   - no experimental.clash_api (single shared credentials cannot identify users)
package singbox

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// ProxyUser is one proxy identity provisioned into an inbound's users[] array.
type ProxyUser struct {
	Name     string // stable display/name field where the protocol supports one
	Username string // socks5 username
	UUID     string // vless / vmess / tuic
	Password string // trojan / ss / hysteria2 / tuic
}

// TransportSettings configures an optional stream transport. tcp (empty), ws
// and httpupgrade are supported (official sing-box builds omit with_grpc).
type TransportSettings struct {
	Type    string            `json:"type,omitempty"` // "" (tcp) | "ws" | "httpupgrade"
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// WS early data (0-RTT). When MaxEarlyData > 0 the server and generated
	// clients agree on a max_early_data byte budget and the header name that
	// carries the base64-encoded early payload.
	MaxEarlyData    int    `json:"max_early_data,omitempty"`
	EarlyDataHeader string `json:"early_data_header,omitempty"`
}

// FallbackSettings is the official sing-box server target shape used by the
// Trojan inbound fallback option.
type FallbackSettings struct {
	Server     string `json:"server,omitempty"`
	ServerPort int    `json:"server_port,omitempty"`
}

// RealitySettings configures REALITY (no real certificate required).
type RealitySettings struct {
	Enabled             bool     `json:"enabled,omitempty"`
	HandshakeServer     string   `json:"handshake_server,omitempty"`
	HandshakeServerPort int      `json:"handshake_server_port,omitempty"`
	PrivateKey          string   `json:"private_key,omitempty"` // server side; from `generate reality-keypair`
	PublicKey           string   `json:"public_key,omitempty"`  // client side; carried into share links
	ShortID             []string `json:"short_id,omitempty"`
}

// TLSSettings configures inbound TLS. Choose exactly one of: explicit cert
// (CertificatePath+KeyPath), ACME (ACMEDomain+ACMEEmail), or REALITY.
type TLSSettings struct {
	Enabled         bool     `json:"enabled,omitempty"`
	ServerName      string   `json:"server_name,omitempty"`
	ALPN            []string `json:"alpn,omitempty"`
	CertificatePath string   `json:"certificate_path,omitempty"`
	KeyPath         string   `json:"key_path,omitempty"`
	// SelfSigned asks the panel to generate a certificate itself. The PEM then
	// lives in Certificate/Key and is emitted INLINE into the config, so nothing
	// has to be created or kept in sync on the node's filesystem.
	SelfSigned  bool            `json:"self_signed,omitempty"`
	Certificate string          `json:"certificate,omitempty"` // PEM
	Key         string          `json:"key,omitempty"`         // PEM
	ACMEDomain  string          `json:"acme_domain,omitempty"`
	ACMEEmail   string          `json:"acme_email,omitempty"`
	Reality     RealitySettings `json:"reality,omitempty"`
	// Insecure asks generated clients to skip certificate verification. SelfSigned
	// implies the same behavior even for older rows where Insecure was not saved.
	Insecure bool `json:"insecure,omitempty"`
	// Fingerprint is the uTLS fingerprint clients present (chrome default).
	// Server configs ignore it; it is mirrored into generated client configs and
	// share links so every subscriber's TLS handshake looks identical.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// ClientInsecure reports whether generated client configurations must skip
// certificate verification. Panel-generated self-signed certificates can
// never pass public CA verification, so they always require this setting.
func (t TLSSettings) ClientInsecure() bool {
	return t.Insecure || t.SelfSigned
}

// InboundSettings is the panel-side, protocol-agnostic representation of an
// inbound's tunables, stored as JSON on model.Inbound.Settings.
type InboundSettings struct {
	// shadowsocks
	Method      string `json:"method,omitempty"`        // e.g. 2022-blake3-aes-128-gcm
	SSServerPSK string `json:"ss_server_psk,omitempty"` // top-level server PSK (base64), generated on create
	// SSPlugin mirrors a SIP002 ss://?plugin=… value (obfs-local / v2ray-plugin)
	// into share links and Clash output. sing-box has no equivalent.
	SSPlugin string `json:"ss_plugin,omitempty"`

	// vless
	Flow string `json:"flow,omitempty"` // "" | xtls-rprx-vision

	// vmess. Security is a client-side option; AlterID is emitted on both the
	// inbound user and generated clients so every subscription stays aligned.
	VMessSecurity string `json:"vmess_security,omitempty"` // auto | aes-128-gcm | chacha20-poly1305
	VMessAlterID  int    `json:"vmess_alter_id,omitempty"` // 0 recommended; 1 enables legacy authentication

	// PacketEncoding is the VLESS UDP wire encoding carried into share links for
	// third-party clients. Generated sing-box clients force xudp regardless; the
	// share-link mirror keeps Shadowrocket & friends on the same encoding.
	PacketEncoding string `json:"packet_encoding,omitempty"` // "" | "xudp"

	// hysteria2
	UpMbps       int    `json:"up_mbps,omitempty"`
	DownMbps     int    `json:"down_mbps,omitempty"`
	ObfsType     string `json:"obfs_type,omitempty"`     // "" | salamander
	ObfsPassword string `json:"obfs_password,omitempty"` // salamander; empty = no obfs
	// IgnoreClientBandwidth tells the server to ignore client-reported
	// bandwidth (server always uses up_mbps/down_mbps).
	IgnoreClientBandwidth bool `json:"ignore_client_bandwidth,omitempty"`

	// tuic
	CongestionControl string `json:"congestion_control,omitempty"` // cubic | new_reno | bbr
	AuthTimeout       string `json:"auth_timeout,omitempty"`       // default 3s (server only)
	ZeroRTTHandshake  bool   `json:"zero_rtt_handshake,omitempty"` // disabled by default due to replay risk
	Heartbeat         string `json:"heartbeat,omitempty"`          // default 10s
	// TUICUDPRelayMode mirrors the tuic://?udp_relay_mode=… client option.
	// The sing-box / mihomo / Surge vocabulary is native | quic. Legacy TUIC v4
	// values (nat | stable | quirky) are normalized to native by
	// TUICRelayModeValue so they can never reach a strict enum check.
	TUICUDPRelayMode string `json:"tuic_udp_relay_mode,omitempty"`

	// trojan
	TrojanFallback *FallbackSettings `json:"trojan_fallback,omitempty"`

	// snell (requires a sing-box 1.14 beta binary on the node)
	SnellVersion  int    `json:"snell_version,omitempty"`   // 5 | 6
	SnellPSK      string `json:"snell_psk,omitempty"`       // server pre-shared key, generated
	SnellObfsMode string `json:"snell_obfs_mode,omitempty"` // v5: none | http
	SnellMode     string `json:"snell_mode,omitempty"`      // v6: default | unshaped | unsafe-raw

	// shadowtls (no TLS object; relays to a real handshake server)
	ShadowTLSVersion       int    `json:"shadowtls_version,omitempty"` // 1 | 2 | 3 (default 3)
	ShadowTLSHandshake     string `json:"shadowtls_handshake,omitempty"`
	ShadowTLSHandshakePort int    `json:"shadowtls_handshake_port,omitempty"`
	ShadowTLSPassword      string `json:"shadowtls_password,omitempty"` // v2 top-level password, generated

	// single-credential mode: when SingleUser is true the inbound presents ONE
	// fixed credential to clients instead of the panel's per-user list — the
	// transit/relay model (and the sing-box template style). UUID/Password are
	// that credential; shadowsocks reuses SSServerPSK, snell reuses SnellPSK.
	SingleUser bool `json:"single_user,omitempty"`
	// MultiUser is an explicit panel-management opt-in. It is separate from
	// SingleUser so old rows and API clients remain single-credential by default;
	// imported raw configs still describe their observed wire shape with
	// SingleUser without being silently adopted as panel-managed multi-user.
	MultiUser bool   `json:"multi_user,omitempty"`
	Username  string `json:"username,omitempty"` // socks5 username
	UUID      string `json:"uuid,omitempty"`     // vless / vmess / tuic single-user id
	Password  string `json:"password,omitempty"` // trojan / hy2 / hysteria / naive / anytls / tuic / shadowtls / snell

	// anytls
	AnyTLSUDPOverStream bool `json:"anytls_udp_over_stream,omitempty"` // udp_over_stream=1 client option

	Transport TransportSettings `json:"transport,omitempty"`
	TLS       TLSSettings       `json:"tls,omitempty"`
}

// SupportsMultiUser reports whether the official inbound wire format used by
// this panel can safely assign one credential per panel user. Snell is kept in
// fixed-PSK mode despite its newer schema exposing users[]: real-world clients
// commonly authenticate only with the top-level PSK, and enabling userkey
// previously broke existing nodes. Legacy Shadowsocks ciphers also have only a
// shared password; Shadowsocks 2022 is the multi-user variant.
func SupportsMultiUser(typ string, s InboundSettings) bool {
	switch typ {
	case "vless", "vmess", "trojan", "hysteria2", "tuic", "anytls", "socks":
		return true
	case "shadowsocks":
		return IsSS2022(s.Method)
	default:
		return false
	}
}

// UseMultiUser is true only for an explicit opt-in on a capable protocol.
func (s InboundSettings) UseMultiUser(typ string) bool {
	return s.MultiUser && SupportsMultiUser(typ, s)
}

// VMessSecurityValue returns the documented VMess client cipher default.
func (s InboundSettings) VMessSecurityValue() string {
	if s.VMessSecurity == "" {
		return "auto"
	}
	return s.VMessSecurity
}

// TUICAuthTimeoutValue returns the official inbound default.
func (s InboundSettings) TUICAuthTimeoutValue() string {
	if s.AuthTimeout == "" {
		return "3s"
	}
	return s.AuthTimeout
}

// TUICHeartbeatValue returns the official heartbeat default shared by the
// server and generated sing-box clients.
func (s InboundSettings) TUICHeartbeatValue() string {
	if s.Heartbeat == "" {
		return "10s"
	}
	return s.Heartbeat
}

// SingleUserIdentity returns the synthetic single user a single-credential
// inbound presents (its own fixed credential), for both server config and
// client subscription so they always match.
func (s InboundSettings) SingleUserIdentity() ProxyUser {
	return ProxyUser{Name: "user", Username: s.Username, UUID: s.UUID, Password: s.Password}
}

// TUICRelayModeValue returns the UDP relay mode in the vocabulary every client
// understands (native | quic), or "" when unset. Anything else — including the
// legacy TUIC v4 values (nat/stable/quirky) — normalizes to native, matching
// what mihomo does for unrecognized values and keeping sing-box's strict enum
// happy.
func (s InboundSettings) TUICRelayModeValue() string {
	switch s.TUICUDPRelayMode {
	case "native", "quic":
		return s.TUICUDPRelayMode
	case "":
		return ""
	default:
		return "native"
	}
}

// HysteriaBandwidth returns the effective Hysteria v1 rates. The server side
// defaults both to 100 when unset, and the client must be told the same numbers
// (sing-box and mihomo treat them as required), so the default lives here and is
// used by every emitter.
func HysteriaBandwidth(s InboundSettings) (up, down int) {
	up, down = s.UpMbps, s.DownMbps
	if up == 0 {
		up = 100
	}
	if down == 0 {
		down = 100
	}
	return up, down
}

// SnellClientPSK returns the credential a Snell client authenticates with:
// the inbound's PSK in single-credential mode, otherwise the user's userkey.
func SnellClientPSK(s InboundSettings, userPassword string) string {
	if s.SingleUser {
		return s.SnellPSK
	}
	return userPassword
}

// SSClientPassword returns the client-side shadowsocks password. Single-user
// and legacy inbounds present the server PSK verbatim; multi-user 2022 inbounds
// present serverPSK:userPSK.
func SSClientPassword(s InboundSettings, userPassword string) string {
	if s.SingleUser || !IsSS2022(s.Method) {
		return s.SSServerPSK
	}
	return s.SSServerPSK + ":" + DeriveSSUserKey(userPassword, s.Method)
}

// SS2022KeyLen returns the base64-decoded key length for a Shadowsocks 2022
// method, or 0 if the method is not a 2022 method.
func SS2022KeyLen(method string) int {
	switch method {
	case "2022-blake3-aes-128-gcm":
		return 16
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		return 32
	default:
		return 0
	}
}

// IsSS2022 reports whether method is a Shadowsocks 2022 method (multi-user capable).
func IsSS2022(method string) bool { return SS2022KeyLen(method) > 0 }

// DeriveSSUserKey deterministically derives a user's SS2022 password (base64 of
// the method's key length) from the user's proxy password. The same derivation
// is used for both the server config and the client subscription so they match.
func DeriveSSUserKey(userPassword, method string) string {
	n := SS2022KeyLen(method)
	if n == 0 {
		return userPassword
	}
	sum := sha256.Sum256([]byte("singbox-panel-ss2022:" + userPassword))
	return base64.StdEncoding.EncodeToString(sum[:n])
}

// transportForClient builds the transport clause shared between inbound and
// client outbound. Returns nil when tcp (no transport).
func (t TransportSettings) normalized() *TransportSettings {
	if t.Type == "" || strings.EqualFold(t.Type, "tcp") {
		return nil
	}
	return &t
}

// Validate performs light sanity checks so obviously-broken settings are
// rejected before they reach the agent's `sing-box check`.
func (s InboundSettings) Validate(typ string) error {
	switch typ {
	case "shadowsocks":
		if s.Method == "" {
			return fmt.Errorf("shadowsocks: method is required")
		}
	case "vmess":
		switch s.VMessSecurityValue() {
		case "auto", "aes-128-gcm", "chacha20-poly1305":
		default:
			return fmt.Errorf("vmess: unsupported security %q", s.VMessSecurity)
		}
		if s.VMessAlterID < 0 {
			return fmt.Errorf("vmess: alterId must be 0 or greater")
		}
	case "hysteria2", "tuic", "trojan", "naive", "hysteria", "anytls":
		if !s.TLS.Enabled && s.TLS.ACMEDomain == "" && !s.TLS.Reality.Enabled {
			return fmt.Errorf("%s: TLS is required", typ)
		}
	case "shadowtls":
		if s.ShadowTLSHandshake == "" {
			return fmt.Errorf("shadowtls: handshake server is required")
		}
	case "snell":
		if s.SnellVersion != 5 && s.SnellVersion != 6 {
			return fmt.Errorf("snell: version must be 5 or 6")
		}
		if s.SnellVersion == 6 && s.SnellMode != "" && s.SnellMode != "default" &&
			s.SnellMode != "unshaped" && s.SnellMode != "unsafe-raw" {
			return fmt.Errorf("snell: unsupported v6 mode %q", s.SnellMode)
		}
	case "socks":
		if (s.Username == "") != (s.Password == "") {
			return fmt.Errorf("socks: username and password must both be set or both be empty")
		}
	}
	if typ == "hysteria2" && s.ObfsType != "" && s.ObfsType != "salamander" {
		return fmt.Errorf("hysteria2: unsupported obfs type %q", s.ObfsType)
	}
	if typ == "tuic" {
		switch s.CongestionControl {
		case "", "cubic", "new_reno", "bbr":
		default:
			return fmt.Errorf("tuic: unsupported congestion_control %q", s.CongestionControl)
		}
		for name, value := range map[string]string{
			"auth_timeout": s.TUICAuthTimeoutValue(),
			"heartbeat":    s.TUICHeartbeatValue(),
		} {
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return fmt.Errorf("tuic: %s must be a positive duration", name)
			}
		}
	}
	if typ == "trojan" && s.TrojanFallback != nil {
		if strings.TrimSpace(s.TrojanFallback.Server) == "" {
			return fmt.Errorf("trojan: fallback server is required")
		}
		if s.TrojanFallback.ServerPort < 1 || s.TrojanFallback.ServerPort > 65535 {
			return fmt.Errorf("trojan: fallback server_port must be between 1 and 65535")
		}
	}
	if tr := s.Transport.Type; tr != "" && !strings.EqualFold(tr, "tcp") && !strings.EqualFold(tr, "ws") && !strings.EqualFold(tr, "httpupgrade") {
		return fmt.Errorf("unsupported transport %q (official builds support tcp/ws/httpupgrade only)", tr)
	}
	// A v2ray (ws/httpupgrade) transport only exists on vless/vmess/trojan.
	// Attaching it to any other type makes `sing-box check` reject the config
	// with an unknown-field error, so guard it here rather than only in the UI.
	if strings.EqualFold(s.Transport.Type, "ws") || strings.EqualFold(s.Transport.Type, "httpupgrade") {
		switch typ {
		case "vless", "vmess", "trojan":
		default:
			return fmt.Errorf("%s: %s transport is only supported on vless/vmess/trojan", typ, s.Transport.Type)
		}
	}
	// VLESS flow=xtls-rprx-vision requires a real TLS/REALITY connection; with
	// plain TCP the client aborts at runtime ("not a valid supported TLS
	// connection"), even though `check` passes. Reject the impossible combo.
	if typ == "vless" && s.Flow != "" && !s.TLS.Enabled && s.TLS.ACMEDomain == "" && !s.TLS.Reality.Enabled {
		return fmt.Errorf("vless: flow %q requires TLS or REALITY", s.Flow)
	}
	return nil
}
