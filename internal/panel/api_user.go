package panel

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
)

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
	c.JSON(http.StatusOK, gin.H{"nodes": a.userNodeDetails(&u)})
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
	if req.NewPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "new password cannot be empty"})
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
	if err := a.db.Model(&model.User{}).Where("id = ?", u.ID).Updates(map[string]any{
		"password":      hash,
		"token_version": gorm.Expr("token_version + 1"),
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save failed"})
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
