package singbox

import "testing"

func TestSubscriptionIssuesIdentifyOnlyUnambiguousSourceNames(t *testing.T) {
	for _, tc := range []struct{ name, raw, sourceName string }{
		{"clash", `proxies: [{name: valid, type: http, server: proxy.example.com, port: 8080}, {name: broken, type: socks5, server: proxy.example.com, port: 1080, username: user}]`, "broken"},
		{"sing-box", `{"outbounds":[{"tag":"valid","type":"http","server":"proxy.example.com","server_port":8080},{"tag":"broken","type":"unsupported"}]}`, "broken"},
		{"surge", "[Proxy]\nvalid = http, proxy.example.com, 8080\nbroken = socks5, proxy.example.com, 1080, username=user", "broken"},
		{"quantumult", "[server_local]\nhttp = proxy.example.com:8080, tag=valid\nsocks5 = proxy.example.com:1080, username=user, tag=broken", "broken"},
		{"sip008", `{"servers":[{"remarks":"valid","server":"proxy.example.com","server_port":8080,"method":"aes-256-gcm","password":"secret"},{"remarks":"broken","server":"proxy.example.com","server_port":8080,"method":"aes-256-gcm"}]}`, "broken"},
		{"unnamed clash error", `proxies: [{name: valid, type: http, server: proxy.example.com, port: 8080}, {type: socks5, server: proxy.example.com, port: 1080, username: user}]`, ""},
		{"URI diagnostic is not a name", "http://proxy.example.com:8080#valid\nunsupported://proxy.example.com:8080#broken", ""},
		{"V2Ray tag cannot identify derived users", `{"outbounds":[{"tag":"valid","protocol":"http","settings":{"servers":[{"address":"proxy.example.com","port":8080}]}},{"tag":"broken","protocol":"vless","settings":{"vnext":[{"address":"proxy.example.com","port":443,"users":[{}]}]}}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := ParseSubscription([]byte(tc.raw))
			if err != nil || len(parsed.Nodes) != 1 || len(parsed.Skipped) != 1 {
				t.Fatalf("parse: nodes=%d skipped=%d err=%v", len(parsed.Nodes), len(parsed.Skipped), err)
			}
			if got := parsed.Skipped[0].SourceName; got != tc.sourceName {
				t.Fatalf("failed source name = %q, want %q", got, tc.sourceName)
			}
		})
	}
}
