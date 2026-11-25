package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// UserPrivateOutbound is the owner-facing view of a credential-only exit. It
// deliberately omits credential fields and the entry server: an exit is
// independent until a forward rule references it from an inbound.
type UserPrivateOutbound struct {
	ID     uint   `json:"id"`
	Tag    string `json:"tag"`
	Type   string `json:"type"`
	Remark string `json:"remark"`
}

type UserInboundOption struct {
	ID         uint   `json:"id"`
	ServerID   uint   `json:"server_id"`
	ServerName string `json:"server_name"`
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	Port       int    `json:"port"`
	MultiUser  bool   `json:"multi_user"`
}

// listUserInbounds lists only the inbounds the caller may use, and identifies
// whether each one can distinguish users for auth_user routing.
func (a *App) listUserInbounds(c *gin.Context) {
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if u.Role != model.RoleAdmin && !userActive(&u) {
		c.JSON(http.StatusForbidden, gin.H{"error": "账号已停用或已到期"})
		return
	}
	var servers []model.Server
	if err := a.db.Select("id", "name", "config_mode", "raw_config").Where("id IN ?", normalizedIDs(u.ServerIDs)).Find(&servers).Error; err != nil {
		log.Printf("list user inbounds servers: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取可用服务器失败"})
		return
	}
	names := make(map[uint]string, len(servers))
	for _, server := range servers {
		if server.EffectiveConfigMode() == model.ConfigModeManaged {
			names[server.ID] = server.Name
		}
	}
	var inbounds []model.Inbound
	if err := a.db.Where("server_id IN ? AND enabled = ?", normalizedIDs(u.ServerIDs), true).Order("server_id, id").Find(&inbounds).Error; err != nil {
		log.Printf("list user inbounds: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取可用入口失败"})
		return
	}
	out := make([]UserInboundOption, 0, len(inbounds))
	for _, inbound := range inbounds {
		if _, managed := names[inbound.ServerID]; !managed || !u.HasInbound(inbound.ServerID, inbound.ID) {
			continue
		}
		var st singbox.InboundSettings
		if len(inbound.Settings) > 0 {
			_ = json.Unmarshal(inbound.Settings, &st)
		}
		out = append(out, UserInboundOption{
			ID: inbound.ID, ServerID: inbound.ServerID, ServerName: names[inbound.ServerID],
			Tag: inbound.Tag, Type: string(inbound.Type), Port: st.EffectiveClientPort(inbound.ListenPort),
			MultiUser: st.UseMultiUser(string(inbound.Type)),
		})
	}
	c.JSON(http.StatusOK, gin.H{"inbounds": out})
}

// listUserOutbounds returns only outbounds owned by the current account.
func (a *App) listUserOutbounds(c *gin.Context) {
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if u.Role != model.RoleAdmin && !userActive(&u) {
		c.JSON(http.StatusForbidden, gin.H{"error": "账号已停用或已到期"})
		return
	}
	var rows []model.Outbound
	if err := a.db.Where("owner_user_id = ?", u.ID).Order("sort, id").Find(&rows).Error; err != nil {
		log.Printf("list user outbounds: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取专属出站失败"})
		return
	}
	out := make([]UserPrivateOutbound, 0, len(rows))
	for _, row := range rows {
		out = append(out, UserPrivateOutbound{
			ID: row.ID, Tag: row.Tag, Type: row.Type, Remark: row.Remark,
		})
	}
	c.JSON(http.StatusOK, gin.H{"outbounds": out})
}

// adminPrivateOutbound is deliberately opaque. Operators can provision or
// remove a private exit without reading its label, protocol, remark, or
// credential: those identify where the account is connecting. The real
// outbound remains visible only in the generated sing-box configuration.
type adminPrivateOutbound struct {
	ID uint `json:"id"`
}

// listAdminUserOutbounds lists private exits by opaque ID for provisioning and
// deletion. Entry servers remain hidden as well; the account's own console is
// the only UI that shows an exit's label and protocol.
func (a *App) listAdminUserOutbounds(c *gin.Context) {
	uid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var u model.User
	if err := a.db.Select("id").First(&u, uid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	var rows []model.Outbound
	if err := a.db.Where("owner_user_id = ?", uid).Order("sort, id").Find(&rows).Error; err != nil {
		log.Printf("list admin user outbounds: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取专属出站失败"})
		return
	}
	out := make([]adminPrivateOutbound, 0, len(rows))
	for _, row := range rows {
		out = append(out, adminPrivateOutbound{ID: row.ID})
	}
	c.JSON(http.StatusOK, gin.H{"outbounds": out})
}

type userOutboundReq struct {
	Tag      string          `json:"tag"`
	Type     string          `json:"type"`
	Settings json.RawMessage `json:"settings"`
	Remark   *string         `json:"remark"`
}

const maxUserPrivateOutbounds = 200

// importUserOutbound accepts a single URI or a subscription URL for one of the
// caller's own outbounds. The URL fetch and parser are shared with the admin
// import path, so protocol support and SSRF limits stay identical.
func (a *App) importUserOutbound(c *gin.Context) {
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if u.Role != model.RoleAdmin && (!userActive(&u) || !u.ForwardingEnabled) {
		c.JSON(http.StatusForbidden, gin.H{"error": "账号不可用或尚未开启转发权限"})
		return
	}
	var req struct {
		URI string `json:"uri"`
		URL string `json:"url"`
		Tag string `json:"tag"`
	}
	if !bindJSON(c, &req) {
		return
	}
	uri := strings.TrimSpace(req.URI)
	urlText := strings.TrimSpace(req.URL)
	if uri == "" && urlText == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写节点 URI 或订阅地址"})
		return
	}
	var nodes []singbox.ImportedNode
	if urlText != "" {
		parsedURL, ok := singleRemoteSubscriptionURL(urlText)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "订阅地址必须是单个 HTTP(S) URL"})
			return
		}
		raw, err := fetchRemoteSubscription(c.Request.Context(), parsedURL)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		parsed, err := singbox.ParseSubscription(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		nodes = parsed.Nodes
	} else {
		client, err := singbox.ParseShareLink(uri)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "链接解析失败：" + err.Error()})
			return
		}
		if client.Type == "http" && client.User.Username == "" && client.Settings.Username == "" && client.User.Password == "" && client.Settings.Password == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "暂不支持无认证的 HTTP(S) 链接作为专属出站"})
			return
		}
		nodes = []singbox.ImportedNode{{Name: client.Name, Link: uri, Protocol: client.Type, Address: client.Server, Port: client.ServerPort}}
	}
	if len(nodes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有解析到可用节点"})
		return
	}
	prepared := make([]outboundReq, 0, len(nodes))
	var lastValidationErr error
	for _, imported := range nodes {
		client, ok := a.importedNodeClient(imported)
		if !ok {
			continue
		}
		body, err := outboundFromClientNode(client)
		if err != nil {
			continue
		}
		body.Settings = normalizeOutboundSettingsJSON(body.Settings)
		tag := strings.TrimSpace(req.Tag)
		if tag == "" {
			tag = body.Tag
		}
		if len(nodes) > 1 {
			tag = fmt.Sprintf("%s-%d", tag, len(prepared)+1)
		}
		if err := a.validatePrivateOutbound(tag, body.Type, body.Settings); err != nil {
			lastValidationErr = err
			if urlText != "" {
				continue
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		body.Tag = tag
		prepared = append(prepared, body)
		if len(prepared) > maxUserPrivateOutbounds {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("专属出站数量不能超过 %d 个", maxUserPrivateOutbounds)})
			return
		}
	}
	if len(prepared) == 0 {
		msg := "该订阅没有可转换为出站的节点"
		if lastValidationErr != nil {
			msg += "：" + lastValidationErr.Error()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	a.userAccessMu.Lock()
	created := make([]UserPrivateOutbound, 0, len(prepared))
	statusCode := http.StatusBadRequest
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&u, currentUID(c)).Error; err != nil {
			return err
		}
		if u.Role != model.RoleAdmin && (!userActive(&u) || !u.ForwardingEnabled) {
			statusCode = http.StatusForbidden
			return fmt.Errorf("账号不可用或尚未开启转发权限")
		}
		var existingCount int64
		if err := tx.Model(&model.Outbound{}).Where("owner_user_id = ?", u.ID).Count(&existingCount).Error; err != nil {
			statusCode = http.StatusInternalServerError
			return err
		}
		if existingCount+int64(len(prepared)) > maxUserPrivateOutbounds {
			return fmt.Errorf("专属出站数量不能超过 %d 个", maxUserPrivateOutbounds)
		}
		for _, body := range prepared {
			tag, err := availablePrivateOutboundTag(tx, body.Tag)
			if err != nil {
				return err
			}
			outbound := model.Outbound{OwnerUserID: u.ID, Tag: tag, Type: body.Type, Settings: model.JSONText(body.Settings)}
			if err := tx.Create(&outbound).Error; err != nil {
				return err
			}
			created = append(created, UserPrivateOutbound{
				ID: outbound.ID, Tag: outbound.Tag, Type: outbound.Type, Remark: outbound.Remark,
			})
		}
		return nil
	})
	a.userAccessMu.Unlock()
	if err != nil {
		if statusCode == http.StatusInternalServerError {
			log.Printf("import user outbounds: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存专属出站失败"})
			return
		}
		c.JSON(statusCode, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"outbounds": created})
}

// previewUserOutbound parses a single share link for an account without saving it,
// returning connection details and a candidate unique tag for confirmation.
func (a *App) previewUserOutbound(c *gin.Context) {
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if u.Role != model.RoleAdmin && (!userActive(&u) || !u.ForwardingEnabled) {
		c.JSON(http.StatusForbidden, gin.H{"error": "账号不可用或尚未开启转发权限"})
		return
	}
	c.Header("Cache-Control", "no-store")
	var req struct {
		URI string `json:"uri"`
	}
	if !bindJSON(c, &req) {
		return
	}
	req.URI = strings.TrimSpace(req.URI)
	if req.URI == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写一条节点 URI"})
		return
	}
	if len(req.URI) > 65536 || strings.ContainsAny(req.URI, "\r\n") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请一次填写一条节点 URI（最多 64 KB）"})
		return
	}
	client, err := singbox.ParseShareLink(req.URI)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "链接解析失败：" + err.Error()})
		return
	}
	if client.Type == "http" && client.User.Username == "" && client.Settings.Username == "" && client.User.Password == "" && client.Settings.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "暂不支持无认证的 HTTP(S) 链接作为专属出站"})
		return
	}
	body, err := outboundFromClientNode(client)
	if err == nil {
		body.Settings = normalizeOutboundSettingsJSON(body.Settings)
		body.Tag, err = availablePrivateOutboundTag(a.db, body.Tag)
	}
	if err == nil {
		err = a.validatePrivateOutbound(body.Tag, body.Type, body.Settings)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"outbound": body})
}

// createUserOutbound creates a manual credential-only private exit owned by the
// signed-in account, sharing the same protocol and SSRF constraints as imports.
func (a *App) createUserOutbound(c *gin.Context) {
	var req userOutboundReq
	if !bindJSON(c, &req) {
		return
	}
	req.Tag = strings.TrimSpace(req.Tag)
	if req.Tag == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "出站名称必填"})
		return
	}
	req.Settings = normalizeOutboundSettingsJSON(req.Settings)
	if err := a.validatePrivateOutbound(req.Tag, req.Type, req.Settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	a.userAccessMu.Lock()
	var created UserPrivateOutbound
	statusCode := http.StatusBadRequest
	err := a.db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		if err := tx.First(&u, currentUID(c)).Error; err != nil {
			statusCode = http.StatusNotFound
			return fmt.Errorf("user not found")
		}
		if u.Role != model.RoleAdmin && (!userActive(&u) || !u.ForwardingEnabled) {
			statusCode = http.StatusForbidden
			return fmt.Errorf("账号不可用或尚未开启转发权限")
		}
		var existingCount int64
		if err := tx.Model(&model.Outbound{}).Where("owner_user_id = ?", u.ID).Count(&existingCount).Error; err != nil {
			statusCode = http.StatusInternalServerError
			return err
		}
		if existingCount+1 > maxUserPrivateOutbounds {
			return fmt.Errorf("专属出站数量不能超过 %d 个", maxUserPrivateOutbounds)
		}
		tag, err := availablePrivateOutboundTag(tx, req.Tag)
		if err != nil {
			return err
		}
		ob := model.Outbound{
			ServerID: 0, OwnerUserID: u.ID, Tag: tag, Type: req.Type,
			Settings: model.JSONText(req.Settings),
		}
		if req.Remark != nil {
			ob.Remark = *req.Remark
		}
		if err := tx.Create(&ob).Error; err != nil {
			statusCode = http.StatusInternalServerError
			return err
		}
		created = UserPrivateOutbound{ID: ob.ID, Tag: ob.Tag, Type: ob.Type, Remark: ob.Remark}
		return nil
	})
	a.userAccessMu.Unlock()
	if err != nil {
		if statusCode == http.StatusInternalServerError {
			log.Printf("create user outbound: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存专属出站失败"})
			return
		}
		c.JSON(statusCode, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"outbound": created})
}

// deleteUserOutbound lets an account clean up an unused private exit without
// administrator involvement. Referenced exits stay protected until the user
// removes the forwarding node or routing rule that depends on them.
func (a *App) deleteUserOutbound(c *gin.Context) {
	oid, ok := uintParam(c, "outboundID")
	if !ok {
		return
	}
	a.userAccessMu.Lock()
	defer a.userAccessMu.Unlock()
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if u.Role != model.RoleAdmin && !userActive(&u) {
		c.JSON(http.StatusForbidden, gin.H{"error": "账号已停用或已到期"})
		return
	}
	var ob model.Outbound
	if err := a.db.Where("id = ? AND owner_user_id = ? AND server_id = 0", oid, u.ID).First(&ob).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "专属出站不存在"})
		return
	}
	var using int64
	if err := a.db.Model(&model.UserForward{}).Where("outbound_id = ?", ob.ID).Count(&using).Error; err != nil {
		log.Printf("check user outbound forward refs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除专属出站失败"})
		return
	}
	if using > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该专属出站仍被转发节点使用，请先删除相关转发"})
		return
	}
	if err := a.db.Model(&model.RouteRule{}).Where("outbound = ?", ob.Tag).Count(&using).Error; err != nil {
		log.Printf("check user outbound route refs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除专属出站失败"})
		return
	}
	if using > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该专属出站仍被分流规则使用，请联系管理员处理"})
		return
	}
	if err := a.db.Where("id = ? AND owner_user_id = ?", ob.ID, u.ID).Delete(&model.Outbound{}).Error; err != nil {
		log.Printf("delete user outbound: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除专属出站失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// availablePrivateOutboundTag returns a tag that is unique across all stored
// outbounds. Tags become sing-box outbound tags on every entry server that
// references the exit, so a private exit cannot safely reuse a shared tag.
// Imports suffix collisions instead of failing an otherwise valid subscription.
func (a *App) availablePrivateOutboundTag(tag string) (string, error) {
	return availablePrivateOutboundTag(a.db, tag)
}

func overlayOutboundTagExistsAnywhere(db *gorm.DB, tag string) (bool, error) {
	var servers []model.Server
	if err := db.Select("id", "config_overlay").Where("config_overlay IS NOT NULL AND config_overlay <> ''").Find(&servers).Error; err != nil {
		return false, err
	}
	for i := range servers {
		if singbox.OverlayHasOutbound(servers[i].ConfigOverlay, tag) {
			return true, nil
		}
	}
	return false, nil
}

func availablePrivateOutboundTag(db *gorm.DB, tag string) (string, error) {
	if strings.TrimSpace(tag) == "" {
		return "", fmt.Errorf("出站名称必填")
	}
	if builtinOutboundTags[tag] {
		return "", fmt.Errorf("%s 是内置标签，不能作为专属出站名称", tag)
	}
	base := tag
	for suffix := 2; suffix < 100; suffix++ {
		var count int64
		if err := db.Model(&model.Outbound{}).
			Where("tag = ?", tag).Count(&count).Error; err != nil {
			return "", err
		}
		overlayConflict, err := overlayOutboundTagExistsAnywhere(db, tag)
		if err != nil {
			return "", err
		}
		if count == 0 && !overlayConflict {
			return tag, nil
		}
		tag = fmt.Sprintf("%s-%d", trimRunes(base, 58), suffix)
	}
	// Adding more than 98 identical tags is rare, but the account should still be
	// able to import a subscription whose provider reuses one display name.
	for attempt := 0; attempt < 16; attempt++ {
		candidate := fmt.Sprintf("%s-%s", trimRunes(base, 56), randHex(3))
		var count int64
		if err := db.Model(&model.Outbound{}).Where("tag = ?", candidate).Count(&count).Error; err != nil {
			return "", err
		}
		overlayConflict, err := overlayOutboundTagExistsAnywhere(db, candidate)
		if err != nil {
			return "", err
		}
		if count == 0 && !overlayConflict {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("专属出站名称重复过多，请更换名称")
}

// validatePrivateOutbound validates a credential-only exit before it is bound
// to an entry server. Server-specific uniqueness and routing references are
// checked when a forward is created.
func (a *App) validatePrivateOutbound(tag, typ string, raw json.RawMessage) error {
	if err := validateTag(tag); err != nil {
		return err
	}
	if !supportedOutboundTypes[typ] || typ == "direct" {
		return fmt.Errorf("unsupported outbound type: %s", typ)
	}
	var st outboundStoredSettings
	if len(raw) == 0 || json.Unmarshal(raw, &st) != nil {
		return fmt.Errorf("出站参数不是合法 JSON")
	}
	if strings.TrimSpace(st.Server) == "" || st.ServerPort < 1 || st.ServerPort > 65535 {
		return fmt.Errorf("落地服务器和 1-65535 端口必填")
	}
	if forbiddenPrivateOutboundHost(st.Server) {
		return fmt.Errorf("落地服务器不能指向本机、内网或保留地址")
	}
	switch typ {
	case "vless", "vmess":
		if st.UUID == "" {
			return fmt.Errorf("%s UUID 必填", typ)
		}
	case "trojan", "hysteria2", "anytls":
		if st.Password == "" {
			return fmt.Errorf("%s 密码必填", typ)
		}
	case "tuic":
		if st.UUID == "" || st.Password == "" {
			return fmt.Errorf("tuic UUID 和密码必填")
		}
	case "shadowsocks":
		if st.Settings.Method == "" || (st.Password == "" && st.Settings.SSServerPSK == "") {
			return fmt.Errorf("shadowsocks 加密方式和密码必填")
		}
	case "socks", "http":
		if (strings.TrimSpace(st.Username) == "") != (st.Password == "") {
			return fmt.Errorf("%s 用户名和密码必须同时填写或同时留空", typ)
		}
	case "snell":
		if st.Password == "" && st.Settings.SnellPSK == "" {
			return fmt.Errorf("snell PSK 必填")
		}
		if st.Settings.SnellPSK == "" {
			st.Settings.SnellPSK = st.Password
		}
	}
	return st.Settings.ValidateClientOutbound(typ)
}

// importedNodeClient turns a parsed subscription item into a client renderer
// input. URI imports keep their link; structured imports use Params, which are
// the same fields the external-node importer already validates.
func (a *App) importedNodeClient(item singbox.ImportedNode) (singbox.ClientNode, bool) {
	if strings.TrimSpace(item.Link) != "" {
		client, err := singbox.ParseShareLink(item.Link)
		if err != nil {
			return singbox.ClientNode{}, false
		}
		if item.Name != "" {
			client.Name = item.Name
		}
		return client, true
	}
	params, err := json.Marshal(item.Params)
	if err != nil {
		return singbox.ClientNode{}, false
	}
	node, ok := a.customNodeToNode(&model.CustomNode{
		Name: item.Name, Protocol: item.Protocol, Address: item.Address, Port: item.Port,
		Params: model.JSONText(params),
	})
	if !ok {
		return singbox.ClientNode{}, false
	}
	return node.clientNode(), true
}

type userForwardReq struct {
	InboundServerID uint    `json:"inbound_server_id"`
	InboundTag      string  `json:"inbound_tag"`
	OutboundID      uint    `json:"outbound_id"`
	Remark          *string `json:"remark"`
}

// listUserForwards returns the derived nodes that belong to the caller.
func (a *App) listUserForwards(c *gin.Context) {
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if u.Role != model.RoleAdmin && !userActive(&u) {
		c.JSON(http.StatusForbidden, gin.H{"error": "账号已停用或已到期"})
		return
	}
	if !u.ForwardingEnabled {
		c.JSON(http.StatusOK, gin.H{"forwards": []UserForward{}})
		return
	}
	var rows []model.UserForward
	if err := a.db.Where("user_id = ?", u.ID).Order("id").Find(&rows).Error; err != nil {
		log.Printf("list user forwards: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取转发节点失败"})
		return
	}
	if len(rows) == 0 {
		c.JSON(http.StatusOK, gin.H{"forwards": []UserForward{}})
		return
	}
	var servers []model.Server
	if err := a.db.Select("id", "name").Where("id IN ?", normalizedIDs(u.ServerIDs)).Find(&servers).Error; err != nil {
		log.Printf("list user forwards servers: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取转发节点失败"})
		return
	}
	var inbounds []model.Inbound
	if err := a.db.Select("id", "server_id", "tag", "enabled", "type", "settings").Where("id IN ?", forwardInboundIDs(rows)).Find(&inbounds).Error; err != nil {
		log.Printf("list user forwards inbounds: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取转发节点失败"})
		return
	}
	var outbounds []model.Outbound
	if err := a.db.Select("id", "tag").Where("id IN ?", forwardOutboundIDs(rows)).Find(&outbounds).Error; err != nil {
		log.Printf("list user forwards outbounds: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取转发节点失败"})
		return
	}
	serverNames := make(map[uint]string, len(servers))
	for _, server := range servers {
		serverNames[server.ID] = server.Name
	}
	inboundsByID := make(map[uint]model.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundsByID[inbound.ID] = inbound
	}
	outboundTags := make(map[uint]string, len(outbounds))
	for _, outbound := range outbounds {
		outboundTags[outbound.ID] = outbound.Tag
	}
	forwards := make([]UserForward, 0)
	for _, row := range rows {
		inbound, found := inboundsByID[row.InboundID]
		outboundTag := outboundTags[row.OutboundID]
		if !found || inbound.Tag == "" || outboundTag == "" || inbound.ServerID != row.ServerID || !u.HasServer(inbound.ServerID) {
			continue
		}
		if !u.HasInbound(inbound.ServerID, row.InboundID) {
			continue
		}
		var st singbox.InboundSettings
		enabled := inbound.Enabled && json.Unmarshal(inbound.Settings, &st) == nil && st.UseMultiUser(string(inbound.Type))
		forwards = append(forwards, UserForward{
			ID: row.ID, ServerID: row.ServerID, ServerName: serverNames[row.ServerID],
			InboundTag: inbound.Tag, OutboundID: row.OutboundID, OutboundTag: outboundTag,
			NodeName: forwardNodeName(serverNames[row.ServerID], inbound.Tag, outboundTag, row.Remark), Remark: row.Remark, Enabled: enabled,
		})
	}
	c.JSON(http.StatusOK, gin.H{"forwards": forwards})
}

func forwardInboundIDs(rows []model.UserForward) []uint {
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.InboundID)
	}
	return normalizedIDs(ids)
}

func forwardOutboundIDs(rows []model.UserForward) []uint {
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.OutboundID)
	}
	return normalizedIDs(ids)
}

// UserForward is the member-facing view of one derived node. It represents
// inbound → outbound, not a server-wide routing rule.
type UserForward struct {
	ID          uint   `json:"id"`
	ServerID    uint   `json:"server_id"`
	ServerName  string `json:"server_name"`
	InboundTag  string `json:"inbound_tag"`
	OutboundID  uint   `json:"outbound_id"`
	OutboundTag string `json:"outbound_tag"`
	NodeName    string `json:"node_name"`
	Remark      string `json:"remark"`
	Enabled     bool   `json:"enabled"`
}

func forwardNodeName(serverName, inboundTag, outboundTag, remark string) string {
	if remark = strings.TrimSpace(remark); remark != "" {
		return serverName + " - " + remark
	}
	return serverName + " - " + inboundTag + " → " + outboundTag
}

// createUserForward derives a separately selectable node with its own
// credential. It leaves the original inbound node and its password untouched.
func (a *App) createUserForward(c *gin.Context) {
	var req userForwardReq
	if !bindJSON(c, &req) {
		return
	}
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if strings.TrimSpace(req.InboundTag) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择入口节点"})
		return
	}
	var inbound model.Inbound
	inboundQuery := a.db.Where("server_id IN ? AND tag = ? AND enabled = ?", normalizedIDs(u.ServerIDs), req.InboundTag, true).Order("server_id, id")
	if req.InboundServerID != 0 {
		if !u.HasServer(req.InboundServerID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "没有该服务器的访问权限"})
			return
		}
		if err := inboundQuery.Where("server_id = ?", req.InboundServerID).First(&inbound).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "入口节点不存在或未启用"})
			return
		}
	} else {
		var candidates []model.Inbound
		if err := inboundQuery.Find(&candidates).Error; err != nil {
			log.Printf("create user forward candidates: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取入口节点失败"})
			return
		}
		var accessible []model.Inbound
		for _, candidate := range candidates {
			if u.HasInbound(candidate.ServerID, candidate.ID) {
				accessible = append(accessible, candidate)
			}
		}
		if len(accessible) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "入口节点不存在或未启用"})
			return
		}
		if len(accessible) > 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "多个服务器存在同名入口节点，请明确指定 inbound_server_id"})
			return
		}
		inbound = accessible[0]
	}
	unlockOperation := a.lockServerOperation(inbound.ServerID)
	defer unlockOperation()
	if !a.requireManagedMode(c, inbound.ServerID) {
		return
	}
	a.userAccessMu.Lock()
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		a.userAccessMu.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if !userActive(&u) || !u.ForwardingEnabled {
		a.userAccessMu.Unlock()
		c.JSON(http.StatusForbidden, gin.H{"error": "账号不可用或尚未开启转发权限"})
		return
	}
	if err := a.db.Where("id = ? AND enabled = ?", inbound.ID, true).First(&inbound).Error; err != nil {
		a.userAccessMu.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": "入口节点不存在或未启用"})
		return
	}
	if !u.HasInbound(inbound.ServerID, inbound.ID) {
		a.userAccessMu.Unlock()
		c.JSON(http.StatusForbidden, gin.H{"error": "没有该入口节点的访问权限"})
		return
	}
	var outbound model.Outbound
	if err := a.db.Where("id = ? AND owner_user_id = ? AND server_id = 0", req.OutboundID, u.ID).First(&outbound).Error; err != nil {
		a.userAccessMu.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "专属出站不存在"})
		return
	}
	if err := a.validatePrivateOutbound(outbound.Tag, outbound.Type, json.RawMessage(outbound.Settings)); err != nil {
		a.userAccessMu.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var entryServer model.Server
	if err := a.db.Select("id", "config_overlay").First(&entryServer, inbound.ServerID).Error; err != nil {
		a.userAccessMu.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	var sharedCollision int64
	if err := a.db.Model(&model.Outbound{}).
		Where("server_id = ? AND owner_user_id = 0 AND tag = ?", inbound.ServerID, outbound.Tag).
		Count(&sharedCollision).Error; err != nil {
		a.userAccessMu.Unlock()
		log.Printf("create user forward shared outbound collision check: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建转发节点失败"})
		return
	}
	if sharedCollision > 0 || singbox.OverlayHasOutbound(entryServer.ConfigOverlay, outbound.Tag) {
		a.userAccessMu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("专属出站标签 %q 与入口服务器已有出站冲突，请重命名后重试", outbound.Tag)})
		return
	}
	var st singbox.InboundSettings
	if len(inbound.Settings) > 0 {
		if err := json.Unmarshal(inbound.Settings, &st); err != nil {
			a.userAccessMu.Unlock()
			c.JSON(http.StatusBadRequest, gin.H{"error": "入口节点参数无效"})
			return
		}
	}
	if !st.UseMultiUser(string(inbound.Type)) {
		a.userAccessMu.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": "入口节点必须启用多用户模式，才能按用户分流"})
		return
	}
	var duplicate int64
	if err := a.db.Model(&model.UserForward{}).
		Where("user_id = ? AND server_id = ? AND inbound_id = ? AND outbound_id = ?", u.ID, inbound.ServerID, inbound.ID, outbound.ID).
		Count(&duplicate).Error; err != nil {
		a.userAccessMu.Unlock()
		log.Printf("create user forward duplicate check: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建转发节点失败"})
		return
	}
	if duplicate > 0 {
		a.userAccessMu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "该入口与出站的转发已存在"})
		return
	}
	forward := model.UserForward{
		UserID: u.ID, ServerID: inbound.ServerID, InboundID: inbound.ID, OutboundID: outbound.ID,
	}
	if req.Remark != nil {
		forward.Remark = *req.Remark
	}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&forward).Error; err != nil {
			return err
		}
		if err := reconcileForwardRule(tx, &forward, &u, &inbound, &outbound); err != nil {
			return err
		}
		return queueProxyAccessSync(tx, []uint{inbound.ServerID})
	})
	a.userAccessMu.Unlock()
	if err != nil {
		if duplicateDatabaseError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "该入口与出站的转发已存在"})
			return
		}
		log.Printf("create user forward: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建转发节点失败"})
		return
	}
	result := a.orch.ApplyDesiredConfig(c.Request.Context(), inbound.ServerID)
	if result.ApplyState == ConfigFailed {
		a.userAccessMu.Lock()
		rollbackErr := a.db.Transaction(func(tx *gorm.DB) error {
			if err := deleteForwardState(tx, []model.UserForward{forward}); err != nil {
				return err
			}
			return queueProxyAccessSync(tx, []uint{inbound.ServerID})
		})
		a.userAccessMu.Unlock()
		if rollbackErr != nil {
			log.Printf("rollback failed user forward %d: %v", forward.ID, rollbackErr)
		} else {
			a.orch.PushConfigIfManaged(inbound.ServerID)
		}
		errMsg := "下发转发配置失败"
		if result.ApplyError != "" {
			errMsg += "：" + result.ApplyError
		}
		c.JSON(http.StatusBadGateway, gin.H{
			"error":       errMsg,
			"apply_state": result.ApplyState,
			"apply_error": result.ApplyError,
		})
		return
	}
	a.reconcileNodeConsumers(inbound.ServerID)
	payload := gin.H{
		"forward":     forward,
		"apply_state": result.ApplyState,
	}
	if result.ApplyError != "" {
		payload["apply_error"] = result.ApplyError
	}
	var srv model.Server
	if err := a.db.Select("config_revision", "config_applied_revision").First(&srv, inbound.ServerID).Error; err == nil {
		payload["config_dirty"] = srv.ConfigRevision > srv.ConfigAppliedRevision
	}
	c.JSON(http.StatusOK, payload)
}

func (a *App) deleteUserForward(c *gin.Context) {
	rid, ok := uintParam(c, "forwardID")
	if !ok {
		return
	}
	var forward model.UserForward
	if err := a.db.Where("id = ? AND user_id = ?", rid, currentUID(c)).First(&forward).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "转发不存在"})
		return
	}
	unlockOperation := a.lockServerOperation(forward.ServerID)
	defer unlockOperation()
	a.userAccessMu.Lock()
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND user_id = ?", rid, currentUID(c)).First(&forward).Error; err != nil {
			return err
		}
		if err := deleteForwardState(tx, []model.UserForward{forward}); err != nil {
			return err
		}
		return queueProxyAccessSync(tx, []uint{forward.ServerID})
	})
	a.userAccessMu.Unlock()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "转发不存在"})
			return
		}
		log.Printf("delete user forward: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除转发节点失败"})
		return
	}
	a.applyConfigAndRespond(c, forward.ServerID, gin.H{"ok": true})
}

// reconcileForwardRule keeps the derived credential's route rule in sync with
// a forward record. The rule matches only the derived identity and inbound;
// the account's original identity keeps its existing route and password.
func reconcileForwardRule(db *gorm.DB, forward *model.UserForward, user *model.User, inbound *model.Inbound, outbound *model.Outbound) error {
	match, err := json.Marshal(singbox.RuleInput{
		Action:   "route",
		AuthUser: []string{forwardTrafficName(user, forward.ID)},
		Inbound:  []string{inbound.Tag},
	})
	if err != nil {
		return err
	}
	var rules []model.RouteRule
	if err := db.Where("server_id = ?", forward.ServerID).Find(&rules).Error; err != nil {
		return err
	}
	for _, rule := range rules {
		var existing singbox.RuleInput
		if len(rule.Match) > 0 && json.Unmarshal(rule.Match, &existing) == nil &&
			forwardRuleMatches(existing, *forward) {
			return db.Model(&model.RouteRule{}).Where("id = ?", rule.ID).Updates(map[string]any{"match": model.JSONText(match), "outbound": outbound.Tag}).Error
		}
	}
	return db.Create(&model.RouteRule{
		ServerID: forward.ServerID, Outbound: outbound.Tag, Match: model.JSONText(match), Enabled: true,
	}).Error
}

func forwardRuleMatches(match singbox.RuleInput, forward model.UserForward) bool {
	return len(match.AuthUser) == 1 && strings.HasPrefix(match.AuthUser[0], fmt.Sprintf("%s%d:%d:", forwardTrafficNamePrefix, forward.UserID, forward.ID))
}

func deleteForwardState(tx *gorm.DB, forwards []model.UserForward) error {
	if len(forwards) == 0 {
		return nil
	}
	var serverIDs, forwardIDs []uint
	for _, forward := range forwards {
		serverIDs = append(serverIDs, forward.ServerID)
		forwardIDs = append(forwardIDs, forward.ID)
	}
	var rules []model.RouteRule
	if err := tx.Where("server_id IN ?", normalizedIDs(serverIDs)).Find(&rules).Error; err != nil {
		return err
	}
	for _, rule := range rules {
		var match singbox.RuleInput
		if json.Unmarshal(rule.Match, &match) != nil {
			continue
		}
		for _, forward := range forwards {
			if rule.ServerID == forward.ServerID && forwardRuleMatches(match, forward) {
				if err := tx.Delete(&rule).Error; err != nil {
					return err
				}
				break
			}
		}
	}
	if err := deleteUserNodeOrderRefs(tx, userNodeTypeForward, forwardIDs); err != nil {
		return err
	}
	return tx.Where("id IN ?", forwardIDs).Delete(&model.UserForward{}).Error
}

// createAdminUserOutbound creates a credential-only exit owned by one user.
// Entry servers are chosen later by forwards; no config push is needed here.
func (a *App) createAdminUserOutbound(c *gin.Context) {
	uid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req userOutboundReq
	if !bindJSON(c, &req) {
		return
	}
	a.userAccessMu.Lock()
	defer a.userAccessMu.Unlock()
	var owner model.User
	if err := a.db.Select("id", "role").First(&owner, uid).Error; err != nil || owner.Role != model.RoleUser {
		c.JSON(http.StatusNotFound, gin.H{"error": "专属出站所属用户不存在"})
		return
	}
	req.Tag = strings.TrimSpace(req.Tag)
	if req.Tag == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标签必填"})
		return
	}
	req.Settings = normalizeOutboundSettingsJSON(req.Settings)
	if err := a.validatePrivateOutbound(req.Tag, req.Type, req.Settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	tag, err := a.availablePrivateOutboundTag(req.Tag)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ob := &model.Outbound{
		ServerID: 0, OwnerUserID: uid, Tag: tag, Type: req.Type,
		Settings: model.JSONText(req.Settings),
	}
	if req.Remark != nil {
		ob.Remark = *req.Remark
	}
	if err := a.db.Create(ob).Error; err != nil {
		log.Printf("create admin user outbound: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建专属出站失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"outbound": adminPrivateOutbound{ID: ob.ID}})
}

func (a *App) deleteAdminUserOutbound(c *gin.Context) {
	uid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	a.userAccessMu.Lock()
	defer a.userAccessMu.Unlock()
	oid, ok := uintParam(c, "outboundID")
	if !ok {
		return
	}
	var ob model.Outbound
	if err := a.db.Where("id = ? AND owner_user_id = ?", oid, uid).First(&ob).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "outbound not found"})
		return
	}
	var owner model.User
	if err := a.db.Select("id", "server_ids").First(&owner, uid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	serverIDs := owner.ServerIDs
	var using int64
	if err := a.db.Model(&model.UserForward{}).Where("outbound_id = ?", ob.ID).Count(&using).Error; err != nil {
		log.Printf("check admin user outbound forward refs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除专属出站失败"})
		return
	}
	if using > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该专属出站仍被转发节点使用，请先删除相关转发"})
		return
	}
	if err := a.db.Model(&model.RouteRule{}).Where("outbound = ?", ob.Tag).Count(&using).Error; err != nil {
		log.Printf("check admin user outbound route refs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除专属出站失败"})
		return
	}
	if using > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该专属出站仍被分流规则使用，请先删除相关规则"})
		return
	}
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", ob.ID).Delete(&model.Outbound{}).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		log.Printf("delete admin user outbound: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除专属出站失败"})
		return
	}
	a.refreshUserProxyAccess(serverIDs)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
