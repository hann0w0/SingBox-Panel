package panel

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/singbox"
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

func (a *App) validateInboundIdentity(db *gorm.DB, serverID, excludeID uint, tag string, port int) error {
	if err := validateTag(tag); err != nil {
		return err
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("监听端口必须在 1-65535 之间")
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

func (a *App) validateOutbound(db *gorm.DB, serverID, excludeID uint, tag, typ string, raw json.RawMessage) error {
	if err := validateTag(tag); err != nil {
		return err
	}
	if tag == "direct" || tag == "block" || tag == "reject" || tag == "sniff" || tag == "hijack-dns" {
		return fmt.Errorf("%s 是内置标签，不能作为自定义出站", tag)
	}
	if !supportedOutboundTypes[typ] || typ == "direct" {
		return fmt.Errorf("unsupported outbound type: %s", typ)
	}
	var n int64
	q := db.Model(&model.Outbound{}).Where("server_id = ? AND tag = ?", serverID, tag)
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

func (a *App) outboundExists(db *gorm.DB, serverID uint, tag string) (bool, error) {
	switch tag {
	case "direct", "block", "reject", "sniff", "hijack-dns":
		return true, nil
	}
	var n int64
	err := db.Model(&model.Outbound{}).Where("server_id = ? AND tag = ?", serverID, tag).Count(&n).Error
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
	if !ok {
		return fmt.Errorf("目标出站 %q 不存在", outbound)
	}
	var rule singbox.RuleInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rule); err != nil {
			return fmt.Errorf("规则匹配条件不是合法 JSON: %w", err)
		}
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
	var ruleSets []string
	if err := db.Model(&model.RuleSet{}).Where("server_id = ?", serverID).Pluck("tag", &ruleSets).Error; err != nil {
		return err
	}
	ruleSetSet := stringSet(ruleSets)
	for _, tag := range rule.RuleSet {
		if !ruleSetSet[tag] {
			return fmt.Errorf("规则引用的规则集 %q 不存在", tag)
		}
	}
	return nil
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
	for _, row := range rows {
		var rule singbox.RuleInput
		if json.Unmarshal(row.Match, &rule) == nil {
			for _, value := range rule.Inbound {
				if value == tag {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (a *App) ruleSetReferenced(db *gorm.DB, serverID uint, tag string) (bool, error) {
	var rows []model.RouteRule
	if err := db.Where("server_id = ?", serverID).Find(&rows).Error; err != nil {
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
	if next.TLS.Reality.PrivateKey == "" {
		next.TLS.Reality.PrivateKey = old.TLS.Reality.PrivateKey
	}
	if next.TLS.Reality.PublicKey == "" {
		next.TLS.Reality.PublicKey = old.TLS.Reality.PublicKey
	}
	if len(next.TLS.Reality.ShortID) == 0 {
		next.TLS.Reality.ShortID = old.TLS.Reality.ShortID
	}
	if next.TLS.Certificate == "" {
		next.TLS.Certificate = old.TLS.Certificate
	}
	if next.TLS.Key == "" {
		next.TLS.Key = old.TLS.Key
	}
}
