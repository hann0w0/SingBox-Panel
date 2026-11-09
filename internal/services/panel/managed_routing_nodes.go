package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
	"gorm.io/gorm"
)

const managedRoutingProxyUserName = "__singbox_panel_routing__"

func managedRoutingNodeTag(id uint) string { return "inbound-" + strconv.FormatUint(uint64(id), 10) }
func managedRoutingNodeID(tag string) (uint, error) {
	if !strings.HasPrefix(tag, "inbound-") {
		return 0, fmt.Errorf("无效的自有节点标识 %q", tag)
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(tag, "inbound-"), 10, strconv.IntSize)
	if err != nil || id == 0 || managedRoutingNodeTag(uint(id)) != tag {
		return 0, fmt.Errorf("无效的自有节点标识 %q", tag)
	}
	return uint(id), nil
}

// This identity is solely for panel-managed server-to-server forwarding. It is
// never assigned to a subscription account or charged to a user's quota.
func managedRoutingIdentity(srv *model.Server, inboundID uint) (singbox.ProxyUser, error) {
	if srv.AgentToken == "" {
		return singbox.ProxyUser{}, fmt.Errorf("服务器 %d 缺少内部路由凭据种子", srv.ID)
	}
	identity := proxyIdentity(&model.User{ProxyToken: "routing:" + srv.AgentToken}, inboundID)
	identity.Name = managedRoutingProxyUserName
	identity.Username = managedRoutingProxyUserName
	return identity, nil
}

func managedRoutingClient(srv *model.Server, ib *model.Inbound) (singbox.ClientNode, error) {
	if !ib.Enabled || srv.EffectiveConfigMode() == model.ConfigModeRaw {
		return singbox.ClientNode{}, fmt.Errorf("自有节点 %q 已停用或不在管理模式", ib.Tag)
	}
	host := srv.Address
	if host == "" {
		host = srv.PublicIP
	}
	host, err := normalizeNodeAddress(host)
	if err != nil || host == "" || isLoopbackOrUnspecifiedHost(host) {
		return singbox.ClientNode{}, fmt.Errorf("自有节点 %q 没有可用连接地址", ib.Tag)
	}
	if ib.ListenPort < 1 || ib.ListenPort > 65535 {
		return singbox.ClientNode{}, fmt.Errorf("自有节点 %q 端口无效", ib.Tag)
	}
	var st singbox.InboundSettings
	if len(ib.Settings) > 0 {
		if err := json.Unmarshal(ib.Settings, &st); err != nil {
			return singbox.ClientNode{}, err
		}
	}
	if err := st.ValidateMultiUser(string(ib.Type)); err != nil {
		return singbox.ClientNode{}, err
	}
	identity := st.SingleUserIdentity()
	if st.UseMultiUser(string(ib.Type)) {
		identity, err = managedRoutingIdentity(srv, ib.ID)
		if err != nil {
			return singbox.ClientNode{}, err
		}
		st.SingleUser = false
	} else {
		st.MultiUser = false
		st.SingleUser = true
	}
	return singbox.ClientNode{Name: managedRoutingNodeTag(ib.ID), Type: string(ib.Type), Server: host, ServerPort: ib.ListenPort, Settings: st, User: identity}, nil
}

func loadManagedRoutingClient(db *gorm.DB, id uint) (singbox.ClientNode, uint, error) {
	var ib model.Inbound
	if err := db.First(&ib, id).Error; err != nil {
		return singbox.ClientNode{}, 0, fmt.Errorf("自有节点 %d 不存在: %w", id, err)
	}
	var srv model.Server
	if err := db.First(&srv, ib.ServerID).Error; err != nil {
		return singbox.ClientNode{}, 0, err
	}
	client, err := managedRoutingClient(&srv, &ib)
	return client, srv.ID, err
}

func listManagedRoutingNodes(db *gorm.DB, sourceID uint) ([]node, error) {
	var servers []model.Server
	if err := db.Preload("Inbounds", func(tx *gorm.DB) *gorm.DB { return tx.Where("enabled = ?", true).Order("id") }).Order("id").Find(&servers).Error; err != nil {
		return nil, err
	}
	applyServerOrder(db, servers)
	nodes := []node{}
	for _, srv := range servers {
		if srv.ID == sourceID {
			continue
		}
		first := len(nodes)
		for _, ib := range srv.Inbounds {
			client, err := managedRoutingClient(&srv, &ib)
			if err != nil {
				continue
			}
			if _, err := singbox.BuildClientOutbound(client); err != nil {
				continue
			}
			// Keep the display tag separate from the stable routing reference.
			nodes = append(nodes, node{tag: ib.Tag, orderID: ib.ID, typ: client.Type})
		}
		nameServerNodes(srv.Name, nodes[first:])
		for i := first; i < len(nodes); i++ {
			nodes[i].tag = managedRoutingNodeTag(nodes[i].orderID)
		}
	}
	return nodes, nil
}

func validateManagedRoutingSource(db *gorm.DB, serverID uint, tag string) error {
	if !strings.HasPrefix(tag, "inbound-") {
		return nil
	}
	id, err := managedRoutingNodeID(tag)
	if err != nil {
		return err
	}
	var ib model.Inbound
	if err := db.First(&ib, id).Error; err != nil {
		return err
	}
	if ib.ServerID == serverID {
		return fmt.Errorf("目标出站不能选择当前服务器自身的节点")
	}
	return nil
}

func (a *App) pushManagedRoutingNodeChanges(ids []uint) {
	if a.orch == nil || len(ids) == 0 {
		return
	}
	tags := make([]string, 0, len(ids))
	for _, id := range ids {
		tags = append(tags, managedRoutingNodeTag(id))
	}
	servers, err := routingTagsAffectedServers(a.db, tags)
	if err != nil {
		return
	}
	var targetIDs []uint
	if err := a.db.Model(&model.Inbound{}).Where("id IN ?", ids).Pluck("server_id", &targetIDs).Error; err != nil {
		return
	}
	targets := map[uint]bool{}
	for _, id := range targetIDs {
		targets[id] = true
	}
	for _, id := range servers {
		if !targets[id] {
			a.orch.PushConfigIfManaged(id)
		}
	}
}

// Refresh previous targets as well as new ones when a reference changes, so
// deleting the last reference removes its dedicated inbound credential.
func (a *App) refreshManagedRoutingTags(tags ...string) {
	if a.orch == nil {
		return
	}
	ids := []uint{}
	for _, tag := range tags {
		if id, err := managedRoutingNodeID(tag); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	var serverIDs []uint
	if err := a.db.Model(&model.Inbound{}).Where("id IN ?", ids).Distinct().Pluck("server_id", &serverIDs).Error; err != nil {
		return
	}
	for _, id := range serverIDs {
		a.orch.PushConfigIfManaged(id)
	}
}

// Install the target's matching inbound credential before applying the source
// server config. Calls are deliberately non-recursive; each target owns its
// own configuration lock, so reciprocal routes cannot deadlock these applies.
func (o *Orchestrator) applyManagedRoutingTargets(ctx context.Context, sourceID uint) ConfigApplyResult {
	var srv model.Server
	if err := o.db.First(&srv, sourceID).Error; err != nil {
		return ConfigApplyResult{ConfigFailed, err.Error()}
	}
	if srv.EffectiveConfigMode() == model.ConfigModeRaw {
		return ConfigApplyResult{ApplyState: ConfigApplied}
	}
	raw, err := o.BuildServerConfig(&srv)
	if err != nil {
		return ConfigApplyResult{ConfigFailed, err.Error()}
	}
	var config struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return ConfigApplyResult{ConfigFailed, err.Error()}
	}
	targets := map[uint]bool{}
	for _, ob := range config.Outbounds {
		id, err := managedRoutingNodeID(ob.Tag)
		if err != nil {
			continue
		}
		var ib model.Inbound
		if err := o.db.First(&ib, id).Error; err != nil {
			return ConfigApplyResult{ConfigFailed, err.Error()}
		}
		targets[ib.ServerID] = true
	}
	ids := make([]uint, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := ConfigApplyResult{ApplyState: ConfigApplied}
	for _, id := range ids {
		if err := o.PushConfig(ctx, id); err != nil {
			if errors.Is(err, ErrAgentOffline) {
				result.ApplyState = ConfigPending
				continue
			}
			return ConfigApplyResult{ConfigFailed, fmt.Sprintf("自有目标服务器 %d 下发失败: %v", id, err)}
		}
	}
	return result
}

func managedRoutingRuleTags(db *gorm.DB, rule *model.RouteRule) ([]string, error) {
	tags := []string{rule.Outbound}
	var match singbox.RuleInput
	if len(rule.Match) > 0 {
		if err := json.Unmarshal(rule.Match, &match); err != nil {
			return nil, err
		}
	}
	if len(match.RuleSet) > 0 {
		var detours []string
		if err := db.Model(&model.RuleSet{}).Where("server_id = 0 AND tag IN ?", match.RuleSet).Pluck("download_detour", &detours).Error; err != nil {
			return nil, err
		}
		tags = append(tags, detours...)
	}
	return tags, nil
}

func (a *App) validateRuleSetRoutingConsumers(tag, detour string) error {
	if !strings.HasPrefix(detour, "inbound-") {
		return nil
	}
	id, err := managedRoutingNodeID(detour)
	if err != nil {
		return err
	}
	var ib model.Inbound
	if err := a.db.First(&ib, id).Error; err != nil {
		return err
	}
	referenced, err := a.ruleSetReferenced(a.db, ib.ServerID, tag)
	if err != nil {
		return err
	}
	if referenced {
		return fmt.Errorf("规则集已被该节点所属服务器引用，下载出站不能指向服务器自身")
	}
	return nil
}
