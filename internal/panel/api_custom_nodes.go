package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

// customNodeReq is the admin payload for a hand-added external node. A node is
// defined by a share link (Link) OR structured fields (Protocol+Address+Port+
// Params). Audience fields are explicit so an empty allow-list can mean nobody
// without accidentally publishing the node to every account.
type customNodeReq struct {
	Name            string          `json:"name"`
	Group           string          `json:"group"`
	Link            string          `json:"link"`
	Protocol        string          `json:"protocol"`
	Address         string          `json:"address"`
	Port            int             `json:"port"`
	Params          json.RawMessage `json:"params"`
	AllUsers        *bool           `json:"all_users"`
	UserIDs         []uint          `json:"user_ids"`
	ExcludedUserIDs *[]uint         `json:"excluded_user_ids"`
	Enabled         *bool           `json:"enabled"`
	SortOrder       *int            `json:"sort_order"`
}

type customNodeRow struct {
	model.CustomNode
	UserEmails []string          `json:"user_emails,omitempty"`
	Detail     *customNodeDetail `json:"detail,omitempty"`
}

// customNodeDetail is the normalized, human-readable connection definition
// shown to administrators. Link-backed rows otherwise only expose their raw
// URI, while structured rows expose address/params separately; normalizing
// both through customNodeToNode gives the UI one consistent detail view.
type customNodeDetail struct {
	Protocol string            `json:"protocol"`
	Address  string            `json:"address"`
	Port     int               `json:"port"`
	Region   string            `json:"region,omitempty"`
	URI      string            `json:"uri,omitempty"`
	Params   map[string]string `json:"params"`
}

// validateCustomNode checks that the payload describes at least one usable
// representation. For link nodes the share link is parsed so bad input fails
// fast with a human-readable reason and the parsed fragment name is returned.
// Structured nodes require the fields their protocol renders.
func validateCustomNode(req *customNodeReq) (parsedName string, err error) {
	req.Link = strings.TrimSpace(req.Link)
	if req.Link != "" {
		cn, perr := singbox.ParseShareLink(req.Link)
		if perr != nil {
			return "", errors.New("链接解析失败: " + perr.Error())
		}
		if err := cn.Settings.ValidateClientOutbound(cn.Type); err != nil {
			return "", errors.New("链接参数无效: " + err.Error())
		}
		return cn.Name, nil
	}
	req.Protocol = strings.ToLower(strings.TrimSpace(req.Protocol))
	req.Address = strings.TrimSpace(req.Address)
	if req.Protocol == "" || req.Address == "" || req.Port < 1 || req.Port > 65535 {
		return "", errors.New("请填写协议、地址和端口（1-65535）")
	}
	raw := map[string]json.RawMessage{}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &raw); err != nil {
			return "", errors.New("参数格式错误")
		}
	}
	stringParam := func(name string) string {
		var value string
		if valueJSON, ok := raw[name]; ok {
			_ = json.Unmarshal(valueJSON, &value)
		}
		return value
	}
	need := func(names ...string) error {
		for _, n := range names {
			if strings.TrimSpace(stringParam(n)) == "" {
				return errors.New("请填写必填参数: " + n)
			}
		}
		return nil
	}
	switch req.Protocol {
	case "snell":
		if err := need("psk"); err != nil {
			return "", err
		}
		version := 5 // A missing version is the historical Snell v5 node shape.
		if versionJSON, ok := raw["version"]; ok {
			if err := json.Unmarshal(versionJSON, &version); err != nil || (version != 4 && version != 5 && version != 6) {
				return "", errors.New("Snell 出站版本必须是 4、5 或 6")
			}
		}
		// Snell v5 is a valid server/node version but sing-box represents its
		// client wire mode as outbound version 4. Validate both through the
		// official outbound schema so fields cannot be combined across versions.
		outboundVersion := singbox.SnellOutboundVersion(version)
		obfsMode := stringParam("obfs_mode")
		if obfsMode == "" {
			obfsMode = stringParam("obfs")
		}
		settings := singbox.InboundSettings{
			SnellVersion: outboundVersion,
			SnellPSK:     stringParam("psk"),
			// Snell is fixed to one PSK in this panel; ignore legacy userkey.
			SnellNetwork:  strings.ToLower(strings.TrimSpace(stringParam("network"))),
			SnellObfsMode: obfsMode,
			SnellObfsHost: stringParam("obfs_host"),
			SnellMode:     stringParam("mode"),
		}
		if err := settings.ValidateClientOutbound("snell"); err != nil {
			return "", errors.New("Snell 参数无效: " + err.Error())
		}
	case "socks", "mixed":
		if (strings.TrimSpace(stringParam("username")) == "") != (strings.TrimSpace(stringParam("password")) == "") {
			return "", errors.New("用户名和密码必须同时填写或同时留空")
		}
	case "vless", "vmess":
		if err := need("uuid"); err != nil {
			return "", err
		}
	case "trojan", "anytls", "hysteria2", "hysteria":
		if err := need("password"); err != nil {
			return "", err
		}
	case "shadowsocks":
		if err := need("password"); err != nil {
			return "", err
		}
	case "tuic":
		if err := need("uuid", "password"); err != nil {
			return "", err
		}
	default:
		return "", errors.New("暂不支持的结构化节点协议: " + req.Protocol)
	}
	if req.Protocol != "snell" {
		candidate := &model.CustomNode{
			Name: req.Name, Link: req.Link, Protocol: req.Protocol,
			Address: req.Address, Port: req.Port, Params: model.JSONText(req.Params),
		}
		n, ok := (&App{}).customNodeToNode(candidate)
		if !ok {
			return "", errors.New("节点参数无法解析")
		}
		if err := n.settings.ValidateClientOutbound(n.typ); err != nil {
			return "", errors.New("节点参数无效: " + err.Error())
		}
	}
	return "", nil
}

func (a *App) listCustomNodes(c *gin.Context) {
	var nodes []model.CustomNode
	if err := a.db.Where("hidden_by_subscription_rule = ?", false).Order("sort_order, id").Find(&nodes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]customNodeRow, 0, len(nodes))
	for i := range nodes {
		row := customNodeRow{CustomNode: nodes[i]}
		if normalized, ok := a.customNodeToNode(&nodes[i]); ok {
			uri, _ := singbox.BuildShareLink(normalized.clientNode())
			row.Detail = &customNodeDetail{
				Protocol: normalized.typ,
				Address:  normalized.server,
				Port:     normalized.port,
				Region:   normalized.region,
				URI:      uri,
				Params:   nodeParams(normalized),
			}
		}
		if !nodes[i].AllUsers && len(nodes[i].UserIDs) > 0 {
			var users []model.User
			a.db.Select("email").Where("id IN ?", nodes[i].UserIDs).Find(&users)
			for _, u := range users {
				row.UserEmails = append(row.UserEmails, u.Email)
			}
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"nodes": out})
}

func (a *App) createCustomNode(c *gin.Context) {
	var req customNodeReq
	if !bindJSON(c, &req) {
		return
	}
	parsedName, err := validateCustomNode(&req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = parsedName
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	// Audience defaults to "nobody": an empty user_ids list means "not assigned
	// to anyone", never "publish to every account". Callers must explicitly pass
	// all_users=true to broadcast a node. (The admin UI always sends the field.)
	allUsers := false
	if req.AllUsers != nil {
		allUsers = *req.AllUsers
	}
	node := &model.CustomNode{
		AllUsers:  allUsers,
		Name:      name,
		Group:     trimRunes(strings.TrimSpace(req.Group), 64),
		Link:      req.Link,
		Protocol:  req.Protocol,
		Address:   req.Address,
		Port:      req.Port,
		Params:    model.JSONText(req.Params),
		Enabled:   enabled,
		SortOrder: 0,
	}
	if allUsers {
		if req.ExcludedUserIDs != nil {
			node.ExcludedUserIDs = normalizedIDs(*req.ExcludedUserIDs)
		}
	} else {
		node.UserIDs = normalizedIDs(req.UserIDs)
	}
	if req.SortOrder != nil {
		node.SortOrder = *req.SortOrder
	}
	if err := a.db.Create(node).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"node": node})
}

func (a *App) updateCustomNode(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var node model.CustomNode
	if err := a.db.First(&node, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}
	unlock := a.lockCustomNodeSubscription(node.SubscriptionID)
	defer unlock()
	// Re-read after taking the subscription lock so a concurrent source delete
	// cannot turn this request into an orphaned managed node.
	if err := a.db.First(&node, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}
	var req customNodeReq
	if !bindJSON(c, &req) {
		return
	}
	if _, err := validateCustomNode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.Name) != "" {
		node.Name = strings.TrimSpace(req.Name)
	}
	// The admin UI always sends the complete node definition, so every field is
	// overwritten (link OR structured fields).
	node.Link = req.Link
	node.Protocol = req.Protocol
	node.Address = req.Address
	node.Port = req.Port
	node.Params = model.JSONText(req.Params)
	// Group is overwritten too: an empty value clears the previous grouping.
	node.Group = trimRunes(strings.TrimSpace(req.Group), 64)
	wasAllUsers := node.AllUsers
	// Same safe default as create: without an explicit all_users the node is not
	// broadcast to every account (empty user_ids = nobody, per the documented
	// contract). The admin UI always sends the field explicitly.
	allUsers := false
	if req.AllUsers != nil {
		allUsers = *req.AllUsers
	}
	node.AllUsers = allUsers
	if allUsers {
		node.UserIDs = []uint{}
		if req.ExcludedUserIDs != nil {
			node.ExcludedUserIDs = normalizedIDs(*req.ExcludedUserIDs)
		} else if !wasAllUsers {
			node.ExcludedUserIDs = []uint{}
		}
	} else {
		node.UserIDs = normalizedIDs(req.UserIDs)
		node.ExcludedUserIDs = []uint{}
	}
	if req.Enabled != nil {
		node.Enabled = *req.Enabled
	}
	if req.SortOrder != nil {
		node.SortOrder = *req.SortOrder
	}
	if err := a.db.Save(&node).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"node": node})
}

func (a *App) deleteCustomNode(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var node model.CustomNode
	if err := a.db.First(&node, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}
	unlock := a.lockCustomNodeSubscription(node.SubscriptionID)
	defer unlock()
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&node, id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&node).Error; err != nil {
			return err
		}
		if node.SubscriptionID != nil {
			var count int64
			if err := tx.Model(&model.CustomNode{}).
				Where("subscription_id = ? AND hidden_by_subscription_rule = ?", *node.SubscriptionID, false).
				Count(&count).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.CustomNodeSubscription{}).Where("id = ?", *node.SubscriptionID).Update("node_count", count).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// batchNodeIDs is the shared payload for batch operations on custom nodes.
type batchNodeIDs struct {
	IDs []uint `json:"ids"`
}

// batchDeleteCustomNodes deletes many custom nodes in one transaction. It is
// the backend for the table's multi-select delete action so N nodes do not
// require N round trips (and a partial failure cannot leave the list half
// deleted).
func (a *App) batchDeleteCustomNodes(c *gin.Context) {
	var req batchNodeIDs
	if !bindJSON(c, &req) {
		return
	}
	ids := normalizedIDs(req.IDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要删除的节点"})
		return
	}
	var managed int64
	if err := a.db.Model(&model.CustomNode{}).Where("id IN ? AND subscription_id IS NOT NULL", ids).Count(&managed).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if managed > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "所选节点包含订阅管理的节点，不能单独删除"})
		return
	}
	result := a.db.Delete(&model.CustomNode{}, ids)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "deleted": result.RowsAffected})
}

// batchSetCustomNodeGroup moves many custom nodes into one group (or clears
// the group when group is empty). Used by the table's "移动到分组" action and
// by the inline group picker on the group column.
func (a *App) batchSetCustomNodeGroup(c *gin.Context) {
	var req struct {
		IDs   []uint `json:"ids"`
		Group string `json:"group"`
	}
	if !bindJSON(c, &req) {
		return
	}
	ids := normalizedIDs(req.IDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要分组的节点"})
		return
	}
	var managed int64
	if err := a.db.Model(&model.CustomNode{}).Where("id IN ? AND subscription_id IS NOT NULL", ids).Count(&managed).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if managed > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "订阅节点的分组由订阅统一管理"})
		return
	}
	group := trimRunes(strings.TrimSpace(req.Group), 64)
	if err := a.db.Model(&model.CustomNode{}).
		Where("id IN ?", ids).
		Update("group", group).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "updated": int64(len(ids))})
}
