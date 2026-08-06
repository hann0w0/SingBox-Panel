package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

// customNodeReq is the admin payload for a hand-added external node. A node is
// defined by a share link (Link) OR structured fields (Protocol+Address+Port+
// Params). Audience fields are explicit so an empty allow-list can mean nobody
// without accidentally publishing the node to every account.
type customNodeReq struct {
	Name            string          `json:"name"`
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
	UserEmails []string `json:"user_emails,omitempty"`
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
		if versionJSON, ok := raw["version"]; ok {
			var version int
			if err := json.Unmarshal(versionJSON, &version); err != nil || (version != 5 && version != 6) {
				return "", errors.New("Snell 版本必须是 5 或 6")
			}
			if version == 6 {
				if psk := stringParam("psk"); len(psk) < 12 || len(psk) > 255 {
					return "", errors.New("Snell v6 的 PSK 长度必须在 12-255 字节之间")
				}
			}
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
	return "", nil
}

func (a *App) listCustomNodes(c *gin.Context) {
	var nodes []model.CustomNode
	a.db.Order("sort_order, id").Find(&nodes)
	out := make([]customNodeRow, 0, len(nodes))
	for i := range nodes {
		row := customNodeRow{CustomNode: nodes[i]}
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
	allUsers := len(req.UserIDs) == 0
	if req.AllUsers != nil {
		allUsers = *req.AllUsers
	}
	node := &model.CustomNode{
		AllUsers:  allUsers,
		Name:      name,
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
	wasAllUsers := node.AllUsers
	allUsers := len(req.UserIDs) == 0
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
	if err := a.db.Delete(&model.CustomNode{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
