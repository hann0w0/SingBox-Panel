package panel

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

const (
	maxNameRewriteRules            = 50
	maxNameRewritePatternRunes     = 256
	maxNameRewriteReplaceRunes     = 512
	maxSubscriptionSourceNameRunes = 1024
	nameRuleActionRename           = "rename"
	nameRuleActionReplaceText      = "replace_text" // legacy alias; exposed as rename
	nameRuleActionExcludeProtocol  = "exclude_protocol"
	nameRuleActionIncludeNode      = "include_node"
	nameRuleActionExcludeNode      = "exclude_node"
	nameRuleMatchText              = "text"
	nameRuleMatchRegexp            = "regexp"
)

type compiledNameRewriteRule struct {
	action      string
	pattern     string
	regexp      *regexp.Regexp
	replacement string
	values      []string
}

func nonNilNameRewriteRules(rules []model.NameRewriteRule) []model.NameRewriteRule {
	if rules == nil {
		return []model.NameRewriteRule{}
	}
	out := make([]model.NameRewriteRule, len(rules))
	for i := range rules {
		out[i] = rules[i]
		originalAction := strings.ToLower(strings.TrimSpace(rules[i].Action))
		out[i].Action = normalizeNameRuleAction(rules[i].Action)
		switch out[i].Action {
		case nameRuleActionRename:
			out[i].MatchMode = normalizeNameRuleMatchMode(originalAction, rules[i].Pattern, rules[i].MatchMode)
		case nameRuleActionExcludeProtocol:
			values := splitRuleParameters(rules[i].Pattern)
			for j := range values {
				values[j] = normalizeSubscriptionProtocol(values[j])
			}
			out[i].Pattern = strings.Join(uniqueNonEmptyStrings(values), ",")
			out[i].Replacement = ""
		case nameRuleActionIncludeNode, nameRuleActionExcludeNode:
			out[i].Pattern = strings.Join(splitRuleParameters(rules[i].Pattern), ",")
			out[i].Replacement = ""
		}
	}
	return out
}

func normalizeNameRuleMatchMode(originalAction, pattern, matchMode string) string {
	matchMode = strings.ToLower(strings.TrimSpace(matchMode))
	if strings.HasPrefix(pattern, "re:") {
		return nameRuleMatchRegexp
	}
	if matchMode == nameRuleMatchText || matchMode == nameRuleMatchRegexp {
		return matchMode
	}
	if originalAction == nameRuleActionReplaceText {
		return nameRuleMatchText
	}
	// Rules stored before MatchMode existed used action=rename (or an empty
	// action) for regular expressions, so retain that behavior until edited.
	return nameRuleMatchRegexp
}

func normalizeNameRuleAction(action string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "", nameRuleActionRename, nameRuleActionReplaceText:
		return nameRuleActionRename
	default:
		return action
	}
}

func normalizeSubscriptionProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "ss", "shadowsocks":
		return "shadowsocks"
	case "socks", "socks5":
		return "socks"
	case "hy2", "hysteria2":
		return "hysteria2"
	case "hy", "hysteria":
		return "hysteria"
	case "tuic", "tuic-v4", "tuic-v5":
		return "tuic"
	default:
		return strings.ToLower(strings.TrimSpace(protocol))
	}
}

func splitRuleParameters(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '，' })
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return uniqueNonEmptyStrings(parts)
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

// compileNameRewriteRules is the single validation path used by writes and
// subscription refreshes. Rename rules are literal by default, keeping ordinary
// characters such as | intuitive. A pattern prefixed with "re:" explicitly
// opts into Go/RE2 and capture-group replacements.
func compileNameRewriteRules(rules []model.NameRewriteRule) ([]compiledNameRewriteRule, error) {
	if len(rules) > maxNameRewriteRules {
		return nil, fmt.Errorf("订阅处理规则最多允许 %d 条", maxNameRewriteRules)
	}
	compiled := make([]compiledNameRewriteRule, 0, len(rules))
	protocolPattern := regexp.MustCompile(`^[a-z0-9][a-z0-9+._-]*$`)
	for i, rule := range rules {
		originalAction := strings.ToLower(strings.TrimSpace(rule.Action))
		action := normalizeNameRuleAction(rule.Action)
		switch action {
		case nameRuleActionRename:
			if strings.TrimSpace(rule.Pattern) == "" {
				return nil, fmt.Errorf("第 %d 条节点重命名规则的匹配内容不能为空", i+1)
			}
			if utf8.RuneCountInString(rule.Pattern) > maxNameRewritePatternRunes {
				return nil, fmt.Errorf("第 %d 条节点重命名规则的匹配内容不能超过 %d 个字符", i+1, maxNameRewritePatternRunes)
			}
			if utf8.RuneCountInString(rule.Replacement) > maxNameRewriteReplaceRunes {
				return nil, fmt.Errorf("第 %d 条节点重命名规则的替换内容不能超过 %d 个字符", i+1, maxNameRewriteReplaceRunes)
			}
			matchMode := normalizeNameRuleMatchMode(originalAction, rule.Pattern, rule.MatchMode)
			pattern := strings.TrimPrefix(rule.Pattern, "re:")
			var re *regexp.Regexp
			if matchMode == nameRuleMatchRegexp {
				if pattern == "" {
					return nil, fmt.Errorf("第 %d 条节点重命名规则的正则内容不能为空", i+1)
				}
				var err error
				re, err = regexp.Compile(pattern)
				if err != nil {
					return nil, fmt.Errorf("第 %d 条节点重命名规则无效: %w", i+1, err)
				}
			} else if matchMode != nameRuleMatchText {
				return nil, fmt.Errorf("第 %d 条节点重命名规则的匹配模式无效", i+1)
			}
			compiled = append(compiled, compiledNameRewriteRule{
				action: action, pattern: pattern, regexp: re, replacement: rule.Replacement,
			})
		case nameRuleActionExcludeProtocol:
			values := splitRuleParameters(rule.Pattern)
			if len(values) == 0 {
				return nil, fmt.Errorf("第 %d 条去除协议规则的协议不能为空", i+1)
			}
			protocols := make([]string, 0, len(values))
			for _, value := range values {
				protocol := normalizeSubscriptionProtocol(value)
				if utf8.RuneCountInString(protocol) > 32 {
					return nil, fmt.Errorf("第 %d 条去除协议规则的单个协议不能超过 32 个字符", i+1)
				}
				if !protocolPattern.MatchString(protocol) {
					return nil, fmt.Errorf("第 %d 条去除协议规则中的协议 %q 格式无效", i+1, value)
				}
				protocols = append(protocols, protocol)
			}
			compiled = append(compiled, compiledNameRewriteRule{action: action, values: uniqueNonEmptyStrings(protocols)})
		case nameRuleActionIncludeNode, nameRuleActionExcludeNode:
			values := splitRuleParameters(rule.Pattern)
			if len(values) == 0 {
				label := "保留节点"
				if action == nameRuleActionExcludeNode {
					label = "去除节点"
				}
				return nil, fmt.Errorf("第 %d 条%s规则的匹配内容不能为空", i+1, label)
			}
			for _, value := range values {
				if utf8.RuneCountInString(value) > maxNameRewritePatternRunes {
					return nil, fmt.Errorf("第 %d 条节点筛选规则的单个参数不能超过 %d 个字符", i+1, maxNameRewritePatternRunes)
				}
			}
			compiled = append(compiled, compiledNameRewriteRule{action: action, values: values})
		default:
			return nil, fmt.Errorf("第 %d 条订阅处理规则的操作类型无效", i+1)
		}
	}
	return compiled, nil
}

func applyCompiledNameRewriteRules(name string, rules []compiledNameRewriteRule) string {
	for _, rule := range rules {
		if rule.action != nameRuleActionRename {
			continue
		}
		if rule.regexp != nil {
			name = rule.regexp.ReplaceAllString(name, rule.replacement)
			continue
		}
		name = strings.ReplaceAll(name, rule.pattern, rule.replacement)
	}
	return name
}

// applySubscriptionNodeRules executes every subscription rule in the order in
// which the administrator saved it.  Name filters therefore see the name as it
// exists at that exact step, rather than a final name produced by later rename
// rules.  This makes sequences such as "保留狮城" then "新加坡→狮城" predictable:
// the first rule does not retroactively match a later rename.
func applySubscriptionNodeRules(sourceName, protocol string, rules []compiledNameRewriteRule) (string, bool) {
	name := sourceName
	hidden := false
	protocol = normalizeSubscriptionProtocol(protocol)
	for _, rule := range rules {
		switch rule.action {
		case nameRuleActionRename:
			if rule.regexp != nil {
				name = rule.regexp.ReplaceAllString(name, rule.replacement)
			} else {
				name = strings.ReplaceAll(name, rule.pattern, rule.replacement)
			}
		case nameRuleActionExcludeProtocol:
			for _, candidate := range rule.values {
				if candidate == protocol {
					hidden = true
					break
				}
			}
		case nameRuleActionIncludeNode:
			hidden = !subscriptionNodeRuleMatches(name, name, rule.values)
		case nameRuleActionExcludeNode:
			if subscriptionNodeRuleMatches(name, name, rule.values) {
				hidden = true
			}
		}
	}
	return normalizedSubscriptionNodeName(sourceName, name), hidden
}

func subscriptionProtocolExcluded(protocol string, rules []compiledNameRewriteRule) bool {
	protocol = normalizeSubscriptionProtocol(protocol)
	for _, rule := range rules {
		if rule.action != nameRuleActionExcludeProtocol {
			continue
		}
		for _, candidate := range rule.values {
			if candidate == protocol {
				return true
			}
		}
	}
	return false
}

func subscriptionNodeHidden(sourceName, displayName, protocol string, rules []compiledNameRewriteRule) bool {
	_, hidden := applySubscriptionNodeRules(sourceName, protocol, rules)
	return hidden
}

func subscriptionNodeRuleMatches(sourceName, displayName string, values []string) bool {
	region := strings.ToLower(regionFromName(sourceName))
	if region == "" {
		region = strings.ToLower(regionFromName(displayName))
	}
	source := strings.ToLower(sourceName)
	display := strings.ToLower(displayName)
	for _, value := range values {
		term := strings.ToLower(strings.TrimSpace(value))
		if term == "" {
			continue
		}
		if nodeNameMatchesTerm(source, display, term) || term == region {
			return true
		}
		if code := subscriptionRegionCode(term); code != "" && code == region {
			return true
		}
	}
	return false
}

func nodeNameMatchesTerm(source, display, term string) bool {
	if len([]rune(term)) == 2 && isASCIIRegionCode(term) {
		for _, name := range []string{source, display} {
			for _, token := range strings.FieldsFunc(name, func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsDigit(r)
			}) {
				if token == term {
					return true
				}
			}
		}
		return false
	}
	return strings.Contains(source, term) || strings.Contains(display, term)
}

func isASCIIRegionCode(value string) bool {
	if len(value) != 2 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 'a' || value[i] > 'z' {
			return false
		}
	}
	return true
}

func subscriptionRegionCode(term string) string {
	aliases := map[string]string{
		"香港": "hk", "hong kong": "hk", "台湾": "tw", "台灣": "tw", "taiwan": "tw",
		"澳门": "mo", "澳門": "mo", "macau": "mo", "新加坡": "sg", "狮城": "sg", "獅城": "sg", "singapore": "sg",
		"日本": "jp", "japan": "jp", "韩国": "kr", "韓國": "kr", "korea": "kr", "美国": "us", "美國": "us", "united states": "us",
		"英国": "gb", "英國": "gb", "united kingdom": "gb", "德国": "de", "德國": "de", "germany": "de", "法国": "fr", "法國": "fr", "france": "fr",
		"加拿大": "ca", "canada": "ca", "澳大利亚": "au", "澳大利亞": "au", "澳洲": "au", "australia": "au", "荷兰": "nl", "荷蘭": "nl", "netherlands": "nl",
		"俄罗斯": "ru", "俄羅斯": "ru", "russia": "ru", "印度": "in", "india": "in", "泰国": "th", "泰國": "th", "thailand": "th",
		"马来西亚": "my", "馬來西亞": "my", "malaysia": "my", "菲律宾": "ph", "菲律賓": "ph", "philippines": "ph",
	}
	return aliases[term]
}

func rewriteSubscriptionNodeName(sourceName string, rules []compiledNameRewriteRule) string {
	return normalizedSubscriptionNodeName(sourceName, applyCompiledNameRewriteRules(sourceName, rules))
}

func normalizedSubscriptionNodeName(sourceName, rewritten string) string {
	original := trimRunes(strings.TrimSpace(sourceName), 128)
	if original == "" {
		return ""
	}
	rewritten = trimRunes(strings.TrimSpace(rewritten), 128)
	if rewritten == "" {
		return original
	}
	return rewritten
}

func normalizeSubscriptionSourceName(name string) string {
	return trimRunes(strings.TrimSpace(name), maxSubscriptionSourceNameRunes)
}

// applyManagedCustomNodeRules applies a subscription's saved rules to its
// existing rows without fetching the remote source. Filtered rows remain in
// storage so removing a rule restores their stable IDs and user assignments.
func applyManagedCustomNodeRules(tx *gorm.DB, subscriptionID uint, rules []compiledNameRewriteRule) (int, error) {
	var nodes []model.CustomNode
	if err := tx.Where("subscription_id = ?", subscriptionID).Find(&nodes).Error; err != nil {
		return 0, err
	}
	visible := 0
	for i := range nodes {
		sourceName := normalizeSubscriptionSourceName(nodes[i].SourceName)
		if sourceName == "" {
			sourceName = normalizeSubscriptionSourceName(nodes[i].Name)
		}
		name, hidden := applySubscriptionNodeRules(sourceName, nodes[i].Protocol, rules)
		if !hidden {
			visible++
		}
		if err := tx.Model(&nodes[i]).Updates(map[string]any{
			"source_name":                 sourceName,
			"name":                        name,
			"hidden_by_subscription_rule": hidden,
		}).Error; err != nil {
			return 0, err
		}
	}
	return visible, nil
}
