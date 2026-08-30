package singbox

import (
	"encoding/base64"
	"math"
	"strings"
	"testing"
)

func TestParseSubscriptionShareLinksAndBase64(t *testing.T) {
	uuid := "a2b0c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
	plain := strings.Join([]string{
		"vless://" + uuid + "@edge.example.com:443?security=tls&sni=cdn.example.com&skip-cert-verify=true&type=ws&path=%2Fws&host=cdn.example.com#HK",
		"not-a-node",
		"trojan://secret@trojan.example.com:443?sni=trojan.example.com&allowInsecure=1#Trojan",
	}, "\n")

	result, err := ParseSubscription([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(result.Nodes), 2; got != want {
		t.Fatalf("nodes = %d, want %d: %+v", got, want, result)
	}
	if got, want := len(result.Skipped), 1; got != want {
		t.Fatalf("skipped = %d, want %d", got, want)
	}
	if result.Nodes[0].Params["insecure"] != true || result.Nodes[0].Params["transport"] != "ws" {
		t.Fatalf("vless params lost: %#v", result.Nodes[0].Params)
	}
	if result.Nodes[0].Params["host"] != "cdn.example.com" || result.Nodes[0].Params["path"] != "/ws" {
		t.Fatalf("vless WS params lost: %#v", result.Nodes[0].Params)
	}

	// Padded standard base64 is the most common provider format. The encoded
	// text deliberately includes bytes whose encoding contains '/' so both '/'
	// and '=' remain accepted base64 characters.
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	decoded, err := ParseSubscription([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SourceType != "base64" || len(decoded.Nodes) != 2 || len(decoded.Skipped) != 1 {
		t.Fatalf("base64 result = %+v", decoded)
	}
}

func TestParseSubscriptionSingBoxJSONOutbounds(t *testing.T) {
	raw := []byte(`{
  "outbounds": [
    {
      "tag": "🇭🇰 Gomami-HKT",
      "type": "trojan",
      "server": "edge.example.com",
      "server_port": 443,
      "password": "secret",
      "tls": {"enabled": true, "server_name": "cdn.example.com", "insecure": false}
    },
    {"tag": "direct", "type": "direct"},
    {"tag": "Auto", "type": "urltest", "outbounds": ["🇭🇰 Gomami-HKT"]}
  ],
  "endpoints": []
}`)

	result, err := ParseSubscription(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceType != "sing-box-json" || len(result.Nodes) != 1 || len(result.Skipped) != 2 {
		t.Fatalf("result = %+v", result)
	}
	node := result.Nodes[0]
	if node.Name != "🇭🇰 Gomami-HKT" || node.Protocol != "trojan" || node.Address != "edge.example.com" || node.Port != 443 {
		t.Fatalf("node = %+v", node)
	}
	if node.Link != "" || node.Params["password"] != "secret" || node.Params["tls"] != "tls" || node.Params["sni"] != "cdn.example.com" {
		t.Fatalf("node params = %#v", node.Params)
	}
}

func TestParseSubscriptionV2RayJSONOutbounds(t *testing.T) {
	raw := []byte(`{
  "outbounds": [
    {
      "tag": "VMess WS",
      "protocol": "vmess",
      "settings": {"vnext": [{"address": "vm.example.com", "port": 443, "users": [{"id": "vm-uuid", "alterId": 0, "security": "aes-128-gcm"}]}]},
      "streamSettings": {
        "network": "ws",
        "security": "tls",
        "tlsSettings": {"serverName": "cdn.example.com", "allowInsecure": true, "alpn": ["h2"]},
        "wsSettings": {"path": "/gateway", "headers": {"Host": "cdn.example.com"}}
      }
    },
    {
      "tag": "VLESS Reality",
      "protocol": "vless",
      "settings": {"vnext": [{"address": "vl.example.com", "port": 443, "users": [{"id": "vl-uuid", "flow": "xtls-rprx-vision"}]}]},
      "streamSettings": {
        "network": "tcp",
        "security": "reality",
        "realitySettings": {"serverName": "www.example.com", "publicKey": "public-key", "shortId": "abcd"}
      }
    },
    {
      "tag": "Trojan",
      "protocol": "trojan",
      "settings": {"servers": [{"address": "tr.example.com", "port": 443, "password": "tr-secret"}]},
      "streamSettings": {"network": "tcp", "security": "tls", "tlsSettings": {"serverName": "tr.example.com"}}
    },
    {
      "tag": "Shadowsocks",
      "protocol": "shadowsocks",
      "settings": {"servers": [{"address": "ss.example.com", "port": 8388, "method": "aes-256-gcm", "password": "ss-secret"}]}
    },
    {
      "tag": "SOCKS",
      "protocol": "socks",
      "settings": {"servers": [{"address": "socks.example.com", "port": 1080, "users": [{"user": "alice", "pass": "secret"}]}]}
    },
    {"tag": "Unsupported", "protocol": "vmess", "settings": {"vnext": [{"address": "grpc.example.com", "port": 443, "users": [{"id": "grpc-uuid"}]}]}, "streamSettings": {"network": "grpc"}}
  ]
}`)

	result, err := ParseSubscription(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceType != "v2ray-json" || len(result.Nodes) != 5 || len(result.Skipped) != 1 {
		t.Fatalf("result = %+v", result)
	}
	byName := map[string]ImportedNode{}
	for _, node := range result.Nodes {
		byName[node.Name] = node
	}
	vmess := byName["VMess WS"]
	if vmess.Protocol != "vmess" || vmess.Params["uuid"] != "vm-uuid" || vmess.Params["security"] != "aes-128-gcm" || vmess.Params["transport"] != "ws" || vmess.Params["host"] != "cdn.example.com" || vmess.Params["insecure"] != true {
		t.Fatalf("vmess = %+v", vmess)
	}
	vless := byName["VLESS Reality"]
	if vless.Protocol != "vless" || vless.Params["uuid"] != "vl-uuid" || vless.Params["flow"] != "xtls-rprx-vision" || vless.Params["tls"] != "reality" || vless.Params["pbk"] != "public-key" || vless.Params["sid"] != "abcd" {
		t.Fatalf("vless = %+v", vless)
	}
	if got := byName["Shadowsocks"].Params; got["method"] != "aes-256-gcm" || got["password"] != "ss-secret" {
		t.Fatalf("shadowsocks = %#v", got)
	}
	if got := byName["SOCKS"].Params; got["username"] != "alice" || got["password"] != "secret" {
		t.Fatalf("socks = %#v", got)
	}
}

func TestParseSubscriptionQuantumultXServerLocal(t *testing.T) {
	raw := `
[server_remote]
https://provider.example.com/profile

[server_local]
vmess=vm.example.com:443, method=chacha20-poly1305, password=vm-uuid, obfs=ws, obfs-host=cdn.example.com, obfs-uri=/gateway, over-tls=true, tls-host=cdn.example.com, tag=VMess WS
trojan=tr.example.com:443, password=tr-secret, over-tls=true, tls-host=tr.example.com, tls-verification=false, tag=Trojan
shadowsocks=ss.example.com:8388, method=aes-256-gcm, password=ss-secret, tag=SS
bad.example.com:443, tag=broken
`

	result, err := ParseSubscription([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceType != "quantumult-x" || len(result.Nodes) != 3 || len(result.Skipped) != 1 {
		t.Fatalf("result = %+v", result)
	}
	byName := map[string]ImportedNode{}
	for _, node := range result.Nodes {
		byName[node.Name] = node
	}
	vmess := byName["VMess WS"]
	if vmess.Protocol != "vmess" || vmess.Params["uuid"] != "vm-uuid" || vmess.Params["security"] != "chacha20-poly1305" || vmess.Params["transport"] != "ws" || vmess.Params["path"] != "/gateway" || vmess.Params["host"] != "cdn.example.com" {
		t.Fatalf("vmess = %+v", vmess)
	}
	trojan := byName["Trojan"]
	if trojan.Protocol != "trojan" || trojan.Params["password"] != "tr-secret" || trojan.Params["insecure"] != true || trojan.Params["sni"] != "tr.example.com" {
		t.Fatalf("trojan = %+v", trojan)
	}
	ss := byName["SS"]
	if ss.Protocol != "shadowsocks" || ss.Params["method"] != "aes-256-gcm" || ss.Params["password"] != "ss-secret" {
		t.Fatalf("shadowsocks = %+v", ss)
	}
}

func TestParseSubscriptionSIP008JSON(t *testing.T) {
	raw := []byte(`[
  {"server": "ss.example.com", "server_port": 8388, "method": "aes-256-gcm", "password": "secret", "remarks": "SS 1"},
  {"server": "ss2.example.com", "server_port": 443, "method": "2022-blake3-aes-128-gcm", "password": "secret2", "remarks": "SS 2", "plugin": "obfs-local", "plugin_opts": "obfs=http;obfs-host=cdn.example.com"}
]`)

	result, err := ParseSubscription(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceType != "sip008-json" || len(result.Nodes) != 2 || len(result.Skipped) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got := result.Nodes[0]; got.Name != "SS 1" || got.Protocol != "shadowsocks" || got.Params["method"] != "aes-256-gcm" || got.Params["password"] != "secret" {
		t.Fatalf("first node = %+v", got)
	}
	if got := result.Nodes[1].Params["ss_plugin"]; got != "obfs-local;obfs=http;obfs-host=cdn.example.com" {
		t.Fatalf("plugin = %#v", got)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	encodedResult, err := ParseSubscription([]byte(encoded))
	if err != nil || encodedResult.SourceType != "sip008-json" || len(encodedResult.Nodes) != 2 {
		t.Fatalf("encoded result = %+v, err=%v", encodedResult, err)
	}
}

func TestParseSubscriptionBase64StructuredProfiles(t *testing.T) {
	clashJSON := `{"proxies":[{"name":"SS","type":"ss","server":"ss.example.com","port":8388,"cipher":"aes-256-gcm","password":"secret"}]}`
	clashResult, err := ParseSubscription([]byte(base64.StdEncoding.EncodeToString([]byte(clashJSON))))
	if err != nil || clashResult.SourceType != "clash-yaml" || len(clashResult.Nodes) != 1 {
		t.Fatalf("base64 Clash JSON result = %+v, err=%v", clashResult, err)
	}

	quantumultX := `[server_local]
trojan=tr.example.com:443, password=secret, over-tls=true, tag=Trojan`
	qxResult, err := ParseSubscription([]byte(base64.StdEncoding.EncodeToString([]byte(quantumultX))))
	if err != nil || qxResult.SourceType != "quantumult-x" || len(qxResult.Nodes) != 1 {
		t.Fatalf("base64 Quantumult X result = %+v, err=%v", qxResult, err)
	}
}

func TestParseSubscriptionClashYAMLPreservesClientOptions(t *testing.T) {
	raw := `
proxies:
  - name: Trojan WS
    type: trojan
    server: tr.example.com
    port: 443
    password: secret
    udp: true
    sni: cdn.example.com
    skip-cert-verify: true
    client-fingerprint: safari
    alpn: [h2, http/1.1]
    network: ws
    ws-opts:
      path: /gateway
      max-early-data: 2048
      early-data-header-name: Sec-WebSocket-Protocol
      headers:
        Host: cdn.example.com
        X-Test: one
  - name: SOCKS
    type: socks5
    server: socks.example.com
    port: 1080
    username: alice
    password: wonderland
    udp: true
  - name: HY2
    type: hysteria2
    server: hy.example.com
    port: 8443
    password: hy-secret
    sni: hy.example.com
    skip-cert-verify: true
    salamander-password: obfs-secret
    upload-bandwidth: 50 Mbps
    download-bandwidth: 100 Mbps
  - name: Snell
    type: snell
    server: snell.example.com
    port: 10023
    psk: 123456789012
    userkey: snell-user
    reuse: true
    network: udp
    version: 5
    udp: true
    obfs-opts:
      mode: http
      host: cdn.example.com
  - name: Unsupported
    type: wireguard
    server: wg.example.com
    port: 51820
`
	result, err := ParseSubscription([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceType != "clash-yaml" || len(result.Nodes) != 4 || len(result.Skipped) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	byName := map[string]ImportedNode{}
	for _, node := range result.Nodes {
		byName[node.Name] = node
	}
	trojan := byName["Trojan WS"]
	if trojan.Link != "" || trojan.Protocol != "trojan" || trojan.Address != "tr.example.com" || trojan.Port != 443 {
		t.Fatalf("trojan = %+v", trojan)
	}
	if trojan.Params["insecure"] != true || trojan.Params["udp"] != true || trojan.Params["transport"] != "ws" {
		t.Fatalf("trojan common params = %#v", trojan.Params)
	}
	if trojan.Params["host"] != "cdn.example.com" || trojan.Params["max_early_data"] != 2048 {
		t.Fatalf("trojan WS params = %#v", trojan.Params)
	}
	headers, ok := trojan.Params["headers"].(map[string]any)
	if !ok || headers["X-Test"] != "one" {
		t.Fatalf("trojan headers = %#v", trojan.Params["headers"])
	}
	socks := byName["SOCKS"]
	if socks.Params["username"] != "alice" || socks.Params["password"] != "wonderland" {
		t.Fatalf("socks credentials = %#v", socks.Params)
	}
	hy2 := byName["HY2"]
	if hy2.Params["obfs"] != "salamander" || hy2.Params["obfs_password"] != "obfs-secret" ||
		hy2.Params["up_mbps"] != 50 || hy2.Params["down_mbps"] != 100 || hy2.Params["insecure"] != true {
		t.Fatalf("hy2 params = %#v", hy2.Params)
	}
	snell := byName["Snell"]
	if snell.Params["psk"] != "123456789012" || snell.Params["userkey"] != nil || snell.Params["reuse"] != true ||
		snell.Params["network"] != "udp" || snell.Params["version"] != 5 || snell.Params["obfs_mode"] != "http" ||
		snell.Params["obfs_host"] != "cdn.example.com" {
		t.Fatalf("snell params = %#v", snell.Params)
	}
}

func TestParseSubscriptionSurgeProxySection(t *testing.T) {
	raw := `
[General]
loglevel = notify

[Proxy]
SS = ss, ss.example.com, 8388, encrypt-method=aes-256-gcm, password=ss-secret, obfs=http, obfs-host=cdn.example.com, udp-relay=true
Trojan = trojan, tr.example.com, 443, password=tr-secret, sni=cdn.example.com, skip-cert-verify=true, ws=true, ws-path=/ws, ws-headers="Host:cdn.example.com|X-Test:one", udp-relay=true
HY2 = hysteria2, hy.example.com, 8443, password=hy-secret, sni=hy.example.com, skip-cert-verify=true, salamander-password=obfs-secret, upload-bandwidth=30 Mbps, download-bandwidth=80 Mbps
VMess Bare Flags = vmess, vm.example.com, 443, username=vm-uuid, tls, ws=true, ws-path=/ws

[Proxy Group]
Auto = url-test, SS, Trojan
`
	result, err := ParseSubscription([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceType != "surge" || len(result.Nodes) != 4 {
		t.Fatalf("result = %+v", result)
	}
	byName := map[string]ImportedNode{}
	for _, node := range result.Nodes {
		byName[node.Name] = node
	}
	ss := byName["SS"]
	if ss.Params["method"] != "aes-256-gcm" || ss.Params["password"] != "ss-secret" ||
		ss.Params["ss_plugin"] != "obfs-local;obfs=http;obfs-host=cdn.example.com" {
		t.Fatalf("surge ss params = %#v", ss.Params)
	}
	trojan := byName["Trojan"]
	if trojan.Params["path"] != "/ws" || trojan.Params["host"] != "cdn.example.com" || trojan.Params["insecure"] != true {
		t.Fatalf("surge trojan params = %#v", trojan.Params)
	}
	headers, ok := trojan.Params["headers"].(map[string]any)
	if !ok || headers["X-Test"] != "one" {
		t.Fatalf("surge trojan headers = %#v", trojan.Params["headers"])
	}
	hy2 := byName["HY2"]
	if hy2.Params["obfs_password"] != "obfs-secret" || hy2.Params["up_mbps"] != 30 || hy2.Params["down_mbps"] != 80 {
		t.Fatalf("surge hy2 params = %#v", hy2.Params)
	}
	vmess := byName["VMess Bare Flags"]
	if vmess.Params["uuid"] != "vm-uuid" || vmess.Params["transport"] != "ws" {
		t.Fatalf("surge vmess params = %#v", vmess.Params)
	}
}

func TestParseSubscriptionLoonPositionalProxySection(t *testing.T) {
	raw := `
[Proxy]
Loon SS = Shadowsocks, ss.example.com, 8388, aes-256-gcm, ss-secret
Loon VMess = VMess, vm.example.com, 443, aes-128-gcm, vm-uuid, ws=true, ws-path=/ws, tls=true, sni=cdn.example.com
Loon Trojan = Trojan, tr.example.com, 443, tr-secret, sni=tr.example.com, skip-cert-verify=true
`
	result, err := ParseSubscription([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceType != "surge-loon" || len(result.Nodes) != 3 || len(result.Skipped) != 0 {
		t.Fatalf("result = %+v", result)
	}
	byName := map[string]ImportedNode{}
	for _, node := range result.Nodes {
		byName[node.Name] = node
	}
	if got := byName["Loon SS"].Params; got["method"] != "aes-256-gcm" || got["password"] != "ss-secret" {
		t.Fatalf("ss = %#v", got)
	}
	if got := byName["Loon VMess"].Params; got["uuid"] != "vm-uuid" || got["security"] != "aes-128-gcm" || got["transport"] != "ws" || got["path"] != "/ws" || got["sni"] != "cdn.example.com" {
		t.Fatalf("vmess = %#v", got)
	}
	if got := byName["Loon Trojan"].Params; got["password"] != "tr-secret" || got["insecure"] != true {
		t.Fatalf("trojan = %#v", got)
	}
}

func TestParseSubscriptionLimitsAndUnsupportedInput(t *testing.T) {
	tooLarge := make([]byte, SubscriptionImportMaxBytes+1)
	if _, err := ParseSubscription(tooLarge); err == nil {
		t.Fatal("oversized subscription unexpectedly accepted")
	}
	result, err := ParseSubscription([]byte("https://subscription.example.com/profile"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 0 || len(result.Skipped) != 1 {
		t.Fatalf("HTTP URL must not be treated as a share link: %+v", result)
	}
}

func TestGetIntRejectsOverflowingNumericValues(t *testing.T) {
	for _, value := range []any{uint64(^uint(0)), math.Inf(1), math.Inf(-1), math.NaN(), float64(math.MaxInt64)} {
		if got := getInt(map[string]any{"port": value}, "port"); got != 0 {
			t.Fatalf("getInt(%v) = %d, want 0", value, got)
		}
	}
	if got := getInt(map[string]any{"port": float64(443.9)}, "port"); got != 443 {
		t.Fatalf("ordinary float conversion = %d, want 443", got)
	}
}
