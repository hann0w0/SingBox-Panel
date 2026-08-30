package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestNameRewriteRulesApplyInOrderAndSupportCaptureGroups(t *testing.T) {
	rules, err := compileNameRewriteRules([]model.NameRewriteRule{
		{Pattern: `re:新加坡 ([0-9]+)`, Replacement: `狮城 $1`},
		{Pattern: `狮城`, Replacement: `SG`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := applyCompiledNameRewriteRules("🇸🇬 新加坡 01", rules); got != "🇸🇬 SG 01" {
		t.Fatalf("rewritten name = %q", got)
	}
	if got := rewriteSubscriptionNodeName("🇸🇬 新加坡 01", rules); got != "🇸🇬 SG 01" {
		t.Fatalf("trimmed rewritten name = %q", got)
	}
}

func TestSubscriptionNodeRulesRespectSavedOrder(t *testing.T) {
	includeThenRename, err := compileNameRewriteRules([]model.NameRewriteRule{
		{Action: nameRuleActionIncludeNode, Pattern: "omega"},
		{Action: nameRuleActionRename, Pattern: "alpha", Replacement: "omega", MatchMode: nameRuleMatchText},
	})
	if err != nil {
		t.Fatal(err)
	}
	name, hidden := applySubscriptionNodeRules("alpha", "trojan", includeThenRename)
	if name != "omega" || !hidden {
		t.Fatalf("include-before-rename = name:%q hidden:%v, want omega/true", name, hidden)
	}

	renameThenInclude, err := compileNameRewriteRules([]model.NameRewriteRule{
		{Action: nameRuleActionRename, Pattern: "alpha", Replacement: "omega", MatchMode: nameRuleMatchText},
		{Action: nameRuleActionIncludeNode, Pattern: "omega"},
	})
	if err != nil {
		t.Fatal(err)
	}
	name, hidden = applySubscriptionNodeRules("alpha", "trojan", renameThenInclude)
	if name != "omega" || hidden {
		t.Fatalf("rename-before-include = name:%q hidden:%v, want omega/false", name, hidden)
	}
}

func TestTextReplacementTreatsVerticalBarsAsLiteralText(t *testing.T) {
	rules, err := compileNameRewriteRules([]model.NameRewriteRule{
		{Action: nameRuleActionReplaceText, Pattern: "🆃 | ", Replacement: ""},
		{Action: nameRuleActionReplaceText, Pattern: "🅠 ｜ ", Replacement: ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rewriteSubscriptionNodeName("🆃 | 🇺🇸 US", rules); got != "🇺🇸 US" {
		t.Fatalf("literal ASCII bar replacement = %q", got)
	}
	if got := rewriteSubscriptionNodeName("🅠 ｜ 🇭🇰 HK", rules); got != "🇭🇰 HK" {
		t.Fatalf("literal full-width bar replacement = %q", got)
	}
}

func TestLegacyRegexpRenameAndNewTextRenameBothRemainCompatible(t *testing.T) {
	legacy, err := compileNameRewriteRules([]model.NameRewriteRule{{Action: nameRuleActionRename, Pattern: ".snell", Replacement: " "}})
	if err != nil {
		t.Fatal(err)
	}
	if got := rewriteSubscriptionNodeName("节点-snell", legacy); got != "节点" {
		t.Fatalf("legacy regexp rename = %q", got)
	}
	textRules, err := compileNameRewriteRules([]model.NameRewriteRule{{Action: nameRuleActionRename, MatchMode: nameRuleMatchText, Pattern: "🆃 | ", Replacement: ""}})
	if err != nil {
		t.Fatal(err)
	}
	if got := rewriteSubscriptionNodeName("🆃 | 🇺🇸 US", textRules); got != "🇺🇸 US" {
		t.Fatalf("new text rename = %q", got)
	}
}

func TestSubscriptionRulesExcludeProtocolExactlyAndNormalizeAliases(t *testing.T) {
	rules, err := compileNameRewriteRules([]model.NameRewriteRule{
		{Action: nameRuleActionExcludeProtocol, Pattern: "AnyTLS, ss，hy2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{"anytls", "ANYTLS", "shadowsocks", "ss", "hysteria2"} {
		if !subscriptionProtocolExcluded(protocol, rules) {
			t.Fatalf("protocol %q was not excluded", protocol)
		}
	}
	if subscriptionProtocolExcluded("trojan", rules) {
		t.Fatal("unrelated protocol was excluded")
	}
	if got := applyCompiledNameRewriteRules("AnyTLS 节点", rules); got != "AnyTLS 节点" {
		t.Fatalf("protocol rule changed node name: %q", got)
	}
}

func TestSubscriptionNodeRulesMatchNameAndRegionInOrder(t *testing.T) {
	if nodeNameMatchesTerm("🇷🇺 Russia", "", "us") {
		t.Fatal("US matched the middle of Russia")
	}
	rules, err := compileNameRewriteRules([]model.NameRewriteRule{
		{Action: nameRuleActionIncludeNode, Pattern: "香港,SG"},
		{Action: nameRuleActionExcludeNode, Pattern: "测试,US"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []struct {
		name   string
		hidden bool
	}{
		{name: "🇭🇰 HK 01", hidden: false},
		{name: "🇸🇬 Singapore 01", hidden: false},
		{name: "🇸🇬 新加坡 测试", hidden: true},
		{name: "🇺🇸 US 01", hidden: true},
		{name: "🇯🇵 JP 01", hidden: true},
	} {
		if got := subscriptionNodeHidden(node.name, node.name, "trojan", rules); got != node.hidden {
			t.Fatalf("node %q hidden=%v, want %v", node.name, got, node.hidden)
		}
	}
	firstExclude, err := compileNameRewriteRules([]model.NameRewriteRule{
		{Action: nameRuleActionExcludeNode, Pattern: "香港"},
		{Action: nameRuleActionIncludeNode, Pattern: "香港"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if subscriptionNodeHidden("🇭🇰 HK 01", "🇭🇰 HK 01", "trojan", firstExclude) {
		t.Fatal("later keep rule did not restore the node")
	}
	firstInclude, err := compileNameRewriteRules([]model.NameRewriteRule{
		{Action: nameRuleActionIncludeNode, Pattern: "香港"},
		{Action: nameRuleActionExcludeNode, Pattern: "香港"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !subscriptionNodeHidden("🇭🇰 HK 01", "🇭🇰 HK 01", "trojan", firstInclude) {
		t.Fatal("later remove rule did not hide the node")
	}
	protocolThenKeep, err := compileNameRewriteRules([]model.NameRewriteRule{
		{Action: nameRuleActionExcludeProtocol, Pattern: "anytls"},
		{Action: nameRuleActionIncludeNode, Pattern: "新加坡"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if subscriptionNodeHidden("🇸🇬 新加坡 01", "🇸🇬 新加坡 01", "anytls", protocolThenKeep) {
		t.Fatal("later keep rule did not override protocol exclusion")
	}
	keepThenProtocol, err := compileNameRewriteRules([]model.NameRewriteRule{
		{Action: nameRuleActionIncludeNode, Pattern: "新加坡"},
		{Action: nameRuleActionExcludeProtocol, Pattern: "anytls"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !subscriptionNodeHidden("🇸🇬 新加坡 01", "🇸🇬 新加坡 01", "anytls", keepThenProtocol) {
		t.Fatal("later protocol exclusion did not hide the node")
	}
}

func TestNameRewriteRulesRejectInvalidAndOversizedRules(t *testing.T) {
	tests := []struct {
		name  string
		rules []model.NameRewriteRule
	}{
		{name: "empty pattern", rules: []model.NameRewriteRule{{Pattern: "  "}}},
		{name: "invalid regexp", rules: []model.NameRewriteRule{{Pattern: "re:(?=新加坡)"}}},
		{name: "too many", rules: make([]model.NameRewriteRule, maxNameRewriteRules+1)},
		{name: "long pattern", rules: []model.NameRewriteRule{{Pattern: strings.Repeat("a", maxNameRewritePatternRunes+1)}}},
		{name: "long replacement", rules: []model.NameRewriteRule{{Pattern: "a", Replacement: strings.Repeat("b", maxNameRewriteReplaceRunes+1)}}},
		{name: "empty protocol", rules: []model.NameRewriteRule{{Action: nameRuleActionExcludeProtocol, Pattern: " "}}},
		{name: "invalid protocol", rules: []model.NameRewriteRule{{Action: nameRuleActionExcludeProtocol, Pattern: "any tls"}}},
		{name: "invalid action", rules: []model.NameRewriteRule{{Action: "delete", Pattern: "anytls"}}},
		{name: "empty text", rules: []model.NameRewriteRule{{Action: nameRuleActionReplaceText, Pattern: "  "}}},
		{name: "empty include", rules: []model.NameRewriteRule{{Action: nameRuleActionIncludeNode, Pattern: " , ， "}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := compileNameRewriteRules(test.rules); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if rules, err := compileNameRewriteRules([]model.NameRewriteRule{{Pattern: "🇸🇬 新加坡 01", Replacement: ""}}); err != nil {
		t.Fatal(err)
	} else if got := rewriteSubscriptionNodeName("🇸🇬 新加坡 01", rules); got != "🇸🇬 新加坡 01" {
		t.Fatalf("empty rewrite should preserve source name = %q", got)
	}
}

func TestCustomNodeSubscriptionKeyTracksNameNotEndpoint(t *testing.T) {
	a := singbox.ImportedNode{
		Name: "Hong Kong 01", Protocol: "vless", Address: "old.example.com", Port: 443,
		Params: map[string]any{"uuid": "old", "tls": "tls"},
	}
	b := singbox.ImportedNode{
		Name: "Hong Kong 01", Protocol: "vless", Address: "new.example.com", Port: 8443,
		Params: map[string]any{"uuid": "new", "tls": "reality"},
	}
	ka, err := customNodeSubscriptionKey(a, 0)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := customNodeSubscriptionKey(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb {
		t.Fatalf("rotated endpoint changed key: %s != %s", ka, kb)
	}
	b.Name = "Hong Kong 02"
	kc, err := customNodeSubscriptionKey(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if kc == ka {
		t.Fatal("renamed node kept the same key")
	}
}

func TestSubscriptionCustomNodeUsesSourceSettings(t *testing.T) {
	source := model.CustomNodeSubscription{ID: 7, Group: "airport", Enabled: false, BaseSortOrder: 20}
	item := singbox.ImportedNode{
		Name: "node", Protocol: "trojan", Address: "example.com", Port: 443,
		Params: map[string]any{"password": "secret", "tls": "tls"},
	}
	row, key, err := subscriptionCustomNode(source, item, 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.SubscriptionID == nil || *row.SubscriptionID != source.ID || row.SubscriptionKey != key {
		t.Fatalf("subscription identity not set: %+v", row)
	}
	if row.Group != "airport" || row.Enabled || row.SortOrder != 23 {
		t.Fatalf("source settings not applied: %+v", row)
	}
	if row.AllUsers || len(row.UserIDs) != 0 || len(row.ExcludedUserIDs) != 0 {
		t.Fatalf("new subscription node was assigned to users: %+v", row)
	}
	var params map[string]any
	if err := json.Unmarshal(row.Params, &params); err != nil || params["password"] != "secret" {
		t.Fatalf("params not retained: %s, %v", row.Params, err)
	}
}

func TestSubscriptionNewNodeAudienceFollowsCompleteSourceAssignments(t *testing.T) {
	db := testDB(t)
	wholeSourceUser := model.User{Email: "whole-source", SubToken: "whole-source-token"}
	partialSourceUser := model.User{Email: "partial-source", SubToken: "partial-source-token"}
	if err := db.Create(&wholeSourceUser).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&partialSourceUser).Error; err != nil {
		t.Fatal(err)
	}

	existing := []model.CustomNode{
		{UserIDs: []uint{wholeSourceUser.ID, partialSourceUser.ID}},
		{UserIDs: []uint{wholeSourceUser.ID}},
		// Rule-hidden rows are not currently part of the assigned source and
		// therefore must not prevent visible source members from inheriting a
		// newly added visible node.
		{UserIDs: []uint{partialSourceUser.ID}, HiddenBySubscriptionRule: true},
	}
	audience, ok, err := subscriptionNewNodeAudience(db, existing)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || audience.AllUsers || len(audience.UserIDs) != 1 || audience.UserIDs[0] != wholeSourceUser.ID {
		t.Fatalf("new-node audience = %+v, inherit=%v", audience, ok)
	}

	newNode := model.CustomNode{}
	audience.apply(&newNode)
	if !newNode.HasUser(wholeSourceUser.ID) || newNode.HasUser(partialSourceUser.ID) {
		t.Fatalf("inherited access is wrong: %+v", newNode)
	}
}

func TestSubscriptionNewNodeAudienceRetainsGlobalAudience(t *testing.T) {
	db := testDB(t)
	excludedByFirst := model.User{Email: "excluded-first", SubToken: "excluded-first-token"}
	excludedBySecond := model.User{Email: "excluded-second", SubToken: "excluded-second-token"}
	if err := db.Create(&excludedByFirst).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&excludedBySecond).Error; err != nil {
		t.Fatal(err)
	}

	audience, ok, err := subscriptionNewNodeAudience(db, []model.CustomNode{
		{AllUsers: true, ExcludedUserIDs: []uint{excludedByFirst.ID}},
		{AllUsers: true, ExcludedUserIDs: []uint{excludedBySecond.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !audience.AllUsers || len(audience.ExcludedUserIDs) != 2 {
		t.Fatalf("global new-node audience = %+v, inherit=%v", audience, ok)
	}
	newNode := model.CustomNode{}
	audience.apply(&newNode)
	if newNode.HasUser(excludedByFirst.ID) || newNode.HasUser(excludedBySecond.ID) {
		t.Fatalf("global exclusions were not retained: %+v", newNode)
	}
}

func TestSubscriptionCustomNodeUsesOriginalNameForKeyAndRewritesDisplayName(t *testing.T) {
	rules, err := compileNameRewriteRules([]model.NameRewriteRule{{Pattern: "新加坡", Replacement: "狮城"}})
	if err != nil {
		t.Fatal(err)
	}
	source := model.CustomNodeSubscription{ID: 7, Group: "airport"}
	item := singbox.ImportedNode{
		Name: "🇸🇬 新加坡 01", Protocol: "trojan", Address: "example.com", Port: 443,
		Params: map[string]any{"password": "secret", "tls": "tls"},
	}
	row, key, err := subscriptionCustomNode(source, item, 0, 0, rules)
	if err != nil {
		t.Fatal(err)
	}
	if row.SourceName != item.Name || row.Name != "🇸🇬 狮城 01" {
		t.Fatalf("source/display names = %q/%q", row.SourceName, row.Name)
	}
	originalKey, err := customNodeSubscriptionKey(item, 0)
	if err != nil {
		t.Fatal(err)
	}
	if key != originalKey {
		t.Fatalf("rewrite changed key: %s != %s", key, originalKey)
	}
}

func TestUpdateCustomNodeSubscriptionAppliesSourceSettings(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	source := model.CustomNodeSubscription{
		Name: "旧订阅", URL: "https://example.com/old", Group: "旧分组",
		Enabled: true, AutoUpdate: true, UpdateIntervalMinutes: 60, BaseSortOrder: 20,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	node := model.CustomNode{
		Name: "节点", Link: "socks5://127.0.0.1:1080", Group: source.Group,
		Enabled: true, SortOrder: 23, SubscriptionID: &source.ID, SubscriptionKey: "node-key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{
		"name": "新订阅", "url": "https://example.com/new", "group": "新分组",
		"enabled": false, "auto_update": false, "update_interval_minutes": 120,
		"base_sort_order": 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/custom-node-subscriptions/:id", app.updateCustomNodeSubscription)
	path := "/custom-node-subscriptions/" + strconv.FormatUint(uint64(source.ID), 10)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", w.Code, w.Body.String())
	}
	if err := db.First(&source, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if source.Name != "新订阅" || source.Group != "新分组" || source.Enabled || source.AutoUpdate || source.BaseSortOrder != 30 {
		t.Fatalf("subscription not updated: %+v", source)
	}
	if err := db.First(&node, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if node.Group != "新分组" || node.Enabled || node.SortOrder != 33 {
		t.Fatalf("managed node settings not updated: %+v", node)
	}
}

func TestUpdateCustomNodeSubscriptionRewritesExistingNamesWithoutFetch(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	source := model.CustomNodeSubscription{
		Name: "订阅", URL: "https://example.com/sub", Enabled: true, AutoUpdate: true, UpdateIntervalMinutes: 60,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	node := model.CustomNode{
		Name: "🇸🇬 新加坡 01", SourceName: "🇸🇬 新加坡 01", Link: "trojan://secret@example.com:443#name",
		UserIDs: []uint{7}, SubscriptionID: &source.ID, SubscriptionKey: "stable-key", Enabled: true,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"name": "订阅", "url": source.URL, "name_rewrite_rules": []model.NameRewriteRule{{Pattern: "新加坡", Replacement: "狮城"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/custom-node-subscriptions/:id", app.updateCustomNodeSubscription)
	req := httptest.NewRequest(http.MethodPut, "/custom-node-subscriptions/"+strconv.FormatUint(uint64(source.ID), 10), bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", w.Code, w.Body.String())
	}
	if err := db.First(&node, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if node.Name != "🇸🇬 狮城 01" || node.SourceName != "🇸🇬 新加坡 01" || len(node.UserIDs) != 1 || node.UserIDs[0] != 7 {
		t.Fatalf("existing node rewrite or audience failed: %+v", node)
	}
	if err := db.First(&source, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(source.NameRewriteRules) != 1 || source.NameRewriteRules[0].Replacement != "狮城" {
		t.Fatalf("rewrite rules were not persisted: %+v", source.NameRewriteRules)
	}
}

func TestUpdateCustomNodeSubscriptionFiltersAndRestoresProtocolAssignments(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	source := model.CustomNodeSubscription{
		Name: "订阅", URL: "https://example.com/sub", Enabled: true, AutoUpdate: true, UpdateIntervalMinutes: 60,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	anyTLS := model.CustomNode{
		Name: "AnyTLS", SourceName: "AnyTLS", Link: "anytls://secret@example.com:443#AnyTLS", Protocol: "anytls", Address: "example.com", Port: 443,
		UserIDs: []uint{7}, SubscriptionID: &source.ID, SubscriptionKey: "anytls-key", Enabled: true,
	}
	trojan := model.CustomNode{
		Name: "Trojan", SourceName: "Trojan", Link: "trojan://secret@example.com:443#Trojan", Protocol: "trojan", Address: "example.com", Port: 443,
		UserIDs: []uint{7}, SubscriptionID: &source.ID, SubscriptionKey: "trojan-key", Enabled: true,
	}
	if err := db.Create(&anyTLS).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&trojan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&source).Update("node_count", 2).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/custom-node-subscriptions/:id", app.updateCustomNodeSubscription)
	router.GET("/custom-nodes", app.listCustomNodes)
	path := "/custom-node-subscriptions/" + strconv.FormatUint(uint64(source.ID), 10)
	payload := `{"name":"订阅","url":"https://example.com/sub","name_rewrite_rules":[{"action":"exclude_protocol","pattern":"AnyTLS","replacement":"ignored"}]}`
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("filter update status=%d body=%s", w.Code, w.Body.String())
	}
	if err := db.First(&anyTLS, anyTLS.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&trojan, trojan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !anyTLS.HiddenBySubscriptionRule || trojan.HiddenBySubscriptionRule {
		t.Fatalf("filter state anytls=%v trojan=%v", anyTLS.HiddenBySubscriptionRule, trojan.HiddenBySubscriptionRule)
	}
	if len(anyTLS.UserIDs) != 1 || anyTLS.UserIDs[0] != 7 {
		t.Fatalf("filtered node assignment changed: %v", anyTLS.UserIDs)
	}
	access, err := effectiveUserAccess(db, &model.User{ID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(access.CustomNodeIDs) != 1 || access.CustomNodeIDs[0] != trojan.ID {
		t.Fatalf("filtered user access = %v", access.CustomNodeIDs)
	}
	if nodes := app.gatherNodes(&model.User{ID: 7}); len(nodes) != 1 || nodes[0].typ != "trojan" {
		t.Fatalf("filtered subscription nodes = %+v", nodes)
	}
	listRecorder := httptest.NewRecorder()
	router.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/custom-nodes", nil))
	if listRecorder.Code != http.StatusOK || strings.Contains(listRecorder.Body.String(), "AnyTLS") || !strings.Contains(listRecorder.Body.String(), "Trojan") {
		t.Fatalf("filtered admin list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	if err := db.First(&source, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if source.NodeCount != 1 || len(source.NameRewriteRules) != 1 || source.NameRewriteRules[0].Pattern != "anytls" || source.NameRewriteRules[0].Replacement != "" {
		t.Fatalf("filtered subscription state: %+v", source)
	}

	payload = `{"name":"订阅","url":"https://example.com/sub","name_rewrite_rules":[]}`
	req = httptest.NewRequest(http.MethodPut, path, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("restore update status=%d body=%s", w.Code, w.Body.String())
	}
	if err := db.First(&anyTLS, anyTLS.ID).Error; err != nil {
		t.Fatal(err)
	}
	if anyTLS.HiddenBySubscriptionRule || len(anyTLS.UserIDs) != 1 || anyTLS.UserIDs[0] != 7 {
		t.Fatalf("restored node lost state: %+v", anyTLS)
	}
	if nodes := app.gatherNodes(&model.User{ID: 7}); len(nodes) != 2 {
		t.Fatalf("restored subscription nodes = %+v", nodes)
	}
	if err := db.First(&source, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if source.NodeCount != 2 {
		t.Fatalf("restored node count = %d", source.NodeCount)
	}
}

func TestDeleteCustomNodeSubscriptionDeletesManagedNodes(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	source := model.CustomNodeSubscription{Name: "订阅", URL: "https://example.com/sub"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	node := model.CustomNode{
		Name: "节点", Link: "socks5://127.0.0.1:1080", Enabled: true,
		SubscriptionID: &source.ID, SubscriptionKey: "node-key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.DELETE("/custom-node-subscriptions/:id", app.deleteCustomNodeSubscription)
	path := "/custom-node-subscriptions/" + strconv.FormatUint(uint64(source.ID), 10)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", w.Code, w.Body.String())
	}
	var sourceCount, nodeCount int64
	if err := db.Model(&model.CustomNodeSubscription{}).Where("id = ?", source.ID).Count(&sourceCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CustomNode{}).Where("subscription_id = ?", source.ID).Count(&nodeCount).Error; err != nil {
		t.Fatal(err)
	}
	if sourceCount != 0 || nodeCount != 0 {
		t.Fatalf("delete left source=%d nodes=%d", sourceCount, nodeCount)
	}
}

func TestSQLiteDSNAddsPragmas(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{input: "panel.db", want: "panel.db?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"},
		{input: "file:panel.db?cache=shared", want: "file:panel.db?cache=shared&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"},
		{input: ":memory:", want: ":memory:?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"},
	} {
		if got := sqliteDSN(tc.input); got != tc.want {
			t.Fatalf("sqliteDSN(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}
