package singbox

import (
	"encoding/json"
	"testing"
)

const realityGuardParseFixture = `{
  "log":{"level":"info"},
  "inbounds":[
    {"type":"direct","tag":"reality-guard","listen":"::","listen_port":31735,"network":"tcp","override_address":"127.0.0.1","override_port":31736},
    {"type":"vless","tag":"reality-in","listen":"127.0.0.1","listen_port":31736,
     "users":[{"uuid":"12345678-1234-1234-1234-123456789012","flow":"xtls-rprx-vision"}],
     "tls":{"enabled":true,"server_name":"music.apple.com","alpn":["h2","http/1.1"],
       "reality":{"enabled":true,"handshake":{"server":"music.apple.com","server_port":443},
         "private_key":"UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc","short_id":["abcd"],"max_time_difference":"1m"}}}
  ],
  "outbounds":[{"type":"direct","tag":"direct"}],
  "route":{"rules":[
    {"inbound":["reality-guard"],"action":"sniff","sniffer":["tls"],"timeout":"300ms"},
    {"inbound":["reality-guard"],"domain":["music.apple.com"],"action":"route","outbound":"direct"},
    {"inbound":["reality-guard"],"action":"reject","method":"drop"}
  ],"final":"direct"}
}`

func TestParseRealityGuardPublicPortAndRoundTrip(t *testing.T) {
	parsed, err := ParseServerConfig([]byte(realityGuardParseFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Inbounds) != 1 || len(parsed.Rules) != 0 || len(parsed.Skipped) != 0 {
		t.Fatalf("unexpected parsed model: %+v", parsed)
	}
	inbound := parsed.Inbounds[0]
	guard := inbound.Settings.TLS.Reality.Guard
	if inbound.Tag != "reality-in" || inbound.ListenPort != 31735 || guard == nil || !guard.Enabled ||
		guard.InternalPort != 31736 || guard.Tag != "reality-guard" || guard.SniffTimeout != "300ms" ||
		len(guard.ServerNames) != 1 || guard.ServerNames[0] != "music.apple.com" || inbound.Settings.TLS.Reality.MaxTimeDifference != "1m" {
		t.Fatalf("guard did not preserve public/internal settings: %+v", inbound)
	}
	generated, err := BuildServerConfig(ServerConfigInput{
		Inbounds: []InboundInput{{Tag: inbound.Tag, Type: inbound.Type, ListenPort: inbound.ListenPort, Settings: inbound.Settings}}, Final: parsed.Final,
	})
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := ParseServerConfig(generated)
	if err != nil || len(reparsed.Inbounds) != 1 || len(reparsed.Rules) != 0 || len(reparsed.Skipped) != 0 {
		t.Fatalf("generated gate was not recognised: %+v, %v", reparsed, err)
	}
	if guard := reparsed.Inbounds[0].Settings.TLS.Reality.Guard; guard == nil || guard.InternalPort != 31736 || reparsed.Inbounds[0].ListenPort != 31735 {
		t.Fatalf("round trip lost guard/public port: %+v", reparsed.Inbounds)
	}
	normalizedOriginal, ok := NormalizeRealityGuardImportJSON([]byte(realityGuardParseFixture))
	if !ok {
		t.Fatal("original gate not normalised")
	}
	normalizedGenerated, ok := NormalizeRealityGuardImportJSON(generated)
	if !ok || canonicalJSON(json.RawMessage(normalizedOriginal)) != canonicalJSON(json.RawMessage(normalizedGenerated)) {
		t.Fatalf("finite guard normalisation differs:\noriginal: %s\ngenerated: %s", normalizedOriginal, normalizedGenerated)
	}
}

func TestParseRealityGuardRejectsAmbiguousOrUnsupportedChains(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown direct field", func(root map[string]any) { mSlice(root, "inbounds")[0].(map[string]any)["udp_fragment"] = true }},
		{"unknown vless field", func(root map[string]any) { mSlice(root, "inbounds")[1].(map[string]any)["detour"] = "another" }},
		{"unknown TLS field", func(root map[string]any) {
			mMap(mSlice(root, "inbounds")[1].(map[string]any), "tls")["min_version"] = "1.3"
		}},
		{"unknown reality field", func(root map[string]any) {
			mMap(mMap(mSlice(root, "inbounds")[1].(map[string]any), "tls"), "reality")["future"] = true
		}},
		{"unknown handshake field", func(root map[string]any) {
			mMap(mMap(mMap(mSlice(root, "inbounds")[1].(map[string]any), "tls"), "reality"), "handshake")["detour"] = "direct"
		}},
		{"unknown user field", func(root map[string]any) {
			mSlice(mSlice(root, "inbounds")[1].(map[string]any), "users")[0].(map[string]any)["future"] = true
		}},
		{"unknown sniff field", func(root map[string]any) {
			mSlice(mMap(root, "route"), "rules")[0].(map[string]any)["override_destination"] = true
		}},
		{"weak same-level domain and IP", func(root map[string]any) {
			allow := mSlice(mMap(root, "route"), "rules")[1].(map[string]any)
			allow["ip_cidr"], allow["port"] = []any{"127.0.0.1/32"}, []any{float64(31736)}
		}},
		{"extra guard reference", func(root map[string]any) {
			route := mMap(root, "route")
			route["rules"] = append(mSlice(route, "rules"), map[string]any{"inbound": []any{"reality-guard"}, "outbound": "direct"})
		}},
		{"nested guard reference", func(root map[string]any) {
			route := mMap(root, "route")
			route["rules"] = append(mSlice(route, "rules"), map[string]any{"type": "logical", "mode": "or", "rules": []any{map[string]any{"inbound": []any{"reality-guard"}}}, "action": "reject"})
		}},
		{"wrong rule order", func(root map[string]any) {
			rules := mSlice(mMap(root, "route"), "rules")
			rules[0], rules[1] = rules[1], rules[0]
		}},
		{"duplicate tag", func(root map[string]any) {
			root["inbounds"] = append(mSlice(root, "inbounds"), map[string]any{"type": "socks", "tag": "reality-in", "listen_port": float64(31800)})
		}},
		{"duplicate port", func(root map[string]any) {
			root["inbounds"] = append(mSlice(root, "inbounds"), map[string]any{"type": "socks", "tag": "other", "listen_port": float64(31736)})
		}},
		{"second forwarder shares target", func(root map[string]any) {
			root["inbounds"] = append(mSlice(root, "inbounds"), map[string]any{"type": "direct", "tag": "second-guard", "listen": "::", "listen_port": float64(31800), "network": "tcp", "override_address": "127.0.0.1", "override_port": float64(31736)})
		}},
		{"fractional port", func(root map[string]any) {
			mSlice(root, "inbounds")[0].(map[string]any)["listen_port"] = float64(31735.5)
		}},
		{"non-loopback target", func(root map[string]any) { mSlice(root, "inbounds")[1].(map[string]any)["listen"] = "::" }},
		{"transport on target", func(root map[string]any) {
			mSlice(root, "inbounds")[1].(map[string]any)["transport"] = map[string]any{"type": "ws"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var root map[string]any
			if err := json.Unmarshal([]byte(realityGuardParseFixture), &root); err != nil {
				t.Fatal(err)
			}
			test.mutate(root)
			raw, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseServerConfig(raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, inbound := range parsed.Inbounds {
				if inbound.Settings.RealityGuardEnabled() || (inbound.Type == "vless" && inbound.Tag == "reality-in" && inbound.ListenPort != 31736) {
					t.Fatalf("unsupported raw chain was collapsed: %+v", inbound)
				}
			}
			if _, ok := NormalizeRealityGuardImportJSON(raw); ok {
				t.Fatal("unsupported raw chain was normalised")
			}
		})
	}
}

func TestParseRealityMaxTimeDifferenceWithoutGuard(t *testing.T) {
	parsed, err := ParseServerConfig([]byte(`{"inbounds":[{"type":"vless","tag":"reality","listen_port":443,"users":[{"uuid":"u"}],"tls":{"enabled":true,"reality":{"enabled":true,"private_key":"key","max_time_difference":"2m","handshake":{"server":"music.apple.com","server_port":443}}}}]}`))
	if err != nil || len(parsed.Inbounds) != 1 || parsed.Inbounds[0].Settings.TLS.Reality.MaxTimeDifference != "2m" {
		t.Fatalf("REALITY time difference was lost: %+v, %v", parsed, err)
	}
}

func TestParseRealityGuardSlidingWindowAndIsolation(t *testing.T) {
	t.Run("guard preceded by unrelated inbound rule and followed by ordinary direct inbound rule", func(t *testing.T) {
		var root map[string]any
		if err := json.Unmarshal([]byte(realityGuardParseFixture), &root); err != nil {
			t.Fatal(err)
		}
		root["inbounds"] = append(mSlice(root, "inbounds"),
			map[string]any{"type": "socks", "tag": "other-in", "listen_port": float64(1080)},
			map[string]any{"type": "direct", "tag": "plain-direct", "listen": "::", "listen_port": float64(20000)},
		)
		route := mMap(root, "route")
		orig := mSlice(route, "rules")
		route["rules"] = append(
			[]any{map[string]any{"inbound": []any{"other-in"}, "outbound": "direct"}},
			append(orig,
				map[string]any{"inbound": []any{"plain-direct"}, "outbound": "direct"},
				map[string]any{"domain": []any{"example.com"}, "outbound": "direct"},
				map[string]any{"domain": []any{"example.org"}, "outbound": "direct"},
			)...,
		)
		raw, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseServerConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		foundGuard := false
		for _, in := range parsed.Inbounds {
			if in.Tag == "reality-in" && in.Settings.RealityGuardEnabled() && in.ListenPort == 31735 {
				foundGuard = true
			}
		}
		if !foundGuard {
			t.Fatalf("valid reality guard was not recognized: %+v", parsed.Inbounds)
		}
	})

	t.Run("guard preceded by catch-all route rule is not collapsed", func(t *testing.T) {
		var root map[string]any
		if err := json.Unmarshal([]byte(realityGuardParseFixture), &root); err != nil {
			t.Fatal(err)
		}
		route := mMap(root, "route")
		route["rules"] = append(
			[]any{map[string]any{"domain": []any{"example.com"}, "outbound": "direct"}},
			mSlice(route, "rules")...,
		)
		raw, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseServerConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range parsed.Inbounds {
			if in.Settings.RealityGuardEnabled() {
				t.Fatalf("guard preceded by catch-all rule should not be collapsed: %+v", in)
			}
		}
	})

	t.Run("second broken guard does not discard first valid guard", func(t *testing.T) {
		var root map[string]any
		if err := json.Unmarshal([]byte(realityGuardParseFixture), &root); err != nil {
			t.Fatal(err)
		}
		root["inbounds"] = append(mSlice(root, "inbounds"),
			map[string]any{"type": "direct", "tag": "bad-guard", "listen": "::", "listen_port": float64(31835), "network": "tcp", "override_address": "127.0.0.1", "override_port": float64(31836), "udp_fragment": true},
			map[string]any{"type": "vless", "tag": "bad-reality", "listen": "127.0.0.1", "listen_port": float64(31836),
				"users": []any{map[string]any{"uuid": "12345678-1234-1234-1234-123456789012"}},
				"tls": map[string]any{"enabled": true, "server_name": "music.apple.com", "reality": map[string]any{"enabled": true, "handshake": map[string]any{"server": "music.apple.com", "server_port": float64(443)}, "private_key": "UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc", "short_id": []any{"abcd"}}}},
		)
		route := mMap(root, "route")
		route["rules"] = append(mSlice(route, "rules"),
			map[string]any{"inbound": []any{"bad-guard"}, "action": "sniff", "sniffer": []any{"tls"}, "timeout": "300ms"},
			map[string]any{"inbound": []any{"bad-guard"}, "domain": []any{"music.apple.com"}, "action": "route", "outbound": "direct"},
			map[string]any{"inbound": []any{"bad-guard"}, "action": "reject", "method": "drop"},
		)
		raw, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseServerConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		var firstOK, secondCollapsed bool
		for _, in := range parsed.Inbounds {
			if in.Tag == "reality-in" && in.Settings.RealityGuardEnabled() {
				firstOK = true
			}
			if in.Tag == "bad-reality" && in.Settings.RealityGuardEnabled() {
				secondCollapsed = true
			}
		}
		if !firstOK || secondCollapsed {
			t.Fatalf("firstOK=%v secondCollapsed=%v inbounds=%+v", firstOK, secondCollapsed, parsed.Inbounds)
		}
	})
}
