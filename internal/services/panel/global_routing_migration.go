package panel

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
	"gorm.io/gorm"
)

// Legacy routing resources are adopted without publishing former server-only
// credentials to user subscriptions. Server routes retain their explicit target.
func migrateGlobalOutbounds(tx *gorm.DB) error {
	var ids []uint
	if err := tx.Model(&model.Outbound{}).Distinct().Pluck("server_id", &ids).Error; err != nil {
		return err
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if err := promoteServerOutbounds(tx, id); err != nil {
			return err
		}
	}
	return nil
}

func promoteServerOutbounds(tx *gorm.DB, serverID uint) error {
	var rows []model.Outbound
	if err := tx.Where("server_id = ?", serverID).Order("id").Find(&rows).Error; err != nil {
		return err
	}
	targets := map[string]string{}
	for _, row := range rows {
		target := "direct"
		if row.Type != "direct" {
			node, err := globalNodeFromOutbound(row)
			if err != nil {
				return fmt.Errorf("server %d outbound %q: %w", serverID, row.Tag, err)
			}
			if id, err := managedRoutingNodeID(row.Tag); err == nil {
				client, ownerID, err := loadManagedRoutingClient(tx, id)
				if err != nil {
					return err
				}
				if ownerID == serverID {
					return fmt.Errorf("目标出站不能选择当前服务器自身的节点")
				}
				matches, err := clientNodeDefinitionMatches(client, node)
				if err != nil {
					return err
				}
				if !matches {
					return fmt.Errorf("自有节点 %q 的连接信息与目标服务器不一致，请先更新目标节点", row.Tag)
				}
				targets[row.Tag] = row.Tag
				if err := tx.Delete(&row).Error; err != nil {
					return err
				}
				continue
			}
			// Repeated config imports reuse an identical global node, including configs
			// exported with the stable node-ID tag.
			if id, err := routingNodeID(row.Tag); err == nil {
				var original model.CustomNode
				if err := tx.First(&original, id).Error; err != nil && err != gorm.ErrRecordNotFound {
					return err
				} else if err == nil && original.Enabled && !original.HiddenBySubscriptionRule {
					matches, err := globalNodeDefinitionMatches(original, node)
					if err != nil {
						return err
					}
					if matches {
						node.ID = original.ID
					}
				}
			}
			var candidates []model.CustomNode
			if err := tx.Where("protocol = ? AND address = ? AND port = ? AND subscription_id IS NULL", node.Protocol, node.Address, node.Port).Order("id").Find(&candidates).Error; err != nil {
				return err
			}
			for _, candidate := range candidates {
				if node.ID != 0 {
					break
				}
				if (candidate.Name == node.Name || routingNodeTag(candidate.ID) == row.Tag) && equivalentJSON(candidate.Params, node.Params) && candidate.Enabled && !candidate.HiddenBySubscriptionRule {
					node.ID = candidate.ID
					break
				}
			}
			if node.ID == 0 {
				if err := tx.Create(&node).Error; err != nil {
					return err
				}
			}
			target = routingNodeTag(node.ID)
		}
		targets[row.Tag] = target
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
	}
	return rewriteServerOutboundTargets(tx, serverID, targets)
}

func globalNodeFromOutbound(row model.Outbound) (model.CustomNode, error) {
	var st outboundStoredSettings
	if err := json.Unmarshal(row.Settings, &st); err != nil {
		return model.CustomNode{}, err
	}
	raw, err := singbox.BuildServerConfig(singbox.ServerConfigInput{Outbounds: []singbox.OutboundInput{{
		Tag: row.Tag, Type: row.Type, Server: st.Server, ServerPort: st.ServerPort, Username: st.Username, UUID: st.UUID, Password: st.Password, Settings: st.Settings,
	}}})
	if err != nil {
		return model.CustomNode{}, err
	}
	parsed, err := singbox.ParseSubscription(raw)
	if err != nil {
		return model.CustomNode{}, err
	}
	if len(parsed.Nodes) != 1 || len(parsed.Skipped) > 0 {
		return model.CustomNode{}, fmt.Errorf("cannot convert outbound losslessly")
	}
	item := parsed.Nodes[0]
	params, err := json.Marshal(item.Params)
	if err != nil {
		return model.CustomNode{}, err
	}
	return model.CustomNode{Name: row.Tag, Group: "迁移的出站", Protocol: item.Protocol, Address: item.Address, Port: item.Port, Params: model.JSONText(params), Enabled: true, UserEnabled: true, AllUsers: false}, nil
}

func migrateGlobalRuleSets(tx *gorm.DB) error {
	var rows []model.RuleSet
	if err := tx.Where("server_id <> ?", 0).Order("server_id, id").Find(&rows).Error; err != nil {
		return err
	}
	mappings := map[uint]map[string]string{}
	for _, row := range rows {
		serverID := row.ServerID
		original := row.Tag
		for suffix := 0; ; suffix++ {
			var existing model.RuleSet
			err := tx.Where("server_id = ? AND tag = ?", 0, row.Tag).First(&existing).Error
			if err == gorm.ErrRecordNotFound {
				break
			}
			if err != nil {
				return err
			}
			if sameRuleSetDefinition(existing, row) {
				if err := tx.Delete(&row).Error; err != nil {
					return err
				}
				row.ID = 0
				break
			}
			row.Tag = fmt.Sprintf("%s-server-%d", trimRunes(original, 40), row.ServerID)
			if suffix > 0 {
				row.Tag = fmt.Sprintf("%s-%d", row.Tag, suffix)
			}
		}
		if mappings[serverID] == nil {
			mappings[serverID] = map[string]string{}
		}
		mappings[serverID][original] = row.Tag
		if row.ID == 0 {
			continue
		}
		row.ServerID = 0
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
	}
	for serverID, mapping := range mappings {
		var rules []model.RouteRule
		if err := tx.Where("server_id = ?", serverID).Find(&rules).Error; err != nil {
			return err
		}
		for _, rule := range rules {
			var match map[string]json.RawMessage
			if len(rule.Match) == 0 {
				continue
			}
			if err := json.Unmarshal(rule.Match, &match); err != nil {
				return err
			}
			var tags []string
			if raw := match["rule_set"]; len(raw) > 0 {
				if err := json.Unmarshal(raw, &tags); err != nil {
					return err
				}
			}
			changed := false
			for i, tag := range tags {
				if next, ok := mapping[tag]; ok && next != tag {
					tags[i] = next
					changed = true
				}
			}
			if !changed {
				continue
			}
			match["rule_set"], _ = json.Marshal(tags)
			blob, err := json.Marshal(match)
			if err != nil {
				return err
			}
			if err := tx.Model(&rule).Update("match", model.JSONText(blob)).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func sameRuleSetDefinition(a, b model.RuleSet) bool {
	return a.Type == b.Type && a.Format == b.Format && a.URL == b.URL && a.Path == b.Path && a.DownloadDetour == b.DownloadDetour && a.UpdateInterval == b.UpdateInterval
}

// Apply each mapping to the original value once so a generated node-ID cannot
// be mistaken for another legacy tag later in the same migration.
func rewriteServerOutboundTargets(tx *gorm.DB, serverID uint, targets map[string]string) error {
	var rules []model.RouteRule
	if err := tx.Where("server_id = ?", serverID).Find(&rules).Error; err != nil {
		return err
	}
	for _, rule := range rules {
		if target, ok := targets[rule.Outbound]; ok {
			if err := tx.Model(&rule).Update("outbound", target).Error; err != nil {
				return err
			}
		}
	}
	var srv model.Server
	err := tx.First(&srv, serverID).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	if err == nil {
		if target, ok := targets[srv.FinalOutbound]; ok {
			if err := tx.Model(&srv).Update("final_outbound", target).Error; err != nil {
				return err
			}
		}
	}
	var sets []model.RuleSet
	if err := tx.Where("server_id = ?", serverID).Find(&sets).Error; err != nil {
		return err
	}
	for _, rs := range sets {
		if target, ok := targets[rs.DownloadDetour]; ok {
			if err := tx.Model(&rs).Update("download_detour", target).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func globalNodeDefinitionMatches(candidate, wanted model.CustomNode) (bool, error) {
	n, ok := (&App{}).customNodeToNode(&candidate)
	if !ok {
		return false, nil
	}
	client := n.clientNode()
	client.Name = routingNodeTag(candidate.ID)
	return clientNodeDefinitionMatches(client, wanted)
}

func clientNodeDefinitionMatches(client singbox.ClientNode, wanted model.CustomNode) (bool, error) {
	raw, err := singbox.BuildClientOutbound(client)
	if err != nil {
		return false, err
	}
	wrapper, err := json.Marshal(map[string]any{"outbounds": []json.RawMessage{raw}})
	if err != nil {
		return false, err
	}
	parsed, err := singbox.ParseSubscription(wrapper)
	if err != nil {
		return false, err
	}
	if len(parsed.Nodes) != 1 {
		return false, nil
	}
	item := parsed.Nodes[0]
	params, err := json.Marshal(item.Params)
	if err != nil {
		return false, err
	}
	return item.Protocol == wanted.Protocol && item.Address == wanted.Address && item.Port == wanted.Port && equivalentJSON(params, wanted.Params), nil
}
