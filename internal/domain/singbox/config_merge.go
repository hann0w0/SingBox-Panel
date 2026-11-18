package singbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// mergeConfigValue applies changes between two panel projections to the
// original value. Object members are compared recursively; sequences must be
// matched by their caller before invoking this function.
func mergeConfigValue(original, before, after any) any {
	if reflect.DeepEqual(before, after) {
		return original
	}
	if oldList, ok := before.([]any); ok {
		if newList, ok := after.([]any); ok {
			if source, ok := original.([]any); ok {
				if merged, ok := mergeConfigObjectList(source, oldList, newList); ok {
					return merged
				}
			}
		}
		if newList, ok := after.([]any); ok && len(oldList) == len(newList) {
			if source, ok := original.([]any); ok && len(source) >= len(oldList) {
				result := append([]any(nil), source...)
				for i := range oldList {
					result[i] = mergeConfigValue(source[i], oldList[i], newList[i])
				}
				return result
			}
		}
	}
	oldObject, oldOK := before.(map[string]any)
	newObject, newOK := after.(map[string]any)
	if !newOK {
		return after
	}
	if !oldOK {
		oldObject = map[string]any{}
	}
	result := make(map[string]any)
	if originalObject, ok := original.(map[string]any); ok {
		for key, value := range originalObject {
			result[key] = value
		}
	}
	for key, oldValue := range oldObject {
		newValue, exists := newObject[key]
		if !exists {
			delete(result, key)
			continue
		}
		if reflect.DeepEqual(oldValue, newValue) {
			continue
		}
		result[key] = mergeConfigValue(result[key], oldValue, newValue)
	}
	for key, newValue := range newObject {
		if _, exists := oldObject[key]; !exists {
			result[key] = mergeConfigValue(result[key], nil, newValue)
		}
	}
	return result
}

// Object arrays with a shared identity can grow or reorder without losing
// custom members on existing entries. Scalar arrays remain a single form field.
func mergeConfigObjectList(source, before, after []any) ([]any, bool) {
	for _, key := range []string{"tag", "name", "id", "username"} {
		ids := func(values []any) ([]string, bool) {
			var result []string
			seen := make(map[string]bool)
			for _, value := range values {
				object, ok := value.(map[string]any)
				if !ok {
					return nil, false
				}
				id, ok := object[key].(string)
				if !ok || id == "" || seen[id] {
					return nil, false
				}
				seen[id] = true
				result = append(result, id)
			}
			return result, true
		}
		oldIDs, oldOK := ids(before)
		newIDs, newOK := ids(after)
		if !oldOK || !newOK {
			continue
		}
		oldByID := make(map[string]int)
		for i, id := range oldIDs {
			oldByID[id] = i
		}
		shared := false
		for _, id := range newIDs {
			if _, ok := oldByID[id]; ok {
				shared = true
			}
		}
		if !shared {
			continue
		}
		var slots []configMergeSlot
		for i, value := range source {
			object, _ := value.(map[string]any)
			id, _ := object[key].(string)
			index, known := oldByID[id]
			if key == "name" && len(before) == 1 && oldIDs[0] == "user" {
				// SingleUserIdentity projects the first manual credential with
				// a canonical name, even when other raw users remain present.
				known = i == 0
				index = 0
				id = oldIDs[0]
			} else if !known && len(source) == len(before) {
				index = i
				id = oldIDs[i]
				known = true
			}
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, false
			}
			slot := configMergeSlot{Original: raw}
			if known {
				slot.ID = id
				slot.Baseline, _ = json.Marshal(before[index])
			}
			slots = append(slots, slot)
		}
		var current []configMergeItem
		for i, value := range after {
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, false
			}
			current = append(current, configMergeItem{ID: newIDs[i], Projection: raw})
		}
		merged, err := mergeConfigSequence(slots, current)
		if err != nil {
			return nil, false
		}
		var result []any
		for _, entry := range merged {
			var value any
			decoder := json.NewDecoder(bytes.NewReader(entry))
			decoder.UseNumber()
			if decoder.Decode(&value) != nil {
				return nil, false
			}
			result = append(result, value)
		}
		return result, true
	}
	return nil, false
}

// mergeConfigJSON compares projections made using the same version of the
// renderer, preserving original members untouched by an actual panel change.
func mergeConfigJSON(original, before, after []byte) ([]byte, error) {
	decode := func(raw []byte) (any, error) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return value, nil
	}
	originalValue, err := decode(original)
	if err != nil {
		return nil, fmt.Errorf("original config: %w", err)
	}
	beforeValue, err := decode(before)
	if err != nil {
		return nil, fmt.Errorf("previous projection: %w", err)
	}
	afterValue, err := decode(after)
	if err != nil {
		return nil, fmt.Errorf("current projection: %w", err)
	}
	return json.Marshal(mergeConfigValue(originalValue, beforeValue, afterValue))
}

// configMergeSlot links an imported array entry to its persistent panel row.
// An empty ID represents an entry that the panel cannot edit.
type configMergeSlot struct {
	ID       string
	Original json.RawMessage
	Baseline json.RawMessage
}

type configMergeItem struct {
	ID         string
	Projection json.RawMessage
}

// mergeConfigSequence matches editable entries by stable identity. Unknown
// entries keep their slots and relative order while editable entries fill the
// surviving editable slots in the requested order. New entries are inserted
// before their next editable neighbour, or appended when there is none.
func mergeConfigSequence(slots []configMergeSlot, current []configMergeItem) ([]json.RawMessage, error) {
	old := make(map[string]configMergeSlot)
	for _, slot := range slots {
		if slot.ID == "" {
			continue
		}
		if _, exists := old[slot.ID]; exists {
			return nil, fmt.Errorf("duplicate imported entry %q", slot.ID)
		}
		old[slot.ID] = slot
	}
	active := make(map[string]bool)
	for _, item := range current {
		if item.ID == "" || active[item.ID] {
			return nil, fmt.Errorf("invalid or duplicate current entry %q", item.ID)
		}
		active[item.ID] = true
	}
	var merged []json.RawMessage
	for _, item := range current {
		if !json.Valid(item.Projection) {
			return nil, fmt.Errorf("invalid current entry %q", item.ID)
		}
		value := item.Projection
		if slot, exists := old[item.ID]; exists {
			merged, err := mergeConfigJSON(slot.Original, slot.Baseline, item.Projection)
			if err != nil {
				return nil, fmt.Errorf("entry %q: %w", item.ID, err)
			}
			value = merged
		}
		merged = append(merged, value)
	}
	result := []json.RawMessage{}
	next := 0
	for _, slot := range slots {
		if slot.ID == "" {
			if !json.Valid(slot.Original) {
				return nil, fmt.Errorf("invalid preserved array entry")
			}
			result = append(result, slot.Original)
			continue
		}
		if !active[slot.ID] {
			continue
		}
		for next < len(current) {
			id := current[next].ID
			result = append(result, merged[next])
			next++
			if _, imported := old[id]; imported {
				break
			}
		}
	}
	result = append(result, merged[next:]...)
	return result, nil
}
