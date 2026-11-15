package panel

import (
	"encoding/json"
	"strings"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func materializeForwardRoutes(db *gorm.DB, serverID uint, inbounds []model.Inbound, saved []model.RouteRule) ([]model.RouteRule, error) {
	var forwards []model.UserForward
	if err := db.Where("server_id = ?", serverID).Order("id").Find(&forwards).Error; err != nil {
		return nil, err
	}
	var users []model.User
	var outbounds []model.Outbound
	if len(forwards) > 0 {
		var userIDs []uint
		for _, forward := range forwards {
			userIDs = append(userIDs, forward.UserID)
		}
		if err := db.Where("id IN ?", normalizedIDs(userIDs)).Find(&users).Error; err != nil {
			return nil, err
		}
		if err := db.Where("id IN ?", forwardOutboundIDs(forwards)).Find(&outbounds).Error; err != nil {
			return nil, err
		}
	}
	usersByID := make(map[uint]model.User, len(users))
	for _, user := range users {
		usersByID[user.ID] = user
	}
	inboundsByID := make(map[uint]model.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundsByID[inbound.ID] = inbound
	}
	outboundsByID := make(map[uint]model.Outbound, len(outbounds))
	for _, outbound := range outbounds {
		outboundsByID[outbound.ID] = outbound
	}
	rules := make([]model.RouteRule, 0, len(saved)+len(forwards))
	for _, forward := range forwards {
		user, userExists := usersByID[forward.UserID]
		inbound, inboundExists := inboundsByID[forward.InboundID]
		outbound, outboundExists := outboundsByID[forward.OutboundID]
		if !userExists || !inboundExists || !outboundExists || !userActive(&user) || !user.ForwardingEnabled || !user.HasInbound(serverID, inbound.ID) || outbound.OwnerUserID != user.ID || outbound.ServerID != 0 {
			continue
		}
		var settings singbox.InboundSettings
		if json.Unmarshal(inbound.Settings, &settings) != nil || !settings.UseMultiUser(string(inbound.Type)) {
			continue
		}
		match, err := json.Marshal(singbox.RuleInput{Action: "route", AuthUser: []string{forwardTrafficName(&user, forward.ID)}, Inbound: []string{inbound.Tag}})
		if err != nil {
			return nil, err
		}
		rules = append(rules, model.RouteRule{ServerID: serverID, Outbound: outbound.Tag, Match: model.JSONText(match), Enabled: true})
	}
	for _, rule := range saved {
		var match singbox.RuleInput
		if json.Unmarshal(rule.Match, &match) == nil && len(match.AuthUser) == 1 && strings.HasPrefix(match.AuthUser[0], forwardTrafficNamePrefix) {
			continue
		}
		rules = append(rules, rule)
	}
	return rules, nil
}
