package panel

import (
	"encoding/json"
	"fmt"
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
	if err := a.db.Select("id", "name").Where("id IN ?", normalizedIDs(u.ServerIDs)).Find(&servers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	names := make(map[uint]string, len(servers))
	for _, server := range servers {
		names[server.ID] = server.Name
	}
	var inbounds []model.Inbound
	if err := a.db.Where("server_id IN ? AND enabled = ?", normalizedIDs(u.ServerIDs), true).Order("server_id, id").Find(&inbounds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]UserInboundOption, 0, len(inbounds))
	for _, inbound := range inbounds {
		if !u.HasInbound(inbound.ServerID, inbound.ID) {
			continue
		}
		var st singbox.InboundSettings
		if len(inbound.Settings) > 0 {
			_ = json.Unmarshal(inbound.Settings, &st)
		}
		out = append(out, UserInboundOption{
			ID: inbound.ID, ServerID: inbound.ServerID, ServerName: names[inbound.ServerID],
			Tag: inbound.Tag, Type: string(inbound.Type), Port: inbound.ListenPort,
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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

// importUserOutbound accepts a single URI or a subscription URL for one of the
// caller's own outbounds. The URL fetch and parser are shared with the admin
// import path, so protocol support and SSRF limits stay identical.
func (a *App) importUserOutbound(c *gin.Context) {
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if u.Role != model.RoleAdmin && !userActive(&u) {
		c.JSON(http.StatusForbidden, gin.H{"error": "账号已停用或已到期"})
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
		nodes = []singbox.ImportedNode{{Name: client.Name, Link: uri, Protocol: client.Type, Address: client.Server, Port: client.ServerPort}}
	}
	if len(nodes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有解析到可用节点"})
		return
	}
	created := make([]model.Outbound, 0, len(nodes))
	for _, imported := range nodes {
		client, ok := a.importedNodeClient(imported)
		if !ok {
			continue
		}
		body, err := outboundFromClientNode(client)
		if err != nil {
			continue
		}
		tag := strings.TrimSpace(req.Tag)
		if tag == "" {
			tag = body.Tag
		}
		if len(nodes) > 1 {
			tag = fmt.Sprintf("%s-%d", tag, len(created)+1)
		}
		resolvedTag, tagErr := a.availablePrivateOutboundTag(tag)
		if tagErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": tagErr.Error()})
			return
		}
		tag = resolvedTag
		ob := model.Outbound{
			ServerID: 0, OwnerUserID: u.ID, Tag: tag, Type: body.Type,
			Settings: model.JSONText(body.Settings),
		}
		if err := a.db.Create(&ob).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		created = append(created, ob)
	}
	if len(created) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该订阅没有可转换为出站的节点"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"outbounds": created})
}

// availablePrivateOutboundTag returns a tag that is unique across all stored
// outbounds. Tags become sing-box outbound tags on every entry server that
// references the exit, so a private exit cannot safely reuse a shared tag.
// Imports suffix collisions instead of failing an otherwise valid subscription.
func (a *App) availablePrivateOutboundTag(tag string) (string, error) {
	if strings.TrimSpace(tag) == "" {
		return "", fmt.Errorf("出站名称必填")
	}
	if builtinOutboundTags[tag] {
		return "", fmt.Errorf("%s 是内置标签，不能作为专属出站名称", tag)
	}
	base := tag
	for suffix := 2; suffix < 100; suffix++ {
		var count int64
		if err := a.db.Model(&model.Outbound{}).
			Where("tag = ?", tag).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return tag, nil
		}
		tag = fmt.Sprintf("%s-%d", base, suffix)
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
	if isLoopbackOrUnspecifiedHost(st.Server) {
		return fmt.Errorf("落地服务器不能指向本机回环或未指定地址")
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
	case "socks":
		if (strings.TrimSpace(st.Username) == "") != (st.Password == "") {
			return fmt.Errorf("socks 用户名和密码必须同时填写或同时留空")
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(rows) == 0 {
		c.JSON(http.StatusOK, gin.H{"forwards": []UserForward{}})
		return
	}
	var servers []model.Server
	if err := a.db.Select("id", "name").Where("id IN ?", normalizedIDs(u.ServerIDs)).Find(&servers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var inbounds []model.Inbound
	if err := a.db.Select("id", "server_id", "tag").Where("id IN ?", forwardInboundIDs(rows)).Find(&inbounds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var outbounds []model.Outbound
	if err := a.db.Select("id", "tag").Where("id IN ?", forwardOutboundIDs(rows)).Find(&outbounds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	serverNames := make(map[uint]string, len(servers))
	for _, server := range servers {
		serverNames[server.ID] = server.Name
	}
	inboundTags := make(map[uint]string, len(inbounds))
	inboundServers := make(map[uint]uint, len(inbounds))
	for _, inbound := range inbounds {
		inboundTags[inbound.ID] = inbound.Tag
		inboundServers[inbound.ID] = inbound.ServerID
	}
	outboundTags := make(map[uint]string, len(outbounds))
	for _, outbound := range outbounds {
		outboundTags[outbound.ID] = outbound.Tag
	}
	forwards := make([]UserForward, 0)
	for _, row := range rows {
		inboundTag, serverID := inboundTags[row.InboundID], inboundServers[row.InboundID]
		outboundTag := outboundTags[row.OutboundID]
		if inboundTag == "" || outboundTag == "" || serverID != row.ServerID || !u.HasServer(serverID) {
			continue
		}
		if !u.HasInbound(serverID, row.InboundID) {
			continue
		}
		forwards = append(forwards, UserForward{
			ID: row.ID, ServerID: row.ServerID, ServerName: serverNames[row.ServerID],
			InboundTag: inboundTag, OutboundID: row.OutboundID, OutboundTag: outboundTag,
			NodeName: forwardNodeName(serverNames[row.ServerID], inboundTag, outboundTag, row.Remark), Remark: row.Remark, Enabled: true,
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
		c.JSON(http.StatusForbidden, gin.H{"error": "管理员尚未为用户开启转发权限"})
		return
	}
	var req userForwardReq
	if !bindJSON(c, &req) {
		return
	}
	var outbound model.Outbound
	if err := a.db.Where("id = ? AND owner_user_id = ? AND server_id = 0", req.OutboundID, u.ID).First(&outbound).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "专属出站不存在"})
		return
	}
	if strings.TrimSpace(req.InboundTag) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择入口节点"})
		return
	}
	var inbound model.Inbound
	inboundQuery := a.db.Where("server_id IN ? AND tag = ? AND enabled = ?", normalizedIDs(u.ServerIDs), req.InboundTag, true)
	if req.InboundServerID != 0 {
		if !u.HasServer(req.InboundServerID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "没有该服务器的访问权限"})
			return
		}
		inboundQuery = inboundQuery.Where("server_id = ?", req.InboundServerID)
	}
	if err := inboundQuery.First(&inbound).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "入口节点不存在或未启用"})
		return
	}
	if !u.HasInbound(inbound.ServerID, inbound.ID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "没有该入口节点的访问权限"})
		return
	}
	var st singbox.InboundSettings
	if len(inbound.Settings) > 0 {
		if err := json.Unmarshal(inbound.Settings, &st); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "入口节点参数无效"})
			return
		}
	}
	if !st.UseMultiUser(string(inbound.Type)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "入口节点必须启用多用户模式，才能按用户分流"})
		return
	}
	var duplicate int64
	if err := a.db.Model(&model.UserForward{}).
		Where("user_id = ? AND server_id = ? AND inbound_id = ? AND outbound_id = ?", u.ID, inbound.ServerID, inbound.ID, outbound.ID).
		Count(&duplicate).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if duplicate > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该入口与出站的转发已存在"})
		return
	}
	forward := model.UserForward{
		UserID: u.ID, ServerID: inbound.ServerID, InboundID: inbound.ID, OutboundID: outbound.ID,
	}
	if req.Remark != nil {
		forward.Remark = *req.Remark
	}
	if err := a.db.Create(&forward).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := a.reconcileForwardRule(&forward, &u, &inbound, &outbound); err != nil {
		_ = a.db.Delete(&forward).Error
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, inbound.ServerID, gin.H{"forward": forward})
}

func (a *App) deleteUserForward(c *gin.Context) {
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	rid, ok := uintParam(c, "forwardID")
	if !ok {
		return
	}
	var forward model.UserForward
	if err := a.db.Where("id = ? AND user_id = ?", rid, u.ID).First(&forward).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "转发不存在"})
		return
	}
	var outbound model.Outbound
	if err := a.db.Select("id", "tag").First(&outbound, forward.OutboundID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "转发不存在"})
		return
	}
	if err := a.db.Delete(&forward).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = a.db.Where("user_id = ? AND node_type = ? AND node_id = ?", u.ID, userNodeTypeForward, forward.ID).Delete(&model.UserNodeOrder{}).Error
	var rules []model.RouteRule
	if err := a.db.Where("server_id = ? AND outbound = ?", forward.ServerID, outbound.Tag).Find(&rules).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, rule := range rules {
		var match singbox.RuleInput
		if len(rule.Match) > 0 && json.Unmarshal(rule.Match, &match) == nil &&
			len(match.AuthUser) == 1 && match.AuthUser[0] == forwardTrafficName(&u, forward.ID) {
			if err := a.db.Delete(&rule).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
	}
	a.applyConfigAndRespond(c, forward.ServerID, gin.H{"ok": true})
}

// reconcileForwardRule keeps the derived credential's route rule in sync with
// a forward record. The rule matches only the derived identity and inbound;
// the account's original identity keeps its existing route and password.
func (a *App) reconcileForwardRule(forward *model.UserForward, user *model.User, inbound *model.Inbound, outbound *model.Outbound) error {
	match, err := json.Marshal(singbox.RuleInput{
		Action:   "route",
		AuthUser: []string{forwardTrafficName(user, forward.ID)},
		Inbound:  []string{inbound.Tag},
	})
	if err != nil {
		return err
	}
	var rules []model.RouteRule
	if err := a.db.Where("server_id = ? AND outbound = ?", forward.ServerID, outbound.Tag).Find(&rules).Error; err != nil {
		return err
	}
	for _, rule := range rules {
		var existing singbox.RuleInput
		if len(rule.Match) > 0 && json.Unmarshal(rule.Match, &existing) == nil &&
			len(existing.AuthUser) == 1 && existing.AuthUser[0] == forwardTrafficName(user, forward.ID) {
			return a.db.Model(&model.RouteRule{}).Where("id = ?", rule.ID).Update("match", model.JSONText(match)).Error
		}
	}
	return a.db.Create(&model.RouteRule{
		ServerID: forward.ServerID, Outbound: outbound.Tag, Match: model.JSONText(match), Enabled: true,
	}).Error
}

// createAdminUserOutbound creates a credential-only exit owned by one user.
// Entry servers are chosen later by forwards; no config push is needed here.
func (a *App) createAdminUserOutbound(c *gin.Context) {
	uid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var owner model.User
	if err := a.db.Select("id", "role").First(&owner, uid).Error; err != nil || owner.Role != model.RoleUser {
		c.JSON(http.StatusNotFound, gin.H{"error": "专属出站所属用户不存在"})
		return
	}
	var req userOutboundReq
	if !bindJSON(c, &req) {
		return
	}
	req.Tag = strings.TrimSpace(req.Tag)
	if req.Tag == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标签必填"})
		return
	}
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"outbound": adminPrivateOutbound{ID: ob.ID}})
}

func (a *App) deleteAdminUserOutbound(c *gin.Context) {
	uid, ok := uintParam(c, "id")
	if !ok {
		return
	}
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
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("outbound_id = ?", ob.ID).Delete(&model.UserForward{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", ob.ID).Delete(&model.Outbound{}).Error; err != nil {
			return err
		}
		return tx.Where("outbound = ? AND server_id IN ?", ob.Tag, normalizedIDs(serverIDs)).Delete(&model.RouteRule{}).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.refreshUserProxyAccess(serverIDs)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
