package panel

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/model"
)

type userReq struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	ServerIDs  []uint `json:"server_ids"`
	InboundIDs []uint `json:"inbound_ids"` // empty = every inbound on those servers
	ExpireAt   *int64 `json:"expire_at"`   // unix seconds; 0/null = never
	Enabled    *bool  `json:"enabled"`
}

func (a *App) listUsers(c *gin.Context) {
	var users []model.User
	a.db.Order("id").Find(&users)
	c.JSON(http.StatusOK, gin.H{"users": users})
}

func (a *App) createUser(c *gin.Context) {
	var req userReq
	if !bindJSON(c, &req) {
		return
	}
	email := strings.TrimSpace(req.Email)
	if email == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email and password are required"})
		return
	}
	var cnt int64
	a.db.Model(&model.User{}).Where("LOWER(email) = ?", strings.ToLower(email)).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "email already exists"})
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	u := &model.User{
		Email:      email,
		Password:   hash,
		Role:       model.RoleUser,
		ServerIDs:  req.ServerIDs,
		InboundIDs: req.InboundIDs,
		ExpireAt:   unixToTime(req.ExpireAt),
		Enabled:    enabled,
		SubToken:   randHex(16),
		ProxyToken: randHex(32),
	}
	if err := a.db.Create(u).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.refreshUserProxyAccess(u.ServerIDs)
	c.JSON(http.StatusOK, gin.H{"user": u})
}

func (a *App) updateUser(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var u model.User
	if err := a.db.First(&u, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	oldServerIDs := append([]uint(nil), u.ServerIDs...)
	var req userReq
	if !bindJSON(c, &req) {
		return
	}
	revokeSessions := false

	if req.Email != "" {
		email := strings.TrimSpace(req.Email)
		if email == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "username cannot be empty"})
			return
		}
		var cnt int64
		if err := a.db.Model(&model.User{}).Where("id <> ? AND LOWER(email) = ?", u.ID, strings.ToLower(email)).Count(&cnt).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if cnt > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "email already exists"})
			return
		}
		u.Email = email
	}
	if req.Password != "" {
		h, err := hashPassword(req.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
			return
		}
		u.Password = h
		revokeSessions = true
	}
	if req.ServerIDs != nil {
		u.ServerIDs = req.ServerIDs
	}
	// A nil slice means "not sent"; an empty one means "cleared" (= all inbounds).
	if req.InboundIDs != nil {
		u.InboundIDs = req.InboundIDs
	}
	if req.ExpireAt != nil {
		u.ExpireAt = unixToTime(req.ExpireAt)
	}
	if req.Enabled != nil {
		if u.Role == model.RoleAdmin && !*req.Enabled {
			c.JSON(http.StatusBadRequest, gin.H{"error": "不能停用管理员账号"})
			return
		}
		if u.Enabled != *req.Enabled {
			revokeSessions = true
		}
		u.Enabled = *req.Enabled
	}
	if revokeSessions {
		u.TokenVersion++
	}
	if err := a.db.Save(&u).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	a.refreshUserProxyAccess(oldServerIDs, u.ServerIDs)
	c.JSON(http.StatusOK, gin.H{"user": u})
}

func (a *App) deleteUser(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var u model.User
	if err := a.db.First(&u, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if u.Role == model.RoleAdmin {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除管理员账号"})
		return
	}
	if err := a.db.Delete(&model.User{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.refreshUserProxyAccess(u.ServerIDs)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
