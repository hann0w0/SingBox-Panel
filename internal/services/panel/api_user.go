package panel

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

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

// handleResetSub regenerates the user's subscription token.
func (a *App) handleResetSub(c *gin.Context) {
	uid := currentUID(c)
	token := randHex(16)
	if err := a.db.Model(&model.User{}).Where("id = ?", uid).Update("sub_token", token).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reset failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sub_token": token, "subscription_url": a.subURL(token)})
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
	if !checkPassword(u.Password, req.OldPassword) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "wrong current password"})
		return
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
