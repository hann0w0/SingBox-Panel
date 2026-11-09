package panel

import (
	"bytes"
	"errors"
	"log"
	"sort"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

var errManagedInboundRoutingProtected = errors.New("该服务器的入站仍被路由引用，请先移除引用再导入或编辑原始配置")

func inboundRoutingTags(ids []uint) []string {
	tags := make([]string, len(ids))
	for i, id := range ids {
		tags[i] = managedRoutingNodeTag(id)
	}
	return tags
}

func rejectInboundRoutingReferences(db *gorm.DB, ids []uint, excludedServerID uint) error {
	return rejectRoutingTagsInUse(db, inboundRoutingTags(ids), excludedServerID)
}

func rejectServerRoutingReferences(db *gorm.DB, serverID, excludedServerID uint) error {
	var ids []uint
	if err := db.Model(&model.Inbound{}).Where("server_id = ?", serverID).Pluck("id", &ids).Error; err != nil {
		return err
	}
	return rejectInboundRoutingReferences(db, ids, excludedServerID)
}

// Raw edits cannot retain the panel-owned routing credentials injected into
// managed inbounds. Protect them before sending any configuration to the Agent.
func rejectManagedInboundRawImport(db *gorm.DB, serverID uint) error {
	err := rejectServerRoutingReferences(db, serverID, 0)
	if errors.Is(err, errRoutingNodeInUse) {
		return errManagedInboundRoutingProtected
	}
	return err
}

func managedInboundConfigChanged(before, after model.Inbound) bool {
	return before.Tag != after.Tag || before.Type != after.Type || before.ListenPort != after.ListenPort || before.Enabled != after.Enabled || (!bytes.Equal(before.Settings, after.Settings) && !equivalentJSON(before.Settings, after.Settings))
}

func (a *App) refreshManagedInboundReferences(serverID uint) {
	var ids []uint
	if err := a.db.Model(&model.Inbound{}).Where("server_id = ?", serverID).Pluck("id", &ids).Error; err != nil {
		log.Printf("managed routing: inspect server %d inbounds: %v", serverID, err)
		return
	}
	a.pushManagedRoutingNodeChanges(ids)
}

// Capture references before replacing a source config, including targets used
// only to download one of its shared rule sets.
func collectServerManagedRoutingTags(db *gorm.DB, serverID uint) ([]string, error) {
	var server model.Server
	if err := db.Select("final_outbound").First(&server, serverID).Error; err != nil {
		return nil, err
	}
	var rules []model.RouteRule
	if err := db.Where("server_id = ?", serverID).Find(&rules).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	add := func(tags []string) {
		for _, tag := range tags {
			if _, err := managedRoutingNodeID(tag); err == nil {
				seen[tag] = true
			}
		}
	}
	add([]string{server.FinalOutbound})
	for i := range rules {
		tags, err := managedRoutingRuleTags(db, &rules[i])
		if err != nil {
			return nil, err
		}
		add(tags)
	}
	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags, nil
}
