package panel

import (
	"net"
	"net/netip"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var validRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := ""
		if incoming := strings.TrimSpace(c.GetHeader("X-Request-ID")); validRequestIDPattern.MatchString(incoming) {
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
				id = incoming
			}
		}
		if id == "" {
			id = uuid.NewString()
		}
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}
