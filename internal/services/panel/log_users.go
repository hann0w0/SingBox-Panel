package panel

import (
	"context"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

var logTrafficUserPattern = regexp.MustCompile(`\[(` +
	regexp.QuoteMeta(proxyTrafficNamePrefix) + `[0-9]+:[0-9a-f]{32}|` +
	regexp.QuoteMeta(forwardTrafficNamePrefix) + `[0-9]+:[0-9]+:[0-9a-f]{32})\]`)

const logUserCacheTTL = 5 * time.Second

// Each history request or SSE connection owns its resolver. A short-lived
// snapshot avoids database queries per log line without hiding later renames.
type logUserResolver struct {
	db        *gorm.DB
	serverID  uint
	names     map[string]string
	refreshAt time.Time
}

func (r *logUserResolver) resolve(ctx context.Context, text string) string {
	if !logTrafficUserPattern.MatchString(text) {
		return text
	}
	if now := time.Now(); !now.Before(r.refreshAt) {
		r.refreshAt = now.Add(logUserCacheTTL)
		r.names = nil
		if err := r.load(ctx); err != nil {
			// Keep the original log available even if display-name lookup fails.
			log.Printf("resolve log users for server %d: %v", r.serverID, err)
			return text
		}
	}
	return logTrafficUserPattern.ReplaceAllStringFunc(text, func(token string) string {
		raw := token[1 : len(token)-1]
		if name := r.names[raw]; name != "" {
			return "[" + name + "]"
		}
		if strings.HasPrefix(raw, forwardTrafficNamePrefix) {
			if name := r.forwardUserDisplayName(ctx, raw); name != "" {
				return "[" + name + "]"
			}
		}
		return token
	})
}

func (r *logUserResolver) load(ctx context.Context) error {
	var rows []struct {
		Name       string
		UserKey    string
		UserID     uint
		Email      string
		ProxyToken string
		SubToken   string
	}
	if err := r.db.WithContext(ctx).Table("user_traffic_identities AS identities").
		Select("DISTINCT identities.name, identities.user_key, users.id AS user_id, users.email, users.proxy_token, users.sub_token").
		Joins("JOIN users ON users.id = identities.user_id").
		Where("identities.server_id = ?", r.serverID).
		Scan(&rows).Error; err != nil {
		return err
	}
	r.names = make(map[string]string, len(rows))
	for _, row := range rows {
		user := model.User{ID: row.UserID, Email: row.Email, ProxyToken: row.ProxyToken, SubToken: row.SubToken}
		// A deleted/restored numeric ID alone is not proof of ownership. Keep
		// unknown or obsolete identities intact instead of labelling a new owner.
		if row.UserKey != proxyTrafficName(&user) {
			continue
		}
		name := user.Email
		if name == "" {
			name = "u" + strconv.FormatUint(uint64(user.ID), 10)
		}
		// Preserve the single-line [user] log format for legacy or unusual names.
		quoted := strconv.QuoteToGraphic(name)
		r.names[row.Name] = strings.NewReplacer("[", `\u005b`, "]", `\u005d`).Replace(quoted[1 : len(quoted)-1])
	}
	return nil
}

// forwardUserDisplayName resolves a derived forward identity to the owning
// account plus a forward label. The token contains IDs and a seed hash, so the
// lookup verifies the current seed instead of trusting the log text.
func (r *logUserResolver) forwardUserDisplayName(ctx context.Context, token string) string {
	parts := strings.Split(strings.TrimPrefix(token, forwardTrafficNamePrefix), ":")
	if len(parts) != 3 {
		return ""
	}
	userID, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || userID == 0 {
		return ""
	}
	forwardID, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || forwardID == 0 {
		return ""
	}
	var row struct {
		Email      string
		ProxyToken string
		SubToken   string
	}
	if err := r.db.WithContext(ctx).Table("users").
		Select("email, proxy_token, sub_token").Where("id = ?", userID).Take(&row).Error; err != nil {
		return ""
	}
	user := model.User{ID: uint(userID), ProxyToken: row.ProxyToken, SubToken: row.SubToken}
	if forwardTrafficName(&user, uint(forwardID)) != token {
		return ""
	}
	name := row.Email
	if name == "" {
		name = "u" + parts[0]
	}
	return name + " → 转发"
}

func (r *logUserResolver) event(ctx context.Context, event liveEvent) liveEvent {
	if entry, ok := event.Data.(protocol.LogEvt); event.Kind == "log" && ok {
		entry.Msg = r.resolve(ctx, entry.Msg)
		event.Data = entry
	}
	return event
}
