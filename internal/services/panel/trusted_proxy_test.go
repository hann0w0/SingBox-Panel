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
	old := getConfiguredTrustedProxies()
	t.Cleanup(func() { setConfiguredTrustedProxies(old) })
	setConfiguredTrustedProxies([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})

	if got := clientIPFor("127.0.0.1:5000", "203.0.113.9"); got != "203.0.113.9" {
		t.Fatalf("configured proxy: got %s", got)
	}
	// Another container on the Docker bridge is no longer trusted.
	if got := clientIPFor("172.17.0.5:5000", "203.0.113.9"); got != "172.17.0.5" {
		t.Fatalf("untrusted private peer forged X-Real-IP: got %s", got)
	}
}

func TestSuccessfulLoginCannotResetAnotherAccountsIPBudget(t *testing.T) {
	g := newLoginGuard()
	ip := "ip:203.0.113.9"
	pair := ip + "|acct:admin"
	acct := "acct:admin"
	for range ipHardFails {
		g.fail(ip, pair, acct)
	}
	if wait := g.retryAfterHard(ip, ipHardFails); wait <= 0 {
		t.Fatal("IP bucket was not hard-blocked at the threshold")
	}
	// Logging into a different account succeeds and clears only its own pair
	// and account keys; the original IP bucket must remain throttled.
	g.succeed("ip:203.0.113.9|acct:other", "acct:other")
	if wait := g.retryAfterHard(ip, ipHardFails); wait <= 0 {
		t.Fatal("a different account erased the shared IP hard limit")
	}
}

func TestExplicitEmptyTrustedProxiesDisablesDefaultPrivateTrust(t *testing.T) {
	old := getConfiguredTrustedProxies()
	t.Cleanup(func() { setConfiguredTrustedProxies(old) })
	setConfiguredTrustedProxies([]netip.Prefix{})

	if got := clientIPFor("172.17.0.1:5000", "203.0.113.9"); got != "172.17.0.1" {
		t.Fatalf("explicit empty trusted_proxies still trusted a private peer: %s", got)
	}
}
