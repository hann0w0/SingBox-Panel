package panel

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

const serverOrderSettingKey = "server_order"

type serverOrderReq struct {
	IDs []uint `json:"ids"`
}

// applyServerOrder sorts servers using the administrator's saved order. IDs
// absent from the saved list (new nodes) are appended in their query order.
func applyServerOrder(db *gorm.DB, servers []model.Server) {
	var setting model.Setting
	result := db.Where("key = ?", serverOrderSettingKey).Limit(1).Find(&setting)
	if result.Error != nil || result.RowsAffected == 0 {
		return
	}
	var ids []uint
	if json.Unmarshal([]byte(setting.Value), &ids) != nil {
		return
	}
	positions := make(map[uint]int, len(ids))
	for i, id := range ids {
		positions[id] = i
	}
	unknownBase := len(ids)
	sort.SliceStable(servers, func(i, j int) bool {
		pi, okI := positions[servers[i].ID]
		if !okI {
			pi = unknownBase
		}
		pj, okJ := positions[servers[j].ID]
		if !okJ {
			pj = unknownBase
		}
		return pi < pj
	})
}

// updateServerOrder persists one complete permutation of the current nodes.
// Requiring every ID prevents a stale browser tab from silently dropping a
// node that was created elsewhere.
func (a *App) updateServerOrder(c *gin.Context) {
	var req serverOrderReq
	if !bindJSON(c, &req) {
		return
	}
	var current []uint
	if err := a.db.Model(&model.Server{}).Order("id").Pluck("id", &current).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(req.IDs) != len(current) {
		c.JSON(http.StatusConflict, gin.H{"error": "节点列表已变化，请刷新后重新排序"})
		return
	}
	want := make(map[uint]bool, len(current))
	for _, id := range current {
		want[id] = true
	}
	seen := make(map[uint]bool, len(req.IDs))
	for _, id := range req.IDs {
		if !want[id] || seen[id] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "节点顺序包含无效或重复 ID"})
			return
		}
		seen[id] = true
	}
	raw, err := json.Marshal(req.IDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := a.db.Save(&model.Setting{Key: serverOrderSettingKey, Value: string(raw)}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
