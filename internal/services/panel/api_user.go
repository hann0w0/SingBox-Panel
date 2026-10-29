package panel

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

var errPasswordChangedConcurrently = errors.New("password changed concurrently")

func updatePasswordIfCurrent(db *gorm.DB, user *model.User, passwordHash string) (bool, error) {
	result := db.Model(&model.User{}).
		Where("id = ? AND password = ? AND token_version = ?", user.ID, user.Password, user.TokenVersion).
		Updates(map[string]any{
			"password":      passwordHash,
			"token_version": gorm.Expr("token_version + 1"),
		})
	return result.RowsAffected == 1, result.Error
}

func (a *App) subURL(token string) string {
	return a.baseURL() + a.cfg.Subscription.PathPrefix + "/" + token
}

// handleMe returns the current user's profile and subscription link.
func (a *App) handleMe(c *gin.Context) {
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"user":             u,
		"subscription_url": a.subURL(u.SubToken),
	})
}

// handleUserNodes lists the nodes the current user can use.
func (a *App) handleUserNodes(c *gin.Context) {
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	// Same gate as the subscription: a disabled/expired account gets no nodes.
	if u.Role != model.RoleAdmin && !userActive(&u) {
		c.JSON(http.StatusForbidden, gin.H{"error": "账号已停用或已到期"})
		return
	}
	nodes, err := a.userNodeDetails(&u)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "暂时无法读取节点，请稍后重试"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"nodes": nodes})
}

// handleResetSub revokes both the subscription URL and per-user proxy credentials.
func (a *App) handleResetSub(c *gin.Context) {
	var user model.User
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, currentUID(c)).Error; err != nil {
			return err
		}
		oldTrafficKey := proxyTrafficName(&user)
		user.SubToken = randHex(16)
		user.ProxyToken = randHex(32)
		if err := tx.Model(&user).Updates(map[string]any{
			"sub_token": user.SubToken, "proxy_token": user.ProxyToken,
		}).Error; err != nil {
			return err
		}
		// Keep delayed usage and historical logs attached to this same account,
		// without reviving bindings left behind by a deleted/recreated account.
		return tx.Model(&model.UserTrafficIdentity{}).
			Where("user_id = ? AND user_key = ?", user.ID, oldTrafficKey).
			Update("user_key", proxyTrafficName(&user)).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reset failed"})
		return
	}
	a.refreshUserProxyAccess(user.ServerIDs)
	c.JSON(http.StatusOK, gin.H{"sub_token": user.SubToken, "subscription_url": a.subURL(user.SubToken)})
}

type changePasswordReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// handleChangePassword updates the current user's password.
func (a *App) handleChangePassword(c *gin.Context) {
	var req changePasswordReq
	if !bindJSON(c, &req) {
		return
	}
	if err := validateNewPassword(req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var u model.User
	if err := a.db.First(&u, currentUID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	// Throttle guesses at the current password like logins are throttled.
	// Otherwise a stolen session could recover the plaintext password (often
	// reused elsewhere) at full bcrypt speed. Keyed by account, not address: the
	// caller already holds this account's session.
	guardKey := "chpw:" + strconv.FormatUint(uint64(u.ID), 10)
	if a.login != nil {
		if wait := a.login.retryAfter(guardKey); wait > 0 {
			tooMany(c, wait)
			return
		}
	}
	if !checkPassword(u.Password, req.OldPassword) {
		if a.login != nil {
			a.login.fail(guardKey)
		}
		// The session is valid; only this form value is wrong. A 401 makes
		// clients revoke the current session and discard the password dialog.
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前密码不正确"})
		return
	}
	if a.login != nil {
		a.login.succeed(guardKey)
	}
	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
		return
	}
	updated, err := updatePasswordIfCurrent(a.db, &u, hash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save failed"})
		return
	}
	if !updated {
		c.JSON(http.StatusConflict, gin.H{"error": errPasswordChangedConcurrently.Error()})
		return
	}
	// Every old session is revoked, including the token used for this request.
	// Issue one replacement token so the user who changed the password stays
	// signed in while sessions on other devices are invalidated.
	u.TokenVersion++
	token, err := a.auth.Issue(&u, sessionTTL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token error; please sign in again"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "token": token})
}
