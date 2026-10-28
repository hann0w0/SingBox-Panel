package panel

import (
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

var errInboundTrafficTagReserved = errors.New("入站历史标签已保留给原入站，不能由其他入站复用，请使用新标签")

func checkInboundTrafficTag(db *gorm.DB, serverID, inboundID uint, tag string) error {
	var binding model.InboundTrafficIdentity
	if err := db.Where("server_id = ? AND tag_key = ?", serverID, model.InboundTrafficTagKey(tag)).Limit(1).Find(&binding).Error; err != nil {
		return err
	}
	if binding.ID != 0 && binding.InboundID != inboundID {
		return fmt.Errorf("%w: %q", errInboundTrafficTagReserved, tag)
	}
	return nil
}

// Call inside the same transaction as the inbound change. The unique key and
// owner check prevent concurrent requests from reassigning an existing tag.
func rememberInboundTrafficTag(tx *gorm.DB, inbound *model.Inbound) error {
	binding := model.InboundTrafficIdentity{ServerID: inbound.ServerID, InboundID: inbound.ID}
	binding.SetTag(inbound.Tag)
	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "server_id"}, {Name: "tag_key"}}, DoNothing: true,
	}).Create(&binding).Error; err != nil {
		return err
	}
	var saved model.InboundTrafficIdentity
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("server_id = ? AND tag_key = ?", inbound.ServerID, model.InboundTrafficTagKey(inbound.Tag)).
		First(&saved).Error; err != nil {
		return err
	}
	if saved.InboundID != inbound.ID {
		return fmt.Errorf("%w: %q", errInboundTrafficTagReserved, inbound.Tag)
	}
	return nil
}

func migrateInboundTrafficIdentities(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.InboundTrafficIdentity{}); err != nil {
		return err
	}
	var inbounds []model.Inbound
	if err := tx.Select("id", "server_id", "tag").Find(&inbounds).Error; err != nil {
		return err
	}
	for i := range inbounds {
		if err := rememberInboundTrafficTag(tx, &inbounds[i]); err != nil {
			return err
		}
	}
	return nil
}

// loadTrafficInbounds resolves historical tags against their original, still
// existing inbound. Deleted bindings remain tombstones, not new traffic rows.
// The current settings continue to decide whether per-user billing is enabled.
func loadTrafficInbounds(db *gorm.DB, serverID uint) ([]model.Inbound, error) {
	var inbounds []model.Inbound
	if err := db.Select("id", "tag", "type", "settings").Where("server_id = ?", serverID).Find(&inbounds).Error; err != nil {
		return nil, err
	}
	var bindings []model.InboundTrafficIdentity
	if err := db.Where("server_id = ?", serverID).Find(&bindings).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]model.Inbound, len(inbounds))
	byTag := make(map[string]uint, len(inbounds)+len(bindings))
	for _, inbound := range inbounds {
		byID[inbound.ID] = inbound
		byTag[inbound.Tag] = inbound.ID
	}
	// A persisted owner always wins over a current tag, including a tombstone.
	for _, binding := range bindings {
		byTag[binding.Tag] = binding.InboundID
	}
	resolved := make([]model.Inbound, 0, len(byTag))
	for tag, id := range byTag {
		if inbound, ok := byID[id]; ok {
			inbound.Tag = tag
			resolved = append(resolved, inbound)
		}
	}
	return resolved, nil
}

// Raw edits must pass this check before being sent to the Agent; checking only
// during the later structured import would reject a config already activated.
func validateImportedInboundTrafficTags(db *gorm.DB, serverID uint, raw []byte) error {
	// Include types the structured parser skips: they can still emit a tag
	// previously owned by a managed inbound.
	var document struct {
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return err
	}
	var inbounds []model.Inbound
	if err := db.Select("id", "tag").Where("server_id = ?", serverID).Find(&inbounds).Error; err != nil {
		return err
	}
	byTag := make(map[string]uint, len(inbounds))
	for _, inbound := range inbounds {
		byTag[inbound.Tag] = inbound.ID
	}
	for _, inbound := range document.Inbounds {
		if err := checkInboundTrafficTag(db, serverID, byTag[inbound.Tag], inbound.Tag); err != nil {
			return err
		}
	}
	return nil
}
