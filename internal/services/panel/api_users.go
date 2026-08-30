package panel

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

var (
	errInvalidServerAccess     = errors.New("服务器列表包含不存在的受管服务器")
	errInvalidInboundAccess    = errors.New("节点列表包含不存在的受管节点")
	errInvalidCustomNodeAccess = errors.New("节点列表包含不存在的自定义节点")
	errInvalidUserNodeOrder    = errors.New("节点排序必须包含每个已分配节点且不能重复")
)

const (
	userNodeTypeManaged = "managed"
	userNodeTypeCustom  = "custom"
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
	ServerIDs     *[]uint              `json:"server_ids"`
	InboundIDs    *[]uint              `json:"inbound_ids"`
	CustomNodeIDs *[]uint              `json:"custom_node_ids"`
	NodeOrder     *[]userNodeOrderItem `json:"node_order"`
}

type userNodeOrderItem struct {
	NodeType string `json:"node_type"`
	NodeID   uint   `json:"node_id"`
}

type userAccessResp struct {
	UserID        uint                `json:"user_id"`
	ServerIDs     []uint              `json:"server_ids"`
	ServerWide    bool                `json:"server_wide"`
	InboundIDs    []uint              `json:"inbound_ids"`
	CustomNodeIDs []uint              `json:"custom_node_ids"`
	NodeOrder     []userNodeOrderItem `json:"node_order"`
}

type userListItem struct {
	model.User
	NodeCount int `json:"node_count"`
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
	resp := userAccessResp{
		UserID: user.ID, ServerIDs: []uint{}, InboundIDs: []uint{},
		CustomNodeIDs: []uint{}, NodeOrder: []userNodeOrderItem{},
	}
	if len(user.ServerIDs) > 0 {
		if err := db.Model(&model.Server{}).Where("id IN ?", user.ServerIDs).Pluck("id", &resp.ServerIDs).Error; err != nil {
			return resp, err
		}
		resp.ServerIDs = normalizedIDs(resp.ServerIDs)
	}
	resp.ServerWide = len(user.InboundIDs) == 0 && len(resp.ServerIDs) > 0
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
	if err := db.Where("hidden_by_subscription_rule = ?", false).Order("sort_order, id").Find(&nodes).Error; err != nil {
		return resp, err
	}
	for i := range nodes {
		if nodes[i].HasUser(user.ID) {
			resp.CustomNodeIDs = append(resp.CustomNodeIDs, nodes[i].ID)
		}
	}
	var err error
	resp.NodeOrder, err = orderedUserNodeOrder(db, user.ID, resp.InboundIDs, resp.CustomNodeIDs)
	if err != nil {
		return resp, err
	}
	return resp, nil
}

func userNodeSet(inboundIDs, customNodeIDs []uint) map[string]bool {
	set := make(map[string]bool, len(inboundIDs)+len(customNodeIDs))
	for _, id := range inboundIDs {
		set[userNodeTypeManaged+":"+strconv.FormatUint(uint64(id), 10)] = true
	}
	for _, id := range customNodeIDs {
		set[userNodeTypeCustom+":"+strconv.FormatUint(uint64(id), 10)] = true
	}
	return set
}

func userNodeKey(item userNodeOrderItem) string {
	return item.NodeType + ":" + strconv.FormatUint(uint64(item.NodeID), 10)
}

// canonicalUserNodeOrder follows the same stable administrator/server order
// used by subscriptions. It is the migration-safe fallback for users without
// a saved personal order and the append order for newly assigned nodes.
func canonicalUserNodeOrder(db *gorm.DB, inboundIDs, customNodeIDs []uint) ([]userNodeOrderItem, error) {
	out := make([]userNodeOrderItem, 0, len(inboundIDs)+len(customNodeIDs))
	selectedInbounds := make(map[uint]model.Inbound, len(inboundIDs))
	if len(inboundIDs) > 0 {
		var inbounds []model.Inbound
		if err := db.Where("id IN ?", inboundIDs).Find(&inbounds).Error; err != nil {
			return nil, err
		}
		for i := range inbounds {
			selectedInbounds[inbounds[i].ID] = inbounds[i]
		}
	}
	serverIDs := make([]uint, 0, len(selectedInbounds))
	seenServers := make(map[uint]bool, len(selectedInbounds))
	for _, ib := range selectedInbounds {
		if !seenServers[ib.ServerID] {
			seenServers[ib.ServerID] = true
			serverIDs = append(serverIDs, ib.ServerID)
		}
	}
	if len(serverIDs) > 0 {
		var servers []model.Server
		if err := db.Where("id IN ?", serverIDs).Order("id").Find(&servers).Error; err != nil {
			return nil, err
		}
		applyServerOrder(db, servers)
		for _, server := range servers {
			var serverIDsForOrder []uint
			for id, ib := range selectedInbounds {
				if ib.ServerID == server.ID {
					serverIDsForOrder = append(serverIDsForOrder, id)
				}
			}
			sort.Slice(serverIDsForOrder, func(i, j int) bool { return serverIDsForOrder[i] < serverIDsForOrder[j] })
			for _, id := range serverIDsForOrder {
				out = append(out, userNodeOrderItem{NodeType: userNodeTypeManaged, NodeID: id})
			}
		}
	}
	// A stale server reference should not normally exist, but retain valid
	// inbounds whose server row is missing rather than silently losing order.
	seen := make(map[uint]bool, len(out))
	for _, item := range out {
		seen[item.NodeID] = true
	}
	var remaining []uint
	for id := range selectedInbounds {
		if !seen[id] {
			remaining = append(remaining, id)
		}
	}
	sort.Slice(remaining, func(i, j int) bool { return remaining[i] < remaining[j] })
	for _, id := range remaining {
		out = append(out, userNodeOrderItem{NodeType: userNodeTypeManaged, NodeID: id})
	}

	if len(customNodeIDs) > 0 {
		var nodes []model.CustomNode
		if err := db.Where("id IN ? AND hidden_by_subscription_rule = ?", customNodeIDs, false).
			Order("sort_order, id").Find(&nodes).Error; err != nil {
			return nil, err
		}
		for _, node := range nodes {
			out = append(out, userNodeOrderItem{NodeType: userNodeTypeCustom, NodeID: node.ID})
		}
	}
	return out, nil
}

// orderedUserNodeOrder applies a saved personal order, filters removed or
// unassigned nodes, and appends new assignments in canonical order.
func orderedUserNodeOrder(db *gorm.DB, userID uint, inboundIDs, customNodeIDs []uint) ([]userNodeOrderItem, error) {
	canonical, err := canonicalUserNodeOrder(db, inboundIDs, customNodeIDs)
	if err != nil {
		return nil, err
	}
	valid := userNodeSet(inboundIDs, customNodeIDs)
	var saved []model.UserNodeOrder
	if err := db.Where("user_id = ?", userID).Order("sort_order, id").Find(&saved).Error; err != nil {
		return nil, err
	}
	out := make([]userNodeOrderItem, 0, len(canonical))
	seen := make(map[string]bool, len(canonical))
	for _, row := range saved {
		item := userNodeOrderItem{NodeType: row.NodeType, NodeID: row.NodeID}
		if !valid[userNodeKey(item)] || seen[userNodeKey(item)] {
			continue
		}
		seen[userNodeKey(item)] = true
		out = append(out, item)
	}
	for _, item := range canonical {
		if !seen[userNodeKey(item)] {
			seen[userNodeKey(item)] = true
			out = append(out, item)
		}
	}
	return out, nil
}

func validateUserNodeOrder(order []userNodeOrderItem, inboundIDs, customNodeIDs []uint) error {
	expected := userNodeSet(inboundIDs, customNodeIDs)
	if len(order) != len(expected) {
		return errInvalidUserNodeOrder
	}
	seen := make(map[string]bool, len(order))
	for i := range order {
		item := &order[i]
		if item.NodeID == 0 || (item.NodeType != userNodeTypeManaged && item.NodeType != userNodeTypeCustom) {
			return errInvalidUserNodeOrder
		}
		key := userNodeKey(*item)
		if !expected[key] || seen[key] {
			return errInvalidUserNodeOrder
		}
		seen[key] = true
	}
	return nil
}

func saveUserNodeOrder(tx *gorm.DB, userID uint, order []userNodeOrderItem) error {
	if err := tx.Where("user_id = ?", userID).Delete(&model.UserNodeOrder{}).Error; err != nil {
		return err
	}
	if len(order) == 0 {
		return nil
	}
	rows := make([]model.UserNodeOrder, len(order))
	for i, item := range order {
		rows[i] = model.UserNodeOrder{UserID: userID, NodeType: item.NodeType, NodeID: item.NodeID, SortOrder: i}
	}
	return tx.Create(&rows).Error
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
	a.userAccessMu.Lock()
	defer a.userAccessMu.Unlock()
	var req userAccessReq
	if !bindJSON(c, &req) {
		return
	}
	if req.InboundIDs == nil || req.CustomNodeIDs == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "inbound_ids 和 custom_node_ids 必须是数组"})
		return
	}
	serverIDs := []uint(nil)
	if req.ServerIDs != nil {
		serverIDs = normalizedIDs(*req.ServerIDs)
	}
	inboundIDs := normalizedIDs(*req.InboundIDs)
	customNodeIDs := normalizedIDs(*req.CustomNodeIDs)
	var user model.User
	if err := a.db.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	oldServerIDs := append([]uint(nil), user.ServerIDs...)

	unlockAudience := a.subscriptionManager().lockAudience()
	defer unlockAudience()
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
		inboundServerIDs := make([]uint, 0, len(inbounds))
		for i := range inbounds {
			inboundServerIDs = append(inboundServerIDs, inbounds[i].ServerID)
		}

		managedOrderInboundIDs := inboundIDs
		if req.ServerIDs != nil && len(inboundIDs) == 0 {
			if len(serverIDs) > 0 {
				var serverCount int64
				if err := tx.Model(&model.Server{}).Where("id IN ?", serverIDs).Count(&serverCount).Error; err != nil {
					return err
				}
				if serverCount != int64(len(serverIDs)) {
					return errInvalidServerAccess
				}
				if err := tx.Model(&model.Inbound{}).Where("server_id IN ?", serverIDs).Pluck("id", &managedOrderInboundIDs).Error; err != nil {
					return err
				}
				managedOrderInboundIDs = normalizedIDs(managedOrderInboundIDs)
			}
			user.ServerIDs = serverIDs
			user.InboundIDs = []uint{}
		} else {
			user.ServerIDs = normalizedIDs(inboundServerIDs)
			user.InboundIDs = inboundIDs
		}

		var nodes []model.CustomNode
		if err := tx.Where("hidden_by_subscription_rule = ?", false).Order("id").Find(&nodes).Error; err != nil {
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

		var order []userNodeOrderItem
		if req.NodeOrder != nil {
			order = append([]userNodeOrderItem(nil), (*req.NodeOrder)...)
			if err := validateUserNodeOrder(order, managedOrderInboundIDs, customNodeIDs); err != nil {
				return err
			}
		} else {
			// Older clients only send assignment arrays. Preserve any saved
			// personal order and append newly assigned nodes deterministically.
			var err error
			order, err = orderedUserNodeOrder(tx, user.ID, managedOrderInboundIDs, customNodeIDs)
			if err != nil {
				return err
			}
		}
		return saveUserNodeOrder(tx, user.ID, order)
	})
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errInvalidServerAccess) || errors.Is(err, errInvalidInboundAccess) || errors.Is(err, errInvalidCustomNodeAccess) || errors.Is(err, errInvalidUserNodeOrder) {
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

// validateUserNodeIDs checks that every server and inbound referenced by the
// user's assignment actually exists, and keeps ServerIDs consistent with a
// non-empty InboundIDs (the inbound's server is always included). Mirrors the
// validation done in updateUserAccess so both write paths reject bad IDs.
func (a *App) validateUserNodeIDs(u *model.User) error {
	if len(u.ServerIDs) > 0 {
		var servers int64
		if err := a.db.Model(&model.Server{}).Where("id IN ?", u.ServerIDs).Count(&servers).Error; err != nil {
			return err
		}
		if servers != int64(len(u.ServerIDs)) {
			return errors.New("服务器列表包含不存在的受管服务器")
		}
	}
	if len(u.InboundIDs) > 0 {
		var inbounds []model.Inbound
		if err := a.db.Where("id IN ?", u.InboundIDs).Find(&inbounds).Error; err != nil {
			return err
		}
		if len(inbounds) != len(u.InboundIDs) {
			return errInvalidInboundAccess
		}
		// A user assigned specific inbounds must also be provisioned on the
		// servers that own them, otherwise proxy-access refreshes would miss
		// those servers. Merge them so the two lists can never drift.
		serverSet := make(map[uint]bool, len(u.ServerIDs)+len(inbounds))
		for _, id := range u.ServerIDs {
			serverSet[id] = true
		}
		for i := range inbounds {
			serverSet[inbounds[i].ServerID] = true
		}
		merged := make([]uint, 0, len(serverSet))
		for id := range serverSet {
			merged = append(merged, id)
		}
		u.ServerIDs = normalizedIDs(merged)
	}
	return nil
}

func (a *App) listUsers(c *gin.Context) {
	var users []model.User
	if err := a.db.Order("id").Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var inbounds []model.Inbound
	if err := a.db.Select("id", "server_id").Find(&inbounds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var nodes []model.CustomNode
	if err := a.db.Where("hidden_by_subscription_rule = ?", false).
		Select("id", "all_users", "user_ids", "excluded_user_ids").Find(&nodes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	inboundServers := make(map[uint]uint, len(inbounds))
	serverInboundCounts := make(map[uint]int)
	for i := range inbounds {
		inboundServers[inbounds[i].ID] = inbounds[i].ServerID
		serverInboundCounts[inbounds[i].ServerID]++
	}
	items := make([]userListItem, 0, len(users))
	for i := range users {
		count := 0
		if len(users[i].InboundIDs) > 0 {
			seen := make(map[uint]bool, len(users[i].InboundIDs))
			for _, inboundID := range users[i].InboundIDs {
				if _, exists := inboundServers[inboundID]; exists && !seen[inboundID] {
					seen[inboundID] = true
					count++
				}
			}
		} else {
			seen := make(map[uint]bool, len(users[i].ServerIDs))
			for _, serverID := range users[i].ServerIDs {
				if !seen[serverID] {
					seen[serverID] = true
					count += serverInboundCounts[serverID]
				}
			}
		}
		for j := range nodes {
			if nodes[j].HasUser(users[i].ID) {
				count++
			}
		}
		items = append(items, userListItem{User: users[i], NodeCount: count})
	}
	c.JSON(http.StatusOK, gin.H{"users": items})
}

func (a *App) createUser(c *gin.Context) {
	a.userAccessMu.Lock()
	defer a.userAccessMu.Unlock()

	var req userReq
	if !bindJSON(c, &req) {
		return
	}
	email, normalizedEmail, err := validateUsername(req.Email)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateNewPassword(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var cnt int64
	if err := a.db.Model(&model.User{}).
		Where("email_normalized = ? OR (email_normalized IS NULL AND LOWER(email) = ?)", normalizedEmail, normalizedEmail).
		Count(&cnt).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if cnt > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "email already exists"})
		return
	}
	u := &model.User{
		Email:           email,
		EmailNormalized: &normalizedEmail,
		Role:            model.RoleUser,
		ServerIDs:       normalizedIDs(req.ServerIDs),
		InboundIDs:      normalizedIDs(req.InboundIDs),
		ExpireAt:        unixToTime(req.ExpireAt),
		SubToken:        randHex(16),
		ProxyToken:      randHex(32),
	}
	// Validate before hashing or writing so a stale node assignment cannot
	// create a user with access data that does not exist.
	if err := a.validateUserNodeIDs(u); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
	u.Password = hash
	u.Enabled = enabled
	if err := a.db.Create(u).Error; err != nil {
		if duplicateDatabaseError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "email already exists"})
			return
		}
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
	a.userAccessMu.Lock()
	defer a.userAccessMu.Unlock()
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
	columns := make([]string, 0, 8)

	if req.Email != "" {
		email, normalizedEmail, err := validateUsername(req.Email)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		var cnt int64
		if err := a.db.Model(&model.User{}).
			Where("id <> ? AND (email_normalized = ? OR (email_normalized IS NULL AND LOWER(email) = ?))", u.ID, normalizedEmail, normalizedEmail).
			Count(&cnt).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if cnt > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "email already exists"})
			return
		}
		u.Email = email
		u.EmailNormalized = &normalizedEmail
		columns = append(columns, "Email", "EmailNormalized")
	}
	if req.Password != "" {
		if err := validateNewPassword(req.Password); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		h, err := hashPassword(req.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
			return
		}
		u.Password = h
		revokeSessions = true
		columns = append(columns, "Password")
	}
	accessChanged := false
	if req.ServerIDs != nil {
		u.ServerIDs = normalizedIDs(req.ServerIDs)
		accessChanged = true
	}
	// A nil slice means "not sent"; an empty one means "cleared" (= all inbounds).
	if req.InboundIDs != nil {
		u.InboundIDs = normalizedIDs(req.InboundIDs)
		accessChanged = true
	}

	// Validate the assigned node lists the same way updateUserAccess does, so an
	// API client passing a stale/nonexistent ID gets a clear 400 instead of a
	// silently dropped assignment.
	if err := a.validateUserNodeIDs(&u); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.ExpireAt != nil {
		u.ExpireAt = unixToTime(req.ExpireAt)
		columns = append(columns, "ExpireAt")
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
		columns = append(columns, "Enabled")
	}
	if accessChanged {
		// validateUserNodeIDs may merge the owning server of a selected inbound.
		columns = append(columns, "ServerIDs", "InboundIDs")
	}
	if len(columns) > 0 {
		err := a.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.User{}).Where("id = ?", u.ID).Select(columns).Updates(&u).Error; err != nil {
				return err
			}
			if revokeSessions {
				return tx.Model(&model.User{}).Where("id = ?", u.ID).
					UpdateColumn("token_version", gorm.Expr("token_version + 1")).Error
			}
			return nil
		})
		if err != nil {
			if duplicateDatabaseError(err) {
				c.JSON(http.StatusConflict, gin.H{"error": "email already exists"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := a.db.First(&u, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	a.refreshUserProxyAccess(oldServerIDs, u.ServerIDs)
	c.JSON(http.StatusOK, gin.H{"user": u})
}

func duplicateDatabaseError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "duplicate key") ||
		strings.Contains(message, "duplicate entry")
}

func (a *App) deleteUser(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	a.userAccessMu.Lock()
	defer a.userAccessMu.Unlock()
	var u model.User
	if err := a.db.First(&u, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if u.Role == model.RoleAdmin {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除管理员账号"})
		return
	}
	unlockAudience := a.subscriptionManager().lockAudience()
	defer unlockAudience()
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.User{}, id).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&model.UserNodeOrder{}).Error; err != nil {
			return err
		}
		// Drop the deleted user from every custom node's audience lists so no
		// stale ID lingers (it would be invisible today but could matter if an
		// ID is ever reused after a backup restore / manual insert).
		var nodes []model.CustomNode
		if err := tx.Order("id").Find(&nodes).Error; err != nil {
			return err
		}
		for i := range nodes {
			n := &nodes[i]
			changed := false
			if ids := removeID(n.UserIDs, id); len(ids) != len(n.UserIDs) {
				n.UserIDs = ids
				changed = true
			}
			if ids := removeID(n.ExcludedUserIDs, id); len(ids) != len(n.ExcludedUserIDs) {
				n.ExcludedUserIDs = ids
				changed = true
			}
			if changed {
				if err := tx.Model(n).Select("UserIDs", "ExcludedUserIDs", "UpdatedAt").Updates(n).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.refreshUserProxyAccess(u.ServerIDs)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// removeID returns ids without the given value, preserving order. The caller
// detects change by comparing lengths (IDs are unique and normalized).
func removeID(ids []uint, id uint) []uint {
	out := make([]uint, 0, len(ids))
	for _, existing := range ids {
		if existing != id {
			out = append(out, existing)
		}
	}
	return out
}
