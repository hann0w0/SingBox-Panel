package panel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// tlsNode builds a node with a full TLS profile (sni + utls chrome + alpn h2).
func tlsNode(t *testing.T, typ string, alpn []string) node {
	t.Helper()
	st := singbox.InboundSettings{SingleUser: true}
	st.TLS.Enabled = true
	st.TLS.ServerName = "cdn.example.com"
	st.TLS.Fingerprint = "chrome"
	st.TLS.ALPN = alpn
	u := singbox.ProxyUser{Name: "user"}
	switch typ {
	case "vless":
		u.UUID = "12345678-1234-1234-1234-123456789012"
	case "vmess":
		u.UUID = "12345678-1234-1234-1234-123456789012"
	case "trojan", "anytls", "hysteria2":
		u.Password = "secret"
	case "tuic":
		u.UUID = "12345678-1234-1234-1234-123456789012"
		u.Password = "secret"
	}
	return node{
		tag: typ, name: typ, server: "1.2.3.4", port: 443, typ: typ,
		settings: st, user: u,
	}
}

// TestSubscriptionTLSParameterMatrix verifies every output format carries the
// TLS parameters that were actually configured, and never invents ones that
// were not. This is the contract the user asked for: the subscription mirrors
// the node configuration.
func TestSubscriptionTLSParameterMatrix(t *testing.T) {
	for _, typ := range []string{"vless", "vmess", "trojan", "anytls", "hysteria2", "tuic"} {
		t.Run(typ, func(t *testing.T) {
			n := tlsNode(t, typ, []string{"h2"})
			cn := n.clientNode()

			// --- URI ---
			link, err := singbox.BuildShareLink(cn)
			if err != nil {
				t.Fatalf("BuildShareLink: %v", err)
			}
			// vmess:// is base64-wrapped JSON; decode it before asserting.
			linkCheck := link
			if typ == "vmess" {
				payload := strings.TrimPrefix(link, "vmess://")
				if dec, derr := base64.StdEncoding.DecodeString(payload); derr == nil {
					linkCheck = "vmess://" + string(dec)
				}
			}
			if !strings.Contains(linkCheck, "alpn=h2") && !strings.Contains(linkCheck, `"alpn":"h2"`) {
				t.Errorf("URI 缺 alpn: %s", link)
			}

			// --- sing-box ---
			ob, err := singbox.BuildClientOutbound(cn)
			if err != nil {
				t.Fatalf("BuildClientOutbound: %v", err)
			}
			sb := string(ob)
			if !strings.Contains(sb, `"alpn":["h2"]`) {
				t.Errorf("sing-box 缺 alpn: %s", sb)
			}

			// --- Clash ---
			clash := clashProxy(n, map[string]int{})
			raw, _ := json.Marshal(clash)
			cstr := string(raw)
			if !strings.Contains(cstr, `"alpn":["h2"]`) {
				t.Errorf("Clash 缺 alpn: %s", cstr)
			}
			// uTLS applies to TCP protocols only; QUIC (hysteria2/tuic) must not
			// carry client-fingerprint (mihomo would reject/handshake-fail).
			switch typ {
			case "vless", "vmess", "trojan", "anytls":
				if !strings.Contains(cstr, `"client-fingerprint":"chrome"`) {
					t.Errorf("Clash 缺 client-fingerprint: %s", cstr)
				}
			default:
				if strings.Contains(cstr, "client-fingerprint") {
					t.Errorf("Clash %s 不应有 client-fingerprint: %s", typ, cstr)
				}
			}

			// --- Surge ---
			line := surgeProxy(n, typ)
			if line == "" {
				// Surge can't express vless; the other TLS protocols must render.
				if typ != "vless" {
					t.Errorf("Surge %s 未渲染: %q", typ, line)
				}
				return
			}
			if !strings.Contains(line, "sni=cdn.example.com") {
				t.Errorf("Surge %s 缺 sni: %s", typ, line)
			}
			if !strings.Contains(line, ", alpn=h2") {
				t.Errorf("Surge %s 缺 alpn: %s", typ, line)
			}
		})
	}
}

// TestSubscriptionALPNFollowsConfig is the flip side: no ALPN configured means
// no alpn in any format.
func TestSubscriptionALPNFollowsConfig(t *testing.T) {
	for _, typ := range []string{"trojan", "anytls", "hysteria2", "vmess"} {
		n := tlsNode(t, typ, nil)
		cn := n.clientNode()

		link, err := singbox.BuildShareLink(cn)
		if err == nil && strings.Contains(link, "alpn=") {
			t.Errorf("%s URI 未配置 alpn 却输出: %s", typ, link)
		}
		ob, _ := singbox.BuildClientOutbound(cn)
		if strings.Contains(string(ob), `"alpn"`) {
			t.Errorf("%s sing-box 未配置 alpn 却输出: %s", typ, ob)
		}
		clash, _ := json.Marshal(clashProxy(n, map[string]int{}))
		if strings.Contains(string(clash), `"alpn"`) {
			t.Errorf("%s Clash 未配置 alpn 却输出: %s", typ, clash)
		}
		line := surgeProxy(n, typ)
		if strings.Contains(line, "alpn=") {
			t.Errorf("%s Surge 未配置 alpn 却输出: %s", typ, line)
		}
	}
}
