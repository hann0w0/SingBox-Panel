package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/singbox"
)

func TestShadowrocketSubscriptionRendersSnellOnly(t *testing.T) {
	n := node{
		name:   "🇺🇸 Snell",
		server: "snell.example.com",
		port:   44046,
		typ:    "snell",
		settings: singbox.InboundSettings{
			SnellVersion:  5,
			SnellObfsMode: "http",
		},
		user: singbox.ProxyUser{Password: "snell-secret"},
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	(&App{}).writeShadowrocket(ctx, []node{n})

	got := recorder.Body.String()
	for _, want := range []string{
		"proxies:",
		"type: snell",
		"name: 🇺🇸 Snell",
		"server: snell.example.com",
		"port: 44046",
		"psk: snell-secret",
		"version: 5",
		"mode: http",
		"udp: true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Shadowrocket Snell YAML missing %q:\n%s", want, got)
		}
	}
}

func TestSubFormatRecognizesShadowrocket(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		ua    string
		want  string
	}{
		{name: "explicit target", query: "?target=shadowrocket", want: "shadowrocket"},
		{name: "user agent", ua: "Shadowrocket/2.2.70", want: "shadowrocket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/sub/token"+tc.query, nil)
			req.Header.Set("User-Agent", tc.ua)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = req
			if got := subFormat(ctx); got != tc.want {
				t.Fatalf("subFormat() = %q, want %q", got, tc.want)
			}
		})
	}
}
