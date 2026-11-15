package panel

import (
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestGeckoSubscriptionOmitsUnsetPacketBounds(t *testing.T) {
	for _, bounds := range [][2]int{{0, 0}, {800, 0}, {0, 1200}, {800, 1200}} {
		node := node{name: "gecko", typ: "hysteria2", server: "exit.example", port: 443, user: singbox.ProxyUser{Password: "credential"}, settings: singbox.InboundSettings{ObfsType: "gecko", ObfsPassword: "obfs-key", GeckoMinPacketSize: bounds[0], GeckoMaxPacketSize: bounds[1]}}
		proxy := clashProxy(node, map[string]int{})
		options, _ := proxy["obfs-opts"].(map[string]any)
		for index, key := range []string{"min-packet-size", "max-packet-size"} {
			value, present := options[key]
			if bounds[index] == 0 && present || bounds[index] > 0 && value != bounds[index] {
				t.Fatalf("bounds=%v options=%v", bounds, options)
			}
		}
	}
}
