package panel

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
)

var (
	errInvalidInboundAccess    = errors.New("节点列表包含不存在的受管节点")
	errInvalidCustomNodeAccess = errors.New("节点列表包含不存在的自定义节点")
)

type userReq struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	ServerIDs  []uint `json:"server_ids"`
	InboundIDs []uint `json:"inbound_ids"` // empty = every inbound on those servers
	ExpireAt   *int64 `json:"expire_at"`   // unix seconds; 0/null = never
	Enabled    *bool  `json:"enabled"`
}

type userAccessReq struct {
	InboundIDs    *[]uint `json:"inbound_ids"`
	CustomNodeIDs *[]uint `json:"custom_node_ids"`
}

type userAccessResp struct {
	UserID        uint   `json:"user_id"`
	InboundIDs    []uint `json:"inbound_ids"`
	CustomNodeIDs []uint `json:"custom_node_ids"`
}

func normalizedIDs(ids []uint) []uint {
	seen := make(map[uint]bool, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func setID(ids []uint, id uint, present bool) []uint {
	out := make([]uint, 0, len(ids)+1)
	found := false
	for _, existing := range ids {
		if existing == id {
			found = true
			if !present {
				continue
			}
		}
		out = append(out, existing)
	}
	if present && !found {
		out = append(out, id)
	}
	return normalizedIDs(out)
}

func effectiveUserAccess(db *gorm.DB, user *model.User) (userAccessResp, error) {
	resp := userAccessResp{UserID: user.ID, InboundIDs: []uint{}, CustomNodeIDs: []uint{}}
	if len(user.InboundIDs) > 0 {
		if err := db.Model(&model.Inbound{}).Where("id IN ?", user.InboundIDs).Pluck("id", &resp.InboundIDs).Error; err != nil {
			return resp, err
		}
		resp.InboundIDs = normalizedIDs(resp.InboundIDs)
	} else if len(user.ServerIDs) > 0 {
		if err := db.Model(&model.Inbound{}).Where("server_id IN ?", user.ServerIDs).Pluck("id", &resp.InboundIDs).Error; err != nil {
			return resp, err
		}
		resp.InboundIDs = normalizedIDs(resp.InboundIDs)
	}
	var nodes []model.CustomNode
	if err := db.Order("id").Find(&nodes).Error; err != nil {
		return resp, err
	}
	for i := range nodes {
		if nodes[i].HasUser(user.ID) {
			resp.CustomNodeIDs = append(resp.CustomNodeIDs, nodes[i].ID)
		}
	}
	return resp, nil
}

func (a *App) getUserAccess(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var user model.User
	if err := a.db.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	access, err := effectiveUserAccess(a.db, &user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"access": access})
}

func (a *App) updateUserAccess(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req userAccessReq
	if !bindJSON(c, &req) {
		return
	}
	if req.InboundIDs == nil || req.CustomNodeIDs == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "inbound_ids 和 custom_node_ids 必须是数组"})
		return
	}
	inboundIDs := normalizedIDs(*req.InboundIDs)
	customNodeIDs := normalizedIDs(*req.CustomNodeIDs)
	var user model.User
	if err := a.db.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	oldServerIDs := append([]uint(nil), user.ServerIDs...)

	err := a.db.Transaction(func(tx *gorm.DB) error {
		var inbounds []model.Inbound
		if len(inboundIDs) > 0 {
			if err := tx.Where("id IN ?", inboundIDs).Find(&inbounds).Error; err != nil {
				return err
			}
			if len(inbounds) != len(inboundIDs) {
				return errInvalidInboundAccess
			}
		}
		serverIDs := make([]uint, 0, len(inbounds))
		for i := range inbounds {
			serverIDs = append(serverIDs, inbounds[i].ServerID)
		}
		user.ServerIDs = normalizedIDs(serverIDs)
		user.InboundIDs = inboundIDs

		var nodes []model.CustomNode
		if err := tx.Order("id").Find(&nodes).Error; err != nil {
			return err
		}
		knownCustomIDs := make(map[uint]bool, len(nodes))
		for i := range nodes {
			knownCustomIDs[nodes[i].ID] = true
		}
		for _, customID := range customNodeIDs {
			if !knownCustomIDs[customID] {
				return errInvalidCustomNodeAccess
			}
		}
		if err := tx.Model(&user).Select("ServerIDs", "InboundIDs", "UpdatedAt").Updates(&user).Error; err != nil {
			return err
		}
		selectedCustomIDs := make(map[uint]bool, len(customNodeIDs))
		for _, customID := range customNodeIDs {
			selectedCustomIDs[customID] = true
		}
		for i := range nodes {
			want := selectedCustomIDs[nodes[i].ID]
			if nodes[i].HasUser(user.ID) == want {
				continue
			}
			if nodes[i].AllUsers {
				nodes[i].ExcludedUserIDs = setID(nodes[i].ExcludedUserIDs, user.ID, !want)
			} else {
				nodes[i].UserIDs = setID(nodes[i].UserIDs, user.ID, want)
			}
			if err := tx.Model(&nodes[i]).Select("UserIDs", "ExcludedUserIDs", "UpdatedAt").Updates(&nodes[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errInvalidInboundAccess) || errors.Is(err, errInvalidCustomNodeAccess) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	a.refreshUserProxyAccess(oldServerIDs, user.ServerIDs)
	access, err := effectiveUserAccess(a.db, &user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"access": access})
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
