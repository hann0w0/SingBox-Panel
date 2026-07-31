package singbox

import (
	"encoding/json"
	"strings"
	"testing"
)

// A hand-written config commonly routes via a selector and a dns outbound —
// neither of which the panel models. Importing must not keep references to
// them, or every config the panel later pushes fails `sing-box check`.
func TestParseDropsDanglingRouteTargets(t *testing.T) {
	raw := []byte(`{
	  "inbounds": [{"type":"shadowsocks","tag":"ss-in","listen_port":1080,"method":"aes-256-gcm","password":"p"}],
	  "outbounds": [
	    {"type":"direct","tag":"direct"},
	    {"type":"dns","tag":"dns-out"},
	    {"type":"selector","tag":"proxy","outbounds":["direct"]},
	    {"type":"shadowsocks","tag":"hk-out","server":"h.example.com","server_port":443,"method":"aes-256-gcm","password":"q"}
	  ],
	  "route": {
	    "rules": [
	      {"protocol":["dns"],"outbound":"dns-out"},
	      {"inbound":["ss-in"],"outbound":"hk-out"}
	    ],
	    "final": "proxy"
	  }
	}`)
	got, err := ParseServerConfig(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Final != "direct" {
		t.Errorf("final = %q, want direct (selector was not imported)", got.Final)
	}
	if len(got.Rules) != 1 || got.Rules[0].Outbound != "hk-out" {
		t.Errorf("rules = %+v, want only the hk-out rule", got.Rules)
	}
	if len(got.Skipped) != 2 {
		t.Errorf("Skipped = %v, want 2 entries warning about final and the dns rule", got.Skipped)
	}

	// The surviving model must generate a config with no dangling tags.
	cfg, err := BuildServerConfig(ServerConfigInput{
		Inbounds: []InboundInput{{Tag: got.Inbounds[0].Tag, Type: got.Inbounds[0].Type,
			ListenPort: got.Inbounds[0].ListenPort, Settings: got.Inbounds[0].Settings}},
		Outbounds: []OutboundInput{{Tag: got.Outbounds[0].Tag, Type: got.Outbounds[0].Type,
			Server: got.Outbounds[0].Server, ServerPort: got.Outbounds[0].ServerPort,
			Settings: got.Outbounds[0].Settings}},
		Rules: []RuleInput{{Inbound: got.Rules[0].Match.Inbound, Outbound: got.Rules[0].Outbound}},
		Final: got.Final,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, bad := range []string{`"proxy"`, `"dns-out"`} {
		if containsStr(string(cfg), bad) {
			t.Errorf("generated config still references %s:\n%s", bad, cfg)
		}
	}
}

func containsStr(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// naive is inbound-only in sing-box: emitting a "naive" OUTBOUND makes the whole
// client profile unloadable, taking every other node down with it.
func TestNaiveHasNoClientOutbound(t *testing.T) {
	_, err := BuildClientOutbound(ClientNode{
		Name: "n", Server: "h", ServerPort: 443, Type: "naive",
		Settings: InboundSettings{SingleUser: true, Password: "p",
			TLS: TLSSettings{Enabled: true, ServerName: "h"}},
	})
	if err == nil {
		t.Fatal("naive must be skipped as a client outbound, not emitted")
	}
	// It must still be usable via a share link.
	link, err := BuildShareLink(ClientNode{
		Name: "n", Server: "h", ServerPort: 443, Type: "naive",
		Settings: InboundSettings{SingleUser: true, Password: "p"},
		User:     ProxyUser{Name: "user", Password: "p"},
	})
	if err != nil || !containsStr(link, "naive+https://") {
		t.Fatalf("naive share link = %q, err = %v", link, err)
	}
}

// The server defaults Hysteria v1 bandwidth to 100/100; clients must be told the
// same numbers or the tunnel is rate-mismatched.
func TestHysteriaBandwidthMatchesServerDefault(t *testing.T) {
	st := InboundSettings{SingleUser: true, Password: "a",
		TLS: TLSSettings{Enabled: true, ServerName: "h"}}

	raw, err := BuildInbound(InboundInput{Tag: "hy", Type: "hysteria", ListenPort: 443, Settings: st})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	var in map[string]any
	_ = json.Unmarshal(raw, &in)

	ob, err := BuildClientOutbound(ClientNode{Name: "hy", Server: "h", ServerPort: 443,
		Type: "hysteria", Settings: st, User: ProxyUser{Name: "user", Password: "a"}})
	if err != nil {
		t.Fatalf("outbound: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal(ob, &out)

	for _, k := range []string{"up_mbps", "down_mbps"} {
		if in[k] != out[k] {
			t.Errorf("%s: server=%v client=%v (must match)", k, in[k], out[k])
		}
	}
}

func TestParseRealityKeysForInboundAndOutbound(t *testing.T) {
	raw := []byte(`{
      "inbounds":[{"type":"vless","tag":"in","listen_port":443,"users":[{"uuid":"u"}],
        "tls":{"enabled":true,"reality":{"enabled":true,"private_key":"UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc","short_id":["abcd"],"handshake":{"server":"www.microsoft.com","server_port":443}}}}],
      "outbounds":[{"type":"vless","tag":"landing","server":"1.2.3.4","server_port":443,"uuid":"u",
        "tls":{"enabled":true,"reality":{"enabled":true,"public_key":"landing-public","short_id":"ef01"}}}]
    }`)
	got, err := ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Inbounds) != 1 || got.Inbounds[0].Settings.TLS.Reality.PublicKey == "" {
		t.Fatalf("inbound REALITY public key was not derived: %+v", got.Inbounds)
	}
	if len(got.Outbounds) != 1 || got.Outbounds[0].Settings.TLS.Reality.PublicKey != "landing-public" {
		t.Fatalf("outbound REALITY public key was not imported: %+v", got.Outbounds)
	}
}

func TestParseManagedProtocolParameters(t *testing.T) {
	raw := []byte(`{
      "inbounds":[
        {"type":"vmess","tag":"vm","listen_port":1001,"users":[{"uuid":"u","alterId":1}]},
        {"type":"tuic","tag":"tuic","listen_port":1002,"users":[{"uuid":"u","password":"p"}],
          "congestion_control":"bbr","auth_timeout":"5s","zero_rtt_handshake":true,"heartbeat":"15s",
          "tls":{"enabled":true}},
        {"type":"trojan","tag":"trojan","listen_port":1003,"users":[{"password":"p"}],
          "fallback":{"server":"127.0.0.1","server_port":8080},"tls":{"enabled":true,"alpn":["h2","http/1.1"]}}
      ]
    }`)
	got, err := ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Inbounds) != 3 {
		t.Fatalf("inbounds = %d, want 3: %v", len(got.Inbounds), got.Skipped)
	}
	vm := got.Inbounds[0].Settings
	if vm.VMessAlterID != 1 || vm.VMessSecurityValue() != "auto" {
		t.Fatalf("VMess parameters = %+v", vm)
	}
	tuic := got.Inbounds[1].Settings
	if tuic.CongestionControl != "bbr" || tuic.AuthTimeout != "5s" || !tuic.ZeroRTTHandshake || tuic.Heartbeat != "15s" {
		t.Fatalf("TUIC parameters = %+v", tuic)
	}
	trojan := got.Inbounds[2].Settings
	if trojan.TrojanFallback == nil || trojan.TrojanFallback.ServerPort != 8080 || len(trojan.TLS.ALPN) != 2 {
		t.Fatalf("Trojan parameters = %+v", trojan)
	}
}

func TestParseSkipsRemovedManagedProtocols(t *testing.T) {
	raw := []byte(`{
      "inbounds":[
        {"type":"naive","tag":"naive","listen_port":1001},
        {"type":"hysteria","tag":"hy1","listen_port":1002},
        {"type":"shadowtls","tag":"stls","listen_port":1003}
      ]
    }`)
	got, err := ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Inbounds) != 0 || len(got.Skipped) != 3 {
		t.Fatalf("removed protocols were imported: inbounds=%v skipped=%v", got.Inbounds, got.Skipped)
	}
}

func TestParseSocksActionsAndLocalRuleSet(t *testing.T) {
	raw := []byte(`{
      "inbounds":[
        {"type":"socks","tag":"open-socks","listen_port":1080},
        {"type":"socks","tag":"auth-socks","listen_port":1081,"users":[{"username":"alice","password":"secret"}]}
      ],
      "outbounds":[
        {"type":"direct","tag":"direct"},
        {"type":"socks","tag":"landing","server":"proxy.example.com","server_port":1080,"username":"bob","password":"pw"}
      ],
      "route":{
        "rules":[
          {"inbound":["open-socks"],"action":"sniff","sniffer":["tls"]},
          {"protocol":["dns"],"action":"hijack-dns"},
          {"inbound":["auth-socks"],"action":"reject","method":"drop"}
        ],
        "rule_set":[{"type":"local","tag":"private","format":"source","path":"/etc/sing-box/private.json"}],
        "final":"landing"
      }
    }`)
	got, err := ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Inbounds) != 2 || got.Inbounds[0].Settings.Username != "" || got.Inbounds[1].Settings.Username != "alice" {
		t.Fatalf("SOCKS inbounds = %+v", got.Inbounds)
	}
	if len(got.Outbounds) != 1 || got.Outbounds[0].Username != "bob" {
		t.Fatalf("SOCKS outbound = %+v", got.Outbounds)
	}
	if len(got.Rules) != 3 || got.Rules[0].Outbound != "sniff" || got.Rules[1].Outbound != "hijack-dns" || got.Rules[2].Match.Method != "drop" {
		t.Fatalf("action rules = %+v", got.Rules)
	}
	if len(got.RuleSets) != 1 || got.RuleSets[0].Type != "local" || got.RuleSets[0].Path == "" {
		t.Fatalf("local rule-set = %+v", got.RuleSets)
	}
	if got.Final != "landing" {
		t.Fatalf("final = %q", got.Final)
	}
}

// A TLS inbound (e.g. anytls) whose certificate is embedded inline in the
// config must survive an import → regenerate roundtrip. If parseTLS drops the
// inline certificate/key, switching that node to managed mode regenerates TLS
// with no certificate and `sing-box check` fails with exit status 1.
func TestParseRecoversInlineTLSCertificate(t *testing.T) {
	const wantCert = "-----BEGIN CERTIFICATE-----\nMIIBfakeCertBody\n-----END CERTIFICATE-----"
	const wantKey = "-----BEGIN PRIVATE KEY-----\nMIIBfakeKeyBody\n-----END PRIVATE KEY-----"
	raw := []byte(`{
      "inbounds":[{
        "type":"anytls","tag":"anytls-in","listen_port":8443,
        "users":[{"password":"secret"}],
        "tls":{"enabled":true,"server_name":"example.com",
          "certificate":["-----BEGIN CERTIFICATE-----","MIIBfakeCertBody","-----END CERTIFICATE-----"],
          "key":["-----BEGIN PRIVATE KEY-----","MIIBfakeKeyBody","-----END PRIVATE KEY-----"]}
      }]
    }`)
	got, err := ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Inbounds) != 1 {
		t.Fatalf("inbounds = %d, want 1: skipped=%v", len(got.Inbounds), got.Skipped)
	}
	tls := got.Inbounds[0].Settings.TLS
	if tls.Certificate != wantCert {
		t.Fatalf("inline certificate not recovered:\n got %q\nwant %q", tls.Certificate, wantCert)
	}
	if tls.Key != wantKey {
		t.Fatalf("inline key not recovered:\n got %q\nwant %q", tls.Key, wantKey)
	}
	// Regenerating the inbound (as switch-to-managed does) must re-emit the cert.
	in := got.Inbounds[0]
	rebuilt, err := BuildInbound(InboundInput{
		Tag:        in.Tag,
		Type:       in.Type,
		ListenPort: in.ListenPort,
		Settings:   in.Settings,
	})
	if err != nil {
		t.Fatalf("regenerate inbound: %v", err)
	}
	if !strings.Contains(string(rebuilt), "MIIBfakeCertBody") || !strings.Contains(string(rebuilt), "MIIBfakeKeyBody") {
		t.Fatalf("regenerated inbound dropped inline TLS material: %s", rebuilt)
	}
}
