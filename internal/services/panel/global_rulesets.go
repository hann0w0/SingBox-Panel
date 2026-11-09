package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
	"gorm.io/gorm"
)

func ruleSetServerIDs(db *gorm.DB, tags []string) ([]uint, error) {
	var rules []model.RouteRule
	if err := db.Where("enabled = ?", true).Find(&rules).Error; err != nil {
		return nil, err
	}
	wanted := stringSet(tags)
	ids := map[uint]bool{}
	for _, r := range rules {
		var match singbox.RuleInput
		if err := json.Unmarshal(r.Match, &match); err != nil {
			return nil, fmt.Errorf("rule %d: %w", r.ID, err)
		}
		for _, tag := range match.RuleSet {
			if wanted[tag] {
				ids[r.ServerID] = true
			}
		}
	}
	result := make([]uint, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

// Persist shared definitions once, then report delivery across their consumers.
func (a *App) applyRuleSetAndRespond(c *gin.Context, tags []string, payload gin.H) {
	ids, err := ruleSetServerIDs(a.db, tags)
	if err != nil {
		payload["apply_state"] = ConfigFailed
		payload["apply_error"] = err.Error()
		c.JSON(http.StatusOK, payload)
		return
	}
	state := ConfigApplied
	failures := []string{}
	for _, id := range ids {
		var srv model.Server
		if err := a.db.First(&srv, id).Error; err != nil {
			state = ConfigFailed
			failures = append(failures, err.Error())
			continue
		}
		if srv.EffectiveConfigMode() == model.ConfigModeRaw {
			continue
		}
		result := a.orch.ApplyDesiredConfig(c.Request.Context(), id)
		if result.ApplyState == ConfigFailed || (result.ApplyState == ConfigPending && state == ConfigApplied) {
			state = result.ApplyState
		}
		if result.ApplyError != "" {
			failures = append(failures, fmt.Sprintf("%s: %s", srv.Name, result.ApplyError))
		}
	}
	payload["apply_state"] = state
	if len(failures) > 0 {
		payload["apply_error"] = strings.Join(failures, "；")
	}
	c.JSON(http.StatusOK, payload)
}
