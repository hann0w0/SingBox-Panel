package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

var supportedOutboundTypes = map[string]bool{
	"direct": true, "vless": true, "vmess": true, "trojan": true,
	"shadowsocks": true, "hysteria2": true, "tuic": true, "socks": true,
	"anytls": true, "snell": true,
}

// ---------- outbounds ----------

func (a *App) listOutbounds(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var rows []model.Outbound
	if err := a.db.Where("server_id = ?", id).Order("sort").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"outbounds": rows})
}

type outboundReq struct {
	Tag      string          `json:"tag"`
	Type     string          `json:"type"`
	Settings json.RawMessage `json:"settings"`
	Remark   *string         `json:"remark"`
	Sort     *int            `json:"sort"`
	// Set only when imported from a panel node; the config build then renders
	// this outbound from that node instead of the stored snapshot.
	SourceKind string `json:"source_kind,omitempty"`
	SourceID   uint   `json:"source_id,omitempty"`
}

func (a *App) createOutbound(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
	if !a.requireManagedMode(c, id) {
		return
	}
	var req outboundReq
	if !bindJSON(c, &req) {
		return
	}
	req.Tag = strings.TrimSpace(req.Tag)
	if req.Tag == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标签必填"})
		return
	}
	if req.Tag == "direct" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "direct 为内置出站，无需添加"})
		return
	}
	if !supportedOutboundTypes[req.Type] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported outbound type: " + req.Type})
		return
	}
	sourceKind, sourceID, err := a.outboundSourceLink(id, req.SourceKind, req.SourceID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := a.validateOutbound(a.db, id, 0, req.Tag, req.Type, req.Settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	remark := ""
	if req.Remark != nil {
		remark = *req.Remark
	}
	sort := 0
	if req.Sort != nil {
		sort = *req.Sort
	}
	ob := &model.Outbound{
		ServerID: id, Tag: req.Tag, Type: req.Type,
		Settings: model.JSONText(req.Settings), Remark: remark, Sort: sort,
		SourceKind: sourceKind, SourceID: sourceID,
	}
	if err := a.db.Create(ob).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ob.Referenced() {
		a.pushRoutingSourceChanges(ob)
	}
	a.applyConfigAndRespond(c, id, gin.H{"outbound": ob})
}

func (a *App) updateOutbound(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(sid)
	defer unlockOperation()
	if !a.requireManagedMode(c, sid) {
		return
	}
	oid, ok := uintParam(c, "outboundID")
	if !ok {
		return
	}
	var ob model.Outbound
	if err := a.db.Where("id = ? AND server_id = ?", oid, sid).First(&ob).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "outbound not found"})
		return
	}
	var req outboundReq
	if !bindJSON(c, &req) {
		return
	}
	nextTag := ob.Tag
	if req.Tag != "" {
		nextTag = strings.TrimSpace(req.Tag)
	}
	nextType := ob.Type
	if req.Type != "" {
		nextType = req.Type
	}
	nextSettings := json.RawMessage(ob.Settings)
	if len(req.Settings) > 0 {
		nextSettings = req.Settings
	}
	// Editing a node-imported outbound through the manual form freezes it: the
	// edited parameters are the administrator's, so it stops following the node.
	previous := ob
	if req.SourceKind == "" && req.SourceID == 0 {
		ob.SourceKind, ob.SourceID = "", 0
	}
	if err := a.validateOutbound(a.db, sid, ob.ID, nextTag, nextType, nextSettings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	oldTag := ob.Tag
	ob.Tag = nextTag
	ob.Type = nextType
	ob.Settings = model.JSONText(nextSettings)
	if req.Remark != nil {
		ob.Remark = *req.Remark
	}
	if req.Sort != nil {
		ob.Sort = *req.Sort
	}
	tx := a.db.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": tx.Error.Error()})
		return
	}
	if oldTag != ob.Tag {
		if err := tx.Model(&model.RouteRule{}).Where("server_id = ? AND outbound = ?", sid, oldTag).Update("outbound", ob.Tag).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if err := tx.Model(&model.Server{}).Where("id = ? AND final_outbound = ?", sid, oldTag).Update("final_outbound", ob.Tag).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if err := tx.Model(&model.ServerRuleSet{}).Where("server_id = ? AND download_detour = ?", sid, oldTag).Update("download_detour", ob.Tag).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Save(&ob).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Losing or changing the source revokes the previous source server's
	// credential, so rebuild that config as well.
	if previous.Referenced() && (previous.SourceKind != ob.SourceKind || previous.SourceID != ob.SourceID) {
		a.pushRoutingSourceChanges(&previous)
	}
	if ob.Referenced() {
		a.pushRoutingSourceChanges(&ob)
	}
	a.applyConfigAndRespond(c, sid, gin.H{"outbound": ob})
}

func (a *App) deleteOutbound(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(sid)
	defer unlockOperation()
	oid, ok := uintParam(c, "outboundID")
	if !ok {
		return
	}
	if !a.requireManagedMode(c, sid) {
		return
	}
	var ob model.Outbound
	if err := a.db.Where("id = ? AND server_id = ?", oid, sid).First(&ob).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "outbound not found"})
		return
	}
	var ruleCount int64
	if err := a.db.Model(&model.RouteRule{}).Where("server_id = ? AND outbound = ?", sid, ob.Tag).Count(&ruleCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var finalCount int64
	if err := a.db.Model(&model.Server{}).Where("id = ? AND final_outbound = ?", sid, ob.Tag).Count(&finalCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var ruleSetCount int64
	if err := a.db.Model(&model.ServerRuleSet{}).Where("server_id = ? AND download_detour = ?", sid, ob.Tag).Count(&ruleSetCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ruleCount > 0 || finalCount > 0 || ruleSetCount > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该出站仍被分流规则、final 或规则集下载引用，请先修改引用"})
		return
	}
	if err := a.db.Delete(&ob).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ob.Referenced() {
		a.pushRoutingSourceChanges(&ob)
	}
	a.applyConfigAndRespond(c, sid, gin.H{"ok": true})
}

// testOutbound asks the node itself to open a TCP connection to this landing
// target, so the admin can tell "the landing is down" apart from "the panel
// can't reach it" — the check runs from the node, not from the panel.
func (a *App) testOutbound(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	oid, ok := uintParam(c, "outboundID")
	if !ok {
		return
	}
	var ob model.Outbound
	if err := a.db.Where("id = ? AND server_id = ?", oid, sid).First(&ob).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "outbound not found"})
		return
	}
	var st outboundStoredSettings
	if len(ob.Settings) > 0 {
		_ = json.Unmarshal(ob.Settings, &st)
	}
	if st.Server == "" || st.ServerPort == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该出站没有落地地址或端口"})
		return
	}
	if isLoopbackOrUnspecifiedHost(st.Server) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "落地服务器不能指向本机回环或未指定地址"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	res, err := a.hub.SendCommand(ctx, sid, protocol.CmdTestOutbound, outboundTestCommand(ob.Type, st))
	if err != nil {
		if strings.Contains(err.Error(), "unknown command") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "该节点的 Agent 版本过旧，请先点「升级 Agent」"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var data protocol.TestOutboundData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解析测试结果失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok": data.OK, "latency_ms": data.LatencyMS, "error": data.Error,
		"inconclusive": data.Inconclusive, "note": data.Note,
		"target": fmt.Sprintf("%s:%d", st.Server, st.ServerPort),
	})
}

// outboundTestCommand picks the probe that matches the landing's transport.
// hysteria2 and tuic run over QUIC and never listen on TCP, so a TCP connect
// would report every healthy QUIC landing as down.
func outboundTestCommand(typ string, st outboundStoredSettings) protocol.TestOutboundCmd {
	cmd := protocol.TestOutboundCmd{Host: st.Server, Port: st.ServerPort}
	switch typ {
	case "hysteria2", "hysteria", "tuic":
		cmd.Network = "quic"
		// salamander/gecko wrap every datagram; without the key the server
		// stays silent. ObfsPassword alone has meant salamander historically.
		cmd.Obfuscated = typ != "tuic" && (st.Settings.ObfsType != "" || st.Settings.ObfsPassword != "")
	}
	return cmd
}

// ---------- route rules ----------

func (a *App) listRules(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var rows []model.RouteRule
	if err := a.db.Where("server_id = ?", id).Order("sort").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rules": rows})
}

type ruleReq struct {
	// Sort/Remark are pointers so an update that omits them keeps the stored
	// value instead of resetting the rule's position in the match chain.
	Sort     *int            `json:"sort"`
	Match    json.RawMessage `json:"match"`
	Outbound string          `json:"outbound"`
	Remark   *string         `json:"remark"`
	Enabled  *bool           `json:"enabled"`
}

func ruleHasMatch(r singbox.RuleInput) bool {
	return len(r.Inbound) > 0 || len(r.Domain) > 0 || len(r.DomainSuffix) > 0 ||
		len(r.DomainKeyword) > 0 || len(r.IPCIDR) > 0 || len(r.SourceIPCIDR) > 0 ||
		len(r.Port) > 0 || len(r.Protocol) > 0 || r.Network != "" || len(r.RuleSet) > 0
}

func (a *App) createRule(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
	if !a.requireManagedMode(c, id) {
		return
	}
	var req ruleReq
	if !bindJSON(c, &req) {
		return
	}
	if strings.TrimSpace(req.Outbound) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "目标出站必填"})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	r := &model.RouteRule{
		ServerID: id, Match: model.JSONText(req.Match),
		Outbound: strings.TrimSpace(req.Outbound), Enabled: enabled,
	}
	if req.Sort != nil {
		r.Sort = *req.Sort
	} else {
		var ri singbox.RuleInput
		_ = json.Unmarshal(req.Match, &ri)
		isTargeted := ruleHasMatch(ri)

		if isTargeted {
			// 精细规则/规则集默认置顶（最高优先级）
			var minSort struct{ Min int }
			a.db.Model(&model.RouteRule{}).Where("server_id = ?", id).Select("COALESCE(MIN(sort), 0) as min").Scan(&minSort)
			r.Sort = minSort.Min - 10
		} else {
			var maxSort struct{ Max int }
			a.db.Model(&model.RouteRule{}).Where("server_id = ?", id).Select("COALESCE(MAX(sort), 0) as max").Scan(&maxSort)
			r.Sort = maxSort.Max + 10
		}
	}
	if req.Remark != nil {
		r.Remark = *req.Remark
	}
	tx := a.db.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": tx.Error.Error()})
		return
	}
	if err := attachRuleSetsToServer(tx, id, req.Match); err != nil {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := a.validateRule(tx, id, req.Match, strings.TrimSpace(req.Outbound)); err != nil {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Create(r).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := syncServerRuleSets(tx, id); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, id, gin.H{"rule": r})
}

func (a *App) updateRule(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(sid)
	defer unlockOperation()
	if !a.requireManagedMode(c, sid) {
		return
	}
	rid, ok := uintParam(c, "ruleID")
	if !ok {
		return
	}
	var r model.RouteRule
	if err := a.db.Where("id = ? AND server_id = ?", rid, sid).First(&r).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "rule not found"})
		return
	}
	var req ruleReq
	if !bindJSON(c, &req) {
		return
	}
	nextMatch := json.RawMessage(r.Match)
	if len(req.Match) > 0 {
		nextMatch = req.Match
	}
	nextOutbound := r.Outbound
	if strings.TrimSpace(req.Outbound) != "" {
		nextOutbound = strings.TrimSpace(req.Outbound)
	}
	if req.Sort != nil {
		r.Sort = *req.Sort
	}
	r.Match = model.JSONText(nextMatch)
	r.Outbound = nextOutbound
	if req.Remark != nil {
		r.Remark = *req.Remark
	}
	if req.Enabled != nil {
		r.Enabled = *req.Enabled
	}
	tx := a.db.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": tx.Error.Error()})
		return
	}
	if err := attachRuleSetsToServer(tx, sid, nextMatch); err != nil {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := a.validateRule(tx, sid, nextMatch, nextOutbound); err != nil {
		tx.Rollback()
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Save(&r).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := syncServerRuleSets(tx, sid); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, sid, gin.H{"rule": r})
}

func (a *App) deleteRule(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(sid)
	defer unlockOperation()
	rid, ok := uintParam(c, "ruleID")
	if !ok {
		return
	}
	if !a.requireManagedMode(c, sid) {
		return
	}
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ? AND server_id = ?", rid, sid).Delete(&model.RouteRule{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return syncServerRuleSets(tx, sid)
	}); err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "rule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, sid, gin.H{"ok": true})
}

type reorderRulesReq struct {
	Order []uint `json:"order"`
}

func (a *App) reorderRules(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(sid)
	defer unlockOperation()
	if !a.requireManagedMode(c, sid) {
		return
	}
	var req reorderRulesReq
	if !bindJSON(c, &req) {
		return
	}
	if rejectTooManyIDs(c, req.Order) {
		return
	}
	var existing []uint
	if err := a.db.Model(&model.RouteRule{}).Where("server_id = ?", sid).Pluck("id", &existing).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	allowed := make(map[uint]bool, len(existing))
	for _, id := range existing {
		allowed[id] = true
	}
	seen := make(map[uint]bool, len(req.Order))
	if len(req.Order) != len(existing) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "排序列表必须包含该节点的全部规则"})
		return
	}
	for _, id := range req.Order {
		if !allowed[id] || seen[id] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "排序列表包含无效或重复的规则"})
			return
		}
		seen[id] = true
	}
	tx := a.db.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": tx.Error.Error()})
		return
	}
	for i, ruleID := range req.Order {
		if err := tx.Model(&model.RouteRule{}).Where("id = ? AND server_id = ?", ruleID, sid).Update("sort", i*10).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, sid, gin.H{"ok": true})
}

// ---------- rule-sets ----------

// Rule sets are panel-level definitions. Servers are attached automatically
// when a route rule references one, so there is no per-server rule-set screen.

func (a *App) listSharedRuleSets(c *gin.Context) {
	var rows []model.RuleSet
	if err := a.db.Order("id").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	references, err := ruleSetServerReferences(a.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	type row struct {
		model.RuleSet
		ServerIDs []uint `json:"server_ids"`
	}
	out := make([]row, 0, len(rows))
	for _, rs := range rows {
		out = append(out, row{RuleSet: rs, ServerIDs: references[rs.Tag]})
	}
	c.JSON(http.StatusOK, gin.H{"rulesets": out})
}

// ruleSetServerReferences derives which servers actually use each shared
// rule-set from their saved route rules. Assignments are not an independent
// editor state: a rule-set is shown on a server only while a rule references
// it. This also keeps the list truthful for data written before automatic
// assignment cleanup existed.
func ruleSetServerReferences(db *gorm.DB) (map[string][]uint, error) {
	var rows []model.RouteRule
	if err := db.Select("server_id", "match").Find(&rows).Error; err != nil {
		return nil, err
	}
	references := make(map[string]map[uint]struct{})
	for _, row := range rows {
		var rule singbox.RuleInput
		if len(row.Match) == 0 || json.Unmarshal(row.Match, &rule) != nil {
			continue
		}
		for _, tag := range rule.RuleSet {
			if references[tag] == nil {
				references[tag] = make(map[uint]struct{})
			}
			references[tag][row.ServerID] = struct{}{}
		}
	}
	out := make(map[string][]uint, len(references))
	for tag, ids := range references {
		list := make([]uint, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		out[tag] = list
	}
	return out, nil
}

type ruleSetReq struct {
	Tag            string `json:"tag"`
	Type           string `json:"type"`
	Format         string `json:"format"`
	URL            string `json:"url"`
	Path           string `json:"path"`
	UpdateInterval string `json:"update_interval"`
}

func normalizeRuleSetReq(req *ruleSetReq) error {
	req.Tag = strings.TrimSpace(req.Tag)
	req.Type = strings.TrimSpace(req.Type)
	req.Format = strings.TrimSpace(req.Format)
	req.URL = strings.TrimSpace(req.URL)
	req.Path = strings.TrimSpace(req.Path)
	req.UpdateInterval = strings.TrimSpace(req.UpdateInterval)
	if err := validateTag(req.Tag); err != nil {
		return err
	}
	if req.Type == "" {
		req.Type = "remote"
	}
	if req.Type != "remote" && req.Type != "local" {
		return fmt.Errorf("规则集类型只能是 remote 或 local")
	}
	if req.Format != "source" {
		req.Format = "binary"
	}
	if req.Type == "local" {
		if req.Path == "" {
			return fmt.Errorf("本地规则集路径必填")
		}
		req.URL = ""
		req.UpdateInterval = ""
		return nil
	}
	if req.URL == "" {
		return fmt.Errorf("远程规则集 URL 必填")
	}
	req.Path = ""
	return nil
}

func (a *App) createSharedRuleSet(c *gin.Context) {
	var req ruleSetReq
	if !bindJSON(c, &req) {
		return
	}
	if err := normalizeRuleSetReq(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var duplicate int64
	if err := a.db.Model(&model.RuleSet{}).Where("tag = ?", req.Tag).Count(&duplicate).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if duplicate > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "规则集标签已存在"})
		return
	}
	rs := &model.RuleSet{Tag: req.Tag, Type: req.Type, Format: req.Format, URL: req.URL, Path: req.Path, UpdateInterval: req.UpdateInterval}
	if err := a.db.Create(rs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ruleset": rs})
}

func (a *App) updateSharedRuleSet(c *gin.Context) {
	rsid, ok := uintParam(c, "rulesetID")
	if !ok {
		return
	}
	var rs model.RuleSet
	if err := a.db.First(&rs, rsid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ruleset not found"})
		return
	}
	var req ruleSetReq
	if !bindJSON(c, &req) {
		return
	}
	if err := normalizeRuleSetReq(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var duplicate int64
	if err := a.db.Model(&model.RuleSet{}).Where("tag = ? AND id <> ?", req.Tag, rs.ID).Count(&duplicate).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if duplicate > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "规则集标签已存在"})
		return
	}
	oldTag := rs.Tag
	tx := a.db.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": tx.Error.Error()})
		return
	}
	if oldTag != req.Tag {
		if err := renameSharedRuleSetRefs(tx, oldTag, req.Tag); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Model(&rs).Updates(map[string]any{
		"tag": req.Tag, "type": req.Type, "format": req.Format, "url": req.URL, "path": req.Path, "update_interval": req.UpdateInterval,
	}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ruleset": rs})
}

func (a *App) deleteSharedRuleSet(c *gin.Context) {
	rsid, ok := uintParam(c, "rulesetID")
	if !ok {
		return
	}
	var rs model.RuleSet
	if err := a.db.First(&rs, rsid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ruleset not found"})
		return
	}
	referenced, err := sharedRuleSetReferencedAnywhere(a.db, rs.Tag)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if referenced {
		c.JSON(http.StatusConflict, gin.H{"error": "该规则集仍被分流规则引用，请先修改相关规则"})
		return
	}
	var assignments []model.ServerRuleSet
	if err := a.db.Where("rule_set_id = ?", rs.ID).Find(&assignments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("rule_set_id = ?", rs.ID).Delete(&model.ServerRuleSet{}).Error; err != nil {
			return err
		}
		return tx.Delete(&rs).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, assignment := range assignments {
		a.orch.PushConfigIfManaged(assignment.ServerID)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------- final outbound ----------

type finalReq struct {
	Outbound string `json:"outbound"`
}

func (a *App) setFinalOutbound(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlockOperation := a.lockServerOperation(id)
	defer unlockOperation()
	if !a.requireManagedMode(c, id) {
		return
	}
	var req finalReq
	if !bindJSON(c, &req) {
		return
	}
	final := strings.TrimSpace(req.Outbound)
	if final == "" {
		final = "direct"
	}
	exists, err := a.outboundExists(a.db, id, final)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !exists || final == "block" || final == "reject" || final == "sniff" || final == "hijack-dns" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "final 出站不存在或不能作为默认出站"})
		return
	}
	if err := a.db.Model(&model.Server{}).Where("id = ?", id).Update("final_outbound", final).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.applyConfigAndRespond(c, id, gin.H{"ok": true, "final_outbound": final})
}
