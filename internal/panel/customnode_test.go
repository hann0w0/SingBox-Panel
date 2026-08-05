package panel

import (
	"encoding/json"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

// TestCustomTrojanTLSDump renders a manually-entered trojan+TLS node (with the
// full credential set: insecure, alpn, fingerprint, ws transport) through every
// subscription format and prints the exact output so missing params are visible.
func TestCustomTrojanTLSDump(t *testing.T) {
	c := &model.CustomNode{
		Name:     "MyTrojan",
		Protocol: "trojan",
		Address:  "example.com",
		Port:     443,
		Params:   model.JSONText(`{"password":"mypass123","tls":"tls","sni":"cdn.example.com","fingerprint":"firefox","insecure":true,"alpn":"h2,http/1.1","transport":"ws","path":"/ws","host":"cdn.example.com"}`),
	}
	n, ok := (&App{}).customNodeToNode(c)
	if !ok {
		t.Fatal("convert failed")
	}
	cn := singbox.ClientNode{
		Name: n.name, Server: n.server, ServerPort: n.port, Type: n.typ,
		Settings: n.settings, User: n.user,
	}
	link, _ := singbox.BuildShareLink(cn)
	t.Logf("link:     %s", link)

	sb, _ := singbox.BuildClientOutbound(cn)
	t.Logf("singbox:  %s", string(sb))

	clash := clashProxy(n, map[string]int{})
	raw, _ := json.Marshal(clash)
	t.Logf("clash:    %s", string(raw))
}
