package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// ---------- inbounds ----------

type inboundReq struct {
	Type       string          `json:"type"`
	Tag        string          `json:"tag"`
	ListenPort int             `json:"listen_port"`
	Settings   json.RawMessage `json:"settings"`
	Remark     *string         `json:"remark"`
	Enabled    *bool           `json:"enabled"`
}

func (a *App) listInbounds(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var inbounds []model.Inbound
	if err := a.db.Where("server_id = ?", id).Order("id").Find(&inbounds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"inbounds": inbounds})
}

func inboundTag(typ, tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return typ + "-" + randHex(3)
	}
	return tag
}

func (a *App) createInbound(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
	if !a.requireManagedMode(c, id) {
		return
	}
	var req inboundReq
	if !bindJSON(c, &req) {
		return
	}
	if !supportedInboundTypes[req.Type] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported inbound type: " + req.Type})
		return
	}
	if req.Type != "tun" && (req.ListenPort <= 0 || req.ListenPort > 65535) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid listen_port"})
		return
	}
	if req.Type == "tun" && (req.ListenPort < 0 || req.ListenPort > 65535) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid listen_port"})
		return
	}

	var st singbox.InboundSettings
	if len(req.Settings) > 0 {
		if err := json.Unmarshal(req.Settings, &st); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid settings: " + err.Error()})
			return
		}
	}
	st.SyncFromRaw(req.Type)
	if err := st.ValidateMultiUser(req.Type); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := fillInboundSecrets(req.Type, &st); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	syncSecretsToRaw(req.Type, &st)
	// Reject bad settings here: an unusable inbound row would otherwise make
	// EVERY later config push for this server fail, silently.
	if err := st.Validate(req.Type); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	raw, _ := json.Marshal(st)

	tag := inboundTag(req.Type, req.Tag)
	if err := a.validateInboundConfiguration(a.db, id, 0, tag, req.Type, req.ListenPort, st); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	remark := ""
	if req.Remark != nil {
		remark = *req.Remark
	}
	ib := &model.Inbound{
		ServerID:   id,
		Type:       model.InboundType(req.Type),
		Tag:        tag,
		ListenPort: req.ListenPort,
		Settings:   model.JSONText(raw),
		Remark:     remark,
		Enabled:    enabled,
	}
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ib).Error; err != nil {
			return err
		}
		return rememberInboundTrafficTag(tx, ib)
	}); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errInboundTrafficTagReserved) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, id, gin.H{"inbound": ib})
}

func (a *App) updateInbound(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(sid)
	defer unlockOperation()
	if !a.requireManagedMode(c, sid) {
		return
	}
	iid, ok := uintParam(c, "inboundID")
	if !ok {
		return
	}
	var ib model.Inbound
	if err := a.db.Where("id = ? AND server_id = ?", iid, sid).First(&ib).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "inbound not found"})
		return
	}
	var req inboundReq
	if !bindJSON(c, &req) {
		return
	}
	nextTag := ib.Tag
	if req.Tag != "" {
		nextTag = strings.TrimSpace(req.Tag)
	}
	nextPort := ib.ListenPort
	if req.ListenPort != 0 {
		nextPort = req.ListenPort
	}
	if err := a.validateInboundIdentityForType(a.db, sid, ib.ID, nextTag, string(ib.Type), nextPort); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	oldTag := ib.Tag
	ib.Tag = nextTag
	ib.ListenPort = nextPort
	if req.Remark != nil {
		ib.Remark = *req.Remark
	}
	if req.Enabled != nil {
		if !*req.Enabled && ib.Enabled {
			// Disabling would revoke a credential another server routes through,
			// silently breaking that route. Refuse until the outbound is re-pointed.
			if n, err := outboundSourceReference(a.db, model.OutboundSourceInbound, ib.ID); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			} else if n > 0 {
				c.JSON(http.StatusConflict, gin.H{"error": "该节点仍被其它服务器的出站引用，请先修改或删除那些出站"})
				return
			}
		}
		ib.Enabled = *req.Enabled
	}
	if len(req.Settings) > 0 {
		var oldSettings singbox.InboundSettings
		if len(ib.Settings) > 0 {
			_ = json.Unmarshal(ib.Settings, &oldSettings)
		}
		var st singbox.InboundSettings
		if err := json.Unmarshal(req.Settings, &st); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid settings: " + err.Error()})
			return
		}
		st.SyncFromRaw(string(ib.Type))
		preserveInboundSecrets(&oldSettings, &st)
		if ib.Type == model.InboundSocks {
			// Omitted credentials mean keep; explicitly clearing both fields means
			// the administrator selected unauthenticated SOCKS.
			var credentials struct {
				Username *string `json:"username"`
				Password *string `json:"password"`
			}
			_ = json.Unmarshal(req.Settings, &credentials)
			if credentials.Username == nil {
				st.Username = oldSettings.Username
			}
			if credentials.Password != nil {
				st.Password = *credentials.Password
			}
		}
		// Preserve previously-generated secrets when the new payload omits them.
		if err := st.ValidateMultiUser(string(ib.Type)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := fillInboundSecrets(string(ib.Type), &st); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		syncSecretsToRaw(string(ib.Type), &st)
		if err := st.Validate(string(ib.Type)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		raw, _ := json.Marshal(st)
		ib.Settings = model.JSONText(raw)
	}
	var nextSettings singbox.InboundSettings
	if len(ib.Settings) > 0 {
		if err := json.Unmarshal(ib.Settings, &nextSettings); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid settings: " + err.Error()})
			return
		}
	}
	if err := nextSettings.ValidateMultiUser(string(ib.Type)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := a.validateInboundConfiguration(a.db, sid, ib.ID, ib.Tag, string(ib.Type), ib.ListenPort, nextSettings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	tx := a.db.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": tx.Error.Error()})
		return
	}
	for _, tag := range []string{oldTag, ib.Tag} {
		if err := rememberInboundTrafficTag(tx, &model.Inbound{ID: ib.ID, ServerID: sid, Tag: tag}); err != nil {
			tx.Rollback()
			status := http.StatusInternalServerError
			if errors.Is(err, errInboundTrafficTagReserved) {
				status = http.StatusBadRequest
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
	}
	if oldTag != ib.Tag {
		if err := renameInboundRuleRefs(tx, sid, oldTag, ib.Tag); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Save(&ib).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, sid, gin.H{"inbound": ib})
}

func (a *App) deleteInbound(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(sid)
	defer unlockOperation()
	iid, ok := uintParam(c, "inboundID")
	if !ok {
		return
	}
	if !a.requireManagedMode(c, sid) {
		return
	}
	var ib model.Inbound
	if err := a.db.Where("id = ? AND server_id = ?", iid, sid).First(&ib).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "inbound not found"})
		return
	}
	referenced, err := a.inboundReferenced(a.db, sid, ib.Tag)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if referenced {
		c.JSON(http.StatusConflict, gin.H{"error": "该入站仍被分流规则引用，请先修改或删除相关规则"})
		return
	}
	if n, err := outboundSourceReference(a.db, model.OutboundSourceInbound, ib.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	} else if n > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该节点仍被其它服务器的出站引用，请先修改或删除那些出站"})
		return
	}
	a.userAccessMu.Lock()
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := rememberInboundTrafficTag(tx, &ib); err != nil {
			return err
		}
		var forwards []model.UserForward
		if err := tx.Where("server_id = ? AND inbound_id = ?", sid, ib.ID).Find(&forwards).Error; err != nil {
			return err
		}
		if err := deleteForwardState(tx, forwards); err != nil {
			return err
		}
		if err := tx.Delete(&ib).Error; err != nil {
			return err
		}
		if err := deleteUserNodeOrderRefs(tx, userNodeTypeManaged, []uint{ib.ID}); err != nil {
			return err
		}
		return removeInboundUserAssignments(tx, ib.ID)
	})
	a.userAccessMu.Unlock()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, sid, gin.H{"ok": true})
}

// removeInboundUserAssignments removes an explicitly assigned inbound without
// turning an empty explicit list into an accidental whole-server grant.
func removeInboundUserAssignments(tx *gorm.DB, inboundID uint) error {
	var users []model.User
	if err := tx.Find(&users).Error; err != nil {
		return err
	}
	for i := range users {
		if !containsID(users[i].InboundIDs, inboundID) {
			continue
		}
		remaining := removeID(users[i].InboundIDs, inboundID)
		var inbounds []model.Inbound
		if len(remaining) > 0 {
			if err := tx.Where("id IN ?", remaining).Find(&inbounds).Error; err != nil {
				return err
			}
		}
		validInboundIDs := make([]uint, 0, len(inbounds))
		serverIDs := make([]uint, 0, len(inbounds))
		for _, inbound := range inbounds {
			validInboundIDs = append(validInboundIDs, inbound.ID)
			serverIDs = append(serverIDs, inbound.ServerID)
		}
		users[i].InboundIDs = normalizedIDs(validInboundIDs)
		users[i].ServerIDs = normalizedIDs(serverIDs)
		if err := tx.Model(&users[i]).Select("ServerIDs", "InboundIDs", "UpdatedAt").Updates(&users[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

func containsID(ids []uint, id uint) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// syncSecretsToRaw keeps s.Raw aligned with auto-generated/preserved credentials
// when the inbound carries a schema-edited raw JSON object.
func syncSecretsToRaw(typ string, s *singbox.InboundSettings) {
	if len(s.Raw) == 0 {
		return
	}
	if typ == "shadowsocks" && s.SSServerPSK != "" {
		s.Raw["password"] = s.SSServerPSK
	}
	if typ == "snell" && s.SnellPSK != "" {
		s.Raw["psk"] = s.SnellPSK
	}
	if s.TLS.Reality.Enabled {
		if tlsMap, ok := s.Raw["tls"].(map[string]any); ok {
			if realityMap, ok := tlsMap["reality"].(map[string]any); ok {
				if s.TLS.Reality.PrivateKey != "" {
					realityMap["private_key"] = s.TLS.Reality.PrivateKey
				}
				if len(s.TLS.Reality.ShortID) > 0 {
					realityMap["short_id"] = s.TLS.Reality.ShortID
				}
			}
		}
	}
}
