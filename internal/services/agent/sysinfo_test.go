package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestDetectPublicIPv4FromRejectsIPv6AndUsesIPv4Fallback(t *testing.T) {
	server6 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("2001:db8::1\n"))
	}))
	defer server6.Close()
	server4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("203.0.113.7\n"))
	}))
	defer server4.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if got := detectPublicIPv4From(ctx, []string{server6.URL, server4.URL}); got != "203.0.113.7" {
		t.Fatalf("public IPv4 = %q", got)
	}
}

func TestSelectLocalIPv6(t *testing.T) {
	candidates := []netip.Addr{
		netip.MustParseAddr("::1"), netip.MustParseAddr("fe80::1"), netip.MustParseAddr("fd00::1"),
		netip.MustParseAddr("::ffff:203.0.113.7"), netip.MustParseAddr("203.0.113.7"),
		netip.MustParseAddr("2001:db8::9"), netip.MustParseAddr("2001:db8::2"),
	}
	if got := selectLocalIPv6(candidates, netip.MustParseAddr("2001:db8::9")); got != "2001:db8::9" {
		t.Fatalf("route source not selected: %s", got)
	}
	if got := selectLocalIPv6(candidates, netip.MustParseAddr("2001:db8::8")); got != "2001:db8::2" {
		t.Fatalf("fallback must be stable and locally assigned: %s", got)
	}
	if got := selectLocalIPv6(candidates[:5], netip.Addr{}); got != "" {
		t.Fatalf("non-public IPv6 selected: %s", got)
	}
}

func TestPublicAddressProbeRejectsWrongFamilyAndInvalidAddresses(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "::1", "::ffff:203.0.113.7", "not-an-ip"} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.Write([]byte(value)) }))
		if got := detectPublicIPv4From(context.Background(), []string{server.URL}); got != "" {
			t.Errorf("accepted %q", got)
		}
		server.Close()
	}
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback unavailable")
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.Write([]byte("2001:db8::7")) }))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	if got := detectPublicAddressFrom(context.Background(), []string{server.URL}, true); got != "2001:db8::7" {
		t.Fatalf("IPv6=%q", got)
	}
}
