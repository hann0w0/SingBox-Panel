package panel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// TestSurgeTUICUsesV5 guards the Surge TUIC wire version: sing-box servers are
// TUIC v5 (uuid+password), so the Surge policy must be `tuic-v5` with the v5
// credential pair — `tuic` + token is v4 and cannot authenticate.
func TestSurgeTUICUsesV5(t *testing.T) {
	n := customNodeFromParams(t, "tuic", `{"uuid":"75d809e7-b5c5-4cf6-b0b6-836916cc9f45","password":"secret","sni":"tuic.example.com","alpn":"h3"}`)
	line := surgeProxy(n, "tuic node")
	if !strings.Contains(line, "= tuic-v5, ") {
		t.Fatalf("Surge TUIC must use tuic-v5 keyword: %s", line)
	}
	if !strings.Contains(line, "uuid=75d809e7-b5c5-4cf6-b0b6-836916cc9f45") || !strings.Contains(line, "password=secret") {
		t.Fatalf("Surge TUIC v5 needs uuid+password: %s", line)
	}
	if strings.Contains(line, "token=") {
		t.Fatalf("Surge TUIC v5 must not use the v4 token field: %s", line)
	}
	if !strings.Contains(line, "alpn=h3") {
		t.Fatalf("Surge TUIC alpn default should be h3: %s", line)
	}
}

// TestTUICRelayModeNormalization: the client vocabulary is native|quic. Legacy
// TUIC v4 values (nat/stable/quirky) must normalize to native and never reach
// sing-box's strict enum; unset must stay unset in sing-box output.
func TestTUICRelayModeNormalization(t *testing.T) {

	// quic stays quic everywhere.
	n := customNodeFromParams(t, "tuic", `{"uuid":"75d809e7-b5c5-4cf6-b0b6-836916cc9f45","password":"secret","sni":"tuic.example.com","udp_relay_mode":"quic"}`)
	if got := n.settings.TUICRelayModeValue(); got != "quic" {
		t.Fatalf("quic relay mode = %q", got)
	}
	if line := surgeProxy(n, "t"); strings.Contains(line, "udp-relay-mode") {
		t.Fatalf("Surge has no udp-relay-mode parameter: %s", line)
	}

	// stable (TUIC v4) normalizes to native.
	n = customNodeFromParams(t, "tuic", `{"uuid":"75d809e7-b5c5-4cf6-b0b6-836916cc9f45","password":"secret","sni":"tuic.example.com","udp_relay_mode":"stable"}`)
	if got := n.settings.TUICRelayModeValue(); got != "native" {
		t.Fatalf("stable relay mode should normalize to native, got %q", got)
	}
	out, err := singbox.BuildClientOutbound(n.clientNode())
	if err != nil {
		t.Fatal(err)
	}
	var ob map[string]any
	if err := json.Unmarshal(out, &ob); err != nil {
		t.Fatal(err)
	}
	if ob["udp_relay_mode"] != "native" {
		t.Fatalf("sing-box tuic udp_relay_mode = %v, want native", ob["udp_relay_mode"])
	}
	if p := clashProxy(n, map[string]int{}); p["udp-relay-mode"] != "native" {
		t.Fatalf("Clash tuic udp-relay-mode = %v, want native", p["udp-relay-mode"])
	}

	// Unset stays unset in sing-box output (client default), Clash defaults native.
	n = customNodeFromParams(t, "tuic", `{"uuid":"75d809e7-b5c5-4cf6-b0b6-836916cc9f45","password":"secret","sni":"tuic.example.com"}`)
	out, err = singbox.BuildClientOutbound(n.clientNode())
	if err != nil {
		t.Fatal(err)
	}
	var ob2 map[string]any
	if err := json.Unmarshal(out, &ob2); err != nil {
		t.Fatal(err)
	}
	if _, has := ob2["udp_relay_mode"]; has {
		t.Fatalf("unset relay mode must not be emitted: %s", out)
	}
	if p := clashProxy(n, map[string]int{}); p["udp-relay-mode"] != "native" {
		t.Fatalf("Clash tuic default udp-relay-mode = %v, want native", p["udp-relay-mode"])
	}
}

// TestSingboxOutboundNoInvalidFields: fields sing-box's schema does not define
// must not leak into generated outbounds (they are silently ignored at best).
func TestSingboxOutboundNoInvalidFields(t *testing.T) {
	// anytls: no udp_over_stream in the outbound JSON.
	n := customNodeFromParams(t, "anytls", `{"password":"secret","sni":"any.example.com","udp_over_stream":true}`)
	raw, err := singbox.BuildClientOutbound(n.clientNode())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "udp_over_stream") {
		t.Fatalf("sing-box anytls outbound must not contain udp_over_stream: %s", raw)
	}

	// trojan / ss: no packet_encoding (vless-only option).
	for _, tc := range []struct {
		typ, params string
	}{
		{"trojan", `{"password":"secret","sni":"t.example.com"}`},
		{"shadowsocks", `{"method":"2022-blake3-aes-128-gcm","password":"secret"}`},
	} {
		n := customNodeFromParams(t, tc.typ, tc.params)
		raw, err := singbox.BuildClientOutbound(n.clientNode())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "packet_encoding") {
			t.Fatalf("sing-box %s outbound must not contain packet_encoding: %s", tc.typ, raw)
		}
	}
}

// TestAnyTLSShareLinkFingerprint: anytls:// must carry fp like the other TCP
// protocols (chrome default), not only when explicitly configured.
func TestAnyTLSShareLinkFingerprint(t *testing.T) {
	n := customNodeFromParams(t, "anytls", `{"password":"secret","sni":"any.example.com"}`)
	link, err := singbox.BuildShareLink(n.clientNode())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "fp=chrome") {
		t.Fatalf("anytls:// must advertise fp=chrome by default: %s", link)
	}
	if strings.Contains(link, "udp_over_stream") {
		t.Fatalf("anytls:// should not claim udp_over_stream when unset: %s", link)
	}

	n2 := customNodeFromParams(t, "anytls", `{"password":"secret","sni":"any.example.com","udp_over_stream":true,"fingerprint":"firefox"}`)
	link, err = singbox.BuildShareLink(n2.clientNode())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "fp=firefox") || !strings.Contains(link, "udp_over_stream=1") {
		t.Fatalf("anytls:// must keep explicit fp and udp_over_stream: %s", link)
	}
}
