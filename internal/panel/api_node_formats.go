package panel

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

// serverNodeFormats returns every single-credential inbound on one server in
// the URI, Clash and Surge formats shown by the admin export dialog.
func (a *App) serverNodeFormats(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var srv model.Server
	if err := a.db.Preload("Inbounds").First(&srv, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	host := srv.Address
	if host == "" {
		host = srv.PublicIP
	}
	host, err := normalizeNodeAddress(host)
	if err != nil || host == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "节点地址未配置或无效"})
		return
	}

	var nodes []node
	for _, ib := range srv.Inbounds {
		if !ib.Enabled {
			continue
		}
		var st singbox.InboundSettings
		if len(ib.Settings) > 0 {
			_ = json.Unmarshal(ib.Settings, &st)
		}
		// Multi-user inbounds have no valid node-wide credential. Export those
		// through each user's subscription instead of inventing a fixed login.
		if st.UseMultiUser(string(ib.Type)) {
			continue
		}
		st.MultiUser = false
		st.SingleUser = true
		nodes = append(nodes, node{
			tag:      ib.Tag,
			name:     formatNodeDisplayName(srv.Name, ib.Tag, string(ib.Type)),
			server:   host,
			port:     ib.ListenPort,
			typ:      string(ib.Type),
			settings: st,
			user:     st.SingleUserIdentity(),
		})
	}

	var uriLines []string
	for _, n := range nodes {
		if link, err := singbox.BuildShareLink(n.clientNode()); err == nil {
			uriLines = append(uriLines, link)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"items": buildNodeFormatItems(nodes),
		"uri":   strings.Join(uriLines, "\n"),
		"clash": clashProxiesYAML(nodes),
		"surge": surgeLines(nodes),
	})
}
