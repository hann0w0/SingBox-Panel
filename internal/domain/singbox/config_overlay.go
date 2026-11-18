package singbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// ConfigProjection keeps model inputs, not rendered JSON. Re-rendering both
// sides with the same code prevents a renderer upgrade from becoming an edit.
type ConfigProjection struct {
	Input ServerConfigInput   `json:"input"`
	IDs   map[string][]string `json:"ids"`
	// RuleInput.Outbound is intentionally excluded from stored Match JSON.
	RuleTargets []string `json:"rule_targets"`
}

type ConfigOverlay struct {
	Version  int                 `json:"version"`
	Original json.RawMessage     `json:"original"`
	Baseline ConfigProjection    `json:"baseline"`
	Bindings map[string][]string `json:"bindings"`
}

var overlayPaths = []string{"inbounds", "outbounds", "rules", "rule_set", "services", "http_clients"}

func HasConfigOverlay(encoded []byte) bool {
	value := bytes.TrimSpace(encoded)
	return len(value) > 0 && !bytes.Equal(value, []byte("null"))
}

func projectionObjects(p ConfigProjection) (map[string]json.RawMessage, map[string][]configMergeItem, error) {
	p.Input.ProjectionOnly = true
	for i, target := range p.RuleTargets {
		if i < len(p.Input.Rules) {
			p.Input.Rules[i].Outbound = target
		}
	}
	raw, err := BuildServerConfig(p.Input)
	if err != nil {
		return nil, nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, nil, err
	}
	identities := make(map[string]map[string]string)
	identities["inbounds"] = make(map[string]string)
	identities["outbounds"] = make(map[string]string)
	identities["rule_set"] = make(map[string]string)
	for i, in := range p.Input.Inbounds {
		if i >= len(p.IDs["inbounds"]) {
			return nil, nil, fmt.Errorf("missing inbound identity")
		}
		id := p.IDs["inbounds"][i]
		identities["inbounds"][in.Tag] = id
		if in.Settings.RealityGuardEnabled() {
			identities["inbounds"][in.Settings.RealityGuardTag(in.Tag)] = "guard:" + id
		}
	}
	for i, ob := range p.Input.Outbounds {
		if i >= len(p.IDs["outbounds"]) {
			return nil, nil, fmt.Errorf("missing outbound identity")
		}
		identities["outbounds"][ob.Tag] = p.IDs["outbounds"][i]
	}
	for i, rs := range p.Input.RuleSets {
		if i >= len(p.IDs["rule_set"]) {
			return nil, nil, fmt.Errorf("missing rule set identity")
		}
		identities["rule_set"][rs.Tag] = p.IDs["rule_set"][i]
	}
	var ruleIDs []string
	for i, in := range p.Input.Inbounds {
		if in.Settings.RealityGuardEnabled() {
			for n := 0; n < 3; n++ {
				ruleIDs = append(ruleIDs, fmt.Sprintf("guard:%s:%d", p.IDs["inbounds"][i], n))
			}
		}
	}
	if p.Input.StatsController != "" {
		ruleIDs = append(ruleIDs, "stats:loopback")
	}
	var rules []json.RawMessage
	if err := json.Unmarshal(configArray(root, "rules"), &rules); err != nil {
		return nil, nil, err
	}
	if len(rules) > len(ruleIDs)+len(p.Input.Rules) {
		ruleIDs = append(ruleIDs, "auto:sniff")
	}
	ruleIDs = append(ruleIDs, p.IDs["rules"]...)
	if len(ruleIDs) != len(rules) {
		return nil, nil, fmt.Errorf("rule identities do not match projection")
	}
	items := make(map[string][]configMergeItem)
	for _, path := range overlayPaths {
		var entries []json.RawMessage
		if err := json.Unmarshal(configArray(root, path), &entries); err != nil {
			return nil, nil, err
		}
		seen := make(map[string]bool)
		for i, entry := range entries {
			id := ""
			if path == "rules" {
				id = ruleIDs[i]
			} else {
				var object struct {
					Tag string `json:"tag"`
				}
				if err := json.Unmarshal(entry, &object); err != nil {
					return nil, nil, err
				}
				id = identities[path][object.Tag]
				if id == "" {
					id = "generated:" + object.Tag
				}
			}
			if seen[id] {
				return nil, nil, fmt.Errorf("duplicate %s identity %q", path, id)
			}
			seen[id] = true
			items[path] = append(items[path], configMergeItem{ID: id, Projection: entry})
		}
	}
	return root, items, nil
}

func configArray(root map[string]json.RawMessage, path string) json.RawMessage {
	if path == "rules" || path == "rule_set" {
		var route map[string]json.RawMessage
		_ = json.Unmarshal(root["route"], &route)
		if value := route[path]; len(value) > 0 {
			return value
		}
	} else if value := root[path]; len(value) > 0 {
		return value
	}
	return json.RawMessage(`[]`)
}

func hasConfigArray(root map[string]json.RawMessage, path string) bool {
	if path == "rules" || path == "rule_set" {
		var route map[string]json.RawMessage
		_ = json.Unmarshal(root["route"], &route)
		_, ok := route[path]
		return ok
	}
	_, ok := root[path]
	return ok
}

func setConfigArray(root map[string]json.RawMessage, path string, value json.RawMessage) {
	if path == "rules" || path == "rule_set" {
		var route map[string]json.RawMessage
		_ = json.Unmarshal(root["route"], &route)
		if route == nil && value == nil {
			return
		}
		if route == nil {
			route = make(map[string]json.RawMessage)
		}
		if value == nil {
			delete(route, path)
		} else {
			route[path] = value
		}
		root["route"], _ = json.Marshal(route)
	} else if value == nil {
		delete(root, path)
	} else {
		root[path] = value
	}
}

// NewConfigOverlay binds recognized imported objects to persistent row IDs;
// unrecognized entries deliberately have no binding and pass through whole.
func NewConfigOverlay(raw []byte, baseline ConfigProjection, ruleSources map[int]string) (ConfigOverlay, error) {
	_, items, err := projectionObjects(baseline)
	if err != nil {
		return ConfigOverlay{}, err
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal(raw, &original); err != nil {
		return ConfigOverlay{}, err
	}
	if original == nil {
		return ConfigOverlay{}, fmt.Errorf("config must be a JSON object")
	}
	o := ConfigOverlay{Version: 1, Original: append([]byte(nil), raw...), Baseline: baseline, Bindings: make(map[string][]string)}
	for _, path := range overlayPaths {
		var entries []json.RawMessage
		if err := json.Unmarshal(configArray(original, path), &entries); err != nil {
			return ConfigOverlay{}, err
		}
		byTag := make(map[string]string)
		for _, item := range items[path] {
			var obj struct {
				Tag string `json:"tag"`
			}
			_ = json.Unmarshal(item.Projection, &obj)
			if obj.Tag != "" {
				byTag[obj.Tag] = item.ID
			}
		}
		for i, entry := range entries {
			id := ""
			if path == "rules" {
				id = ruleSources[i]
				if id == "" {
					for _, item := range items[path] {
						// Only generated rules are inferred by content; editable
						// rules have an explicit parser source index.
						if len(item.ID) > 0 && !isPanelRuleID(item.ID, baseline.IDs["rules"]) && canonicalRaw(entry) == canonicalRaw(item.Projection) {
							id = item.ID
							break
						}
					}
				}
			} else {
				var obj struct {
					Tag string `json:"tag"`
				}
				_ = json.Unmarshal(entry, &obj)
				if obj.Tag != "" {
					id = byTag[obj.Tag]
				}
			}
			o.Bindings[path] = append(o.Bindings[path], id)
		}
	}
	return o, nil
}

// NewRenderedConfigOverlay upgrades the previous residual representation.
// Its rule array already has exactly the renderer's order and prefix rules.
func NewRenderedConfigOverlay(raw []byte, baseline ConfigProjection) (ConfigOverlay, error) {
	_, items, err := projectionObjects(baseline)
	if err != nil {
		return ConfigOverlay{}, err
	}
	sources := make(map[int]string)
	for i, item := range items["rules"] {
		sources[i] = item.ID
	}
	return NewConfigOverlay(raw, baseline, sources)
}

func isPanelRuleID(id string, ids []string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func canonicalRaw(raw []byte) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return ""
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func ApplyConfigOverlay(encoded []byte, current ConfigProjection) ([]byte, error) {
	var overlay ConfigOverlay
	if err := json.Unmarshal(encoded, &overlay); err != nil {
		return nil, err
	}
	if overlay.Version != 1 {
		return nil, fmt.Errorf("unsupported config overlay version %d", overlay.Version)
	}
	before, oldItems, err := projectionObjects(overlay.Baseline)
	if err != nil {
		return nil, err
	}
	after, newItems, err := projectionObjects(current)
	if err != nil {
		return nil, err
	}
	unchanged := reflect.DeepEqual(before, after) && reflect.DeepEqual(oldItems, newItems)
	var original map[string]json.RawMessage
	if err := json.Unmarshal(overlay.Original, &original); err != nil {
		return nil, err
	}
	if original == nil {
		return nil, fmt.Errorf("preserved config must be a JSON object")
	}
	mergedArrays := make(map[string]json.RawMessage)
	for _, path := range overlayPaths {
		var entries []json.RawMessage
		if err := json.Unmarshal(configArray(original, path), &entries); err != nil {
			return nil, err
		}
		if len(entries) != len(overlay.Bindings[path]) {
			return nil, fmt.Errorf("invalid %s bindings", path)
		}
		baseline := make(map[string]json.RawMessage)
		for _, item := range oldItems[path] {
			baseline[item.ID] = item.Projection
		}
		var slots []configMergeSlot
		bound := make(map[string]bool)
		for i, entry := range entries {
			id := overlay.Bindings[path][i]
			if id != "" && baseline[id] == nil {
				return nil, fmt.Errorf("missing %s baseline %s", path, id)
			}
			slots = append(slots, configMergeSlot{ID: id, Original: entry, Baseline: baseline[id]})
			bound[id] = true
		}
		var active []configMergeItem
		for _, item := range newItems[path] {
			// A renderer default absent from the original is not a user edit.
			if !bound[item.ID] && canonicalRaw(baseline[item.ID]) == canonicalRaw(item.Projection) {
				continue
			}
			active = append(active, item)
		}
		values, err := mergeConfigSequence(slots, active)
		if err != nil {
			return nil, fmt.Errorf("merge %s: %w", path, err)
		}
		if len(values) > 0 || hasConfigArray(original, path) {
			mergedArrays[path], err = json.Marshal(values)
			if err != nil {
				return nil, err
			}
		}
		setConfigArray(original, path, nil)
		setConfigArray(before, path, nil)
		setConfigArray(after, path, nil)
	}
	if unchanged {
		return append([]byte(nil), overlay.Original...), nil
	}
	originalJSON, _ := json.Marshal(original)
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	merged, err := mergeConfigJSON(originalJSON, beforeJSON, afterJSON)
	if err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(merged, &root); err != nil {
		return nil, err
	}
	for _, path := range overlayPaths {
		if value := mergedArrays[path]; value != nil {
			setConfigArray(root, path, value)
		}
	}
	return json.MarshalIndent(root, "", "  ")
}

func OverlayHasOutbound(encoded []byte, tag string) bool {
	var overlay ConfigOverlay
	if json.Unmarshal(encoded, &overlay) != nil {
		return false
	}
	var root struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if json.Unmarshal(overlay.Original, &root) != nil {
		return false
	}
	for i, item := range root.Outbounds {
		if item.Tag == tag && i < len(overlay.Bindings["outbounds"]) && overlay.Bindings["outbounds"][i] == "" {
			return true
		}
	}
	return false
}

// UnmanagedConfigEntries returns whole imported entries with no panel row.
// Identity/port validation must account for these invisible objects as well.
func UnmanagedConfigEntries(encoded []byte, section string) ([]json.RawMessage, error) {
	if !HasConfigOverlay(encoded) {
		return nil, nil
	}
	var overlay ConfigOverlay
	if err := json.Unmarshal(encoded, &overlay); err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(overlay.Original, &root); err != nil {
		return nil, err
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(configArray(root, section), &entries); err != nil {
		return nil, err
	}
	if len(entries) != len(overlay.Bindings[section]) {
		return nil, fmt.Errorf("invalid %s bindings", section)
	}
	var result []json.RawMessage
	for i, entry := range entries {
		if overlay.Bindings[section][i] == "" {
			result = append(result, entry)
		}
	}
	return result, nil
}

// PreservedConfigReferences finds conventional sing-box reference fields in
// data outside the panel projection. They cannot be rewritten by a form edit,
// so callers must keep their targets or reject deleting/renaming a target.
func PreservedConfigReferences(encoded []byte, key string, activeIDs ...map[string][]string) ([]string, error) {
	if !HasConfigOverlay(encoded) {
		return nil, nil
	}
	var overlay ConfigOverlay
	if err := json.Unmarshal(encoded, &overlay); err != nil {
		return nil, err
	}
	baseline, items, err := projectionObjects(overlay.Baseline)
	if err != nil {
		return nil, err
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal(overlay.Original, &original); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var scan func(any, any)
	scan = func(source, model any) {
		switch source := source.(type) {
		case map[string]any:
			projection, _ := model.(map[string]any)
			for field, value := range source {
				modelValue, owned := projection[field]
				if field == key && !owned {
					switch value := value.(type) {
					case string:
						seen[value] = true
					case []any:
						for _, entry := range value {
							if tag, ok := entry.(string); ok {
								seen[tag] = true
							}
						}
					}
				}
				scan(value, modelValue)
			}
		case []any:
			projection, _ := model.([]any)
			for i, value := range source {
				var modelValue any
				if i < len(projection) {
					modelValue = projection[i]
				}
				scan(value, modelValue)
			}
		}
	}
	compare := func(source, projection []byte) {
		var a, b any
		_ = json.Unmarshal(source, &a)
		_ = json.Unmarshal(projection, &b)
		scan(a, b)
	}
	for _, path := range overlayPaths {
		var entries []json.RawMessage
		if err := json.Unmarshal(configArray(original, path), &entries); err != nil {
			return nil, err
		}
		if len(entries) != len(overlay.Bindings[path]) {
			return nil, fmt.Errorf("invalid %s bindings", path)
		}
		byID := make(map[string]json.RawMessage)
		for _, item := range items[path] {
			byID[item.ID] = item.Projection
		}
		for i, entry := range entries {
			id := overlay.Bindings[path][i]
			if len(activeIDs) > 0 && id != "" && isPanelRuleID(id, overlay.Baseline.IDs[path]) && !isPanelRuleID(id, activeIDs[0][path]) {
				continue
			}
			compare(entry, byID[overlay.Bindings[path][i]])
		}
		setConfigArray(original, path, nil)
		setConfigArray(baseline, path, nil)
	}
	a, _ := json.Marshal(original)
	b, _ := json.Marshal(baseline)
	compare(a, b)
	var result []string
	for tag := range seen {
		result = append(result, tag)
	}
	return result, nil
}
