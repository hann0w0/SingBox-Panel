package singbox

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMergeConfigJSONDropsExtraUsersWhenSwitchingSingleToMultiUser(t *testing.T) {
	original := []byte(`{"users":[{"name":"alice","uuid":"1111","custom":true},{"name":"bob","uuid":"2222"}]}`)
	before := []byte(`{"users":[{"name":"user","uuid":"1111"}]}`)
	after := []byte(`{"users":[{"name":"__singbox_panel_user__:1:abc","uuid":"3333"}]}`)

	got, err := mergeConfigJSON(original, before, after)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Users []map[string]any `json:"users"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Users) != 1 || parsed.Users[0]["name"] != "__singbox_panel_user__:1:abc" || parsed.Users[0]["custom"] != nil {
		t.Fatalf("stale user or unknown custom field leaked across multi-user switch: %s", got)
	}

	// Equal-length disjoint identity swap must not leak custom attributes across identities.
	gotEqual, err := mergeConfigJSON(
		[]byte(`{"users":[{"name":"alice","uuid":"1111","custom":true}]}`),
		[]byte(`{"users":[{"name":"alice","uuid":"1111"}]}`),
		[]byte(`{"users":[{"name":"bob","uuid":"2222"}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotEqual, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Users) != 1 || parsed.Users[0]["name"] != "bob" || parsed.Users[0]["custom"] != nil {
		t.Fatalf("custom attributes leaked across disjoint user replacement: %s", gotEqual)
	}
}

func TestValidateJSONAgainstSchemaAcceptsListableScalarsAndArrays(t *testing.T) {
	bundle := GetSchemaBundle()
	err := ValidateJSONAgainstSchema(bundle.Inbound, map[string]any{
		"type":        "vless",
		"tag":         "listable-in",
		"listen_port": float64(443),
		"tls": map[string]any{
			"enabled":     true,
			"alpn":        "h2",
			"certificate": "-----BEGIN CERTIFICATE-----",
			"key":         "-----BEGIN PRIVATE KEY-----",
			"reality": map[string]any{
				"enabled":     true,
				"private_key": "priv",
				"short_id":    "01ab",
				"handshake":   map[string]any{"server": "www.apple.com", "server_port": float64(443)},
			},
		},
		"transport": map[string]any{
			"type": "http",
			"host": []string{"cdn.example.com", "edge.example.com"},
		},
	})
	if err != nil {
		t.Fatalf("listable inbound scalar/array forms rejected: %v", err)
	}

	if err := ValidateJSONAgainstSchema(bundle.Inbound, map[string]any{
		"type":        "tun",
		"tag":         "tun0",
		"address":     "172.19.0.1/30",
		"auto_route":  true,
		"strict_route": true,
	}); err != nil {
		t.Fatalf("listable tun address scalar rejected: %v", err)
	}

	if err := ValidateJSONAgainstSchema(bundle.RouteRule, map[string]any{
		"action": "route",
		"domain": "example.com",
		"port":   float64(443),
	}); err != nil {
		t.Fatalf("listable route rule scalar rejected: %v", err)
	}

	if err := ValidateJSONAgainstSchema(bundle.RouteRule, map[string]any{
		"action": "route",
		"port":   float64(0),
	}); err == nil {
		t.Fatal("expected listable scalar integer 0 below minimum to fail")
	}
}

func TestBuildAndParseTransportPerProtocolFields(t *testing.T) {
	raw, err := BuildInbound(InboundInput{
		Tag: "grpc-in", Type: "vless", ListenPort: 443,
		Settings: InboundSettings{
			SingleUser: true, UUID: "11111111-2222-4333-8444-555555555555",
			Transport: TransportSettings{
				Type:        "grpc",
				ServiceName: "TunService",
				Path:        "/ignored",
				Headers:     map[string]string{"Host": "ignored.example.com"},
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildInbound grpc: %v", err)
	}
	var grpcDoc map[string]any
	if err := json.Unmarshal(raw, &grpcDoc); err != nil {
		t.Fatal(err)
	}
	tr, _ := grpcDoc["transport"].(map[string]any)
	if tr["type"] != "grpc" || tr["service_name"] != "TunService" || tr["path"] != nil || tr["headers"] != nil {
		t.Fatalf("grpc transport invalid fields: %v", tr)
	}

	quicRaw, err := BuildInbound(InboundInput{
		Tag: "quic-in", Type: "vless", ListenPort: 443,
		Settings: InboundSettings{
			SingleUser: true, UUID: "11111111-2222-4333-8444-555555555555",
			Transport: TransportSettings{Type: "quic", Path: "/bad", Headers: map[string]string{"Host": "bad.example.com"}},
		},
	})
	if err != nil {
		t.Fatalf("BuildInbound quic: %v", err)
	}
	var quicDoc map[string]any
	_ = json.Unmarshal(quicRaw, &quicDoc)
	if qtr, _ := quicDoc["transport"].(map[string]any); len(qtr) != 1 || qtr["type"] != "quic" {
		t.Fatalf("quic transport should emit only type: %v", qtr)
	}

	httpRaw, err := BuildInbound(InboundInput{
		Tag: "http-in", Type: "vless", ListenPort: 443,
		Settings: InboundSettings{
			SingleUser: true, UUID: "11111111-2222-4333-8444-555555555555",
			Transport: TransportSettings{
				Type: "http", Path: "/h2", Method: "PUT",
				HostList: []string{"a.example.com", "b.example.com"},
				Headers:  map[string]string{"X-Test": "1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildInbound http: %v", err)
	}
	cfgJSON, _ := json.Marshal(map[string]any{"inbounds": []json.RawMessage{httpRaw, raw}})
	parsed, err := ParseServerConfig(cfgJSON)
	if err != nil || len(parsed.Inbounds) != 2 {
		t.Fatalf("ParseServerConfig: %v", err)
	}
	httpTr := parsed.Inbounds[0].Settings.Transport
	if httpTr.Method != "PUT" || len(httpTr.HostList) != 2 || httpTr.Host() != "a.example.com" {
		t.Fatalf("parsed http transport incomplete: %+v", httpTr)
	}
	if parsed.Inbounds[1].Settings.Transport.ServiceName != "TunService" {
		t.Fatalf("parsed grpc service_name lost: %+v", parsed.Inbounds[1].Settings.Transport)
	}
}

func TestQuantumultSSWSUsesV2RayPluginAndBase64SurgeAndTruncateUTF8(t *testing.T) {
	qx := "[server_local]\nshadowsocks=1.2.3.4:443, method=aes-128-gcm, password=secret, obfs=wss, obfs-host=cdn.example.com, obfs-uri=/ws, tag=QX-SS\n"
	res, err := ParseSubscription([]byte(qx))
	if err != nil {
		t.Fatalf("ParseSubscription QX SS+WSS failed: %v", err)
	}
	if len(res.Nodes) != 1 || !strings.HasPrefix(res.Nodes[0].Params["ss_plugin"].(string), "v2ray-plugin;") {
		t.Fatalf("expected v2ray-plugin for QX SS+WSS, got %+v", res.Nodes)
	}

	surgeRaw := "[Proxies]\nS1 = trojan, 1.2.3.4, 443, password=secret\n"
	b64 := base64.StdEncoding.EncodeToString([]byte(surgeRaw))
	b64Res, err := ParseSubscription([]byte(b64))
	if err != nil || len(b64Res.Nodes) != 1 {
		t.Fatalf("base64 Surge [Proxies] failed: res=%+v err=%v", b64Res, err)
	}

	longUTF8 := strings.Repeat("中", 260)
	truncated := truncateImportInput(longUTF8)
	if !utf8.ValidString(truncated) {
		t.Fatalf("truncateImportInput produced invalid UTF-8: %q", truncated)
	}
	if len([]rune(truncated)) != 241 {
		t.Fatalf("truncated rune count = %d; want 241", len([]rune(truncated)))
	}
}
