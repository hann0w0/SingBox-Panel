package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// Node import sources for outbounds.
//
// An outbound created from "节点导入" points at a panel node instead of storing
// a frozen copy of its connection parameters:
//
//   - a panel-managed inbound (the server's own, or another server's) is
//     rendered from that inbound's live rows, so TLS/transport/REALITY edits on
//     the source are picked up. Multi-user inbounds issue a dedicated identity
//     (routingOutboundIdentity) that no subscription account shares.
//   - an external (custom) node already carries its own credential.
//
// URI imports keep the previous behaviour: a validated, independent snapshot.
//
// Unusable sources never block a config apply. Rendering falls back to the
// stored snapshot in Settings, and the picker explains why the source is
// currently unavailable.

// routingOutboundUserName is the dedicated multi-user identity a panel inbound
// issues for outbounds that route through it. It is never assigned to a
// subscription account, so its traffic is not charged to any user.
const routingOutboundUserName = "__singbox_panel_route__"

// outboundSource describes one selectable node in the "节点导入" picker.
type outboundSource struct {
	Kind     string `json:"kind"` // inbound | custom_node
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"group,omitempty"` // owning server or group label
	Type     string `json:"type,omitempty"`
	Server   string `json:"server,omitempty"`
	Port     int    `json:"server_port,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Reason   string `json:"unavailable_reason,omitempty"`
}

// routingOutboundIdentity derives the dedicated identity a source inbound issues
// to one consuming server. It is deterministic (repeated config builds stay
// byte-identical, so no needless sing-box restart) and scoped to both the
// inbound and the consumer, so two servers routing through the same inbound
// never share a credential.
func routingOutboundUsername(consumerServerID uint) string {
	if consumerServerID == 0 {
		return routingOutboundUserName
	}
	return fmt.Sprintf("%s:%d", routingOutboundUserName, consumerServerID)
}

func routingOutboundIdentity(source *model.Server, inboundID, consumerServerID uint) (singbox.ProxyUser, error) {
	if source.AgentToken == "" {
		return singbox.ProxyUser{}, fmt.Errorf("服务器 %d 缺少内部路由凭据种子", source.ID)
	}
	seed := &model.User{ProxyToken: fmt.Sprintf("route:%s:%d", source.AgentToken, consumerServerID)}
	identity := proxyIdentity(seed, inboundID)
	routeUser := routingOutboundUsername(consumerServerID)
	identity.Name = routeUser
	identity.Username = routeUser
	return identity, nil
}

// outboundSourceClient renders the client view of an outbound's source node.
// consumerServerID is the outbound's owning server, which scopes the issued
// credential.
func (a *App) outboundSourceClient(ob *model.Outbound, consumerServerID uint) (singbox.ClientNode, error) {
	return a.outboundSourceClientWithDB(a.db, ob, consumerServerID)
}

func (a *App) outboundSourceClientWithDB(db *gorm.DB, ob *model.Outbound, consumerServerID uint) (singbox.ClientNode, error) {
	switch ob.SourceKind {
	case model.OutboundSourceInbound:
		return a.inboundOutboundClientWithDB(db, ob.SourceID, consumerServerID)
	case model.OutboundSourceCustomNode:
		return a.customNodeOutboundClientWithDB(db, ob.SourceID)
	default:
		return singbox.ClientNode{}, fmt.Errorf("出站 %q 不是节点导入", ob.Tag)
	}
}

// inboundOutboundClient builds the client view of a panel-managed inbound as
// consumed by consumerServerID. Multi-user inbounds issue the dedicated routing
// identity; single-credential inbounds reuse their fixed credential.
func (a *App) inboundOutboundClient(inboundID, consumerServerID uint) (singbox.ClientNode, error) {
	return a.inboundOutboundClientWithDB(a.db, inboundID, consumerServerID)
}

func (a *App) inboundOutboundClientWithDB(db *gorm.DB, inboundID, consumerServerID uint) (singbox.ClientNode, error) {
	if db == nil {
		db = a.db
	}
	var ib model.Inbound
	if err := db.First(&ib, inboundID).Error; err != nil {
		return singbox.ClientNode{}, fmt.Errorf("来源节点已被删除")
	}
	var srv model.Server
	if err := db.First(&srv, ib.ServerID).Error; err != nil {
		return singbox.ClientNode{}, fmt.Errorf("来源节点所属服务器已被删除")
	}
	return inboundOutboundClientFromModels(&srv, &ib, consumerServerID)
}

func inboundOutboundClientFromModels(srv *model.Server, ib *model.Inbound, consumerServerID uint) (singbox.ClientNode, error) {
	if consumerServerID != 0 && ib.ServerID == consumerServerID {
		return singbox.ClientNode{}, fmt.Errorf("不能把节点自身作为出站")
	}
	if !ib.Enabled {
		return singbox.ClientNode{}, fmt.Errorf("来源节点 %q 已停用", ib.Tag)
	}
	if srv.EffectiveConfigMode() == model.ConfigModeRaw {
		return singbox.ClientNode{}, fmt.Errorf("来源服务器已切换到原始配置模式")
	}
	if ib.ListenPort < 1 || ib.ListenPort > 65535 {
		return singbox.ClientNode{}, fmt.Errorf("来源节点 %q 的监听端口无效", ib.Tag)
	}

	var st singbox.InboundSettings
	if len(ib.Settings) > 0 {
		if err := json.Unmarshal(ib.Settings, &st); err != nil {
			return singbox.ClientNode{}, fmt.Errorf("读取来源节点 %q 参数失败", ib.Tag)
		}
	}
	if err := st.ValidateMultiUser(string(ib.Type)); err != nil {
		return singbox.ClientNode{}, err
	}
	host, err := inboundNodeAddress(srv, &st)
	if err != nil {
		return singbox.ClientNode{}, nodeAddressError(err)
	}

	var identity singbox.ProxyUser
	if st.UseMultiUser(string(ib.Type)) {
		st.SingleUser = false
		identity, err = routingOutboundIdentity(srv, ib.ID, consumerServerID)
		if err != nil {
			return singbox.ClientNode{}, err
		}
	} else {
		st.MultiUser = false
		st.SingleUser = true
		identity = st.SingleUserIdentity()
	}
	return singbox.ClientNode{
		Name:       formatNodeDisplayName(srv.Name, ib.Tag, string(ib.Type)),
		Server:     host,
		ServerPort: st.EffectiveClientPort(ib.ListenPort),
		Type:       string(ib.Type),
		Settings:   st,
		User:       identity,
	}, nil
}

// customNodeOutboundClient renders an external node's client view.
func (a *App) customNodeOutboundClient(id uint) (singbox.ClientNode, error) {
	return a.customNodeOutboundClientWithDB(a.db, id)
}

func (a *App) customNodeOutboundClientWithDB(db *gorm.DB, id uint) (singbox.ClientNode, error) {
	if db == nil {
		db = a.db
	}
	var node model.CustomNode
	if err := db.First(&node, id).Error; err != nil {
		return singbox.ClientNode{}, fmt.Errorf("来源外部节点已被删除")
	}
	return a.customNodeOutboundClientFromModel(&node)
}

func (a *App) customNodeOutboundClientFromModel(node *model.CustomNode) (singbox.ClientNode, error) {
	if node.HiddenBySubscriptionRule {
		return singbox.ClientNode{}, fmt.Errorf("来源外部节点 %q 已被订阅规则隐藏", node.Name)
	}
	n, ok := a.customNodeToNode(node)
	if !ok {
		return singbox.ClientNode{}, fmt.Errorf("来源外部节点 %q 参数无法解析", node.Name)
	}
	return n.clientNode(), nil
}

// outboundSourceReason reports why a source cannot be used right now, which is
// surfaced in the picker instead of silently offering a broken choice.
func (a *App) outboundSourceReason(kind string, id, targetServerID uint) string {
	_, err := a.outboundSourceClient(&model.Outbound{ServerID: targetServerID, SourceKind: kind, SourceID: id}, targetServerID)
	if err != nil {
		return err.Error()
	}
	return ""
}

// listOutboundSources returns every selectable node for the import dialog. The
// target server's own inbounds cannot be its own outbound and are marked so.
func (a *App) listOutboundSources(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(sid)
	defer unlockOperation()
	if !a.requireManagedMode(c, sid) {
		return
	}
	c.Header("Cache-Control", "no-store")
	rows, err := a.outboundSourceRows(sid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sources": rows})
}

func (a *App) outboundSourceRows(targetServerID uint) ([]outboundSource, error) {
	var servers []model.Server
	if err := a.db.Preload("Inbounds", func(tx *gorm.DB) *gorm.DB { return tx.Order("id") }).Order("id").Find(&servers).Error; err != nil {
		return nil, err
	}
	applyServerOrder(a.db, servers)
	rows := make([]outboundSource, 0)
	for i := range servers {
		srv := &servers[i]
		for _, ib := range srv.Inbounds {
			row := outboundSource{
				Kind:  model.OutboundSourceInbound,
				ID:    ib.ID,
				Name:  formatNodeDisplayName(srv.Name, ib.Tag, string(ib.Type)),
				Group: srv.Name,
				Type:  string(ib.Type),
			}
			if srv.ID == targetServerID {
				row.Disabled = true
				row.Reason = "不能把节点自身作为出站"
			} else if client, err := inboundOutboundClientFromModels(srv, &ib, targetServerID); err != nil {
				row.Disabled = true
				row.Reason = err.Error()
			} else {
				row.Server, row.Port = client.Server, client.ServerPort
			}
			rows = append(rows, row)
		}
	}

	var customs []model.CustomNode
	if err := a.db.Where("hidden_by_subscription_rule = ?", false).Order("sort_order, id").Find(&customs).Error; err != nil {
		return nil, err
	}
	for i := range customs {
		node := &customs[i]
		row := outboundSource{
			Kind:  model.OutboundSourceCustomNode,
			ID:    node.ID,
			Name:  node.Name,
			Group: node.Group,
		}
		if client, err := a.customNodeOutboundClientFromModel(node); err != nil {
			row.Disabled = true
			row.Reason = err.Error()
			row.Type = node.Protocol
		} else {
			row.Type, row.Server, row.Port = client.Type, client.Server, client.ServerPort
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// outboundSourceBody converts a chosen source into the stored outbound body,
// keeping a snapshot of the source in Settings for display and as a fallback.
func (a *App) outboundSourceBody(targetServerID uint, kind string, id uint) (outboundReq, error) {
	ob := &model.Outbound{ServerID: targetServerID, SourceKind: kind, SourceID: id}
	client, err := a.outboundSourceClient(ob, targetServerID)
	if err != nil {
		return outboundReq{}, err
	}
	body, err := outboundFromClientNode(client)
	if err != nil {
		return outboundReq{}, err
	}
	if body.Tag, err = a.availableOutboundTag(targetServerID, client.Name); err != nil {
		return outboundReq{}, err
	}
	if err := a.validateOutbound(a.db, targetServerID, 0, body.Tag, body.Type, 0, body.Settings); err != nil {
		return outboundReq{}, err
	}
	return body, nil
}

// outboundSourceLink validates a source reported by the client. An absent
// source (manual entry or URI import) returns empty values.
func (a *App) outboundSourceLink(targetServerID uint, kind string, id uint) (string, uint, error) {
	if kind == "" && id == 0 {
		return "", 0, nil
	}
	normalized, err := normalizeSourceKind(kind)
	if err != nil {
		return "", 0, err
	}
	if id == 0 {
		return "", 0, fmt.Errorf("请选择一个节点")
	}
	if _, err := a.outboundSourceClient(&model.Outbound{ServerID: targetServerID, SourceKind: normalized, SourceID: id}, targetServerID); err != nil {
		return "", 0, err
	}
	return normalized, id, nil
}

// recordStagedRoutingSourceAdd marks a cross-server source inbound as newly
// staged on consumerServerID without pushing the source server prematurely.
func (a *App) recordStagedRoutingSourceAdd(consumerServerID, sourceInboundID uint) {
	if a == nil || a.orch == nil {
		return
	}
	a.orch.routingTracker().recordAdd(consumerServerID, sourceInboundID)
}

// recordStagedRoutingSourceRemove retains a source inbound's routing credential
// while consumerServerID still has unapplied staged edits, so live traffic is
// not broken before the consumer applies its updated config.
func (a *App) recordStagedRoutingSourceRemove(consumerServerID, sourceInboundID uint) {
	if a == nil || a.orch == nil {
		return
	}
	a.orch.routingTracker().recordRemove(a.db, consumerServerID, sourceInboundID)
}

// normalizeSourceKind validates the kind reported by the client.
func normalizeSourceKind(kind string) (string, error) {
	switch kind {
	case model.OutboundSourceInbound, model.OutboundSourceCustomNode:
		return kind, nil
	}
	return "", fmt.Errorf("未知的节点来源")
}

// routingConsumers returns the servers holding an outbound that routes through
// the given source, plus the target server's own ID when the source is one of
// its inbounds. The source server must provision each consumer's credential.
func routingConsumers(db *gorm.DB, kind string, sourceID uint) ([]uint, error) {
	var ids []uint
	if err := db.Model(&model.Outbound{}).
		Where("source_kind = ? AND source_id = ?", kind, sourceID).
		Distinct().Pluck("server_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func routingIdentitiesFromConsumers(srv *model.Server, inboundID uint, consumers []uint) ([]singbox.ProxyUser, error) {
	identities := make([]singbox.ProxyUser, 0, len(consumers))
	seen := make(map[uint]bool, len(consumers))
	for _, consumer := range consumers {
		if consumer == 0 || consumer == srv.ID || seen[consumer] {
			continue
		}
		seen[consumer] = true
		identity, err := routingOutboundIdentity(srv, inboundID, consumer)
		if err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].UUID < identities[j].UUID })
	return identities, nil
}

// outboundSourceReferenceCount counts outbounds (plus unapplied staged consumer
// removals) that still depend on a source node, refusing deletion or disabling
// while live traffic may still route through it.
func (a *App) outboundSourceReferenceCount(db *gorm.DB, kind string, sourceID uint) (int64, error) {
	var n int64
	if err := db.Model(&model.Outbound{}).Where("source_kind = ? AND source_id = ?", kind, sourceID).Count(&n).Error; err != nil {
		return 0, err
	}
	if kind == model.OutboundSourceInbound && a != nil && a.orch != nil {
		retained := a.orch.routingTracker().activeRetainedConsumers(db, []uint{sourceID})
		n += int64(len(retained[sourceID]))
	}
	return n, nil
}
