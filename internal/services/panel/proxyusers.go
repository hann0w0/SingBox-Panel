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
	for i := range users {
		user := &users[i]
		if user.Expired(now) || !user.HasInbound(inbound.ServerID, inbound.ID) {
			continue
		}
		identities = append(identities, proxyIdentity(user, inbound.ID))
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
		lockout.Name = "__singbox_panel_disabled__"
		lockout.Username = lockout.Name
		identities = append(identities, lockout)
	}
	return identities, nil
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
