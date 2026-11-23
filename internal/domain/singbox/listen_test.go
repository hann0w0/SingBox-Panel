package singbox

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestListenModesRoundTrip(t *testing.T) {
	for _, mode := range []struct {
		name, address string
		ipv6Only      bool
	}{
		{"default", "", false},
		{"ipv4", "0.0.0.0", false},
		{"ipv6", "2001:db8::2", true},
		{"local", "127.0.0.1", false},
		{"imported", "2001:db8::1", true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			inbound := InboundInput{Tag: "local", Type: "socks", ListenPort: 18081}
			inbound.Settings.Listen = mode.address
			inbound.Settings.IPv6Only = mode.ipv6Only
			raw, err := BuildServerConfig(ServerConfigInput{Inbounds: []InboundInput{inbound}})
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]any
			if err := json.Unmarshal(raw, &root); err != nil {
				t.Fatal(err)
			}
			public := mSlice(root, "inbounds")[0].(map[string]any)
			if mStr(public, "listen") != inbound.Settings.ListenAddress() {
				t.Fatalf("wrong bind address: %s", raw)
			}
			parsed, err := ParseServerConfig(raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed.Inbounds) != 1 || parsed.Inbounds[0].Settings.ListenAddress() != inbound.Settings.ListenAddress() || parsed.Inbounds[0].Settings.IPv6Only != mode.ipv6Only {
				t.Fatalf("listen mode lost on import: %+v", parsed)
			}
			if len(parsed.Rules) != 0 {
				t.Fatalf("generated rules leaked into user rules: %+v", parsed.Rules)
			}
		})
	}
}

func TestListenValidation(t *testing.T) {
	for _, settings := range []InboundSettings{
		{Listen: "example.com"}, {Listen: "127.0.0.1:1080"},
		{Listen: "0.0.0.0", IPv6Only: true}, {Listen: "::ffff:127.0.0.1", IPv6Only: true},
		{Listen: "::", IPv6Only: true},
	} {
		if _, err := BuildInbound(InboundInput{Tag: "invalid", Type: "socks", ListenPort: 1080, Settings: settings}); err == nil {
			t.Fatalf("accepted invalid listen settings: %+v", settings)
		}
	}
}

func TestIPv6OnlyLive(t *testing.T) {
	binary := os.Getenv("SING_BOX_BINARY")
	if binary == "" {
		t.Skip("set SING_BOX_BINARY to verify IPv6-only traffic with sing-box")
	}
	reserved, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	reserved.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		io.WriteString(writer, "ipv6-only-ok")
	}))
	defer origin.Close()
	raw, err := BuildServerConfig(ServerConfigInput{Inbounds: []InboundInput{{Tag: "ipv6", Type: "socks", ListenPort: port, Settings: InboundSettings{Listen: "::1", IPv6Only: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(binary, "check", "-c", config).CombinedOutput(); err != nil {
		t.Fatalf("sing-box check: %v: %s", err, output)
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, binary, "run", "-c", config)
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); command.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp6", net.JoinHostPort("::1", strconv.Itoa(port)), 100*time.Millisecond)
		if err == nil {
			connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sing-box IPv6 listener did not start")
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, host := range []string{"::1", "127.0.0.1"} {
		if host == "127.0.0.1" {
			connection, err := net.DialTimeout("tcp4", net.JoinHostPort(host, strconv.Itoa(port)), time.Second)
			if err == nil {
				connection.Close()
				t.Fatal("IPv4 TCP handshake succeeded on IPv6-only inbound")
			}
		}
		proxy, _ := url.Parse("socks5://" + net.JoinHostPort(host, strconv.Itoa(port)))
		transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
		client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
		response, err := client.Get(origin.URL)
		transport.CloseIdleConnections()
		if host == "127.0.0.1" {
			if err == nil {
				response.Body.Close()
				t.Fatal("IPv4 source was allowed through IPv6-only inbound")
			}
			continue
		}
		if err != nil {
			t.Fatalf("IPv6 source was rejected: %v", err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != "ipv6-only-ok" {
			t.Fatalf("unexpected IPv6 response: %s, %v", body, err)
		}
	}
}
