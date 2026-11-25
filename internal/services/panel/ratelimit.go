package panel

import (
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// loginGuard throttles failed logins so the panel cannot be brute-forced.
//
// Keys are split by who controls them, which decides whether a key may deny a
// request outright:
//   - ip and ip|account are controlled by the CALLER, so exceeding them rejects
//     the request before any password work. An attacker only locks themselves.
//   - account alone is controlled by the VICTIM's name, which any stranger can
//     type. It therefore only ever throttles a WRONG password — a correct
//     password is always accepted. Letting it hard-block would hand anyone a
//     trivial way to lock the admin out of their own panel.
//
// Failures grow an exponential cooldown, capped at maxBackoff, and reset both
// on success and after failDecay of quiet.
type loginGuard struct {
	mu      sync.Mutex
	entries map[string]*guardEntry
}

type guardEntry struct {
	fails    int
	blocked  time.Time // no attempt accepted before this instant
	lastFail time.Time
}

const (
	// Attempts allowed before throttling kicks in.
	freeAttempts = 5
	// Failures for ONE (source address, account) pair before it is hard-blocked
	// before password hashing, while still allowing recovery from a few typos.
	pairHardFails = 10
	// Failures from ONE source address before it is hard-blocked before any
	// password work. Set high enough that a human mistyping never reaches it;
	// its only job is to stop a single host burning CPU on bcrypt.
	ipHardFails = 20
	// First cooldown after the free attempts are spent; doubles per failure.
	baseBackoff = 5 * time.Second
	maxBackoff  = 5 * time.Minute
	// Entries idle for longer than this are dropped.
	guardTTL        = 30 * time.Minute
	maxGuardEntries = 8192
	// A key with no recent failure starts over, so an old counter cannot keep an
	// account pinned at the maximum backoff forever.
	failDecay = 2 * maxBackoff
)

func newLoginGuard() *loginGuard {
	return &loginGuard{entries: map[string]*guardEntry{}}
}

// retryAfter reports how long the caller must wait, or 0 when allowed. It is
// read-only: probing must not keep an entry alive or extend a block.
func (g *loginGuard) retryAfter(keys ...string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked(time.Now())
	var worst time.Duration
	for _, k := range keys {
		e := g.entries[k]
		if e == nil {
			continue
		}
		if d := time.Until(e.blocked); d > worst {
			worst = d
		}
	}
	return worst
}

// retryAfterHard is the pre-password gate: it only reports a wait once the key
// has failed at least minFails times, so an ordinary run of typos never stops
// someone who then types the right password.
func (g *loginGuard) retryAfterHard(key string, minFails int) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked(time.Now())
	e := g.entries[key]
	if e == nil || e.fails < minFails {
		return 0
	}
	return time.Until(e.blocked)
}

// fail records a failed attempt against every key.
func (g *loginGuard) fail(keys ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		e := g.entries[k]
		if e == nil {
			e = &guardEntry{}
			g.entries[k] = e
		}
		// Start over when the previous failure is ancient, so a long-idle key is
		// not still sitting at the maximum backoff.
		if !e.lastFail.IsZero() && now.Sub(e.lastFail) > failDecay {
			e.fails = 0
		}
		e.fails++
		e.lastFail = now
		if e.fails > freeAttempts {
			backoff := baseBackoff << (e.fails - freeAttempts - 1)
			if backoff > maxBackoff || backoff <= 0 {
				backoff = maxBackoff
			}
			e.blocked = now.Add(backoff)
		}
	}
	g.sweepLocked(now)
	if len(g.entries) > maxGuardEntries {
		g.evictOldestLocked(len(g.entries) - maxGuardEntries)
	}
}

// succeed clears the counters after a valid login.
func (g *loginGuard) succeed(keys ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, k := range keys {
		delete(g.entries, k)
	}
}

func (g *loginGuard) sweepLocked(now time.Time) {
	for k, e := range g.entries {
		if now.Sub(e.lastFail) > guardTTL {
			delete(g.entries, k)
		}
	}
}

func (g *loginGuard) evictOldestLocked(count int) {
	for count > 0 {
		oldestIPKey := ""
		var oldestIP time.Time
		oldestAnyKey := ""
		var oldestAny time.Time
		for key, entry := range g.entries {
			if strings.HasPrefix(key, "ip:") || strings.HasPrefix(key, "agentreg:") {
				if oldestIPKey == "" || entry.lastFail.Before(oldestIP) {
					oldestIPKey = key
					oldestIP = entry.lastFail
				}
			}
			if oldestAnyKey == "" || entry.lastFail.Before(oldestAny) {
				oldestAnyKey = key
				oldestAny = entry.lastFail
			}
		}
		victim := oldestIPKey
		if victim == "" {
			victim = oldestAnyKey
		}
		if victim == "" {
			return
		}
		delete(g.entries, victim)
		count--
	}
}

// rateLimitPeerKey normalizes IPv6 client addresses to their /64 prefix so a
// caller with an IPv6 allocation cannot bypass peer limits by rotating host
// identifiers within the same subnet, while keeping IPv4 addresses unchanged.
func rateLimitPeerKey(ipStr string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(ipStr))
	if err != nil {
		return ipStr
	}
	addr = addr.Unmap()
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}

// clientIP resolves the caller address behind the local reverse proxy.
//
// Never trust forwarding headers from an arbitrary peer: a public client could
// forge CF-Connecting-IP/X-Forwarded-For and rotate the login limiter key on
// every request. The supported deployments expose the panel only on loopback or
// a private reverse-proxy network, so X-Real-IP is accepted only from such a peer.
func clientIP(c *gin.Context) string {
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		host = c.Request.RemoteAddr
	}
	peer := net.ParseIP(host)
	prefixes := getConfiguredTrustedProxies()
	if val, ok := c.Get("singbox_panel_trusted_proxies"); ok {
		prefixes, _ = val.([]netip.Prefix)
	}
	if trustedProxyPeerWithPrefixes(peer, prefixes) {
		if forwarded := net.ParseIP(c.GetHeader("X-Real-IP")); forwarded != nil {
			return forwarded.String()
		}
	}
	if peer != nil {
		return peer.String()
	}
	return host
}

func trustedProxiesMiddleware(prefixes []netip.Prefix) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("singbox_panel_trusted_proxies", prefixes)
		c.Next()
	}
}

// configuredTrustedProxies narrows which peers may set X-Real-IP. nil keeps the
// private-network default; a non-nil empty slice trusts no proxy headers.
var (
	configuredTrustedProxiesMu sync.RWMutex
	configuredTrustedProxies   []netip.Prefix
)

func setConfiguredTrustedProxies(prefixes []netip.Prefix) {
	configuredTrustedProxiesMu.Lock()
	defer configuredTrustedProxiesMu.Unlock()
	if prefixes == nil {
		configuredTrustedProxies = nil
		return
	}
	cp := make([]netip.Prefix, len(prefixes))
	copy(cp, prefixes)
	configuredTrustedProxies = cp
}

func getConfiguredTrustedProxies() []netip.Prefix {
	configuredTrustedProxiesMu.RLock()
	defer configuredTrustedProxiesMu.RUnlock()
	if configuredTrustedProxies == nil {
		return nil
	}
	cp := make([]netip.Prefix, len(configuredTrustedProxies))
	copy(cp, configuredTrustedProxies)
	return cp
}

func trustedProxyPeer(ip net.IP) bool {
	return trustedProxyPeerWithPrefixes(ip, getConfiguredTrustedProxies())
}

func trustedProxyPeerWithPrefixes(ip net.IP, prefixes []netip.Prefix) bool {
	if ip == nil {
		return false
	}
	if prefixes != nil {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			return false
		}
		addr = addr.Unmap()
		for _, prefix := range prefixes {
			if prefix.Contains(addr) {
				return true
			}
		}
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
