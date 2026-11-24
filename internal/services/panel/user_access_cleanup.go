package panel

import (
	"encoding/json"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
	"gorm.io/gorm"
)

// removeServerUserAssignments runs after the server's inbounds are deleted in
// the same transaction. An empty explicit inbound list must never become a
// whole-server grant on one of the user's remaining servers.
func removeServerUserAssignments(tx *gorm.DB, serverID uint, deletedInboundIDs []uint) error {
	var users []model.User
	if err := tx.Find(&users).Error; err != nil {
		return err
	}
	deleted := make(map[uint]bool, len(deletedInboundIDs))
	for _, id := range deletedInboundIDs {
		deleted[id] = true
	}
	for i := range users {
		user := &users[i]
		affected := containsID(user.ServerIDs, serverID)
		for _, id := range user.InboundIDs {
			affected = affected || deleted[id]
		}
		if !affected {
			continue
		}
		servers := removeID(user.ServerIDs, serverID)
		if len(user.InboundIDs) > 0 {
			var remaining []model.Inbound
			if len(servers) > 0 {
				if err := tx.Select("id", "server_id").Where("id IN ? AND server_id IN ?", user.InboundIDs, servers).Find(&remaining).Error; err != nil {
					return err
				}
			}
			user.InboundIDs = make([]uint, 0, len(remaining))
			servers = make([]uint, 0, len(remaining))
			for _, inbound := range remaining {
				user.InboundIDs = append(user.InboundIDs, inbound.ID)
				servers = append(servers, inbound.ServerID)
			}
			user.InboundIDs = normalizedIDs(user.InboundIDs)
		}
		user.ServerIDs = normalizedIDs(servers)
		if err := tx.Model(user).Select("ServerIDs", "InboundIDs", "UpdatedAt").Updates(user).Error; err != nil {
			return err
		}
	}
	return nil
}

// cleanupDisallowedUserForwards removes derived forwards whose entry inbound is
// no longer part of the user's granted server/inbound access. Without this,
// revoking one server leaves hidden UserForward rows that block deleting the
// user's private outbound.
func cleanupDisallowedUserForwards(tx *gorm.DB, user *model.User) ([]uint, error) {
	var forwards []model.UserForward
	if err := tx.Where("user_id = ?", user.ID).Find(&forwards).Error; err != nil {
		return nil, err
	}
	if len(forwards) == 0 {
		return nil, nil
	}
	removed := make([]model.UserForward, 0, len(forwards))
	serverIDs := make([]uint, 0, len(forwards))
	for _, forward := range forwards {
		if !user.HasInbound(forward.ServerID, forward.InboundID) {
			removed = append(removed, forward)
			serverIDs = append(serverIDs, forward.ServerID)
		}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if err := deleteForwardState(tx, removed); err != nil {
		return nil, err
	}
	return normalizedIDs(serverIDs), nil
}

// cleanupDeletedUserRouteIdentities strips a deleted account's proxy traffic
// identity from shared server route rules. If a rule was scoped exclusively to
// that account, the rule itself is removed rather than turned into an unscoped
// server-wide route.
func cleanupDeletedUserRouteIdentities(tx *gorm.DB, user *model.User) ([]uint, error) {
	target := proxyTrafficName(user)
	var rules []model.RouteRule
	if err := tx.Find(&rules).Error; err != nil {
		return nil, err
	}
	var affected []uint
	for _, rule := range rules {
		var match singbox.RuleInput
		if len(rule.Match) == 0 || json.Unmarshal(rule.Match, &match) != nil || len(match.AuthUser) == 0 {
			continue
		}
		remaining := make([]string, 0, len(match.AuthUser))
		changed := false
		for _, name := range match.AuthUser {
			if name == target {
				changed = true
				continue
			}
			remaining = append(remaining, name)
		}
		if !changed {
			continue
		}
		affected = append(affected, rule.ServerID)
		if len(remaining) == 0 {
			if err := tx.Delete(&model.RouteRule{}, rule.ID).Error; err != nil {
				return nil, err
			}
			continue
		}
		match.AuthUser = remaining
		raw, err := json.Marshal(match)
		if err != nil {
			return nil, err
		}
		if err := tx.Model(&model.RouteRule{}).Where("id = ?", rule.ID).Update("match", model.JSONText(raw)).Error; err != nil {
			return nil, err
		}
	}
	return normalizedIDs(affected), nil
}
