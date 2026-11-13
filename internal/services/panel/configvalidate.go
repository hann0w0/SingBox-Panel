package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

type outboundStoredSettings struct {
	Server     string                  `json:"server"`
	ServerPort int                     `json:"server_port"`
	UUID       string                  `json:"uuid"`
	Password   string                  `json:"password"`
	Username   string                  `json:"username"`
	Settings   singbox.InboundSettings `json:"settings"`
}

func (a *App) requireManagedMode(c *gin.Context, serverID uint) bool {
	var srv model.Server
	if err := a.db.Select("id", "config_mode", "raw_config").First(&srv, serverID).Error; err != nil {
		c.JSON(404, gin.H{"error": "server not found"})
		return false
	}
	if srv.EffectiveConfigMode() == model.ConfigModeRaw {
		c.JSON(409, gin.H{"error": "该节点当前使用原始配置模式；请先明确切换到「面板管理」再编辑协议或路由"})
		return false
	}
	return true
}

func validateTag(tag string) error {
	if strings.TrimSpace(tag) == "" {
		return fmt.Errorf("标签必填")
	}
	if strings.ContainsAny(tag, "\r\n\t") {
		return fmt.Errorf("标签不能包含换行或制表符")
	}
	return nil
}

func localTrafficPort() int {
	return addressPort(protocol.LocalTrafficAddress)
}

func isLoopbackOrUnspecifiedHost(raw string) bool {
	host := strings.TrimSuffix(strings.ToLower(strings.Trim(strings.TrimSpace(raw), "[]")), ".")
	if zoneAt := strings.LastIndexByte(host, '%'); zoneAt >= 0 {
		host = host[:zoneAt]
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

// addressPort returns the numeric port of a fixed host:port constant.
func addressPort(address string) int {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0
	}
	return port
}

func (a *App) validateInboundIdentity(db *gorm.DB, serverID, excludeID uint, tag string, port int) error {
	if err := validateTag(tag); err != nil {
		return err
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("监听端口必须在 1-65535 之间")
	}
	if port == localTrafficPort() || port == addressPort(protocol.LocalStatsAPIAddress) {
		return fmt.Errorf("监听端口 %d 已保留给节点流量统计服务", port)
	}
	var n int64
	q := db.Model(&model.Inbound{}).Where("server_id = ? AND tag = ?", serverID, tag)
	if excludeID != 0 {
		q = q.Where("id <> ?", excludeID)
	}
	if err := q.Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("入站标签 %q 已存在", tag)
	}
	if err := checkInboundTrafficTag(db, serverID, excludeID, tag); err != nil {
		return err
	}
	q = db.Model(&model.Inbound{}).Where("server_id = ? AND listen_port = ?", serverID, port)
	if excludeID != 0 {
		q = q.Where("id <> ?", excludeID)
	}
	if err := q.Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("监听端口 %d 已被该节点的其它入站使用", port)
	}
	return nil
}

// validateInboundConfiguration includes virtual guard listeners in identity
// checks. Disabled rows still reserve their ports and tags for later enabling.
func (a *App) validateInboundConfiguration(db *gorm.DB, serverID, excludeID uint, tag, typ string, port int, settings singbox.InboundSettings) error {
	if err := a.validateInboundIdentity(db, serverID, excludeID, tag, port); err != nil {
		return err
	}
	if settings.RealityGuardEnabled() {
		guardPort := settings.TLS.Reality.Guard.InternalPort
		if guardPort == localTrafficPort() || guardPort == addressPort(protocol.LocalStatsAPIAddress) {
			return fmt.Errorf("REALITY 内部端口 %d 已保留给节点流量统计服务", guardPort)
		}
		if err := validateTag(settings.RealityGuardTag(tag)); err != nil {
			return err
		}
	}
	var rows []model.Inbound
	if err := db.Where("server_id = ? AND id <> ?", serverID, excludeID).Find(&rows).Error; err != nil {
		return err
	}
	inputs := []singbox.InboundInput{{Tag: tag, Type: typ, ListenPort: port, Settings: settings}}
	for _, row := range rows {
		var other singbox.InboundSettings
		if len(row.Settings) > 0 {
			if err := json.Unmarshal(row.Settings, &other); err != nil {
				return fmt.Errorf("读取入站 %q 参数失败: %w", row.Tag, err)
			}
		}
		inputs = append(inputs, singbox.InboundInput{Tag: row.Tag, Type: string(row.Type), ListenPort: row.ListenPort, Settings: other})
	}
	return singbox.ValidateRealityGuardLayout(inputs)
}

func (a *App) validateOutbound(db *gorm.DB, serverID, excludeID uint, tag, typ string, ownerUserID uint, raw json.RawMessage) error {
	if err := validateTag(tag); err != nil {
		return err
	}
	if tag == "direct" || tag == "block" || tag == "reject" || tag == "sniff" || tag == "hijack-dns" {
		return fmt.Errorf("%s 是内置标签，不能作为自定义出站", tag)
	}
	if !supportedOutboundTypes[typ] || typ == "direct" {
		return fmt.Errorf("unsupported outbound type: %s", typ)
	}
	if ownerUserID != 0 {
		var cnt int64
		if err := db.Model(&model.User{}).Where("id = ? AND role = ?", ownerUserID, model.RoleUser).Count(&cnt).Error; err != nil {
			return err
		}
		if cnt == 0 {
			return fmt.Errorf("专属出站所属用户不存在")
		}
	}
	var n int64
	q := db.Model(&model.Outbound{}).Where("server_id = ? AND tag = ? AND owner_user_id = ?", serverID, tag, ownerUserID)
	if excludeID != 0 {
		q = q.Where("id <> ?", excludeID)
	}
	if err := q.Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("出站标签 %q 已存在", tag)
	}
	var st outboundStoredSettings
	if len(raw) == 0 || json.Unmarshal(raw, &st) != nil {
		return fmt.Errorf("出站参数不是合法 JSON")
	}
	if strings.TrimSpace(st.Server) == "" || st.ServerPort < 1 || st.ServerPort > 65535 {
		return fmt.Errorf("落地服务器和 1-65535 端口必填")
	}
	if isLoopbackOrUnspecifiedHost(st.Server) {
		return fmt.Errorf("落地服务器不能指向本机回环或未指定地址")
	}
	switch typ {
	case "vless", "vmess":
		if st.UUID == "" {
			return fmt.Errorf("%s UUID 必填", typ)
		}
	case "trojan", "hysteria2":
		if st.Password == "" {
			return fmt.Errorf("%s 密码必填", typ)
		}
	case "anytls":
		if st.Password == "" {
			return fmt.Errorf("anytls 密码必填")
		}
	case "snell":
		if st.Password == "" && st.Settings.SnellPSK == "" {
			return fmt.Errorf("snell PSK 必填")
		}
		// The panel stores the outbound PSK in the top-level password field for
		// compatibility with all other password-based outbounds. Mirror it into
		// the protocol settings only for validation so v6 checks the effective
		// credential without changing the stored shape.
		if st.Settings.SnellPSK == "" {
			st.Settings.SnellPSK = st.Password
		}
	case "tuic":
		if st.UUID == "" || st.Password == "" {
			return fmt.Errorf("tuic UUID 和密码必填")
		}
	case "shadowsocks":
		if st.Settings.Method == "" || (st.Password == "" && st.Settings.SSServerPSK == "") {
			return fmt.Errorf("shadowsocks 加密方式和密码必填")
		}
	case "socks":
		if (strings.TrimSpace(st.Username) == "") != (st.Password == "") {
			return fmt.Errorf("socks 用户名和密码必须同时填写或同时留空")
		}
	}
	if err := st.Settings.ValidateClientOutbound(typ); err != nil {
		return err
	}
	return nil
}

var builtinOutboundTags = map[string]bool{
	"direct": true, "block": true, "reject": true, "sniff": true, "hijack-dns": true,
}

// outboundExists checks the server's shared outbounds. User-private exits are
// resolved separately in validateRule because their owner identity scopes them.
func (a *App) outboundExists(db *gorm.DB, serverID uint, tag string) (bool, error) {
	switch tag {
	case "direct", "block", "reject", "sniff", "hijack-dns":
		return true, nil
	}
	var n int64
	err := db.Model(&model.Outbound{}).
		Where("server_id = ? AND tag = ? AND owner_user_id = 0", serverID, tag).Count(&n).Error
	return n > 0, err
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func (a *App) validateRule(db *gorm.DB, serverID uint, raw json.RawMessage, outbound string) error {
	if strings.TrimSpace(outbound) == "" {
		return fmt.Errorf("目标出站必填")
	}
	ok, err := a.outboundExists(db, serverID, outbound)
	if err != nil {
		return err
	}
	var rule singbox.RuleInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rule); err != nil {
			return fmt.Errorf("规则匹配条件不是合法 JSON: %w", err)
		}
	}
	// A private exit is scoped to its owner. A hand-written rule that targets
	// it must also carry exactly that account's auth_user identity; otherwise
	// one user's home broadband could be used by other accounts.
	var privateOutbound model.Outbound
	privateErr := db.Where("tag = ? AND owner_user_id <> 0", outbound).First(&privateOutbound).Error
	if privateErr == nil {
		if ok {
			return fmt.Errorf("出站标签 %q 同时属于公共出站和专属出站，请先重命名", outbound)
		}
		if privateOutbound.ServerID != 0 && privateOutbound.ServerID != serverID {
			return fmt.Errorf("专属出站 %q 绑定在其它入口服务器，无法在此使用", outbound)
		}
		var owner model.User
		if err := db.Select("id", "email", "proxy_token", "sub_token").First(&owner, privateOutbound.OwnerUserID).Error; err != nil {
			return fmt.Errorf("专属出站的所属用户不存在")
		}
		expected := proxyTrafficName(&owner)
		if len(rule.AuthUser) != 1 || rule.AuthUser[0] != expected {
			return fmt.Errorf("专属出站 %q 只能由所属用户的身份规则使用", outbound)
		}
	} else if !errors.Is(privateErr, gorm.ErrRecordNotFound) {
		return privateErr
	} else if !ok {
		return fmt.Errorf("目标出站 %q 不存在", outbound)
	}
	action := rule.Action
	if action == "" {
		switch outbound {
		case "block", "reject":
			action = "reject"
		case "sniff", "hijack-dns":
			action = outbound
		default:
			action = "route"
		}
	}
	switch action {
	case "route":
		if outbound == "block" || outbound == "reject" || outbound == "sniff" || outbound == "hijack-dns" {
			return fmt.Errorf("路由动作必须选择可用出站")
		}
	case "reject":
		if outbound != "block" && outbound != "reject" {
			return fmt.Errorf("拒绝动作的目标出站必须为 block")
		}
		if rule.Method != "" && rule.Method != "default" && rule.Method != "drop" {
			return fmt.Errorf("拒绝方式只能是 default 或 drop")
		}
	case "sniff", "hijack-dns":
		if outbound != action {
			return fmt.Errorf("%s 动作与目标出站不一致", action)
		}
	default:
		return fmt.Errorf("不支持的规则动作 %q", action)
	}
	if rule.Network != "" && rule.Network != "tcp" && rule.Network != "udp" && rule.Network != "icmp" {
		return fmt.Errorf("network 只能是 tcp、udp 或 icmp")
	}
	if rule.RuleSetIPCIDRMatchSource && len(rule.RuleSet) == 0 {
		return fmt.Errorf("规则集匹配来源 IP 时必须选择规则集")
	}
	for _, port := range rule.Port {
		if port < 1 || port > 65535 {
			return fmt.Errorf("规则端口必须在 1-65535 之间")
		}
	}
	var inbounds []string
	if err := db.Model(&model.Inbound{}).Where("server_id = ?", serverID).Pluck("tag", &inbounds).Error; err != nil {
		return err
	}
	inboundSet := stringSet(inbounds)
	for _, tag := range rule.Inbound {
		if !inboundSet[tag] {
			return fmt.Errorf("规则引用的入站 %q 不存在", tag)
		}
	}
	ruleSets, err := assignedRuleSetTags(db, serverID)
	if err != nil {
		return err
	}
	ruleSetSet := stringSet(ruleSets)
	for _, tag := range rule.RuleSet {
		if !ruleSetSet[tag] {
			return fmt.Errorf("规则引用的规则集 %q 不存在或未分配给该服务器", tag)
		}
	}
	return nil
}

// referencedRuleSetIDs resolves the shared rule-set tags in one rule payload.
// Used by the rule API to attach a definition to the server at save time, so
// the administrator never has to visit a separate assignment screen.
func referencedRuleSetIDs(db *gorm.DB, raw json.RawMessage) ([]uint, error) {
	var rule singbox.RuleInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rule); err != nil {
			return nil, err
		}
	}
	if len(rule.RuleSet) == 0 {
		return nil, nil
	}
	var rows []model.RuleSet
	if err := db.Where("tag IN ?", rule.RuleSet).Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

func attachRuleSetsToServer(db *gorm.DB, serverID uint, raw json.RawMessage) error {
	ids, err := referencedRuleSetIDs(db, raw)
	if err != nil {
		return err
	}
	for _, ruleSetID := range ids {
		assignment := model.ServerRuleSet{ServerID: serverID, RuleSetID: ruleSetID}
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&assignment).Error; err != nil {
			return err
		}
	}
	return nil
}

// syncServerRuleSets makes the server's rule-set assignments exactly match the
// rule sets referenced by its saved route rules. Assignments are an implicit
// consequence of rule references: when the last referencing rule is deleted or
// changed, the now-unused definition must leave the server config as well.
func syncServerRuleSets(db *gorm.DB, serverID uint) error {
	referenced, err := referencedRuleSetIDsForServer(db, serverID)
	if err != nil {
		return err
	}
	if len(referenced) == 0 {
		return db.Where("server_id = ?", serverID).Delete(&model.ServerRuleSet{}).Error
	}
	return db.Where("server_id = ? AND rule_set_id NOT IN ?", serverID, referenced).
		Delete(&model.ServerRuleSet{}).Error
}

// referencedRuleSetIDsForServer resolves tags from every saved rule on one
// server. A tag that has no definition yet is ignored (rule validation reports
// it separately); the caller only needs IDs of real shared definitions.
func referencedRuleSetIDsForServer(db *gorm.DB, serverID uint) ([]uint, error) {
	var rows []model.RouteRule
	if err := db.Where("server_id = ?", serverID).Find(&rows).Error; err != nil {
		return nil, err
	}
	tags := make(map[string]struct{})
	for _, row := range rows {
		var rule singbox.RuleInput
		if len(row.Match) == 0 || json.Unmarshal(row.Match, &rule) != nil {
			continue
		}
		for _, tag := range rule.RuleSet {
			tags[tag] = struct{}{}
		}
	}
	if len(tags) == 0 {
		return nil, nil
	}
	list := make([]string, 0, len(tags))
	for tag := range tags {
		list = append(list, tag)
	}
	var ids []uint
	if err := db.Model(&model.RuleSet{}).Where("tag IN ?", list).Order("id").Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// assignedRuleSetTags lists only shared rule sets assigned to this server, so
// one server's route rules cannot reference another node's assignment.
func assignedRuleSetTags(db *gorm.DB, serverID uint) ([]string, error) {
	var tags []string
	err := db.Table("server_rule_sets").
		Joins("JOIN rule_sets ON rule_sets.id = server_rule_sets.rule_set_id").
		Where("server_rule_sets.server_id = ?", serverID).
		Pluck("rule_sets.tag", &tags).Error
	return tags, err
}

func replaceString(values []string, old, next string) ([]string, bool) {
	changed := false
	for i, value := range values {
		if value == old {
			values[i] = next
			changed = true
		}
	}
	return values, changed
}

func renameInboundRuleRefs(tx *gorm.DB, serverID uint, old, next string) error {
	var rows []model.RouteRule
	if err := tx.Where("server_id = ?", serverID).Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		var rule singbox.RuleInput
		if len(rows[i].Match) == 0 || json.Unmarshal(rows[i].Match, &rule) != nil {
			continue
		}
		var changed bool
		rule.Inbound, changed = replaceString(rule.Inbound, old, next)
		if !changed {
			continue
		}
		raw, err := json.Marshal(rule)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.RouteRule{}).Where("id = ?", rows[i].ID).Update("match", model.JSONText(raw)).Error; err != nil {
			return err
		}
	}
	return nil
}

func renameRuleSetRefs(tx *gorm.DB, serverID uint, old, next string) error {
	var rows []model.RouteRule
	if err := tx.Where("server_id = ?", serverID).Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		var rule singbox.RuleInput
		if len(rows[i].Match) == 0 || json.Unmarshal(rows[i].Match, &rule) != nil {
			continue
		}
		var changed bool
		rule.RuleSet, changed = replaceString(rule.RuleSet, old, next)
		if !changed {
			continue
		}
		raw, err := json.Marshal(rule)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.RouteRule{}).Where("id = ?", rows[i].ID).Update("match", model.JSONText(raw)).Error; err != nil {
			return err
		}
	}
	return nil
}

func (a *App) inboundReferenced(db *gorm.DB, serverID uint, tag string) (bool, error) {
	var rows []model.RouteRule
	if err := db.Where("server_id = ?", serverID).Find(&rows).Error; err != nil {
		return false, err
	}
	// Derived forward rules target the inbound being deleted and are removed
	// together with their forward rows. They must not by themselves block the
	// deletion, or the inbound could never be removed while a user forward exists.
	var forwards []model.UserForward
	if err := db.Where("server_id = ?", serverID).Find(&forwards).Error; err != nil {
		return false, err
	}
	forwardNames := make(map[string]bool, len(forwards))
	if len(forwards) > 0 {
		var users []model.User
		userIDs := make([]uint, 0, len(forwards))
		for _, forward := range forwards {
			userIDs = append(userIDs, forward.UserID)
		}
		if err := db.Where("id IN ?", normalizedIDs(userIDs)).Find(&users).Error; err != nil {
			return false, err
		}
		byID := make(map[uint]model.User, len(users))
		for _, user := range users {
			byID[user.ID] = user
		}
		for _, forward := range forwards {
			if user, ok := byID[forward.UserID]; ok {
				forwardNames[forwardTrafficName(&user, forward.ID)] = true
			}
		}
	}
	for _, row := range rows {
		var rule singbox.RuleInput
		if json.Unmarshal(row.Match, &rule) == nil {
			if len(rule.AuthUser) == 1 && forwardNames[rule.AuthUser[0]] && len(rule.Inbound) == 1 && rule.Inbound[0] == tag {
				continue
			}
			for _, value := range rule.Inbound {
				if value == tag {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// sharedRuleSetReferencedAnywhere reports whether any server's route rule still
// names this tag. Deleting a shared definition must not silently break a rule
// on a server that happens to use the same tag.
func sharedRuleSetReferencedAnywhere(db *gorm.DB, tag string) (bool, error) {
	var rows []model.RouteRule
	if err := db.Find(&rows).Error; err != nil {
		return false, err
	}
	for _, row := range rows {
		var rule singbox.RuleInput
		if json.Unmarshal(row.Match, &rule) == nil {
			for _, value := range rule.RuleSet {
				if value == tag {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func renameSharedRuleSetRefs(tx *gorm.DB, old, next string) error {
	var rows []model.RouteRule
	if err := tx.Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		var rule singbox.RuleInput
		if len(rows[i].Match) == 0 || json.Unmarshal(rows[i].Match, &rule) != nil {
			continue
		}
		var changed bool
		rule.RuleSet, changed = replaceString(rule.RuleSet, old, next)
		if !changed {
			continue
		}
		raw, err := json.Marshal(rule)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.RouteRule{}).Where("id = ?", rows[i].ID).Update("match", model.JSONText(raw)).Error; err != nil {
			return err
		}
	}
	return nil
}

// preserveInboundSecrets merges credentials that partial API clients commonly
// omit. The UI sends full settings, but an omitted UUID/key must never rotate a
// live protocol unexpectedly.
func preserveInboundSecrets(old, next *singbox.InboundSettings) {
	if next.UUID == "" {
		next.UUID = old.UUID
	}
	if next.Password == "" {
		next.Password = old.Password
	}
	if next.SSServerPSK == "" {
		next.SSServerPSK = old.SSServerPSK
	}
	if next.SnellPSK == "" {
		next.SnellPSK = old.SnellPSK
	}
	if next.ShadowTLSPassword == "" {
		next.ShadowTLSPassword = old.ShadowTLSPassword
	}
	if next.TLS.Reality.Enabled && old.TLS.Reality.Enabled {
		if next.TLS.Reality.PrivateKey == "" {
			next.TLS.Reality.PrivateKey = old.TLS.Reality.PrivateKey
		}
		if next.TLS.Reality.PublicKey == "" {
			next.TLS.Reality.PublicKey = old.TLS.Reality.PublicKey
		}
		if len(next.TLS.Reality.ShortID) == 0 {
			next.TLS.Reality.ShortID = old.TLS.Reality.ShortID
		}
	}
	// Omitted inline PEM means "keep" only while staying in the same certificate
	// mode. Restoring it after a switch to REALITY, ACME or paths creates mutually
	// exclusive TLS sources and prevents the new settings from being saved.
	inlineMode := (next.TLS.Enabled || next.TLS.SelfSigned) &&
		!next.TLS.Reality.Enabled && next.TLS.ACMEDomain == "" &&
		next.TLS.CertificatePath == "" && next.TLS.KeyPath == "" &&
		next.TLS.SelfSigned == old.TLS.SelfSigned
	if inlineMode {
		if next.TLS.Certificate == "" {
			next.TLS.Certificate = old.TLS.Certificate
		}
		if next.TLS.Key == "" {
			next.TLS.Key = old.TLS.Key
		}
	}
}
