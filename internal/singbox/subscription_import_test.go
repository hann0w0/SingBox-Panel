package singbox

import (
	"encoding/base64"
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
    version: 5
    udp: true
    obfs-opts:
      mode: http
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
	headers, ok := trojan.Params["headers"].(map[string]string)
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
	if snell.Params["psk"] != "123456789012" || snell.Params["version"] != 5 || snell.Params["obfs_mode"] != "http" {
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

[Proxy Group]
Auto = url-test, SS, Trojan
`
	result, err := ParseSubscription([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceType != "surge" || len(result.Nodes) != 3 {
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
	headers, ok := trojan.Params["headers"].(map[string]string)
	if !ok || headers["X-Test"] != "one" {
		t.Fatalf("surge trojan headers = %#v", trojan.Params["headers"])
	}
	hy2 := byName["HY2"]
	if hy2.Params["obfs_password"] != "obfs-secret" || hy2.Params["up_mbps"] != 30 || hy2.Params["down_mbps"] != 80 {
		t.Fatalf("surge hy2 params = %#v", hy2.Params)
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
