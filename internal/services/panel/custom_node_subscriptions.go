package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

const (
	minCustomNodeSubscriptionInterval = 15
	maxCustomNodeSubscriptionInterval = 7 * 24 * 60
)

type customNodeSubscriptionManager struct {
	db         *gorm.DB
	locks      *keyedMutex[uint]
	audienceMu sync.Mutex
}

type customNodeSubscriptionSyncResult struct {
	Created  int `json:"created"`
	Updated  int `json:"updated"`
	Deleted  int `json:"deleted"`
	Total    int `json:"total"`
	Skipped  int `json:"skipped"`
	Filtered int `json:"filtered"`
}

func newCustomNodeSubscriptionManager(db *gorm.DB) *customNodeSubscriptionManager {
	return &customNodeSubscriptionManager{db: db, locks: newKeyedMutex[uint]()}
}

// subscriptionManager keeps focused handler tests and other lightweight App
// constructions safe while NewApp still owns the long-lived production
// manager. The fallback is only used when a caller intentionally omitted the
// optional dependency.
func (a *App) subscriptionManager() *customNodeSubscriptionManager {
	if a.customSubscriptions != nil {
		return a.customSubscriptions
	}
	return newCustomNodeSubscriptionManager(a.db)
}

func normalizeCustomNodeSubscriptionInterval(minutes int) int {
	if minutes == 0 {
		return 60
	}
	if minutes < minCustomNodeSubscriptionInterval {
		return minCustomNodeSubscriptionInterval
	}
	if minutes > maxCustomNodeSubscriptionInterval {
		return maxCustomNodeSubscriptionInterval
	}
	return minutes
}

func validateCustomNodeSubscriptionURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, ok := singleRemoteSubscriptionURL(raw)
	if !ok {
		return "", errors.New("请输入完整的 HTTP(S) 订阅地址")
	}
	if len(raw) > 2048 {
		return "", errors.New("订阅地址过长")
	}
	return u.String(), nil
}

func (m *customNodeSubscriptionManager) lock(id uint) func() {
	return m.locks.lock(id)
}

func (m *customNodeSubscriptionManager) lockAudience() func() {
	m.audienceMu.Lock()
	return m.audienceMu.Unlock
}

func (a *App) lockCustomNodeSubscription(id *uint) func() {
	if id == nil || a.customSubscriptions == nil {
		return func() {}
	}
	return a.customSubscriptions.lock(*id)
}

func (m *customNodeSubscriptionManager) run(ctx context.Context) {
	// Let the HTTP server finish starting before the first due-source scan.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		m.syncDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *customNodeSubscriptionManager) syncDue(parent context.Context) {
	var sources []model.CustomNodeSubscription
	if err := m.db.Where("auto_update = ? AND enabled = ?", true, true).Find(&sources).Error; err != nil {
		log.Printf("custom subscriptions: list due sources: %v", err)
		return
	}
	now := time.Now()
	for i := range sources {
		source := sources[i]
		interval := time.Duration(normalizeCustomNodeSubscriptionInterval(source.UpdateIntervalMinutes)) * time.Minute
		if source.LastSyncAt != nil && now.Sub(*source.LastSyncAt) < interval {
			continue
		}
		go func(id uint) {
			ctx, cancel := context.WithTimeout(parent, 30*time.Second)
			defer cancel()
			if _, err := m.sync(ctx, id); err != nil {
				log.Printf("custom subscriptions: sync %d failed: %v", id, err)
			}
		}(source.ID)
	}
}

func (m *customNodeSubscriptionManager) sync(ctx context.Context, id uint) (customNodeSubscriptionSyncResult, error) {
	unlock := m.lock(id)
	defer unlock()

	var source model.CustomNodeSubscription
	if err := m.db.First(&source, id).Error; err != nil {
		return customNodeSubscriptionSyncResult{}, err
	}
	now := time.Now()
	markFailure := func(err error) error {
		message := trimRunes(err.Error(), 1000)
		if dbErr := m.db.Model(&model.CustomNodeSubscription{}).Where("id = ?", id).Updates(map[string]any{
			"last_sync_at": &now,
			"last_error":   message,
		}).Error; dbErr != nil {
			return fmt.Errorf("%v; save sync error: %w", err, dbErr)
		}
		return err
	}
	compiledRules, err := compileNameRewriteRules(source.NameRewriteRules)
	if err != nil {
		return customNodeSubscriptionSyncResult{}, markFailure(err)
	}

	u, ok := singleRemoteSubscriptionURL(source.URL)
	if !ok {
		return customNodeSubscriptionSyncResult{}, markFailure(errors.New("保存的订阅地址无效"))
	}
	raw, err := fetchRemoteSubscription(ctx, u)
	if err != nil {
		return customNodeSubscriptionSyncResult{}, markFailure(err)
	}
	parsed, err := singbox.ParseSubscription(raw)
	if err != nil {
		return customNodeSubscriptionSyncResult{}, markFailure(err)
	}
	if len(parsed.Nodes) == 0 {
		return customNodeSubscriptionSyncResult{}, markFailure(errors.New("订阅没有返回可用节点，已保留上次同步结果"))
	}

	desired := make(map[string]model.CustomNode, len(parsed.Nodes))
	order := make([]string, 0, len(parsed.Nodes))
	occurrences := make(map[string]int, len(parsed.Nodes))
	definitions := make(map[string]bool, len(parsed.Nodes))
	visibleCount := 0
	for i := range parsed.Nodes {
		definitionKey, keyErr := customNodeSubscriptionDefinitionKey(parsed.Nodes[i])
		if keyErr != nil {
			return customNodeSubscriptionSyncResult{}, markFailure(keyErr)
		}
		// A byte-for-byte-equivalent node is a provider duplicate, not a second
		// independently assignable endpoint. Ignore later copies before assigning
		// occurrence keys; same-name nodes with different connection data remain.
		if definitions[definitionKey] {
			continue
		}
		definitions[definitionKey] = true
		baseKey, keyErr := customNodeSubscriptionBaseKey(parsed.Nodes[i])
		if keyErr != nil {
			return customNodeSubscriptionSyncResult{}, markFailure(keyErr)
		}
		occurrence := occurrences[baseKey]
		occurrences[baseKey] = occurrence + 1
		row, key, buildErr := subscriptionCustomNode(source, parsed.Nodes[i], i, occurrence, compiledRules)
		if buildErr != nil {
			return customNodeSubscriptionSyncResult{}, markFailure(buildErr)
		}
		// Providers occasionally emit duplicate definitions. Keep the first row so
		// sort order is deterministic and one refresh cannot violate the unique key.
		if _, exists := desired[key]; exists {
			continue
		}
		desired[key] = row
		order = append(order, key)
		if !row.HiddenBySubscriptionRule {
			visibleCount++
		}
	}
	if len(desired) == 0 {
		return customNodeSubscriptionSyncResult{}, markFailure(errors.New("订阅节点均无法保存，已保留上次同步结果"))
	}

	result := customNodeSubscriptionSyncResult{
		Total: visibleCount, Skipped: len(parsed.Skipped), Filtered: len(desired) - visibleCount,
	}
	partial := len(parsed.Skipped) > 0
	unlockAudience := m.lockAudience()
	defer unlockAudience()
	err = m.db.Transaction(func(tx *gorm.DB) error {
		var existing []model.CustomNode
		if err := tx.Where("subscription_id = ?", source.ID).Find(&existing).Error; err != nil {
			return err
		}
		// A saved subscription is normally assigned as a complete set.  Keep
		// that intent when the upstream provider adds a node (or renames one,
		// which deliberately becomes a new row because the source name is part
		// of the stable key).  The inherited audience is the intersection of
		// every *visible* existing subscription node: a user who was granted
		// only part of a source must not gain its newly added nodes.  Hidden
		// rows are intentionally excluded because rule changes can temporarily
		// hide an otherwise independently assigned node.
		audience, inheritAudience, err := subscriptionNewNodeAudience(tx, existing)
		if err != nil {
			return err
		}
		byKey := make(map[string]*model.CustomNode, len(existing))
		for i := range existing {
			byKey[existing[i].SubscriptionKey] = &existing[i]
		}
		for _, key := range order {
			want := desired[key]
			if current := byKey[key]; current != nil {
				updates := map[string]any{
					"name": want.Name, "source_name": want.SourceName, "group": want.Group, "link": want.Link,
					"protocol": want.Protocol, "address": want.Address, "port": want.Port,
					"params": want.Params, "enabled": want.Enabled, "sort_order": want.SortOrder,
					"hidden_by_subscription_rule": want.HiddenBySubscriptionRule,
				}
				if err := tx.Model(current).Updates(updates).Error; err != nil {
					return err
				}
				result.Updated++
				delete(byKey, key)
				continue
			}
			if inheritAudience {
				audience.apply(&want)
			}
			if err := tx.Create(&want).Error; err != nil {
				return err
			}
			result.Created++
		}
		// A parser can recover some nodes from a damaged provider response. Updating
		// those rows is safe, but deleting every unmatched old row is not: the
		// skipped item may be exactly that node. Only a complete parse may perform
		// destructive reconciliation.
		if !partial {
			for _, stale := range byKey {
				if err := tx.Delete(stale).Error; err != nil {
					return err
				}
				if err := deleteUserNodeOrderRefs(tx, userNodeTypeCustom, []uint{stale.ID}); err != nil {
					return err
				}
				result.Deleted++
			}
		}
		var actualVisible int64
		if err := tx.Model(&model.CustomNode{}).
			Where("subscription_id = ? AND hidden_by_subscription_rule = ?", source.ID, false).
			Count(&actualVisible).Error; err != nil {
			return err
		}
		result.Total = int(actualVisible)
		lastError := ""
		updates := map[string]any{
			"last_sync_at": &now,
			"last_error":   lastError,
			"source_type":  parsed.SourceType,
			"node_count":   actualVisible,
		}
		if partial {
			updates["last_error"] = fmt.Sprintf("有 %d 个节点解析失败，已保留未匹配的旧节点", len(parsed.Skipped))
		} else {
			updates["last_success_at"] = &now
		}
		return tx.Model(&model.CustomNodeSubscription{}).Where("id = ?", source.ID).Updates(updates).Error
	})
	if err != nil {
		return customNodeSubscriptionSyncResult{}, markFailure(fmt.Errorf("保存订阅节点失败: %w", err))
	}
	return result, nil
}

// customNodeAudience is the assignment portion of a CustomNode.  Keeping it
// separate from the connection fields makes it explicit that subscription
// refreshes only inherit access policy and never credentials or node options.
type customNodeAudience struct {
	AllUsers        bool
	UserIDs         []uint
	ExcludedUserIDs []uint
}

func (a customNodeAudience) apply(node *model.CustomNode) {
	node.AllUsers = a.AllUsers
	node.UserIDs = append([]uint{}, a.UserIDs...)
	node.ExcludedUserIDs = append([]uint{}, a.ExcludedUserIDs...)
}

// subscriptionNewNodeAudience derives the access policy for nodes newly seen
// during a refresh.  It grants only users who can currently use every visible
// node from this source, which preserves intentional per-node assignments.
//
// If all source nodes are global, retain the global form (with the union of
// exclusions) so accounts created later still receive the same subscription.
// Any mixed/global-per-user source is represented as an explicit list of the
// current users that have the complete source.
func subscriptionNewNodeAudience(db *gorm.DB, existing []model.CustomNode) (customNodeAudience, bool, error) {
	visible := make([]model.CustomNode, 0, len(existing))
	for i := range existing {
		if !existing[i].HiddenBySubscriptionRule {
			visible = append(visible, existing[i])
		}
	}
	if len(visible) == 0 {
		return customNodeAudience{}, false, nil
	}

	allUsers := true
	excluded := make([]uint, 0)
	for i := range visible {
		if !visible[i].AllUsers {
			allUsers = false
			break
		}
		excluded = append(excluded, visible[i].ExcludedUserIDs...)
	}
	if allUsers {
		return customNodeAudience{
			AllUsers:        true,
			UserIDs:         []uint{},
			ExcludedUserIDs: normalizedIDs(excluded),
		}, true, nil
	}

	var users []model.User
	if err := db.Select("id").Find(&users).Error; err != nil {
		return customNodeAudience{}, false, err
	}
	granted := make([]uint, 0, len(users))
	for i := range users {
		hasEveryNode := true
		for j := range visible {
			if !visible[j].HasUser(users[i].ID) {
				hasEveryNode = false
				break
			}
		}
		if hasEveryNode {
			granted = append(granted, users[i].ID)
		}
	}
	return customNodeAudience{
		AllUsers:        false,
		UserIDs:         normalizedIDs(granted),
		ExcludedUserIDs: []uint{},
	}, true, nil
}

func subscriptionCustomNode(source model.CustomNodeSubscription, item singbox.ImportedNode, index, occurrence int, rules []compiledNameRewriteRule) (model.CustomNode, string, error) {
	params, err := json.Marshal(item.Params)
	if err != nil {
		return model.CustomNode{}, "", fmt.Errorf("节点 %q 参数无法保存: %w", item.Name, err)
	}
	link := strings.TrimSpace(item.Link)
	if len(link) > 1024 {
		link = ""
	}
	sourceName := normalizeSubscriptionSourceName(item.Name)
	name, hidden := applySubscriptionNodeRules(sourceName, item.Protocol, rules)
	validation := customNodeReq{
		Name: name, Link: link, Protocol: item.Protocol,
		Address: item.Address, Port: item.Port, Params: params,
	}
	if _, err := validateCustomNode(&validation); err != nil {
		return model.CustomNode{}, "", fmt.Errorf("节点 %q 无法保存: %w", item.Name, err)
	}
	key, err := customNodeSubscriptionKey(item, occurrence)
	if err != nil {
		return model.CustomNode{}, "", err
	}
	return model.CustomNode{
		AllUsers: false, UserIDs: []uint{}, ExcludedUserIDs: []uint{},
		Name: name, SourceName: sourceName, Group: source.Group,
		Link: validation.Link, Protocol: validation.Protocol, Address: validation.Address,
		Port: validation.Port, Params: model.JSONText(params), Enabled: source.Enabled,
		HiddenBySubscriptionRule: hidden,
		SortOrder:                source.BaseSortOrder + index, SubscriptionID: &source.ID, SubscriptionKey: key,
	}, key, nil
}

func customNodeSubscriptionBaseKey(item singbox.ImportedNode) (string, error) {
	// Providers normally keep display names stable while rotating endpoint or
	// credentials. Matching by protocol+name preserves the row's user audience
	// through those rotations. A rename is treated as a remove+add, which matches
	// the subscription's visible node list.
	canonical := struct {
		Protocol string `json:"protocol"`
		Name     string `json:"name"`
	}{
		Protocol: strings.ToLower(strings.TrimSpace(item.Protocol)),
		Name:     strings.TrimSpace(item.Name),
	}
	if canonical.Name == "" {
		canonical.Name = fmt.Sprintf("%s %s:%d", canonical.Protocol, strings.ToLower(strings.TrimSpace(item.Address)), item.Port)
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func customNodeSubscriptionDefinitionKey(item singbox.ImportedNode) (string, error) {
	canonical := struct {
		Name     string         `json:"name"`
		Link     string         `json:"link,omitempty"`
		Protocol string         `json:"protocol"`
		Address  string         `json:"address"`
		Port     int            `json:"port"`
		Params   map[string]any `json:"params,omitempty"`
	}{
		Name: strings.TrimSpace(item.Name), Link: strings.TrimSpace(item.Link),
		Protocol: strings.ToLower(strings.TrimSpace(item.Protocol)),
		Address:  strings.ToLower(strings.TrimSpace(item.Address)), Port: item.Port, Params: item.Params,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func customNodeSubscriptionKey(item singbox.ImportedNode, occurrence int) (string, error) {
	base, err := customNodeSubscriptionBaseKey(item)
	if err != nil {
		return "", err
	}
	if occurrence == 0 {
		return base, nil
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", base, occurrence)))
	return hex.EncodeToString(sum[:]), nil
}
