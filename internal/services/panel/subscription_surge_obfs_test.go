package panel

import (
	"strings"
	"testing"
)

func TestSurgeShadowsocksExportPreservesSimpleObfs(t *testing.T) {
	for _, mode := range []string{"http", "tls"} {
		t.Run(mode, func(t *testing.T) {
			n := customNodeFromParams(t, "ss", `{"method":"aes-128-gcm","password":"secret","ss_plugin":"obfs-local;obfs=`+mode+`;obfs-host=cdn.example.com"}`)
			line := surgeProxy(n, "node")
			if !strings.Contains(line, "obfs="+mode) || !strings.Contains(line, "obfs-host=cdn.example.com") {
				t.Fatalf("simple-obfs lost from Surge export: %s", line)
			}
		})
	}
}

func TestSurgeSkipsShadowsocksWithUnsupportedPlugin(t *testing.T) {
	for _, plugin := range []string{"v2ray-plugin;mode=websocket", "obfs-local;obfs=http;unsupported=value", `obfs-local;obfs=http;obfs-host=bad"host`} {
		n := customNodeFromParams(t, "ss", `{"method":"aes-128-gcm","password":"secret","ss_plugin":"`+strings.ReplaceAll(plugin, `"`, `\"`)+`"}`)
		if line := surgeProxy(n, "node"); line != "" {
			t.Fatalf("unrepresentable plugin %q exported without its required options: %s", plugin, line)
		}
	}
}

func TestClashShadowsocksPluginVocabularyAndSurgeTrojanTransport(t *testing.T) {
	mergedObfs := mergeSSPluginOptions("obfs", map[string]any{"mode": "http", "host": "cdn.example.com"})
	if mergedObfs != "obfs-local;obfs-host=cdn.example.com;obfs=http" {
		t.Fatalf("mergeSSPluginOptions obfs = %q", mergedObfs)
	}
	name, opts := splitSSPluginOptions(mergedObfs)
	if name != "obfs-local" || opts["mode"] != "http" || opts["host"] != "cdn.example.com" {
		t.Fatalf("splitSSPluginOptions obfs = %q %+v", name, opts)
	}

	mergedV2 := mergeSSPluginOptions("v2ray-plugin", map[string]any{"mode": "websocket", "tls": true, "mux": false, "host": "ws.example.com", "path": "/v2"})
	if !strings.Contains(mergedV2, ";tls") || strings.Contains(mergedV2, "tls=true") || !strings.Contains(mergedV2, "mux=0") {
		t.Fatalf("mergeSSPluginOptions v2ray-plugin = %q", mergedV2)
	}
	v2Name, v2Opts := splitSSPluginOptions(mergedV2)
	if v2Name != "v2ray-plugin" || v2Opts["tls"] != true || v2Opts["mux"] != false {
		t.Fatalf("splitSSPluginOptions v2ray-plugin = %q %+v", v2Name, v2Opts)
	}

	// Surge Trojan skips REALITY and HTTPUpgrade, and preserves WebSocket options.
	realityTrojan := customNodeFromParams(t, "trojan", `{"password":"secret","tls":"reality","pbk":"pubkey","sni":"example.com"}`)
	if line := surgeProxy(realityTrojan, "tr-reality"); line != "" {
		t.Fatalf("expected Surge to skip Trojan+REALITY, got %q", line)
	}
	huTrojan := customNodeFromParams(t, "trojan", `{"password":"secret","tls":"tls","sni":"example.com","transport":"httpupgrade","path":"/up"}`)
	if line := surgeProxy(huTrojan, "tr-hu"); line != "" {
		t.Fatalf("expected Surge to skip Trojan+HTTPUpgrade, got %q", line)
	}
	wsTrojan := customNodeFromParams(t, "trojan", `{"password":"secret","tls":"tls","sni":"example.com","transport":"ws","path":"/ws","host":"ws.example.com"}`)
	wsLine := surgeProxy(wsTrojan, "tr-ws")
	if !strings.Contains(wsLine, "ws=true") || !strings.Contains(wsLine, "ws-path=/ws") || !strings.Contains(wsLine, "ws-headers=Host:ws.example.com") {
		t.Fatalf("Surge Trojan WS line missing WS options: %s", wsLine)
	}
}
