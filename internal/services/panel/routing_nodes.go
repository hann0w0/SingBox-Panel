package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func routingNodeTag(id uint) string { return "node-" + strconv.FormatUint(uint64(id), 10) }

func routingNodeID(tag string) (uint, error) {
	if !strings.HasPrefix(tag, "node-") {
		return 0, fmt.Errorf("无效的全局节点标识 %q", tag)
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(tag, "node-"), 10, strconv.IntSize)
	if err != nil || id == 0 || routingNodeTag(uint(id)) != tag {
		return 0, fmt.Errorf("无效的全局节点标识 %q", tag)
	}
	return uint(id), nil
}

func (a *App) listRoutingNodes(c *gin.Context) {
	var sourceID uint
	if value, present := c.GetQuery("server_id"); present {
		id, err := strconv.ParseUint(value, 10, strconv.IntSize)
		if err != nil || id == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的服务器 ID"})
			return
		}
		sourceID = uint(id)
	}
	var rows []model.CustomNode
	if err := a.db.Where("enabled = ? AND hidden_by_subscription_rule = ?", true, false).Order("sort_order, id").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	type routingNode struct {
		Tag  string `json:"tag"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	nodes := make([]routingNode, 0, len(rows))
	for i := range rows {
		n, ok := a.customNodeToNode(&rows[i])
		if !ok {
			continue
		}
		if _, err := singbox.BuildClientOutbound(n.clientNode()); err != nil {
			continue
		}
		nodes = append(nodes, routingNode{routingNodeTag(rows[i].ID), rows[i].Name, n.typ})
	}
	managed, err := listManagedRoutingNodes(a.db, sourceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, n := range managed {
		nodes = append(nodes, routingNode{n.tag, n.name, n.typ})
	}
	c.JSON(http.StatusOK, gin.H{"nodes": nodes})
}

var errRoutingNodeInUse = errors.New("节点仍被路由、默认出站或规则集下载引用，请先修改引用")

// rejectReferencedRoutingNodes also protects disabled rules: enabling a saved
// rule later must not revive a dangling target.
func rejectReferencedRoutingNodes(db *gorm.DB, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	tags := make([]string, len(ids))
	for i, id := range ids {
		tags[i] = routingNodeTag(id)
	}
	return rejectRoutingTagsInUse(db, tags, 0)
}

func rejectRoutingTagsInUse(db *gorm.DB, tags []string, excludedServerID uint) error {
	if len(tags) == 0 {
		return nil
	}
	for _, query := range []struct {
		model  any
		column string
	}{
		{&model.RouteRule{}, "outbound"}, {&model.Server{}, "final_outbound"}, {&model.RuleSet{}, "download_detour"},
	} {
		var count int64
		q := db.Model(query.model).Where(query.column+" IN ?", tags)
		if excludedServerID != 0 {
			switch query.model.(type) {
			case *model.Server:
				q = q.Where("id <> ?", excludedServerID)
			case *model.RouteRule:
				q = q.Where("server_id <> ?", excludedServerID)
			}
		}
		if err := q.Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errRoutingNodeInUse
		}
	}
	return nil
}

func routingNodeReferenced(db *gorm.DB, id uint) (bool, error) {
	err := rejectReferencedRoutingNodes(db, []uint{id})
	if errors.Is(err, errRoutingNodeInUse) {
		return true, nil
	}
	return false, err
}

// routingNodeAffectedServers follows only effective managed references. Shared
// rule sets have no owning server, so their download route is followed through
// each server's enabled rules.
func routingNodeAffectedServers(db *gorm.DB, ids []uint) ([]uint, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tags := make([]string, len(ids))
	for i, id := range ids {
		tags[i] = routingNodeTag(id)
	}
	return routingTagsAffectedServers(db, tags)
}

func routingTagsAffectedServers(db *gorm.DB, tags []string) ([]uint, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	affected := map[uint]bool{}
	var servers []model.Server
	if err := db.Find(&servers).Error; err != nil {
		return nil, err
	}
	managed := map[uint]bool{}
	for _, s := range servers {
		if s.EffectiveConfigMode() == model.ConfigModeRaw {
			continue
		}
		managed[s.ID] = true
		for _, tag := range tags {
			if s.FinalOutbound == tag {
				affected[s.ID] = true
				break
			}
		}
	}
	var sets []model.RuleSet
	if err := db.Where("server_id = 0 AND download_detour IN ?", tags).Find(&sets).Error; err != nil {
		return nil, err
	}
	setTags := map[string]bool{}
	for _, rs := range sets {
		setTags[rs.Tag] = true
	}
	var rules []model.RouteRule
	if err := db.Where("enabled = ?", true).Find(&rules).Error; err != nil {
		return nil, err
	}
	tagSet := map[string]bool{}
	for _, tag := range tags {
		tagSet[tag] = true
	}
	for _, r := range rules {
		if !managed[r.ServerID] {
			continue
		}
		if tagSet[r.Outbound] {
			affected[r.ServerID] = true
			continue
		}
		if len(setTags) == 0 || len(r.Match) == 0 {
			continue
		}
		var match singbox.RuleInput
		if err := json.Unmarshal(r.Match, &match); err != nil {
			return nil, fmt.Errorf("route rule %d match: %w", r.ID, err)
		}
		for _, tag := range match.RuleSet {
			if setTags[tag] {
				affected[r.ServerID] = true
				break
			}
		}
	}
	out := make([]uint, 0, len(affected))
	for id := range affected {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (a *App) pushRoutingNodeChanges(ids []uint) {
	if a.orch == nil {
		return
	}
	a.orch.pushRoutingNodeChanges(ids)
}

func (o *Orchestrator) pushRoutingNodeChanges(ids []uint) {
	servers, err := routingNodeAffectedServers(o.db, ids)
	if err != nil { // Keep the mutation successful; async delivery errors are observable in logs.
		log.Printf("routing nodes: inspect affected servers: %v", err)
		return
	}
	for _, id := range servers {
		o.PushConfigIfManaged(id)
	}
}

func buildRoutingNodeOutbounds(db *gorm.DB, sourceID uint, tags map[string]bool) ([]json.RawMessage, error) {
	ordered := make([]string, 0, len(tags))
	for tag := range tags {
		ordered = append(ordered, tag)
	}
	sort.Strings(ordered)
	out := make([]json.RawMessage, 0, len(ordered))
	for _, tag := range ordered {
		if tag == "" || tag == "direct" {
			continue
		}
		if strings.HasPrefix(tag, "inbound-") {
			id, err := managedRoutingNodeID(tag)
			if err != nil {
				return nil, err
			}
			client, serverID, err := loadManagedRoutingClient(db, id)
			if err != nil {
				return nil, err
			}
			if serverID == sourceID {
				return nil, fmt.Errorf("目标出站不能选择当前服务器自身的节点")
			}
			raw, err := singbox.BuildClientOutbound(client)
			if err != nil {
				return nil, err
			}
			out = append(out, raw)
			continue
		}
		id, err := routingNodeID(tag)
		if err != nil {
			return nil, err
		}
		var row model.CustomNode
		if err := db.First(&row, id).Error; err != nil {
			return nil, fmt.Errorf("路由节点 %q 不存在: %w", tag, err)
		}
		if !row.Enabled || row.HiddenBySubscriptionRule {
			return nil, fmt.Errorf("路由节点 %q 已停用或被过滤", tag)
		}
		n, ok := (&App{}).customNodeToNode(&row)
		if !ok {
			return nil, fmt.Errorf("路由节点 %q 参数无法解析", tag)
		}
		client := n.clientNode()
		client.Name = tag
		raw, err := singbox.BuildClientOutbound(client)
		if err != nil {
			return nil, fmt.Errorf("路由节点 %q: %w", tag, err)
		}
		out = append(out, raw)
	}
	return out, nil
}

// appendRoutingNodeOutbounds preserves the complete client outbound JSON,
// including protocol-specific fields absent from the legacy server model.
func appendRoutingNodeOutbounds(raw []byte, nodes []json.RawMessage) ([]byte, error) {
	if len(nodes) == 0 {
		return raw, nil
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	var outbounds []json.RawMessage
	if err := json.Unmarshal(config["outbounds"], &outbounds); err != nil {
		return nil, err
	}
	outbounds = append(outbounds, nodes...)
	value, err := json.Marshal(outbounds)
	if err != nil {
		return nil, err
	}
	config["outbounds"] = value
	return json.MarshalIndent(config, "", "  ")
}
