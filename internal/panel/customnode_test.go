package panel

import (
	"encoding/json"
	"reflect"
	"strings"
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

func customNodeFromParams(t *testing.T, protocol, params string) node {
	t.Helper()
	n, ok := (&App{}).customNodeToNode(&model.CustomNode{
		Name:     protocol + " imported",
		Protocol: protocol,
		Address:  "203.0.113.10",
		Port:     443,
		Params:   model.JSONText(params),
	})
	if !ok {
		t.Fatalf("customNodeToNode(%s) failed", protocol)
	}
	return n
}

func TestCustomNodeToNodeMapsVMessTLSAndTransport(t *testing.T) {
	n := customNodeFromParams(t, "vmess", `{
		"uuid":"75d809e7-b5c5-4cf6-b0b6-836916cc9f45",
		"security":"chacha20-poly1305",
		"alter_id":7,
		"tls":"tls",
		"sni":"origin.example.com",
		"fingerprint":"firefox",
		"skip_cert_verify":true,
		"alpn":" h2, http/1.1 ",
		"transport":"ws",
		"path":"/socket",
		"host":"cdn.example.com",
		"headers":{"X-Imported":"yes"},
		"max_early_data":2048,
		"early_data_header_name":"Sec-WebSocket-Protocol",
		"udp":false
	}`)

	if n.user.UUID != "75d809e7-b5c5-4cf6-b0b6-836916cc9f45" {
		t.Fatalf("UUID = %q", n.user.UUID)
	}
	if n.settings.VMessSecurity != "chacha20-poly1305" || n.settings.VMessAlterID != 7 {
		t.Fatalf("VMess settings = security %q, alter id %d", n.settings.VMessSecurity, n.settings.VMessAlterID)
	}
	if !n.settings.TLS.Enabled || !n.settings.TLS.Insecure || n.settings.TLS.ServerName != "origin.example.com" || n.settings.TLS.Fingerprint != "firefox" {
		t.Fatalf("TLS settings = %#v", n.settings.TLS)
	}
	if got, want := n.settings.TLS.ALPN, []string{"h2", "http/1.1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ALPN = %#v, want %#v", got, want)
	}
	transport := n.settings.Transport
	if transport.Type != "ws" || transport.Path != "/socket" || transport.Headers["Host"] != "cdn.example.com" || transport.Headers["X-Imported"] != "yes" {
		t.Fatalf("transport = %#v", transport)
	}
	if transport.MaxEarlyData != 2048 || transport.EarlyDataHeader != "Sec-WebSocket-Protocol" {
		t.Fatalf("early data settings = %#v", transport)
	}
	if n.udpEnabled() {
		t.Fatal("explicit udp=false was not preserved")
	}

	raw, err := singbox.BuildClientOutbound(n.clientNode())
	if err != nil {
		t.Fatal(err)
	}
	var outbound map[string]any
	if err := json.Unmarshal(raw, &outbound); err != nil {
		t.Fatal(err)
	}
	if outbound["security"] != "chacha20-poly1305" || outbound["alter_id"] != float64(7) {
		t.Fatalf("outbound lost VMess settings: %s", raw)
	}
	clash := clashProxy(n, map[string]int{})
	if clash["udp"] != false || clash["skip-cert-verify"] != true {
		t.Fatalf("Clash flags = %#v", clash)
	}
}

func TestCustomNodeToNodeMapsRealityAndWSAliases(t *testing.T) {
	n := customNodeFromParams(t, "vless", `{
		"uuid":"75d809e7-b5c5-4cf6-b0b6-836916cc9f45",
		"packet_encoding":"xudp",
		"tls":"reality",
		"sni":"www.example.com",
		"pbk":"public-key",
		"sid":"abcd, ef01",
		"fingerprint":"chrome",
		"network":"http-upgrade",
		"path":"/upgrade",
		"host":"edge.example.com"
	}`)

	if n.settings.Transport.Type != "httpupgrade" || n.settings.Transport.Path != "/upgrade" || n.settings.Transport.Headers["Host"] != "edge.example.com" {
		t.Fatalf("transport = %#v", n.settings.Transport)
	}
	reality := n.settings.TLS.Reality
	if n.settings.PacketEncoding != "xudp" || !n.settings.TLS.Enabled || !reality.Enabled || reality.PublicKey != "public-key" || !reflect.DeepEqual(reality.ShortID, []string{"abcd", "ef01"}) {
		t.Fatalf("reality = %#v", reality)
	}
	proxy := clashProxy(n, map[string]int{})
	opts, _ := proxy["reality-opts"].(map[string]any)
	if opts["public-key"] != "public-key" || opts["short-id"] != "abcd" {
		t.Fatalf("Clash REALITY options = %#v", proxy)
	}
}

func TestCustomNodeToNodeMapsProtocolSpecificImportedParams(t *testing.T) {
	t.Run("shadowsocks plugin and udp", func(t *testing.T) {
		n := customNodeFromParams(t, "ss", `{"method":"aes-256-gcm","password":"secret","plugin":"v2ray-plugin","ss_plugin_opts":{"mode":"websocket","host":"cdn.example.com"},"udp":true}`)
		if n.typ != "shadowsocks" || n.settings.SSPlugin != "v2ray-plugin;host=cdn.example.com;mode=websocket" {
			t.Fatalf("Shadowsocks node = %#v", n)
		}
		link, err := singbox.BuildShareLink(n.clientNode())
		if err != nil || !strings.Contains(link, "plugin=") {
			t.Fatalf("share link lost plugin: %q, %v", link, err)
		}
		proxy := clashProxy(n, map[string]int{})
		opts, _ := proxy["plugin-opts"].(map[string]any)
		if proxy["plugin"] != "v2ray-plugin" || opts["host"] != "cdn.example.com" || opts["mode"] != "websocket" || proxy["udp"] != true {
			t.Fatalf("Clash Shadowsocks = %#v", proxy)
		}
	})

	t.Run("trojan defaults to TLS", func(t *testing.T) {
		n := customNodeFromParams(t, "trojan", `{"password":"secret","sni":"trojan.example.com","insecure":true,"udp":true}`)
		if !n.settings.TLS.Enabled || !n.settings.TLS.Insecure {
			t.Fatalf("TLS settings = %#v", n.settings.TLS)
		}
		line := surgeProxy(n, n.name)
		if !strings.Contains(line, "tls=true") || !strings.Contains(line, "skip-cert-verify=true") || !strings.Contains(line, "udp-relay=true") {
			t.Fatalf("Surge Trojan = %s", line)
		}
	})

	t.Run("hysteria2 obfs and bandwidth", func(t *testing.T) {
		n := customNodeFromParams(t, "hysteria2", `{"password":"secret","sni":"hy2.example.com","obfs_type":"salamander","obfs_password":"obfs-secret","up_mbps":80,"down_mbps":240,"insecure":true}`)
		st := n.settings
		if !st.TLS.Enabled || !st.TLS.Insecure || st.ObfsType != "salamander" || st.ObfsPassword != "obfs-secret" || st.UpMbps != 80 || st.DownMbps != 240 {
			t.Fatalf("Hysteria2 settings = %#v", st)
		}
		proxy := clashProxy(n, map[string]int{})
		if proxy["obfs"] != "salamander" || proxy["obfs-password"] != "obfs-secret" || proxy["up"] != "80 Mbps" || proxy["down"] != "240 Mbps" {
			t.Fatalf("Clash Hysteria2 = %#v", proxy)
		}
	})

	t.Run("tuic transport controls", func(t *testing.T) {
		n := customNodeFromParams(t, "tuic", `{"uuid":"75d809e7-b5c5-4cf6-b0b6-836916cc9f45","password":"secret","sni":"tuic.example.com","alpn":"h3","congestion_controller":"bbr","udp_relay_mode":"stable","zero_rtt_handshake":true,"heartbeat":"15s","insecure":true}`)
		st := n.settings
		// Legacy TUIC v4 relay-mode values (nat/stable/quirky) are normalized to
		// native, the vocabulary sing-box/mihomo/Surge all understand.
		if !st.TLS.Enabled || st.CongestionControl != "bbr" || st.TUICUDPRelayMode != "native" || !st.ZeroRTTHandshake || st.Heartbeat != "15s" {
			t.Fatalf("TUIC settings = %#v", st)
		}
		raw, err := singbox.BuildClientOutbound(n.clientNode())
		if err != nil {
			t.Fatal(err)
		}
		var outbound map[string]any
		if err := json.Unmarshal(raw, &outbound); err != nil {
			t.Fatal(err)
		}
		if outbound["congestion_control"] != "bbr" || outbound["udp_relay_mode"] != "native" || outbound["zero_rtt_handshake"] != true || outbound["heartbeat"] != "15s" {
			t.Fatalf("sing-box TUIC = %s", raw)
		}
	})

	t.Run("anytls udp over stream", func(t *testing.T) {
		n := customNodeFromParams(t, "anytls", `{"password":"secret","sni":"anytls.example.com","udp_over_stream":true,"udp":true}`)
		if !n.settings.TLS.Enabled || !n.settings.AnyTLSUDPOverStream {
			t.Fatalf("AnyTLS settings = %#v", n.settings)
		}
		raw, err := singbox.BuildClientOutbound(n.clientNode())
		if err != nil {
			t.Fatal(err)
		}
		// sing-box's anytls outbound has no udp_over_stream field (AnyTLS UDP
		// is always carried over the stream), so it must not be emitted.
		if strings.Contains(string(raw), "udp_over_stream") {
			t.Fatalf("sing-box AnyTLS must not emit udp_over_stream: %s", raw)
		}
		proxy := clashProxy(n, map[string]int{})
		if proxy["udp"] != true {
			t.Fatalf("Clash AnyTLS = %#v", proxy)
		}
		// mihomo's anytls outbound has no udp-over-stream option (AnyTLS UDP is
		// always carried over the stream); `udp: true` is all it needs.
		if _, has := proxy["udp-over-stream"]; has {
			t.Fatalf("Clash AnyTLS must not emit udp-over-stream: %#v", proxy)
		}
	})

	t.Run("snell credentials and obfs", func(t *testing.T) {
		n := customNodeFromParams(t, "snell", `{"psk":"snell-secret","userkey":"snell-user","reuse":true,"network":"udp","version":5,"obfs":"http","obfs_host":"cdn.example.com","udp":true}`)
		st := n.settings
		if st.SnellPSK != "snell-secret" || !st.SnellReuse ||
			st.SnellNetwork != "udp" || st.SnellVersion != 5 || st.SnellObfsMode != "http" || st.SnellObfsHost != "cdn.example.com" {
			t.Fatalf("Snell settings = %#v", st)
		}
		raw, err := singbox.BuildClientOutbound(n.clientNode())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"version":4`) || strings.Contains(string(raw), `userkey`) ||
			!strings.Contains(string(raw), `"network":"udp"`) || !strings.Contains(string(raw), `"obfs_host":"cdn.example.com"`) {
			t.Fatalf("sing-box Snell = %s", raw)
		}
		proxy := clashProxy(n, map[string]int{})
		opts, _ := proxy["obfs-opts"].(map[string]any)
		if proxy["psk"] != "snell-secret" || proxy["version"] != 5 || opts["mode"] != "http" || proxy["udp"] != true {
			t.Fatalf("Clash Snell = %#v", proxy)
		}
		if line := surgeProxy(n, n.name); !strings.Contains(line, "psk=snell-secret") || !strings.Contains(line, "version=5") || !strings.Contains(line, "obfs=http") {
			t.Fatalf("Surge Snell = %s", line)
		}
	})
}

func TestClashImportParamsRoundTripThroughCustomNode(t *testing.T) {
	result, err := singbox.ParseSubscription([]byte(`proxies:
  - name: vmess-full
    type: vmess
    server: vm.example.com
    port: 443
    uuid: 75d809e7-b5c5-4cf6-b0b6-836916cc9f45
    cipher: aes-128-gcm
    alter-id: 3
    tls: true
    servername: origin.example.com
    skip-cert-verify: true
    alpn: [h2, http/1.1]
    client-fingerprint: safari
    network: ws
    ws-opts:
      path: /socket
      headers:
        Host: cdn.example.com
      max-early-data: 1024
      early-data-header-name: Sec-WebSocket-Protocol
  - name: ss-plugin
    type: ss
    server: ss.example.com
    port: 8388
    cipher: aes-256-gcm
    password: ss-secret
    plugin: v2ray-plugin
    plugin-opts:
      mode: websocket
      host: cdn.example.com
    udp: true
  - name: hy2-full
    type: hysteria2
    server: hy2.example.com
    port: 443
    password: hy2-secret
    sni: origin.example.com
    skip-cert-verify: true
    obfs: salamander
    obfs-password: obfs-secret
    up: 60 Mbps
    down: 180 Mbps
  - name: tuic-full
    type: tuic
    server: tuic.example.com
    port: 443
    uuid: 75d809e7-b5c5-4cf6-b0b6-836916cc9f45
    password: tuic-secret
    sni: origin.example.com
    congestion-controller: bbr
    udp-relay-mode: stable
    reduce-rtt: true
    heartbeat-interval: 15000
  - name: anytls-full
    type: anytls
    server: anytls.example.com
    port: 443
    password: anytls-secret
    sni: origin.example.com
    udp-over-stream: true
  - name: snell-full
    type: snell
    server: snell.example.com
    port: 443
    psk: snell-secret
    version: 5
    obfs-opts:
      mode: http
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 0 || len(result.Nodes) != 6 {
		t.Fatalf("parsed nodes=%d skipped=%#v", len(result.Nodes), result.Skipped)
	}

	nodes := make(map[string]node, len(result.Nodes))
	for _, imported := range result.Nodes {
		params, err := json.Marshal(imported.Params)
		if err != nil {
			t.Fatal(err)
		}
		n, ok := (&App{}).customNodeToNode(&model.CustomNode{
			Name: imported.Name, Protocol: imported.Protocol, Address: imported.Address,
			Port: imported.Port, Params: model.JSONText(params),
		})
		if !ok {
			t.Fatalf("customNodeToNode(%s) failed with params %s", imported.Name, params)
		}
		nodes[imported.Name] = n
	}

	vmess := nodes["vmess-full"].settings
	if vmess.VMessSecurity != "aes-128-gcm" || vmess.VMessAlterID != 3 || !vmess.TLS.Insecure || vmess.TLS.Fingerprint != "safari" {
		t.Fatalf("VMess round trip = %#v", vmess)
	}
	if vmess.Transport.Type != "ws" || vmess.Transport.Path != "/socket" || vmess.Transport.Headers["Host"] != "cdn.example.com" || vmess.Transport.MaxEarlyData != 1024 || vmess.Transport.EarlyDataHeader != "Sec-WebSocket-Protocol" {
		t.Fatalf("VMess transport round trip = %#v", vmess.Transport)
	}

	ss := nodes["ss-plugin"]
	if ss.settings.SSPlugin != "v2ray-plugin;host=cdn.example.com;mode=websocket" || !ss.udpEnabled() {
		t.Fatalf("Shadowsocks round trip = %#v", ss)
	}
	hy2 := nodes["hy2-full"].settings
	if hy2.ObfsType != "salamander" || hy2.ObfsPassword != "obfs-secret" || hy2.UpMbps != 60 || hy2.DownMbps != 180 || !hy2.TLS.Insecure {
		t.Fatalf("Hysteria2 round trip = %#v", hy2)
	}
	tuic := nodes["tuic-full"].settings
	if tuic.CongestionControl != "bbr" || tuic.TUICUDPRelayMode != "native" || !tuic.ZeroRTTHandshake || tuic.Heartbeat != "15s" {
		t.Fatalf("TUIC round trip = %#v", tuic)
	}
	if anytls := nodes["anytls-full"].settings; !anytls.TLS.Enabled || !anytls.AnyTLSUDPOverStream {
		t.Fatalf("AnyTLS round trip = %#v", anytls)
	}
	snell := nodes["snell-full"].settings
	if snell.SnellPSK != "snell-secret" || snell.SnellVersion != 5 || snell.SnellObfsMode != "http" {
		t.Fatalf("Snell round trip = %#v", snell)
	}
}
