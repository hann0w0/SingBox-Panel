package panel

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/gin-gonic/gin"
)

func clientIPFor(remoteAddr, realIP string) string {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.RemoteAddr = remoteAddr
	if realIP != "" {
		c.Request.Header.Set("X-Real-IP", realIP)
	}
	return clientIP(c)
}

func TestClientIPDefaultTrustsPrivatePeers(t *testing.T) {
	if got := clientIPFor("172.17.0.1:5000", "203.0.113.9"); got != "203.0.113.9" {
		t.Fatalf("private proxy: got %s", got)
	}
	if got := clientIPFor("198.51.100.7:5000", "203.0.113.9"); got != "198.51.100.7" {
		t.Fatalf("public peer header must be ignored: got %s", got)
	}
}

func TestClientIPHonoursConfiguredTrustedProxies(t *testing.T) {
	old := configuredTrustedProxies
	t.Cleanup(func() { configuredTrustedProxies = old })
	configuredTrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}

	if got := clientIPFor("127.0.0.1:5000", "203.0.113.9"); got != "203.0.113.9" {
		t.Fatalf("configured proxy: got %s", got)
	}
	// Another container on the Docker bridge is no longer trusted.
	if got := clientIPFor("172.17.0.5:5000", "203.0.113.9"); got != "172.17.0.5" {
		t.Fatalf("untrusted private peer forged X-Real-IP: got %s", got)
	}
}
