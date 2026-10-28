package panel

import (
	"strings"
	"testing"
)

func TestSubscriptionExportsPreserveCaseInsensitiveHost(t *testing.T) {
	for _, header := range []string{"host", "HOST"} {
		t.Run(header, func(t *testing.T) {
			n := customNodeFromParams(t, "vmess", `{"uuid":"11111111-1111-4111-8111-111111111111","transport":"ws","path":"/proxy","headers":{"`+header+`":["cdn.example.com","backup.example.com"]}}`)
			proxy := clashProxy(n, map[string]int{})
			opts, ok := proxy["ws-opts"].(map[string]any)
			if !ok {
				t.Fatalf("missing WebSocket options: %+v", proxy)
			}
			headers, ok := opts["headers"].(map[string]any)
			if !ok || headers["Host"] != "cdn.example.com" {
				t.Fatalf("Clash Host lost: %+v", proxy)
			}
			if line := surgeProxy(n, "node"); !strings.Contains(line, "ws-headers=Host:cdn.example.com") {
				t.Fatalf("Surge Host lost: %s", line)
			}
			if params := nodeParams(n); params["Host"] != "cdn.example.com" {
				t.Fatalf("node details Host lost: %+v", params)
			}
		})
	}
}

func TestClashWebSocketExportPreservesCustomHeaders(t *testing.T) {
	for _, transport := range []struct{ input, opts string }{{"ws", "ws-opts"}, {"httpupgrade", "http-upgrade-opts"}} {
		t.Run(transport.input, func(t *testing.T) {
			n := customNodeFromParams(t, "vmess", `{"uuid":"11111111-1111-4111-8111-111111111111","transport":"`+transport.input+`","path":"/proxy","headers":{"host":["cdn.example.com","backup.example.com"],"X-Access-Token":"required-token"}}`)
			proxy := clashProxy(n, map[string]int{})
			opts, ok := proxy[transport.opts].(map[string]any)
			if !ok {
				t.Fatalf("missing transport options: %+v", proxy)
			}
			headers, ok := opts["headers"].(map[string]any)
			if !ok || headers["Host"] != "cdn.example.com" || headers["X-Access-Token"] != "required-token" || len(headers) != 2 {
				t.Fatalf("Clash headers lost: %+v", opts)
			}
		})
	}
}
