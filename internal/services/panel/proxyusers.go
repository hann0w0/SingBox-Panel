package panel

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func proxyIdentity(user *model.User, inboundID uint) singbox.ProxyUser {
	seed := user.ProxyToken
	if seed == "" {
		// Rows are backfilled by schema migration 3. This fallback keeps tests and
		// manually-created rows deterministic without coupling proxy credentials
		// to the login password.
		seed = user.SubToken
	}
	identity := user.Email
	if identity == "" {
		identity = fmt.Sprintf("u%d", user.ID)
	}
	uuidSum := sha256.Sum256([]byte(fmt.Sprintf("uuid:%s:%d", seed, inboundID)))
	uuidBytes := uuidSum[:16]
	uuidBytes[6] = (uuidBytes[6] & 0x0f) | 0x40
	uuidBytes[8] = (uuidBytes[8] & 0x3f) | 0x80
	passwordSum := sha256.Sum256([]byte(fmt.Sprintf("password:%s:%d", seed, inboundID)))
	return singbox.ProxyUser{
		Name:     identity,
		Username: identity,
		UUID: fmt.Sprintf("%x-%x-%x-%x-%x",
			uuidBytes[0:4], uuidBytes[4:6], uuidBytes[6:8], uuidBytes[8:10], uuidBytes[10:16]),
		Password: base64.RawURLEncoding.EncodeToString(passwordSum[:18]),
	}
}

// forwardProxyIdentity derives a second credential for the same account. A
// forward must never reuse the normal inbound identity: clients select an
// extra node with its own password while the original node and password remain
// unchanged.
func forwardProxyIdentity(user *model.User, forwardID, inboundID uint) singbox.ProxyUser {
	seed := user.ProxyToken
	if seed == "" {
		seed = user.SubToken
	}
	identity := proxyIdentity(user, inboundID)
	uuidSum := sha256.Sum256([]byte(fmt.Sprintf("forward-uuid:%s:%d:%d", seed, forwardID, inboundID)))
	uuidBytes := uuidSum[:16]
	uuidBytes[6] = (uuidBytes[6] & 0x0f) | 0x40
	uuidBytes[8] = (uuidBytes[8] & 0x3f) | 0x80
	passwordSum := sha256.Sum256([]byte(fmt.Sprintf("forward-password:%s:%d:%d", seed, forwardID, inboundID)))
	identity.UUID = fmt.Sprintf("%x-%x-%x-%x-%x",
		uuidBytes[0:4], uuidBytes[4:6], uuidBytes[6:8], uuidBytes[8:10], uuidBytes[10:16])
	identity.Password = base64.RawURLEncoding.EncodeToString(passwordSum[:18])
	identity.Name = forwardTrafficName(user, forwardID)
	identity.Username = identity.Name
	return identity
}

func proxyUsersForInbound(db *gorm.DB, inbound *model.Inbound, settings singbox.InboundSettings) ([]singbox.ProxyUser, error) {
	if !settings.UseMultiUser(string(inbound.Type)) {
		return nil, nil
	}
	var users []model.User
	if err := db.Where("enabled = ?", true).Order("id").Find(&users).Error; err != nil {
		return nil, err
	}
	now := time.Now()
	identities := make([]singbox.ProxyUser, 0, len(users))
	trafficIdentities := make([]model.UserTrafficIdentity, 0, len(users))
	for i := range users {
		user := &users[i]
		if user.Expired(now) || !user.HasInbound(inbound.ServerID, inbound.ID) {
			continue
		}
		identity := proxyIdentity(user, inbound.ID)
		identity.Name = proxyTrafficName(user)
		identities = append(identities, identity)
		trafficIdentities = append(trafficIdentities, model.UserTrafficIdentity{
			ServerID: inbound.ServerID, InboundID: inbound.ID, Name: identity.Name,
			UserID: user.ID, UserKey: identity.Name,
		})
	}
	if err := rememberProxyTrafficIdentities(db, trafficIdentities); err != nil {
		return nil, fmt.Errorf("register proxy traffic identities: %w", err)
	}
	forwards, forwardBindings, err := forwardIdentitiesForInbound(db, inbound)
	if err != nil {
		return nil, err
	}
	identities = append(identities, forwards...)
	trafficIdentities = append(trafficIdentities, forwardBindings...)
	if err := rememberProxyTrafficIdentities(db, forwardBindings); err != nil {
		return nil, fmt.Errorf("register forwarded traffic identities: %w", err)
	}
	if len(identities) == 0 {
		// Several protocols interpret an omitted/empty users list as anonymous or
		// single-credential mode. Keep one unissued credential so revoking the last
		// real user never turns the listener into an open proxy. The Agent token is
		// a stable server-side secret and is never exposed in subscriptions.
		var server model.Server
		if err := db.Select("agent_token").First(&server, inbound.ServerID).Error; err != nil {
			return nil, err
		}
		if server.AgentToken == "" {
			return nil, fmt.Errorf("server %d has no agent token for multi-user lockout credential", inbound.ServerID)
		}
		lockout := proxyIdentity(&model.User{ProxyToken: "lockout:" + server.AgentToken}, inbound.ID)
		lockout.Name = lockoutProxyUserName
		lockout.Username = lockout.Name
		identities = append(identities, lockout)
	}
	return identities, nil
}

// forwardIdentitiesForInbound provisions one additional user on each inbound
// that has derived nodes. Each identity belongs to the same account as the
// underlying node but has a distinct name and credential, so a client can
// select the original node or an inbound→outbound node independently.
func forwardIdentitiesForInbound(db *gorm.DB, inbound *model.Inbound) ([]singbox.ProxyUser, []model.UserTrafficIdentity, error) {
	var forwards []model.UserForward
	if err := db.Where("inbound_id = ? AND server_id = ? AND user_id IN (?)",
		inbound.ID, inbound.ServerID,
		db.Model(&model.User{}).Select("id").Where("forwarding_enabled = ?", true),
	).Order("id").Find(&forwards).Error; err != nil {
		return nil, nil, err
	}
	if len(forwards) == 0 {
		return nil, nil, nil
	}
	userIDs := make([]uint, 0, len(forwards))
	for _, forward := range forwards {
		userIDs = append(userIDs, forward.UserID)
	}
	var users []model.User
	if err := db.Where("id IN ? AND enabled = ?", normalizedIDs(userIDs), true).Find(&users).Error; err != nil {
		return nil, nil, err
	}
	byID := make(map[uint]model.User, len(users))
	for _, user := range users {
		byID[user.ID] = user
	}
	now := time.Now()
	out := make([]singbox.ProxyUser, 0, len(forwards))
	bindings := make([]model.UserTrafficIdentity, 0, len(forwards))
	for _, forward := range forwards {
		user, ok := byID[forward.UserID]
		if !ok || user.Expired(now) || !user.HasInbound(inbound.ServerID, inbound.ID) {
			continue
		}
		identity := forwardProxyIdentity(&user, forward.ID, inbound.ID)
		out = append(out, identity)
		bindings = append(bindings, model.UserTrafficIdentity{
			ServerID: inbound.ServerID, InboundID: inbound.ID, Name: identity.Name,
			UserID: user.ID, UserKey: proxyTrafficName(&user),
		})
	}
	return out, bindings, nil
}

func mergeServerIDs(groups ...[]uint) []uint {
	seen := make(map[uint]bool)
	var ids []uint
	for _, group := range groups {
		for _, id := range group {
			if id == 0 || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// refreshUserProxyAccess schedules convergence for every affected managed
// node after a grant, enablement, expiry or deletion changes. The orchestrator
// coalesces repeated requests per server, so a burst of user edits cannot leave
// dozens of goroutines queued behind the same slow or offline Agent.
func (a *App) refreshUserProxyAccess(serverIDs ...[]uint) {
	ids := mergeServerIDs(serverIDs...)
	if len(ids) == 0 || a.orch == nil {
		return
	}
	for _, serverID := range ids {
		a.orch.PushConfigIfManaged(serverID)
	}
}
