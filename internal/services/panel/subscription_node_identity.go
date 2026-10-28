package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// matchSubscriptionNodeKeys retains row identities before the database upsert.
// Occurrence positions are only initial keys: provider reordering or removal
// must not transfer an individually granted endpoint to another row's users.
func matchSubscriptionNodeKeys(existing []model.CustomNode, desired map[string]model.CustomNode, order []string) (map[string]model.CustomNode, []string, error) {
	type identity struct{ group, definition string }
	identify := func(row model.CustomNode) (identity, error) {
		name := row.SourceName
		if name == "" {
			name = row.Name
		}
		item := singbox.ImportedNode{Name: name, Protocol: row.Protocol, Address: row.Address, Port: row.Port}
		if len(row.Params) > 0 {
			if err := json.Unmarshal(row.Params, &item.Params); err != nil {
				return identity{}, fmt.Errorf("节点 %q 参数无法识别: %w", name, err)
			}
		}
		// Older URI edits could clear the imported structured fields. Recover the
		// same canonical definition before matching so existing grants survive.
		if item.Protocol == "" || item.Address == "" || item.Port == 0 {
			if row.Link != "" {
				parsed, err := singbox.ParseSubscription([]byte(row.Link))
				if err == nil && len(parsed.Nodes) == 1 && len(parsed.Skipped) == 0 {
					item = parsed.Nodes[0]
					item.Name = name
					item.Link = ""
				} else {
					item.Link = row.Link
				}
			}
		}
		group, err := customNodeSubscriptionBaseKey(item)
		if err != nil {
			return identity{}, err
		}
		definition, err := customNodeSubscriptionDefinitionKey(item)
		return identity{group: group, definition: definition}, err
	}

	byDefinition := make(map[string][]string, len(existing))
	existingGroups := make(map[string][]string)
	reserved := make(map[string]bool, len(existing)+len(desired))
	for _, row := range existing {
		id, err := identify(row)
		if err != nil {
			return nil, nil, err
		}
		byDefinition[id.definition] = append(byDefinition[id.definition], row.SubscriptionKey)
		existingGroups[id.group] = append(existingGroups[id.group], row.SubscriptionKey)
		reserved[row.SubscriptionKey] = true
	}
	desiredIDs := make(map[string]identity, len(desired))
	desiredGroupCounts := make(map[string]int)
	for _, key := range order {
		id, err := identify(desired[key])
		if err != nil {
			return nil, nil, err
		}
		desiredIDs[key] = id
		desiredGroupCounts[id.group]++
	}

	matched := make(map[string]string, len(desired))
	for _, key := range order {
		definition := desiredIDs[key].definition
		if candidates := byDefinition[definition]; len(candidates) > 0 {
			matched[key] = candidates[0]
			byDefinition[definition] = candidates[1:]
		}
	}
	result := make(map[string]model.CustomNode, len(desired))
	resultOrder := make([]string, 0, len(order))
	for _, key := range order {
		id := desiredIDs[key]
		nextKey := matched[key]
		if nextKey == "" && len(existingGroups[id.group]) == 1 && desiredGroupCounts[id.group] == 1 {
			// A unique provider name may rotate its address or credentials while
			// remaining the same logical node. Duplicate names cannot prove that.
			nextKey = existingGroups[id.group][0]
		}
		if nextKey == "" {
			nextKey = key
			for suffix := 0; reserved[nextKey]; suffix++ {
				// Keep stale keys reserved even for complete refreshes: partial
				// parses retain stale rows and inserts precede destructive cleanup.
				sum := sha256.Sum256([]byte(fmt.Sprintf("definition:%s:%d", id.definition, suffix)))
				nextKey = hex.EncodeToString(sum[:])
			}
		}
		reserved[nextKey] = true
		row := desired[key]
		row.SubscriptionKey = nextKey
		result[nextKey] = row
		resultOrder = append(resultOrder, nextKey)
	}
	return result, resultOrder, nil
}
