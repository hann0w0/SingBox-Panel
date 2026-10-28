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
