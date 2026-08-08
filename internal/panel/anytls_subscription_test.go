package panel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/singbox"
)

// mkTLSNode builds a node whose TLS carries uTLS fingerprint + ALPN, the way a
// panel-managed inbound with utls/alpn configured ends up.
func mkTLSNode(t *testing.T, typ string) node {
	t.Helper()
	st := singbox.InboundSettings{SingleUser: true}
	st.TLS.Enabled = true
	st.TLS.ServerName = "cdn.example.com"
	st.TLS.Fingerprint = "chrome"
	st.TLS.ALPN = []string{"h2", "http/1.1"}
	u := singbox.ProxyUser{Name: "user"}
	switch typ {
	case "vless":
		u.UUID = "12345678-1234-1234-1234-123456789012"
	case "anytls":
		u.Password = "secret"
	}
	return node{tag: typ, name: typ, server: "1.2.3.4", port: 443, typ: typ, settings: st, user: u}
}

// Regression: anytls must carry the uTLS fingerprint in Clash output, exactly
// like vless/vmess do. It previously only emitted alpn.
func TestClashAnyTLSIncludesClientFingerprint(t *testing.T) {
	for _, typ := range []string{"vless", "anytls"} {
		clash := clashProxy(mkTLSNode(t, typ), map[string]int{})
		raw, _ := json.Marshal(clash)
		if !strings.Contains(string(raw), `"client-fingerprint":"chrome"`) {
			t.Errorf("%s clash output missing client-fingerprint: %s", typ, raw)
		}
		if !strings.Contains(string(raw), `"alpn"`) {
			t.Errorf("%s clash output missing alpn: %s", typ, raw)
		}
	}
}

// Regression: Surge anytls line must include alpn when configured (Surge
// supports the shared TLS parameters for AnyTLS). Single values are bare;
// multi-value lists are quoted per Surge's syntax.
func TestSurgeAnyTLSIncludesALPN(t *testing.T) {
	n := mkTLSNode(t, "anytls")
	line := surgeProxy(n, n.name)
	if !strings.Contains(line, "alpn=\"h2,http/1.1\"") {
		t.Errorf("surge anytls line missing quoted alpn list: %s", line)
	}

	single := mkTLSNode(t, "anytls")
	single.settings.TLS.ALPN = []string{"h2"}
	line = surgeProxy(single, single.name)
	if !strings.Contains(line, "alpn=h2") || strings.Contains(line, "alpn=\"") {
		t.Errorf("surge anytls line wrong for single alpn: %s", line)
	}

	none := mkTLSNode(t, "anytls")
	none.settings.TLS.ALPN = nil
	if strings.Contains(surgeProxy(none, none.name), "alpn=") {
		t.Errorf("surge anytls line should omit alpn when unset: %s", surgeProxy(none, none.name))
	}
}

// Regression: vless share URIs must carry alpn like trojan/anytls URIs do.
// It previously emitted fp/sni but silently dropped the ALPN list.
func TestVlessURLCarriesALPN(t *testing.T) {
	n := mkTLSNode(t, "vless")
	link, err := singbox.BuildShareLink(n.clientNode())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "alpn=h2%2Chttp%2F1.1") {
		t.Errorf("vless URI missing alpn: %s", link)
	}
	if !strings.Contains(link, "fp=chrome") {
		t.Errorf("vless URI missing fp: %s", link)
	}
}
