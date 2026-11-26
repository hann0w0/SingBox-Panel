package panel

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

type NodeDetail struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Server string            `json:"server"`
	Port   int               `json:"port"`
	Region string            `json:"region"` // ISO-ish code (US/HK/JP…) for the dashboard map
	Link   string            `json:"link"`
	Params map[string]string `json:"params"`
}

func (a *App) userNodeDetails(user *model.User) ([]NodeDetail, error) {
	nodes, err := a.gatherNodesWithError(user)
	if err != nil {
		return nil, err
	}
	out := make([]NodeDetail, 0, len(nodes))
	for _, n := range nodes {
		link, _ := singbox.BuildShareLink(n.clientNode())
		out = append(out, NodeDetail{
			ID:     fmt.Sprintf("%s:%d", n.orderType, n.orderID),
			Name:   n.name,
			Type:   n.typ,
			Server: n.server,
			Port:   n.port,
			Region: n.region,
			Link:   link,
			Params: nodeParams(n),
		})
	}
	return out, nil
}

func nodeParams(n node) map[string]string {
	st := n.settings
	u := n.getUserIdentity()
	p := map[string]string{"服务器": n.server, "端口": strconv.Itoa(n.port), "协议": n.typ}
	net := "tcp"
	switch st.Transport.Type {
	case "ws", "httpupgrade":
		net = st.Transport.Type
	}
	sni := sniOf(st, n.sniHost())
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
		if nodeTLSOn(st) {
			p["安全"] = "tls"
			p["SNI"] = sni
		} else {
			p["安全"] = "none"
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
	case "http", "socks":
		if st.Username != "" {
			p["用户名"] = st.Username
		}
		if st.Password != "" {
			p["密码"] = st.Password
		}
		if st.Username == "" && st.Password == "" {
			p["认证"] = "无需认证"
		}
		if n.typ == "http" {
			p["安全"] = "none"
			if nodeTLSOn(st) {
				p["安全"] = "tls"
				p["SNI"] = sni
			}
			if st.HTTPPath != "" {
				p["Path"] = st.HTTPPath
			}
		}
	case "hysteria2":
		p["密码"] = u.Password
		p["SNI"] = sni
		if st.ObfsPassword != "" {
			obfsType := st.ObfsType
			if obfsType == "" {
				obfsType = "salamander"
			}
			p["Obfs"] = obfsType
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
	switch n.typ {
	case "vless", "vmess", "trojan":
		if net == "ws" || net == "httpupgrade" {
			p["Path"] = st.Transport.Path
			if host := st.Transport.Host(); host != "" {
				p["Host"] = host
			}
		}
		if st.TLS.Reality.Enabled {
			p["安全"] = "reality"
			p["SNI"] = sni
			p["PublicKey"] = st.TLS.Reality.PublicKey
			p["ShortID"] = firstOf(st.TLS.Reality.ShortID)
			p["指纹"] = st.TLS.Fingerprint
			if p["指纹"] == "" {
				p["指纹"] = "chrome"
			}
		}
	}
	return p
}

// writeSurge emits just the [Proxy] section; the user pastes/includes it into
// their own Surge profile.
