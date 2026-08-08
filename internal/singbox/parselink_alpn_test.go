package singbox

import (
	"strings"
	"testing"
)

// TestParseVlessCarriesALPN guards the import side of the vless round-trip:
// a link with alpn/fp must not drop them when adopted as a custom node.
func TestParseVlessCarriesALPN(t *testing.T) {
	link := "vless://12345678-1234-1234-1234-123456789012@cdn.example.com:443?encryption=none&security=tls&type=tcp&sni=cdn.example.com&fp=firefox&alpn=h2,http%2F1.1&packetEncoding=xudp#TestNode"
	cn, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}
	if cn.Settings.TLS.Fingerprint != "firefox" {
		t.Errorf("fingerprint = %q, want firefox", cn.Settings.TLS.Fingerprint)
	}
	if len(cn.Settings.TLS.ALPN) != 2 || cn.Settings.TLS.ALPN[0] != "h2" || cn.Settings.TLS.ALPN[1] != "http/1.1" {
		t.Errorf("alpn = %v, want [h2 http/1.1]", cn.Settings.TLS.ALPN)
	}

	// Round-trip: re-exporting must keep alpn (and fp).
	out, err := BuildShareLink(cn)
	if err != nil {
		t.Fatalf("BuildShareLink: %v", err)
	}
	if !strings.Contains(out, "alpn=h2%2Chttp%2F1.1") && !strings.Contains(out, "alpn=h2,http%2F1.1") {
		t.Errorf("round-trip lost alpn: %s", out)
	}
	if !strings.Contains(out, "fp=firefox") {
		t.Errorf("round-trip lost fp: %s", out)
	}
}
