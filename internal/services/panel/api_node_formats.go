package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

type nodeExportUser struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
}

// Multi-user inbounds require an active, granted account: there is no shared
// credential to export. Fixed-credential nodes remain available without one.
func (a *App) serverNodeFormats(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
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

	var userID uint
	if raw, present := c.GetQuery("user_id"); present {
		parsed, err := strconv.ParseUint(raw, 10, strconv.IntSize)
		if err != nil || parsed == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户 ID"})
			return
		}
		userID = uint(parsed)
	}

	settings := make(map[uint]singbox.InboundSettings)
	var multiInbounds []uint
	for _, ib := range srv.Inbounds {
		if !ib.Enabled {
			continue
		}
		var st singbox.InboundSettings
		if len(ib.Settings) > 0 {
			_ = json.Unmarshal(ib.Settings, &st)
		}
		if err := st.ValidateMultiUser(string(ib.Type)); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": fmt.Sprintf("inbound %q settings: %v", ib.Tag, err)})
			return
		}
		settings[ib.ID] = st
		if st.UseMultiUser(string(ib.Type)) {
			multiInbounds = append(multiInbounds, ib.ID)
		}
	}
	choices := make([]nodeExportUser, 0)
	var selected *model.User
	if len(multiInbounds) > 0 {
		var users []model.User
		if err := a.db.Where("enabled = ?", true).Order("id").Find(&users).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "加载导出用户失败"})
			return
		}
		now := time.Now()
		for i := range users {
			u := &users[i]
			if u.Expired(now) {
				continue
			}
			for _, inboundID := range multiInbounds {
				if !u.HasInbound(srv.ID, inboundID) {
					continue
				}
				choices = append(choices, nodeExportUser{ID: u.ID, Email: u.Email})
				if u.ID == userID {
					selected = u
				}
				break
			}
		}
	}
	if userID != 0 && selected == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "该用户不可用或未获授权使用此服务器的多用户入站"})
		return
	}

	var nodes []node
	for _, ib := range srv.Inbounds {
		st, enabled := settings[ib.ID]
		if !enabled {
			continue
		}
		var identity singbox.ProxyUser
		if st.UseMultiUser(string(ib.Type)) {
			if selected == nil || !selected.HasInbound(srv.ID, ib.ID) {
				continue
			}
			st.SingleUser = false
			identity = proxyIdentity(selected, ib.ID)
		} else {
			st.MultiUser = false
			st.SingleUser = true
			identity = st.SingleUserIdentity()
		}
		nodes = append(nodes, node{
			tag:      ib.Tag,
			server:   host,
			port:     ib.ListenPort,
			typ:      string(ib.Type),
			settings: st,
			user:     identity,
		})
	}
	nameServerNodes(srv.Name, nodes)

	var uriLines []string
	for _, n := range nodes {
		if link, err := singbox.BuildShareLink(n.clientNode()); err == nil {
			uriLines = append(uriLines, link)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"users":            choices,
		"multi_user_count": len(multiInbounds),
		"user_id":          userID,
		"items":            buildNodeFormatItems(nodes),
		"uri":              strings.Join(uriLines, "\n"),
		"clash":            clashProxiesYAML(nodes),
		"surge":            surgeLines(nodes),
	})
}
