package panel

import (
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

// deleteUserNodeOrderRefs removes polymorphic order references when their
// underlying nodes are physically deleted. Sort gaps are harmless and are
// compacted the next time the user's assignment is saved.
func deleteUserNodeOrderRefs(tx *gorm.DB, nodeType string, nodeIDs []uint) error {
	if len(nodeIDs) == 0 {
		return nil
	}
	return tx.Where("node_type = ? AND node_id IN ?", nodeType, nodeIDs).
		Delete(&model.UserNodeOrder{}).Error
}
