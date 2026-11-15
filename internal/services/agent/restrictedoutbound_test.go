package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func restrictedConfig(t *testing.T, outbounds ...map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"outbounds": outbounds,
		"route":     map[string]any{"final": "private"},
		"extra":     json.RawMessage(`{"precise":9007199254740993,"unchanged":"value"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func restrictedProxy(server string) map[string]any {
	return map[string]any{"tag": "private", "type": "vless", "server": server, "server_port": 443}
}

func decodeRestrictedOutbounds(t *testing.T, raw []byte) []map[string]json.RawMessage {
	t.Helper()
	var config struct {
		Outbounds []map[string]json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	return config.Outbounds
}

func TestRestrictedOutboundPinsPublicDNSDeterministicallyAndPreservesOtherSettings(t *testing.T) {
	proxy := restrictedProxy("exit.example")
	proxy["tls"] = map[string]any{"enabled": true, "alpn": []string{"h2"}, "reality": map[string]any{"enabled": true, "public_key": "key"}}
	proxy["transport"] = map[string]any{"type": "ws", "headers": map[string]string{"Host": "transport.example"}}
	admin := map[string]any{"tag": "admin", "type": "socks", "server": "127.0.0.1", "server_port": 9000}
	raw := restrictedConfig(t, proxy, admin)
	original := bytes.Clone(raw)
	answers := []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111"), netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")}
	lookups := 0
	resolve := func(ctx context.Context, host string) ([]netip.Addr, error) {
		lookups++
		if host != "exit.example" {
			t.Fatalf("unexpected DNS request for %q", host)
		}
		deadline, bounded := ctx.Deadline()
		if !bounded || time.Until(deadline) > restrictedOutboundLookupTimeout {
			t.Fatal("node DNS lookup lacks its bounded context")
		}
		return answers, nil
	}
	prepared, err := prepareRestrictedOutbounds(context.Background(), raw, []string{"private"}, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, original) || lookups != 1 {
		t.Fatal("preparation mutated its source or resolved an unrestricted outbound")
	}
	rows := decodeRestrictedOutbounds(t, prepared)
	if string(rows[0]["server"]) != `"1.1.1.1"` {
		t.Fatalf("DNS was not pinned to a deterministic public IPv4: %s", rows[0]["server"])
	}
	var tls map[string]json.RawMessage
	if err := json.Unmarshal(rows[0]["tls"], &tls); err != nil {
		t.Fatal(err)
	}
	if string(tls["server_name"]) != `"exit.example"` || string(tls["alpn"]) != `["h2"]` || string(tls["reality"]) != `{"enabled":true,"public_key":"key"}` {
		t.Fatalf("pinning lost TLS identity or settings: %s", rows[0]["tls"])
	}
	before := decodeRestrictedOutbounds(t, original)
	if !reflect.DeepEqual(rows[1], before[1]) || !bytes.Equal(rows[0]["transport"], before[0]["transport"]) || !bytes.Contains(prepared, []byte(`9007199254740993`)) {
		t.Fatal("pinning modified unrelated settings or precise JSON values")
	}
	answers = []netip.Addr{answers[2], answers[0], answers[1]}
	reordered, err := prepareRestrictedOutbounds(context.Background(), raw, []string{"private"}, resolve)
	if err != nil || !bytes.Equal(prepared, reordered) {
		t.Fatalf("DNS answer order changed the installed configuration: %v", err)
	}
}

func TestRestrictedOutboundRejectsEveryUnsafeLiteralAndDNSAnswer(t *testing.T) {
	unsafe := []string{
		"0.0.0.0", "0.1.2.3", "10.0.0.1", "127.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "100.100.100.200", "192.0.0.1", "192.0.2.1", "198.18.0.1", "198.19.255.255",
		"198.51.100.1", "203.0.113.1", "224.0.0.1", "239.1.2.3", "240.0.0.1", "255.255.255.255",
		"::", "::1", "fc00::1", "fd00:ec2::254", "fe80::1", "ff02::1", "2001:db8::1", "::ffff:127.0.0.1", "::ffff:10.1.2.3",
	}
	for _, host := range unsafe {
		t.Run(host, func(t *testing.T) {
			bad := netip.MustParseAddr(host)
			for _, literal := range []bool{false, true} {
				server := "exit.example"
				if literal {
					server = host
				}
				raw := restrictedConfig(t, restrictedProxy(server))
				prepared, err := prepareRestrictedOutbounds(context.Background(), raw, []string{"private"}, func(context.Context, string) ([]netip.Addr, error) {
					if literal {
						t.Fatal("literal triggered DNS resolution")
					}
					return []netip.Addr{netip.MustParseAddr("1.1.1.1"), bad}, nil
				})
				if err == nil || prepared != nil || !strings.Contains(err.Error(), `restricted outbound "private"`) || !strings.Contains(err.Error(), "every address must be public") {
					t.Fatalf("unsafe address accepted or error not actionable (literal=%v): %s, %v", literal, prepared, err)
				}
			}
		})
	}
}

func TestRestrictedOutboundPreservesTLSIdentityAndSupportsPrivateTypes(t *testing.T) {
	for _, typ := range []string{"vless", "vmess", "trojan", "shadowsocks", "ss", "hysteria2", "tuic", "socks", "anytls", "snell"} {
		t.Run(typ, func(t *testing.T) {
			for _, setting := range []struct {
				name string
				tls  map[string]any
				want string
			}{
				{"implicit", map[string]any{"enabled": true}, "exit.example"},
				{"explicit empty", map[string]any{"enabled": true, "server_name": ""}, "exit.example"},
				{"explicit", map[string]any{"enabled": true, "server_name": "sni.example"}, "sni.example"},
				{"disabled", map[string]any{"enabled": false}, ""},
				{"omitted", nil, ""},
			} {
				t.Run(setting.name, func(t *testing.T) {
					proxy := restrictedProxy("exit.example")
					proxy["type"] = typ
					if setting.tls != nil {
						proxy["tls"] = setting.tls
					}
					prepared, err := prepareRestrictedOutbounds(context.Background(), restrictedConfig(t, proxy), []string{"private"}, func(context.Context, string) ([]netip.Addr, error) {
						return []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111")}, nil
					})
					if err != nil {
						t.Fatal(err)
					}
					row := decodeRestrictedOutbounds(t, prepared)[0]
					if string(row["server"]) != `"2606:4700:4700::1111"` {
						t.Fatalf("IPv6-only DNS not pinned: %s", row["server"])
					}
					var tls struct {
						ServerName string `json:"server_name"`
					}
					if len(row["tls"]) > 0 {
						if err := json.Unmarshal(row["tls"], &tls); err != nil {
							t.Fatal(err)
						}
					}
					if tls.ServerName != setting.want {
						t.Fatalf("TLS server_name=%q, want %q", tls.ServerName, setting.want)
					}
				})
			}
		})
	}
}

func TestRestrictedOutboundPublicLiteralsDoNotResolveOrInventTLSName(t *testing.T) {
	for _, host := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111", "[2606:4700:4700::1111]", "::ffff:1.1.1.1"} {
		t.Run(host, func(t *testing.T) {
			proxy := restrictedProxy(host)
			proxy["tls"] = map[string]any{"enabled": true}
			prepared, err := prepareRestrictedOutbounds(context.Background(), restrictedConfig(t, proxy), []string{"private"}, func(context.Context, string) ([]netip.Addr, error) {
				t.Fatal("public literal unexpectedly resolved via DNS")
				return nil, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			row := decodeRestrictedOutbounds(t, prepared)[0]
			want := netip.MustParseAddr(strings.Trim(host, "[]")).Unmap().String()
			if string(row["server"]) != fmt.Sprintf("%q", want) || string(row["tls"]) != `{"enabled":true}` {
				t.Fatalf("literal or TLS name changed: %s", prepared)
			}
		})
	}
}

func TestRestrictedOutboundFailsClosedForInvalidConfigOrMetadata(t *testing.T) {
	for _, scenario := range []struct {
		name string
		raw  string
		tags []string
	}{
		{"invalid JSON", `{`, []string{"private"}},
		{"null config", `null`, []string{"private"}},
		{"missing array", `{}`, []string{"private"}},
		{"invalid array", `{"outbounds":{}}`, []string{"private"}},
		{"missing tag", `{"outbounds":[]}`, []string{"private"}},
		{"empty requested tag", `{"outbounds":[]}`, []string{""}},
		{"duplicate requested tag", `{"outbounds":[{"tag":"private","type":"vless","server":"1.1.1.1"}]}`, []string{"private", "private"}},
		{"duplicate outbound tag", `{"outbounds":[{"tag":"private","type":"vless","server":"1.1.1.1"},{"tag":"private","type":"vless","server":"127.0.0.1"}]}`, []string{"private"}},
		{"unsupported type", `{"outbounds":[{"tag":"private","type":"direct","server":"1.1.1.1"}]}`, []string{"private"}},
		{"missing server", `{"outbounds":[{"tag":"private","type":"vless"}]}`, []string{"private"}},
		{"null server", `{"outbounds":[{"tag":"private","type":"vless","server":null}]}`, []string{"private"}},
		{"invalid TLS", `{"outbounds":[{"tag":"private","type":"vless","server":"exit.example","tls":true}]}`, []string{"private"}},
		{"invalid TLS enabled", `{"outbounds":[{"tag":"private","type":"vless","server":"exit.example","tls":{"enabled":"true"}}]}`, []string{"private"}},
		{"invalid TLS name", `{"outbounds":[{"tag":"private","type":"vless","server":"exit.example","tls":{"enabled":true,"server_name":4}}]}`, []string{"private"}},
		{"wildcard is not metadata expansion", `{"outbounds":[{"tag":"private","type":"vless","server":"1.1.1.1"}]}`, []string{"*"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			prepared, err := prepareRestrictedOutbounds(context.Background(), []byte(scenario.raw), scenario.tags, func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
			})
			if err == nil || prepared != nil {
				t.Fatalf("invalid restricted configuration accepted: %s, %v", prepared, err)
			}
		})
	}
	for _, host := range []string{"localhost", "x.localhost.", "fe80::1%eth0", "*.example.com", "exit.example:443", ""} {
		t.Run("host/"+host, func(t *testing.T) {
			_, err := prepareRestrictedOutbounds(context.Background(), restrictedConfig(t, restrictedProxy(host)), []string{"private"}, func(context.Context, string) ([]netip.Addr, error) {
				return nil, errors.New("invalid hostname")
			})
			if err == nil {
				t.Fatal("invalid or local host accepted")
			}
		})
	}
}

func TestRestrictedOutboundDNSFailuresAndCancellationNeverProduceConfig(t *testing.T) {
	raw := restrictedConfig(t, restrictedProxy("exit.example"))
	for _, scenario := range []struct {
		name      string
		addresses []netip.Addr
		err       error
	}{
		{"no addresses", nil, nil},
		{"failed lookup", nil, errors.New("DNS unavailable")},
		{"invalid address", []netip.Addr{{}}, nil},
		{"zoned result", []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111%eth0")}, nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			prepared, err := prepareRestrictedOutbounds(context.Background(), raw, []string{"private"}, func(context.Context, string) ([]netip.Addr, error) {
				return scenario.addresses, scenario.err
			})
			if err == nil || prepared != nil {
				t.Fatal("failed lookup produced an installable config")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	prepared, err := prepareRestrictedOutbounds(ctx, raw, []string{"private"}, func(lookupCtx context.Context, _ string) ([]netip.Addr, error) {
		cancel()
		<-lookupCtx.Done()
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	})
	if !errors.Is(err, context.Canceled) || prepared != nil {
		t.Fatalf("canceled lookup produced a config: %s %v", prepared, err)
	}
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer timeoutCancel()
	prepared, err = prepareRestrictedOutbounds(timeoutCtx, raw, []string{"private"}, func(lookupCtx context.Context, _ string) ([]netip.Addr, error) {
		<-lookupCtx.Done()
		return nil, lookupCtx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || prepared != nil {
		t.Fatalf("lookup ignored parent deadline: %s %v", prepared, err)
	}
}

func TestRestrictedOutboundChecksEverySelectedTagBeforeApply(t *testing.T) {
	first := restrictedProxy("first.example")
	second := restrictedProxy("second.example")
	second["tag"] = "second"
	raw := restrictedConfig(t, first, second)
	var hosts []string
	prepared, err := prepareRestrictedOutbounds(context.Background(), raw, []string{"private", "second"}, func(_ context.Context, host string) ([]netip.Addr, error) {
		hosts = append(hosts, host)
		if host == "second.example" {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	})
	if !reflect.DeepEqual(hosts, []string{"first.example", "second.example"}) || prepared != nil || err == nil || !strings.Contains(err.Error(), `restricted outbound "second"`) {
		t.Fatalf("did not reject the unsafe second outbound: hosts=%v prepared=%s error=%v", hosts, prepared, err)
	}
	if !bytes.Contains(raw, []byte(`"server":"first.example"`)) {
		t.Fatal("a later validation failure mutated the requested config")
	}
}

func TestRestrictedOutboundNoMetadataLeavesRawBytesUntouched(t *testing.T) {
	for _, raw := range [][]byte{[]byte("  {\"outbounds\":[{\"type\":\"direct\"}]} \n"), []byte(`not strict JSON`)} {
		prepared, err := prepareRestrictedOutbounds(context.Background(), raw, nil, func(context.Context, string) ([]netip.Addr, error) {
			t.Fatal("unrestricted config performed DNS validation")
			return nil, nil
		})
		if err != nil || !bytes.Equal(prepared, raw) {
			t.Fatalf("unrestricted config changed: %s %v", prepared, err)
		}
	}
}

func TestRestrictedOutboundCommandRejectsBeforeApplying(t *testing.T) {
	env, err := protocol.NewEnvelope(protocol.CmdApplyConfig, "unsafe-apply", protocol.ApplyConfigCmd{
		Config: restrictedConfig(t, restrictedProxy("127.0.0.1")), RestrictedOutboundTags: []string{"private"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := (&Agent{}).onCommand(context.Background(), env)
	if result.OK || result.ID != env.ID || !strings.Contains(result.Error, "unsafe address") || !strings.Contains(result.Error, `"private"`) {
		t.Fatalf("command did not enforce node validation before applying: %+v", result)
	}
}
