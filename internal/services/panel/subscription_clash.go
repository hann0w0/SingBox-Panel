package panel

import (
	"sort"

	"github.com/goccy/go-yaml"
)

func marshalClashProxiesYAML(proxies []map[string]any) ([]byte, error) {
	ordered := make([]yaml.MapSlice, 0, len(proxies))
	for _, proxy := range proxies {
		ordered = append(ordered, orderedClashProxyMap(proxy))
	}
	doc := yaml.MapSlice{{Key: "proxies", Value: ordered}}
	return yaml.MarshalWithOptions(doc, yaml.IndentSequence(true))
}

func orderedClashProxyMap(proxy map[string]any) yaml.MapSlice {
	ordered := make(yaml.MapSlice, 0, len(proxy))
	written := make(map[string]struct{}, len(proxy))
	appendKey := func(key string) {
		value, ok := proxy[key]
		if !ok {
			return
		}
		ordered = append(ordered, yaml.MapItem{Key: key, Value: value})
		written[key] = struct{}{}
	}

	for _, key := range clashProxyLeadingKeys {
		appendKey(key)
	}

	middleKeys := make([]string, 0, len(proxy))
	for key := range proxy {
		if _, ok := written[key]; ok {
			continue
		}
		if clashProxyKeyListed(clashProxyTrailingKeys, key) {
			continue
		}
		middleKeys = append(middleKeys, key)
	}
	sort.Strings(middleKeys)
	for _, key := range middleKeys {
		appendKey(key)
	}

	for _, key := range clashProxyTrailingKeys {
		appendKey(key)
	}
	return ordered
}

func clashProxyKeyListed(keys []string, target string) bool {
	for _, key := range keys {
		if key == target {
			return true
		}
	}
	return false
}

func clashProxies(nodes []node) (proxies []map[string]any, names []string) {
	seen := map[string]int{}
	for _, n := range nodes {
		p := clashProxy(n, seen)
		if p == nil {
			continue
		}
		proxies = append(proxies, p)
		names = append(names, p["name"].(string))
	}
	return proxies, names
}

func shadowrocketProxies(nodes []node) (proxies []map[string]any, names []string) {
	seen := map[string]int{}
	for _, n := range nodes {
		p := clashProxy(n, seen)
		if p == nil {
			continue
		}
		// Shadowrocket supports Snell v6's mode field. It is intentionally
		// added here instead of clashProxy so the regular Clash output keeps
		// its existing protocol compatibility contract.
		if n.typ == "snell" && n.settings.SnellVersion == 6 && n.settings.SnellMode != "" && n.settings.SnellMode != "default" {
			p["mode"] = n.settings.SnellMode
		}
		proxies = append(proxies, p)
		names = append(names, p["name"].(string))
	}
	return proxies, names
}
