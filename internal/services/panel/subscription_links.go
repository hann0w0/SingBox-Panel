package panel

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func (a *App) writeLinks(c *gin.Context, nodes []node) {
	var lines []string
	for _, n := range nodes {
		link, err := singbox.BuildShareLink(n.clientNode())
		if err == nil {
			lines = append(lines, link)
		}
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(strings.Join(lines, "\n")+"\n"))
}

func (a *App) writeSingbox(c *gin.Context, nodes []node) {
	var outbounds []json.RawMessage
	var tags []string
	seen := map[string]int{"auto": 1, "proxy": 1, "direct": 1}
	for _, n := range nodes {
		cn := n.clientNode()
		cn.Name = uniqueName(seen, cn.Name)
		ob, err := singbox.BuildClientOutbound(cn)
		if err != nil {
			continue
		}
		outbounds = append(outbounds, ob)
		tags = append(tags, cn.Name)
	}

	// With no renderable node, emitting the selector/urltest groups would give
	// sing-box an empty (null) member list and the whole profile fails to load.
	all := []any{}
	final := "direct"
	if len(tags) > 0 {
		all = append(all,
			map[string]any{"type": "selector", "tag": "proxy", "outbounds": append([]string{"auto"}, tags...)},
			map[string]any{"type": "urltest", "tag": "auto", "outbounds": tags,
				"url": "https://www.gstatic.com/generate_204", "interval": "5m"},
		)
		final = "proxy"
	}
	for _, ob := range outbounds {
		all = append(all, ob)
	}
	all = append(all, map[string]any{"type": "direct", "tag": "direct"})

	cfg := map[string]any{
		"log": map[string]any{"level": "info"},
		"inbounds": []map[string]any{
			{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080},
		},
		"outbounds": all,
		"route": map[string]any{
			"final":                 final,
			"auto_detect_interface": true,
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	c.Data(http.StatusOK, "application/json; charset=utf-8", data)
}

func (a *App) writeClash(c *gin.Context, nodes []node) {
	proxies, _ := clashProxies(nodes)
	// Only the proxies array: rules/groups belong to the user's own config.
	data, _ := marshalClashProxiesYAML(proxies)
	c.Data(http.StatusOK, "text/yaml; charset=utf-8", data)
}

// writeShadowrocket emits the YAML proxy list understood by Shadowrocket.
// Shadowrocket supports Snell as a structured proxy, while Snell has no
// portable share URI. Keep this format separate from the default URI output.
func (a *App) writeShadowrocket(c *gin.Context, nodes []node) {
	proxies, _ := shadowrocketProxies(nodes)
	data, _ := marshalClashProxiesYAML(proxies)
	c.Data(http.StatusOK, "text/yaml; charset=utf-8", data)
}

var clashProxyLeadingKeys = []string{
	"type",
	"name",
	"server",
	"port",
	"username",
	"uuid",
	"password",
	"psk",
	"auth-str",
	"version",
}

var clashProxyTrailingKeys = []string{
	"client-fingerprint",
	"tls",
	"skip-cert-verify",
	"udp",
}

// marshalClashProxiesYAML keeps the human-facing Clash fields in a stable,
// conventional order and emits names as native Unicode so flag emoji remain
// readable. MapSlice provides ordering directly without inspecting names.
