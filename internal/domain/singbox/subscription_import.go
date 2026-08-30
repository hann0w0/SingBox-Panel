package singbox

// This file contains the deliberately small, dependency-light subscription
// importer used by the admin API. It accepts plain or base64 encoded share-link
// lists, Clash/Mihomo YAML, Surge/Loon INI, Quantumult X server_local INI,
// sing-box JSON, V2Ray/Xray JSON, and SIP008 Shadowsocks JSON.
// Network fetching is intentionally kept in the panel package: this parser
// only consumes bytes and therefore cannot accidentally turn a node payload
// into an SSRF primitive.

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// SubscriptionImportMaxBytes is the maximum payload accepted by the
	// format parser. The HTTP endpoint applies the same limit before calling
	// ParseSubscription, so this also protects callers which use the parser
	// directly.
	SubscriptionImportMaxBytes = 4 << 20
	SubscriptionImportMaxNodes = 512
)

// ImportedNode is the storage-neutral representation returned by the
// subscription parser. Link is retained only when the input was already a
// standard share URI. Structured formats intentionally leave Link empty and
// carry every recognized option in Params; this prevents a lossy canonical
// link conversion (for example, Clash plugin options or UDP flags).
type ImportedNode struct {
	Name     string         `json:"name"`
	Link     string         `json:"link,omitempty"`
	Protocol string         `json:"protocol"`
	Address  string         `json:"address"`
	Port     int            `json:"port"`
	Params   map[string]any `json:"params,omitempty"`
}

// ImportIssue describes one item that could not be parsed. A malformed item
// does not discard otherwise valid nodes from the same subscription.
type ImportIssue struct {
	Input string `json:"input"`
	Error string `json:"error"`
}

// SubscriptionParseResult is the result of ParseSubscription.
type SubscriptionParseResult struct {
	Nodes      []ImportedNode `json:"nodes"`
	Skipped    []ImportIssue  `json:"skipped"`
	SourceType string         `json:"source_type"`
}

// ParseSubscription parses a plain-text or base64 share-link list, Clash/
// Mihomo YAML, Surge/Loon INI, Quantumult X server_local INI, sing-box JSON,
// V2Ray/Xray JSON, or SIP008 Shadowsocks JSON payload. It never performs
// network I/O. The returned skipped items are suitable for showing in an
// admin preview.
func ParseSubscription(raw []byte) (SubscriptionParseResult, error) {
	if len(raw) > SubscriptionImportMaxBytes {
		return SubscriptionParseResult{}, fmt.Errorf("订阅内容超过 %d 字节限制", SubscriptionImportMaxBytes)
	}
	raw = bytes.TrimSpace(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}))
	if len(raw) == 0 {
		return SubscriptionParseResult{}, fmt.Errorf("订阅内容为空")
	}
	return parseSubscriptionDepth(raw, 0)
}

func parseSubscriptionDepth(raw []byte, depth int) (SubscriptionParseResult, error) {
	if depth > 2 {
		return SubscriptionParseResult{}, fmt.Errorf("订阅编码嵌套层数过深")
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}))
	if len(trimmed) == 0 {
		return SubscriptionParseResult{}, fmt.Errorf("订阅内容为空")
	}

	// A Surge profile has a distinctive section header — or, for providers that
	// ship a bare server list, at least one Surge-shaped proxy line. Parse it
	// before YAML so comments and arbitrary INI keys cannot be mistaken for
	// URI lines.
	if hasSurgeProxySection(trimmed) || looksLikeSurgeProxies(trimmed) {
		return parseSurgeSubscription(trimmed)
	}

	// Clash profiles are YAML mappings with a top-level proxies sequence. We
	// only claim the YAML format when that key is present; this lets a YAML-ish
	// error payload still produce useful per-line diagnostics below.
	if isClashYAML(trimmed) {
		return parseClashSubscription(trimmed)
	}

	// Quantumult X stores inline nodes in [server_local] as protocol=host:port
	// lines. Only parse that local section; server_remote entries are references
	// to another subscription and must not trigger a second network fetch here.
	if hasQuantumultServerSection(trimmed) {
		return parseQuantumultSubscription(trimmed)
	}

	// Some subscription services negotiate a sing-box JSON profile from the
	// request headers. It is still a node subscription when it contains a
	// top-level outbounds array; parse it before the generic line parser so the
	// JSON itself is not reported as one malformed share link.
	if isSingBoxJSON(trimmed) {
		return parseSingBoxSubscription(trimmed)
	}

	// V2Ray/Xray profiles use a different JSON schema: each outbound has a
	// protocol field and keeps its server credentials under settings. Handle
	// those profiles separately instead of treating them as sing-box JSON.
	if isV2RayJSON(trimmed) {
		return parseV2RayJSONSubscription(trimmed)
	}

	// SIP008 is the standardized JSON subscription format for Shadowsocks.
	// It may be either a top-level array or an object containing `servers`.
	if isSIP008JSON(trimmed) {
		return parseSIP008Subscription(trimmed)
	}

	// Providers generally base64-encode a newline-separated URI list. Decode
	// only strings which look like base64 and recurse once; ordinary URI lists
	// go straight to line parsing. Both padded and raw URL alphabets are used
	// in the wild.
	if decoded, ok := decodeSubscriptionBase64(trimmed); ok {
		result, err := parseSubscriptionDepth(decoded, depth+1)
		if err == nil {
			if result.SourceType == "plain" || result.SourceType == "links" {
				result.SourceType = "base64"
			}
			return result, nil
		}
		// A base64-looking value that does not decode to a recognized profile is
		// reported as an item below rather than returning an opaque decoder error.
	}

	return parseLinkLines(trimmed)
}

func hasSurgeProxySection(raw []byte) bool {
	s := strings.ToLower(string(raw))
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "[proxy]" || line == "[proxies]" {
			return true
		}
	}
	return false
}

// surgeProxyTypes are the proxy type tokens Surge emits as the first field of
// a [Proxy] line. They are also the tokens recognized when a provider ships a
// "naked" proxy list without any [Proxy] section header.
var surgeProxyTypes = map[string]bool{
	"ss": true, "shadowsocks": true,
	"vmess": true, "vless": true,
	"trojan":    true,
	"hysteria2": true, "hy2": true, "hysteria": true,
	"tuic": true, "tuic-v4": true, "tuic-v5": true,
	"anytls": true,
	"socks5": true, "socks": true,
	"snell": true,
}

// isSurgeProxyLine reports whether a non-comment line has the shape
// "name = <known-type>, server, port, ...". It is used both to detect
// sectionless Surge profiles and to let parseSurgeSubscription accept proxy
// lines outside an explicit [Proxy]/[Proxies] header (common with providers
// that emit a bare server list).
func isSurgeProxyLine(line string) bool {
	i := strings.IndexByte(line, '=')
	if i < 0 {
		return false
	}
	rest := strings.TrimSpace(line[i+1:])
	if j := strings.IndexByte(rest, ','); j >= 0 {
		rest = rest[:j]
	}
	return surgeProxyTypes[strings.ToLower(strings.TrimSpace(rest))]
}

// looksLikeSurgeProxies reports whether the payload contains at least one
// Surge-shaped proxy line (with or without a [Proxy] section header). It
// deliberately ignores section headers so a bare server list is still
// recognized, and it never matches YAML (colon key syntax) or share URIs.
func looksLikeSurgeProxies(raw []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), SubscriptionImportMaxBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if isSurgeProxyLine(line) {
			return true
		}
	}
	return false
}

func isClashYAML(raw []byte) bool {
	var root map[string]any
	if err := yaml.Unmarshal(raw, &root); err != nil || root == nil {
		return false
	}
	for k, v := range root {
		if normalizeImportKey(k) != "proxies" {
			continue
		}
		switch v.(type) {
		case []any, []map[string]any:
			return true
		default:
			return false
		}
	}
	return false
}

func isSingBoxJSON(raw []byte) bool {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(root["outbounds"], &items); err != nil {
		return false
	}
	for _, item := range items {
		var m map[string]json.RawMessage
		if json.Unmarshal(item, &m) == nil {
			if _, ok := m["type"]; ok {
				return true
			}
		}
	}
	return false
}

func isV2RayJSON(raw []byte) bool {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(root["outbounds"], &items); err != nil {
		return false
	}
	for _, item := range items {
		var m map[string]json.RawMessage
		if json.Unmarshal(item, &m) == nil {
			if _, ok := m["protocol"]; ok {
				return true
			}
		}
	}
	return false
}

func hasQuantumultServerSection(raw []byte) bool {
	for _, line := range strings.Split(strings.ToLower(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "[server_local]" || line == "[server-local]" {
			return true
		}
	}
	return false
}

func isSIP008JSON(raw []byte) bool {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return false
	}
	items := root
	if object, ok := root.(map[string]any); ok {
		items = object["servers"]
	}
	list, ok := items.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	for _, item := range list {
		m := yamlMap(item)
		if m != nil && getString(m, "server") != "" && getInt(m, "server_port") > 0 && getString(m, "method") != "" && getString(m, "password") != "" {
			return true
		}
	}
	return false
}

// parseSingBoxSubscription imports client outbounds from a sing-box JSON
// profile. Built-in and group outbounds are intentionally skipped because they
// are routing objects, not standalone nodes that can be assigned to a user.
func parseSingBoxSubscription(raw []byte) (SubscriptionParseResult, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return SubscriptionParseResult{}, fmt.Errorf("sing-box JSON 解析失败: %w", err)
	}
	items, ok := root["outbounds"].([]any)
	if !ok {
		return SubscriptionParseResult{}, errors.New("sing-box JSON 未找到 outbounds 节点")
	}
	result := SubscriptionParseResult{SourceType: "sing-box-json"}
	for _, item := range items {
		if len(result.Nodes) >= SubscriptionImportMaxNodes {
			result.Skipped = append(result.Skipped, ImportIssue{Error: "节点数量超过限制"})
			continue
		}
		m, ok := item.(map[string]any)
		if !ok {
			result.Skipped = append(result.Skipped, ImportIssue{Error: "sing-box outbounds 条目不是对象"})
			continue
		}
		ob, ok := parseOutbound(m)
		if !ok {
			if typ := mStr(m, "type"); typ != "" {
				result.Skipped = append(result.Skipped, ImportIssue{
					Input: mStr(m, "tag"), Error: fmt.Sprintf("暂不支持或无需导入的出站类型 %q", typ),
				})
			}
			continue
		}
		if strings.TrimSpace(ob.Server) == "" || ob.ServerPort < 1 || ob.ServerPort > 65535 {
			result.Skipped = append(result.Skipped, ImportIssue{Input: ob.Tag, Error: "地址或端口无效"})
			continue
		}
		cn := ClientNode{
			Name: ob.Tag, Server: ob.Server, ServerPort: ob.ServerPort, Type: ob.Type,
			Settings: ob.Settings,
			User:     ProxyUser{Username: ob.Username, UUID: ob.UUID, Password: ob.Password},
		}
		result.Nodes = append(result.Nodes, importedNodeFromClient(cn, "", false))
	}
	if len(result.Nodes) == 0 {
		return result, errors.New("sing-box JSON 中没有可导入节点")
	}
	return result, nil
}

func parseV2RayJSONSubscription(raw []byte) (SubscriptionParseResult, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return SubscriptionParseResult{}, fmt.Errorf("V2Ray/Xray JSON 解析失败: %w", err)
	}
	items, ok := root["outbounds"].([]any)
	if !ok {
		return SubscriptionParseResult{}, errors.New("V2Ray/Xray JSON 未找到 outbounds 节点")
	}
	result := SubscriptionParseResult{SourceType: "v2ray-json"}
	for _, item := range items {
		m := yamlMap(item)
		if m == nil {
			result.Skipped = append(result.Skipped, ImportIssue{Error: "V2Ray/Xray outbound 条目不是对象"})
			continue
		}
		nodes, issues := parseV2RayOutbound(m)
		result.Skipped = append(result.Skipped, issues...)
		for _, node := range nodes {
			if len(result.Nodes) >= SubscriptionImportMaxNodes {
				result.Skipped = append(result.Skipped, ImportIssue{Input: node.Name, Error: "节点数量超过限制"})
				continue
			}
			result.Nodes = append(result.Nodes, importedNodeFromClient(node, "", false))
		}
	}
	if len(result.Nodes) == 0 {
		return result, errors.New("V2Ray/Xray JSON 中没有可导入节点")
	}
	return result, nil
}

func parseV2RayOutbound(m map[string]any) ([]ClientNode, []ImportIssue) {
	protocol := normalizeProtocol(getString(m, "protocol", "type"))
	tag := strings.TrimSpace(getString(m, "tag", "name"))
	settings := yamlMap(getAny(m, "settings"))
	stream := yamlMap(getAny(m, "streamSettings", "stream_settings"))
	if settings == nil {
		return nil, []ImportIssue{{Input: tag, Error: "缺少 settings"}}
	}

	baseSettings, err := parseV2RayStreamSettings(stream)
	if err != nil {
		return nil, []ImportIssue{{Input: tag, Error: err.Error()}}
	}
	var nodes []ClientNode
	var issues []ImportIssue
	addNode := func(server string, port int, suffix string, nodeSettings InboundSettings, user ProxyUser) {
		server = strings.TrimSpace(server)
		if server == "" || port < 1 || port > 65535 {
			issues = append(issues, ImportIssue{Input: tag, Error: "地址或端口无效"})
			return
		}
		name := tag
		if name == "" {
			name = fmt.Sprintf("%s %s:%d", protocol, server, port)
		}
		if suffix != "" {
			name += " " + suffix
		}
		nodes = append(nodes, ClientNode{
			Name: name, Server: server, ServerPort: port, Type: protocol,
			Settings: nodeSettings, User: user,
		})
	}

	switch protocol {
	case "vmess", "vless":
		for endpointIndex, rawEndpoint := range yamlList(getAny(settings, "vnext")) {
			endpoint := yamlMap(rawEndpoint)
			if endpoint == nil {
				issues = append(issues, ImportIssue{Input: tag, Error: "vnext 条目不是对象"})
				continue
			}
			server := getString(endpoint, "address", "server", "host")
			port := getInt(endpoint, "port", "server-port", "server_port")
			users := yamlList(getAny(endpoint, "users"))
			if len(users) == 0 {
				issues = append(issues, ImportIssue{Input: tag, Error: "vnext 缺少 users"})
				continue
			}
			for userIndex, rawUser := range users {
				user := yamlMap(rawUser)
				if user == nil {
					issues = append(issues, ImportIssue{Input: tag, Error: "vnext users 条目不是对象"})
					continue
				}
				nodeSettings := baseSettings
				credential := ProxyUser{Name: getString(user, "email", "name")}
				credential.UUID = getString(user, "id", "uuid")
				if protocol == "vmess" {
					nodeSettings.VMessSecurity = getString(user, "security", "cipher")
					if nodeSettings.VMessSecurity == "" {
						nodeSettings.VMessSecurity = "auto"
					}
					nodeSettings.VMessAlterID = getInt(user, "alterId", "alter_id", "aid")
				} else {
					nodeSettings.Flow = getString(user, "flow")
				}
				if credential.UUID == "" {
					issues = append(issues, ImportIssue{Input: tag, Error: protocol + " 用户缺少 id/uuid"})
					continue
				}
				suffix := ""
				if len(users) > 1 {
					suffix = credential.Name
					if suffix == "" {
						suffix = fmt.Sprintf("user-%d", userIndex+1)
					}
				}
				if len(yamlList(getAny(settings, "vnext"))) > 1 && suffix == "" {
					suffix = fmt.Sprintf("server-%d", endpointIndex+1)
				}
				addNode(server, port, suffix, nodeSettings, credential)
			}
		}
	case "trojan":
		servers := yamlList(getAny(settings, "servers"))
		for index, rawServer := range servers {
			server := yamlMap(rawServer)
			if server == nil {
				issues = append(issues, ImportIssue{Input: tag, Error: "trojan servers 条目不是对象"})
				continue
			}
			nodeSettings := baseSettings
			if !nodeSettings.TLS.Enabled && !nodeSettings.TLS.Reality.Enabled {
				nodeSettings.TLS.Enabled = true
			}
			password := getString(server, "password", "passwd")
			if password == "" {
				issues = append(issues, ImportIssue{Input: tag, Error: "trojan server 缺少 password"})
				continue
			}
			suffix := ""
			if len(servers) > 1 {
				suffix = fmt.Sprintf("server-%d", index+1)
			}
			addNode(getString(server, "address", "server", "host"), getInt(server, "port", "server-port", "server_port"), suffix, nodeSettings, ProxyUser{Password: password})
		}
	case "shadowsocks":
		servers := yamlList(getAny(settings, "servers"))
		for index, rawServer := range servers {
			server := yamlMap(rawServer)
			if server == nil {
				issues = append(issues, ImportIssue{Input: tag, Error: "shadowsocks servers 条目不是对象"})
				continue
			}
			password := getString(server, "password", "passwd")
			method := getString(server, "method", "cipher", "encrypt-method")
			if password == "" || method == "" {
				issues = append(issues, ImportIssue{Input: tag, Error: "shadowsocks server 缺少 method 或 password"})
				continue
			}
			nodeSettings := baseSettings
			nodeSettings.Method = method
			nodeSettings.SSServerPSK = password
			nodeSettings.SingleUser = true
			nodeSettings.SSPlugin = getString(server, "plugin")
			suffix := ""
			if len(servers) > 1 {
				suffix = fmt.Sprintf("server-%d", index+1)
			}
			addNode(getString(server, "address", "server", "host"), getInt(server, "port", "server-port", "server_port"), suffix, nodeSettings, ProxyUser{Password: password})
		}
	case "socks":
		servers := yamlList(getAny(settings, "servers"))
		for index, rawServer := range servers {
			server := yamlMap(rawServer)
			if server == nil {
				issues = append(issues, ImportIssue{Input: tag, Error: "socks servers 条目不是对象"})
				continue
			}
			users := yamlList(getAny(server, "users"))
			if len(users) == 0 {
				users = []any{map[string]any{}}
			}
			for userIndex, rawUser := range users {
				user := yamlMap(rawUser)
				if user == nil {
					issues = append(issues, ImportIssue{Input: tag, Error: "socks users 条目不是对象"})
					continue
				}
				suffix := ""
				if len(servers) > 1 || len(users) > 1 {
					suffix = fmt.Sprintf("server-%d-user-%d", index+1, userIndex+1)
				}
				nodeSettings := baseSettings
				nodeSettings.Username = getString(user, "user", "username")
				nodeSettings.Password = getString(user, "pass", "password")
				addNode(getString(server, "address", "server", "host"), getInt(server, "port", "server-port", "server_port"), suffix, nodeSettings, ProxyUser{})
			}
		}
	default:
		if protocol != "" {
			issues = append(issues, ImportIssue{Input: tag, Error: fmt.Sprintf("暂不支持或无需导入的 V2Ray/Xray 协议 %q", protocol)})
		}
	}
	return nodes, issues
}

func parseV2RayStreamSettings(stream map[string]any) (InboundSettings, error) {
	settings := InboundSettings{}
	if stream == nil {
		return settings, nil
	}
	network := strings.ToLower(strings.TrimSpace(getString(stream, "network")))
	switch network {
	case "", "tcp":
		if tcp := yamlMap(getAny(stream, "tcpSettings", "tcp_settings")); tcp != nil {
			if header := yamlMap(getAny(tcp, "header")); header != nil && strings.ToLower(getString(header, "type")) != "none" && getString(header, "type") != "" {
				return InboundSettings{}, fmt.Errorf("V2Ray/Xray TCP header 类型 %q 暂不支持", getString(header, "type"))
			}
		}
	case "ws":
		settings.Transport.Type = "ws"
		ws := yamlMap(getAny(stream, "wsSettings", "ws_settings"))
		if ws != nil {
			settings.Transport.Path = getString(ws, "path")
			for key, values := range stringValuesMap(yamlMap(getAny(ws, "headers"))) {
				settings.Transport.SetHeaderValues(key, values)
			}
			settings.Transport.MaxEarlyData = getInt(ws, "maxEarlyData", "max_early_data")
			settings.Transport.EarlyDataHeader = getString(ws, "earlyDataHeaderName", "early_data_header_name")
		}
	case "httpupgrade":
		settings.Transport.Type = "httpupgrade"
		httpUpgrade := yamlMap(getAny(stream, "httpupgradeSettings", "httpupgrade_settings", "http-upgrade-opts"))
		if httpUpgrade != nil {
			settings.Transport.Path = getString(httpUpgrade, "path")
			for key, values := range stringValuesMap(yamlMap(getAny(httpUpgrade, "headers"))) {
				settings.Transport.SetHeaderValues(key, values)
			}
			if host := getString(httpUpgrade, "host"); host != "" {
				settings.Transport.SetHeaderValues("Host", []string{host})
			}
		}
	case "grpc", "h2", "http", "quic":
		return InboundSettings{}, fmt.Errorf("V2Ray/Xray %s 传输暂不支持", network)
	default:
		return InboundSettings{}, fmt.Errorf("V2Ray/Xray 网络类型 %q 暂不支持", network)
	}
	security := strings.ToLower(strings.TrimSpace(getString(stream, "security")))
	if security == "tls" {
		settings.TLS.Enabled = true
		tls := yamlMap(getAny(stream, "tlsSettings", "tls_settings"))
		if tls != nil {
			settings.TLS.ServerName = getString(tls, "serverName", "server_name")
			settings.TLS.Insecure = getBool(tls, "allowInsecure", "allow_insecure", "insecure")
			settings.TLS.Fingerprint = getString(tls, "fingerprint", "clientFingerprint", "client_fingerprint")
			settings.TLS.ALPN = getStringList(tls, "alpn")
		}
	} else if security == "reality" {
		settings.TLS.Enabled = true
		settings.TLS.Reality.Enabled = true
		reality := yamlMap(getAny(stream, "realitySettings", "reality_settings"))
		if reality != nil {
			settings.TLS.ServerName = getString(reality, "serverName", "server_name")
			settings.TLS.Reality.PublicKey = getString(reality, "publicKey", "public_key", "pbk")
			settings.TLS.Reality.ShortID = splitOrNil(getString(reality, "shortId", "short_id", "sid"))
		}
		if settings.TLS.Reality.PublicKey == "" {
			return InboundSettings{}, errors.New("V2Ray/Xray REALITY 缺少 publicKey")
		}
	} else if security != "" && security != "none" {
		return InboundSettings{}, fmt.Errorf("V2Ray/Xray 安全类型 %q 暂不支持", security)
	}
	return settings, nil
}

func parseQuantumultSubscription(raw []byte) (SubscriptionParseResult, error) {
	result := SubscriptionParseResult{SourceType: "quantumult-x"}
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), SubscriptionImportMaxBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if section != "server_local" && section != "server-local" {
			continue
		}
		protocolRaw, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		protocol := normalizeProtocol(protocolRaw)
		fields := splitSurgeFields(value)
		if protocol == "" || len(fields) < 1 {
			continue
		}
		host, port, err := splitHostPort(strings.TrimSpace(fields[0]))
		if err != nil {
			result.Skipped = append(result.Skipped, ImportIssue{Input: strings.TrimSpace(protocolRaw), Error: "Quantumult X 地址或端口无效"})
			continue
		}
		m := map[string]any{"type": protocol, "server": host, "port": port}
		for _, field := range fields[1:] {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			key, fieldValue, hasValue := strings.Cut(field, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			fieldValue = strings.Trim(strings.TrimSpace(fieldValue), "\"")
			if !hasValue {
				m[key] = true
				continue
			}
			m[key] = fieldValue
		}
		if tag := getString(m, "tag", "name"); tag != "" {
			m["name"] = tag
		}
		// Quantumult X uses `password` for VMess/VLESS UUIDs and `method` for
		// the VMess cipher. Normalize those fields into the Clash-shaped map.
		if protocol == "vmess" || protocol == "vless" {
			if uuid := getString(m, "username", "user", "password", "uuid", "id"); uuid != "" {
				m["uuid"] = uuid
			}
			if method := getString(m, "method", "cipher", "security"); method != "" {
				m["cipher"] = method
			}
		}
		if protocol == "shadowsocks" {
			if method := getString(m, "method", "cipher"); method != "" {
				m["cipher"] = method
			}
		}
		if getBool(m, "over-tls", "over_tls", "tls") {
			m["tls"] = true
		}
		if verification, present := lookupMapValue(m, "tls-verification", "tls_verification"); present && !isTruthy(fmt.Sprint(verification)) {
			m["skip-cert-verify"] = true
		}
		if tlsHost := getString(m, "tls-host", "tls_host"); tlsHost != "" {
			m["sni"] = tlsHost
		}
		obfs := strings.ToLower(getString(m, "obfs"))
		if obfs == "ws" || obfs == "wss" {
			m["network"] = "ws"
			wsOpts := map[string]any{}
			if path := getString(m, "obfs-uri", "obfs_uri", "path"); path != "" {
				wsOpts["path"] = path
			}
			if host := getString(m, "obfs-host", "obfs_host"); host != "" {
				wsOpts["headers"] = map[string]any{"Host": host}
			}
			m["ws-opts"] = wsOpts
			if obfs == "wss" {
				m["tls"] = true
			}
		}
		if len(result.Nodes) >= SubscriptionImportMaxNodes {
			result.Skipped = append(result.Skipped, ImportIssue{Input: getString(m, "name"), Error: "节点数量超过限制"})
			continue
		}
		node, parseErr := parseClashProxy(m)
		if parseErr != nil {
			result.Skipped = append(result.Skipped, ImportIssue{Input: getString(m, "name"), Error: parseErr.Error()})
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("读取 Quantumult X 订阅失败: %w", err)
	}
	if len(result.Nodes) == 0 {
		return result, errors.New("订阅 Quantumult X [server_local] 中没有可导入节点")
	}
	return result, nil
}

func parseSIP008Subscription(raw []byte) (SubscriptionParseResult, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return SubscriptionParseResult{}, fmt.Errorf("SIP008 JSON 解析失败: %w", err)
	}
	items := root
	if object, ok := root.(map[string]any); ok {
		items = object["servers"]
	}
	list, ok := items.([]any)
	if !ok {
		return SubscriptionParseResult{}, errors.New("SIP008 JSON 未找到 servers 节点")
	}
	result := SubscriptionParseResult{SourceType: "sip008-json"}
	for _, item := range list {
		m := yamlMap(item)
		if m == nil {
			result.Skipped = append(result.Skipped, ImportIssue{Error: "SIP008 server 条目不是对象"})
			continue
		}
		if len(result.Nodes) >= SubscriptionImportMaxNodes {
			result.Skipped = append(result.Skipped, ImportIssue{Input: getString(m, "remarks", "name", "id"), Error: "节点数量超过限制"})
			continue
		}
		method := getString(m, "method")
		password := getString(m, "password")
		server := getString(m, "server")
		port := getInt(m, "server_port")
		if method == "" || password == "" || server == "" || port < 1 || port > 65535 {
			result.Skipped = append(result.Skipped, ImportIssue{Input: getString(m, "remarks", "name", "id"), Error: "SIP008 server 缺少有效的 server/server_port/method/password"})
			continue
		}
		settings := InboundSettings{Method: method, SSServerPSK: password, SingleUser: true}
		settings.SSPlugin = JoinShadowsocksPluginFields(getString(m, "plugin"), getString(m, "plugin_opts"))
		result.Nodes = append(result.Nodes, importedNodeFromClient(ClientNode{
			Name: getString(m, "remarks", "name", "id"), Server: server, ServerPort: port,
			Type: "shadowsocks", Settings: settings, User: ProxyUser{Password: password},
		}, "", false))
	}
	if len(result.Nodes) == 0 {
		return result, errors.New("SIP008 JSON 中没有可导入节点")
	}
	return result, nil
}

func parseLinkLines(raw []byte) (SubscriptionParseResult, error) {
	result := SubscriptionParseResult{SourceType: "plain"}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	// A single VMess URI can be large when metadata is embedded. Keep a hard
	// line limit below the overall payload cap to avoid unbounded allocations.
	scanner.Buffer(make([]byte, 1024), SubscriptionImportMaxBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		// Some providers prefix links with a list marker. It is safe to strip
		// only the conventional marker, never arbitrary punctuation.
		line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if len(result.Nodes) >= SubscriptionImportMaxNodes {
			result.Skipped = append(result.Skipped, ImportIssue{Input: line, Error: "节点数量超过限制"})
			continue
		}
		cn, err := ParseShareLink(line)
		if err != nil {
			result.Skipped = append(result.Skipped, ImportIssue{Input: truncateImportInput(line), Error: err.Error()})
			continue
		}
		if cn.Name == "" {
			cn.Name = fmt.Sprintf("%s %s:%d", cn.Type, cn.Server, cn.ServerPort)
		}
		result.Nodes = append(result.Nodes, importedNodeFromClient(cn, line, true))
		result.SourceType = "links"
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("读取订阅内容失败: %w", err)
	}
	if len(result.Nodes) == 0 && len(result.Skipped) == 0 {
		return result, fmt.Errorf("未找到可识别的分享链接")
	}
	return result, nil
}

func decodeSubscriptionBase64(raw []byte) ([]byte, bool) {
	s := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		default:
			return r
		}
	}, string(raw))
	if len(s) < 16 {
		return nil, false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("+/=_-", r) {
			continue
		}
		return nil, false
	}
	decoders := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range decoders {
		decoded, err := enc.DecodeString(s)
		if err != nil || len(decoded) == 0 || len(decoded) > SubscriptionImportMaxBytes {
			continue
		}
		text := strings.TrimSpace(string(decoded))
		// Avoid treating arbitrary binary as a subscription. URI schemes, known
		// YAML profiles, and the supported JSON subscription signatures are the
		// accepted decoded formats.
		decodedPayload := []byte(text)
		lower := strings.ToLower(text)
		if strings.Contains(lower, "://") || strings.Contains(lower, "[proxy]") ||
			isClashYAML(decodedPayload) || hasQuantumultServerSection(decodedPayload) ||
			isSingBoxJSON(decodedPayload) || isV2RayJSON(decodedPayload) || isSIP008JSON(decodedPayload) {
			return []byte(text), true
		}
	}
	return nil, false
}

func parseClashSubscription(raw []byte) (SubscriptionParseResult, error) {
	var root map[string]any
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return SubscriptionParseResult{}, fmt.Errorf("订阅 Clash YAML 解析失败: %w", err)
	}
	var proxies any
	for k, v := range root {
		if normalizeImportKey(k) == "proxies" {
			proxies = v
			break
		}
	}
	items := yamlList(proxies)
	if len(items) == 0 {
		return SubscriptionParseResult{}, fmt.Errorf("订阅 Clash YAML 未找到 proxies 节点")
	}
	result := SubscriptionParseResult{SourceType: "clash-yaml"}
	for _, item := range items {
		if len(result.Nodes) >= SubscriptionImportMaxNodes {
			result.Skipped = append(result.Skipped, ImportIssue{Error: "节点数量超过限制"})
			continue
		}
		m := yamlMap(item)
		if m == nil {
			result.Skipped = append(result.Skipped, ImportIssue{Error: "代理条目不是对象"})
			continue
		}
		node, err := parseClashProxy(m)
		if err != nil {
			result.Skipped = append(result.Skipped, ImportIssue{Input: getString(m, "name"), Error: err.Error()})
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	if len(result.Nodes) == 0 {
		return result, fmt.Errorf("订阅 Clash YAML 中没有可导入节点")
	}
	return result, nil
}

func parseClashProxy(m map[string]any) (ImportedNode, error) {
	typ := normalizeProtocol(getString(m, "type"))
	if typ == "" {
		return ImportedNode{}, fmt.Errorf("缺少代理类型")
	}
	if typ == "http" || typ == "wireguard" || typ == "socks4" {
		return ImportedNode{}, fmt.Errorf("暂不支持 Clash 代理类型 %q", typ)
	}
	server := strings.TrimSpace(getString(m, "server", "address", "host"))
	port := getInt(m, "port", "server-port", "server_port")
	// Some exporters (Clash→Surge converters, SingBox share links) fold the
	// port into the server field ("1.2.3.4:8388"). Split it when the port
	// field is missing or unparseable.
	if (port < 1 || port > 65535) && strings.Contains(server, ":") {
		if h, p, err := net.SplitHostPort(server); err == nil {
			if n, aerr := strconv.Atoi(p); aerr == nil && n > 0 && n <= 65535 {
				server, port = strings.TrimSpace(h), n
			}
		}
	}
	if server == "" || port < 1 || port > 65535 {
		return ImportedNode{}, fmt.Errorf("地址或端口无效")
	}
	name := strings.TrimSpace(getString(m, "name", "ps"))
	if name == "" {
		name = fmt.Sprintf("%s %s:%d", typ, server, port)
	}

	cn := ClientNode{Name: name, Server: server, ServerPort: port, Type: typ}

	// Common TLS fields. Trojan and the QUIC protocols are TLS by definition
	// unless a profile explicitly disables it (Clash normally omits the field).
	tls := getBool(m, "tls", "tls-enabled")
	if _, present := lookupMapValue(m, "tls", "tls-enabled"); !present {
		tls = typ == "trojan" || typ == "hysteria2" || typ == "hysteria" || typ == "tuic" || typ == "anytls"
	}
	sni := getString(m, "sni", "servername", "server-name", "server_name", "peer")
	if sni == "" {
		sni = server
	}
	cn.Settings.TLS.Enabled = tls
	cn.Settings.TLS.ServerName = sni
	cn.Settings.TLS.Insecure = getBool(m, "skip-cert-verify", "skip_cert_verify", "insecure", "allow-insecure")
	cn.Settings.TLS.Fingerprint = getString(m, "client-fingerprint", "fingerprint", "fp")
	cn.Settings.TLS.ALPN = getStringList(m, "alpn")

	if reality := yamlMap(getAny(m, "reality-opts", "reality_opts")); reality != nil || getBool(m, "reality") {
		cn.Settings.TLS.Enabled = true
		cn.Settings.TLS.Reality.Enabled = true
		if reality != nil {
			cn.Settings.TLS.Reality.PublicKey = getString(reality, "public-key", "public_key", "pbk")
			if sid := getString(reality, "short-id", "short_id", "sid"); sid != "" {
				cn.Settings.TLS.Reality.ShortID = []string{sid}
			}
		}
		if cn.Settings.TLS.Reality.PublicKey == "" {
			return ImportedNode{}, fmt.Errorf("REALITY 缺少 public-key")
		}
	}

	// Clash transport options.
	network := strings.ToLower(strings.TrimSpace(getString(m, "network", "transport")))
	var transport map[string]any
	if network == "ws" {
		transport = yamlMap(getAny(m, "ws-opts", "ws_opts"))
	} else if network == "http-upgrade" || network == "httpupgrade" {
		network = "httpupgrade"
		transport = yamlMap(getAny(m, "http-upgrade-opts", "http_upgrade_opts"))
	}
	if network == "grpc" {
		return ImportedNode{}, fmt.Errorf("暂不支持 gRPC 传输")
	}
	if network == "ws" || network == "httpupgrade" {
		cn.Settings.Transport.Type = network
		if transport != nil {
			cn.Settings.Transport.Path = getString(transport, "path")
			for key, values := range stringValuesMap(yamlMap(getAny(transport, "headers"))) {
				cn.Settings.Transport.SetHeaderValues(key, values)
			}
			cn.Settings.Transport.MaxEarlyData = getInt(transport, "max-early-data", "max_early_data")
			cn.Settings.Transport.EarlyDataHeader = getString(transport, "early-data-header-name", "early_data_header_name")
		}
	}

	switch typ {
	case "vless":
		cn.User.UUID = getString(m, "uuid", "id")
		if cn.User.UUID == "" {
			return ImportedNode{}, fmt.Errorf("VLESS 缺少 uuid")
		}
		cn.Settings.Flow = getString(m, "flow")
		cn.Settings.PacketEncoding = getString(m, "packet-encoding", "packet_encoding")
	case "vmess":
		cn.User.UUID = getString(m, "uuid", "id")
		if cn.User.UUID == "" {
			return ImportedNode{}, fmt.Errorf("VMess 缺少 uuid")
		}
		cn.Settings.VMessAlterID = getInt(m, "alter-id", "alter_id", "aid")
		cn.Settings.VMessSecurity = getString(m, "cipher", "security", "scy")
		if cn.Settings.VMessSecurity == "" {
			cn.Settings.VMessSecurity = "auto"
		}
	case "trojan":
		cn.User.Password = getString(m, "password", "passwd")
		if cn.User.Password == "" {
			return ImportedNode{}, fmt.Errorf("trojan 缺少 password")
		}
		cn.Settings.PacketEncoding = getString(m, "packet-encoding", "packet_encoding")
	case "shadowsocks":
		cn.Settings.Method = getString(m, "cipher", "method", "encrypt-method", "encrypt_method")
		cn.User.Password = getString(m, "password", "passwd")
		if cn.Settings.Method == "" || cn.User.Password == "" {
			return ImportedNode{}, fmt.Errorf("shadowsocks 缺少 cipher 或 password")
		}
		cn.Settings.SingleUser = true
		cn.Settings.SSServerPSK = cn.User.Password
		cn.Settings.SSPlugin = getString(m, "plugin")
		// Surge expresses simple-obfs as separate obfs/obfs-host fields.
		// Preserve it as the standard SIP002 plugin string used by the panel.
		if cn.Settings.SSPlugin == "" {
			if obfs := getString(m, "obfs"); obfs != "" {
				cn.Settings.SSPlugin = "obfs-local;obfs=" + obfs
				if host := getString(m, "obfs-host", "obfs_host"); host != "" {
					cn.Settings.SSPlugin += ";obfs-host=" + host
				}
			}
		}
	case "socks":
		cn.Settings.Username = getString(m, "username", "user")
		cn.Settings.Password = getString(m, "password", "passwd")
	case "hysteria2":
		cn.User.Password = getString(m, "password", "auth", "auth-str", "auth_str")
		if cn.User.Password == "" {
			return ImportedNode{}, fmt.Errorf("Hysteria2 缺少 password")
		}
		cn.Settings.ObfsType = getString(m, "obfs")
		cn.Settings.ObfsPassword = getString(m, "obfs-password", "obfs_password", "salamander-password")
		cn.Settings.GeckoMinPacketSize = getInt(m, "min-packet-size", "min_packet_size")
		cn.Settings.GeckoMaxPacketSize = getInt(m, "max-packet-size", "max_packet_size")
		if obfsOpts := yamlMap(getAny(m, "obfs-opts", "obfs_opts")); obfsOpts != nil {
			cn.Settings.GeckoMinPacketSize = getInt(obfsOpts, "min-packet-size", "min_packet_size")
			cn.Settings.GeckoMaxPacketSize = getInt(obfsOpts, "max-packet-size", "max_packet_size")
			if cn.Settings.ObfsPassword == "" {
				cn.Settings.ObfsPassword = getString(obfsOpts, "password", "obfs-password", "obfs_password")
			}
		}
		if cn.Settings.ObfsType == "" && cn.Settings.ObfsPassword != "" {
			cn.Settings.ObfsType = "salamander"
		}
		cn.Settings.UpMbps = getRateInt(m, "up", "up-mbps", "up_mbps", "upload-bandwidth")
		cn.Settings.DownMbps = getRateInt(m, "down", "down-mbps", "down_mbps", "download-bandwidth")
	case "hysteria":
		cn.User.Password = getString(m, "auth-str", "auth_str", "auth", "password")
		if cn.User.Password == "" {
			return ImportedNode{}, fmt.Errorf("hysteria 缺少认证信息")
		}
		cn.Settings.ObfsPassword = getString(m, "obfs")
		cn.Settings.UpMbps = getRateInt(m, "up", "up-mbps", "up_mbps")
		cn.Settings.DownMbps = getRateInt(m, "down", "down-mbps", "down_mbps")
	case "tuic":
		cn.User.UUID = getString(m, "uuid", "id")
		// Surge expresses the TUIC credential as token=, Clash as password=.
		cn.User.Password = getString(m, "password", "passwd", "token")
		if cn.User.UUID == "" || cn.User.Password == "" {
			return ImportedNode{}, fmt.Errorf("tuic 缺少 uuid 或 password")
		}
		cn.Settings.CongestionControl = getString(m, "congestion-controller", "congestion_control", "congestion-control")
		cn.Settings.TUICUDPRelayMode = getString(m, "udp-relay-mode", "udp_relay_mode")
		cn.Settings.ZeroRTTHandshake = getBool(m, "reduce-rtt", "reduce_rtt", "zero-rtt-handshake")
		if hb := getString(m, "heartbeat-interval", "heartbeat_interval", "heartbeat"); hb != "" {
			if duration, err := time.ParseDuration(hb); err == nil {
				cn.Settings.Heartbeat = duration.String()
			} else if millis := getInt(m, "heartbeat-interval", "heartbeat_interval"); millis > 0 {
				cn.Settings.Heartbeat = (time.Duration(millis) * time.Millisecond).String()
			}
		}
	case "anytls":
		cn.User.Password = getString(m, "password", "passwd")
		if cn.User.Password == "" {
			return ImportedNode{}, fmt.Errorf("anytls 缺少 password")
		}
		cn.Settings.AnyTLSUDPOverStream = getBool(m, "udp-over-stream", "udp_over_stream")
	case "snell":
		cn.Settings.SnellPSK = getString(m, "psk", "password")
		if cn.Settings.SnellPSK == "" {
			return ImportedNode{}, fmt.Errorf("snell 缺少 psk")
		}
		// Snell is fixed to one PSK in this panel; ignore legacy userkey values.
		cn.Settings.SnellReuse = getBool(m, "reuse")
		cn.Settings.SnellNetwork = strings.ToLower(strings.TrimSpace(getString(m, "network")))
		if cn.Settings.SnellNetwork != "" && cn.Settings.SnellNetwork != "tcp" && cn.Settings.SnellNetwork != "udp" {
			return ImportedNode{}, fmt.Errorf("snell network 必须是 tcp 或 udp")
		}
		cn.Settings.SnellVersion = getInt(m, "version")
		if cn.Settings.SnellVersion == 0 {
			cn.Settings.SnellVersion = 5
		}
		if cn.Settings.SnellVersion != 4 && cn.Settings.SnellVersion != 5 && cn.Settings.SnellVersion != 6 {
			return ImportedNode{}, fmt.Errorf("snell 版本必须是 4、5 或 6")
		}
		obfs := yamlMap(getAny(m, "obfs-opts", "obfs_opts"))
		if obfs != nil {
			cn.Settings.SnellObfsMode = getString(obfs, "mode")
			cn.Settings.SnellObfsHost = getString(obfs, "host")
		} else {
			cn.Settings.SnellObfsMode = getString(m, "obfs", "obfs_mode")
		}
		if cn.Settings.SnellObfsHost == "" {
			cn.Settings.SnellObfsHost = getString(m, "obfs-host", "obfs_host")
		}
		cn.Settings.SnellMode = getString(m, "mode")
		validateSettings := cn.Settings
		validateSettings.SnellVersion = SnellOutboundVersion(validateSettings.SnellVersion)
		if err := validateSettings.ValidateClientOutbound("snell"); err != nil {
			return ImportedNode{}, fmt.Errorf("snell 参数无效: %w", err)
		}
	default:
		return ImportedNode{}, fmt.Errorf("暂不支持协议 %q", typ)
	}

	node := importedNodeFromClient(cn, "", false)
	node.Params["udp"] = getBoolDefault(m, true, "udp", "udp-relay")
	if cn.Settings.PacketEncoding != "" {
		node.Params["packet_encoding"] = cn.Settings.PacketEncoding
	}
	if len(cn.Settings.Transport.Headers) > 0 {
		node.Params["headers"] = cn.Settings.Transport.HeaderObject()
	}
	if pluginOpts := yamlMap(getAny(m, "plugin-opts", "plugin_opts")); len(pluginOpts) > 0 {
		node.Params["ss_plugin_opts"] = pluginOpts
	}
	return node, nil
}

// parseSurgeSubscription handles the [Proxy] section of a Surge profile. It
// intentionally ignores [Proxy Group], [Rule], and other sections: only
// concrete proxy entries can become custom nodes.
func parseSurgeSubscription(raw []byte) (SubscriptionParseResult, error) {
	result := SubscriptionParseResult{SourceType: "surge"}
	section := ""
	positionalNodes := false
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), SubscriptionImportMaxBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		// Accept proxy lines both inside [Proxy]/[Proxies] and in sectionless
		// profiles where the provider omitted the header entirely. Group lines
		// (GLOBAL = select, ...) fail isSurgeProxyLine and stay skipped.
		if section != "proxy" && section != "proxies" && !isSurgeProxyLine(line) {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		fields := splitSurgeFields(line[i+1:])
		if len(fields) < 3 {
			result.Skipped = append(result.Skipped, ImportIssue{Input: name, Error: "Surge 代理字段不足"})
			continue
		}
		// Some exporters fold the port into the host field ("1.2.3.4:8388").
		// Split it up front so the real parameters (fields[2] onwards) do not
		// get misaligned: otherwise encrypt-method= would be swallowed as the
		// port and the proxy would fail with "缺少 cipher 或 password".
		extraStart := 3
		serverField, portField := fields[1], fields[2]
		if strings.Contains(serverField, ":") {
			if h, p, err := net.SplitHostPort(serverField); err == nil {
				if n, aerr := strconv.Atoi(p); aerr == nil && n > 0 && n <= 65535 {
					serverField, portField, extraStart = h, p, 2
				}
			}
		}
		m := map[string]any{"name": name, "type": fields[0], "server": serverField, "port": portField}
		for _, field := range fields[extraStart:] {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			if k, v, ok := strings.Cut(field, "="); ok {
				m[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), "\"")
			} else {
				m[field] = true
			}
		}
		if normalizeSurgePositionalFields(m, fields[extraStart:]) {
			positionalNodes = true
		}
		// Surge uses slightly different names from Clash. Normalize the fields
		// into the Clash-shaped map understood by parseClashProxy.
		if b, ok := m["tls"].(bool); ok && b {
			m["tls"] = true
		}
		for k, v := range map[string]string{
			"encrypt-method":   "cipher",
			"username":         "uuid", // corrected below for SOCKS
			"ws-path":          "path",
			"ws-headers":       "headers",
			"servername":       "sni",
			"skip-cert-verify": "skip-cert-verify",
		} {
			if x, exists := m[k]; exists {
				m[v] = x
			}
		}
		typ := normalizeProtocol(getString(m, "type"))
		if typ == "socks" {
			if x, ok := m["username"]; ok {
				m["username"] = x
				delete(m, "uuid")
			}
		}
		// Surge's ws=true is a flag rather than a network value.
		if getBool(m, "ws") {
			m["network"] = "ws"
			opts := map[string]any{}
			if path := getString(m, "ws-path", "path"); path != "" {
				opts["path"] = path
			}
			if headers := getString(m, "ws-headers"); headers != "" {
				opts["headers"] = parseSurgeHeaders(headers)
			}
			m["ws-opts"] = opts
		}
		// Surge's bare `tls` marker is a bool; protocol defaults are handled by
		// parseClashProxy for TLS-native protocols.
		if len(result.Nodes) >= SubscriptionImportMaxNodes {
			result.Skipped = append(result.Skipped, ImportIssue{Input: name, Error: "节点数量超过限制"})
			continue
		}
		node, err := parseClashProxy(m)
		if err != nil {
			result.Skipped = append(result.Skipped, ImportIssue{Input: name, Error: err.Error()})
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("读取 Surge 订阅失败: %w", err)
	}
	if len(result.Nodes) == 0 {
		return result, fmt.Errorf("订阅 Surge [Proxy] 中没有可导入节点")
	}
	if positionalNodes {
		result.SourceType = "surge-loon"
	}
	return result, nil
}

// normalizeSurgePositionalFields handles the Loon-compatible positional
// dialect commonly found in [Proxy] sections. Surge usually writes named
// fields (method=..., password=...), while Loon writes e.g.
// `ss, host, port, aes-256-gcm, password`.
func normalizeSurgePositionalFields(m map[string]any, extraFields []string) bool {
	if len(extraFields) == 0 {
		return false
	}
	typ := normalizeProtocol(getString(m, "type"))
	// Do not reinterpret ordinary Surge bare flags as Loon credentials when
	// the corresponding named credential already exists.
	switch typ {
	case "shadowsocks":
		if getString(m, "cipher", "method", "encrypt-method") != "" && getString(m, "password", "passwd") != "" {
			return false
		}
	case "vmess", "vless":
		if getString(m, "uuid", "id", "username") != "" {
			return false
		}
	case "trojan":
		if getString(m, "password", "passwd") != "" {
			return false
		}
	case "socks":
		if getString(m, "username", "user") != "" {
			return false
		}
	}
	positional := make([]string, 0, len(extraFields))
	for _, field := range extraFields {
		field = strings.TrimSpace(field)
		if field == "" || strings.Contains(field, "=") {
			continue
		}
		if isSurgeBareOption(field) {
			continue
		}
		positional = append(positional, strings.Trim(field, "\""))
	}
	if len(positional) == 0 {
		return false
	}
	put := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			if _, exists := m[key]; !exists {
				m[key] = strings.TrimSpace(value)
			}
		}
	}
	switch typ {
	case "shadowsocks":
		if len(positional) >= 2 {
			put("cipher", positional[0])
			put("password", positional[1])
		} else {
			return false
		}
	case "vmess":
		if len(positional) >= 2 {
			put("cipher", positional[0])
			put("uuid", positional[1])
		} else {
			put("uuid", positional[0])
		}
	case "vless":
		put("uuid", positional[0])
	case "trojan":
		put("password", positional[0])
	case "socks":
		put("username", positional[0])
		if len(positional) >= 2 {
			put("password", positional[1])
		}
	default:
		return false
	}
	return true
}

func isSurgeBareOption(field string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Trim(field, "\""))) {
	case "tls", "ws", "tfo", "fast-open", "udp", "udp-relay", "skip-cert-verify":
		return true
	default:
		return false
	}
}

func splitSurgeFields(raw string) []string {
	var out []string
	var buf strings.Builder
	quoted := false
	escaped := false
	for _, r := range raw {
		if escaped {
			buf.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quoted {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if r == ',' && !quoted {
			out = append(out, strings.TrimSpace(buf.String()))
			buf.Reset()
			continue
		}
		buf.WriteRune(r)
	}
	out = append(out, strings.TrimSpace(buf.String()))
	return out
}

func parseSurgeHeaders(raw string) map[string]any {
	out := map[string]any{}
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool { return r == '|' || r == ';' }) {
		item = strings.TrimSpace(item)
		if k, v, ok := strings.Cut(item, ":"); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		} else if k, v, ok := strings.Cut(item, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func importedNodeFromClient(cn ClientNode, link string, preserveLink bool) ImportedNode {
	name := strings.TrimSpace(cn.Name)
	if name == "" {
		name = fmt.Sprintf("%s %s:%d", cn.Type, cn.Server, cn.ServerPort)
	}
	params := clientParams(cn)
	if !preserveLink {
		link = ""
	}
	return ImportedNode{Name: name, Link: link, Protocol: cn.Type, Address: cn.Server, Port: cn.ServerPort, Params: params}
}

// clientParams mirrors the fields consumed by panel.customNodeToNode. Keep
// this map explicit rather than serializing InboundSettings directly: the
// latter would expose server-only certificate/private-key fields in an admin
// response and would not match the structured-node API's stable names.
func clientParams(cn ClientNode) map[string]any {
	p := map[string]any{"udp": true}
	st := cn.Settings
	if cn.User.UUID != "" {
		p["uuid"] = cn.User.UUID
	}
	if cn.User.Password != "" {
		p["password"] = cn.User.Password
	}
	if cn.User.Username != "" {
		p["username"] = cn.User.Username
	}
	if cn.Type == "shadowsocks" && st.SSServerPSK != "" {
		p["password"] = st.SSServerPSK
	}
	if cn.Type == "socks" {
		if st.Username != "" {
			p["username"] = st.Username
		}
		if st.Password != "" {
			p["password"] = st.Password
		}
	}
	if st.Method != "" {
		p["method"] = st.Method
	}
	if st.SSPlugin != "" {
		p["ss_plugin"] = st.SSPlugin
	}
	if st.Flow != "" {
		p["flow"] = st.Flow
	}
	if st.PacketEncoding != "" {
		p["packet_encoding"] = st.PacketEncoding
	}
	if st.VMessSecurity != "" {
		p["security"] = st.VMessSecurity
	}
	if st.VMessAlterID != 0 {
		p["alter_id"] = st.VMessAlterID
	}
	if st.TLS.Reality.Enabled {
		p["tls"] = "reality"
		p["pbk"] = st.TLS.Reality.PublicKey
		if len(st.TLS.Reality.ShortID) > 0 {
			p["sid"] = st.TLS.Reality.ShortID[0]
		}
	} else if st.TLS.Enabled {
		p["tls"] = "tls"
	} else {
		p["tls"] = "none"
	}
	if st.TLS.ServerName != "" {
		p["sni"] = st.TLS.ServerName
	}
	if st.TLS.Fingerprint != "" {
		p["fingerprint"] = st.TLS.Fingerprint
	}
	if st.TLS.Insecure {
		p["insecure"] = true
	}
	if len(st.TLS.ALPN) > 0 {
		p["alpn"] = strings.Join(st.TLS.ALPN, ",")
	}
	if st.Transport.Type != "" {
		p["transport"] = st.Transport.Type
		if st.Transport.Path != "" {
			p["path"] = st.Transport.Path
		}
		if h := st.Transport.Headers["Host"]; h != "" {
			p["host"] = h
		}
		if len(st.Transport.Headers) > 0 {
			p["headers"] = st.Transport.HeaderObject()
		}
		if st.Transport.MaxEarlyData > 0 {
			p["max_early_data"] = st.Transport.MaxEarlyData
		}
		if st.Transport.EarlyDataHeader != "" {
			p["early_data_header_name"] = st.Transport.EarlyDataHeader
		}
	}
	if st.ObfsType != "" {
		p["obfs"] = st.ObfsType
	}
	if st.ObfsPassword != "" {
		p["obfs_password"] = st.ObfsPassword
	}
	if st.GeckoMinPacketSize != 0 {
		p["gecko_min_packet_size"] = st.GeckoMinPacketSize
	}
	if st.GeckoMaxPacketSize != 0 {
		p["gecko_max_packet_size"] = st.GeckoMaxPacketSize
	}
	if st.UpMbps != 0 {
		p["up_mbps"] = st.UpMbps
	}
	if st.DownMbps != 0 {
		p["down_mbps"] = st.DownMbps
	}
	if st.CongestionControl != "" {
		p["congestion_control"] = st.CongestionControl
	}
	if st.TUICUDPRelayMode != "" {
		p["udp_relay_mode"] = st.TUICUDPRelayMode
	}
	if st.ZeroRTTHandshake {
		p["zero_rtt_handshake"] = true
	}
	if st.Heartbeat != "" {
		p["heartbeat"] = st.Heartbeat
	}
	if st.AnyTLSUDPOverStream {
		p["udp_over_stream"] = true
	}
	if st.SnellPSK != "" {
		p["psk"] = st.SnellPSK
	}
	if st.SnellReuse {
		p["reuse"] = true
	}
	if st.SnellNetwork != "" {
		p["network"] = st.SnellNetwork
	}
	if st.SnellVersion != 0 {
		p["version"] = st.SnellVersion
	}
	if st.SnellObfsMode != "" {
		p["obfs_mode"] = st.SnellObfsMode
	}
	if st.SnellObfsHost != "" {
		p["obfs_host"] = st.SnellObfsHost
	}
	if st.SnellMode != "" {
		p["mode"] = st.SnellMode
	}
	return p
}

func normalizeProtocol(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
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
	case "httpupgrade":
		return "httpupgrade"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

func normalizeImportKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(k)
}

func lookupMapValue(m map[string]any, keys ...string) (any, bool) {
	for _, want := range keys {
		want = normalizeImportKey(want)
		for k, v := range m {
			if normalizeImportKey(k) == want {
				return v, true
			}
		}
	}
	return nil, false
}

func getAny(m map[string]any, keys ...string) any {
	v, _ := lookupMapValue(m, keys...)
	return v
}

func getString(m map[string]any, keys ...string) string {
	v, ok := lookupMapValue(m, keys...)
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func getStringList(m map[string]any, keys ...string) []string {
	v, ok := lookupMapValue(m, keys...)
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	default:
		if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
			return strings.Split(s, ",")
		}
	}
	return nil
}

func getBool(m map[string]any, keys ...string) bool {
	v, ok := lookupMapValue(m, keys...)
	if !ok {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return isTruthy(x)
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	default:
		return false
	}
}

func getBoolDefault(m map[string]any, fallback bool, keys ...string) bool {
	if _, ok := lookupMapValue(m, keys...); !ok {
		return fallback
	}
	return getBool(m, keys...)
}

func getInt(m map[string]any, keys ...string) int {
	v, ok := lookupMapValue(m, keys...)
	if !ok || v == nil {
		return 0
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		maxInt := int64(^uint(0) >> 1)
		minInt := -maxInt - 1
		if x > maxInt || x < minInt {
			return 0
		}
		return int(x)
	case uint64:
		if x > uint64(^uint(0)>>1) {
			return 0
		}
		return int(x)
	case float64:
		maxInt := float64(^uint(0) >> 1)
		minInt := -maxInt - 1
		if math.IsNaN(x) || math.IsInf(x, 0) || x >= maxInt || x <= minInt {
			return 0
		}
		return int(math.Trunc(x))
	case string:
		return atoiSafe(x)
	default:
		return atoiSafe(fmt.Sprint(x))
	}
}

func getRateInt(m map[string]any, keys ...string) int {
	v := getString(m, keys...)
	if v == "" {
		return getInt(m, keys...)
	}
	fields := strings.FieldsFunc(v, func(r rune) bool { return r < '0' || r > '9' })
	if len(fields) > 0 {
		return atoiSafe(fields[0])
	}
	return 0
}

func yamlMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, value := range m {
			if s, ok := k.(string); ok {
				out[s] = value
			}
		}
		return out
	default:
		return nil
	}
}

func yamlList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out
	default:
		return nil
	}
}

func stringValuesMap(m map[string]any) map[string][]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string][]string, len(m))
	for k, v := range m {
		switch values := v.(type) {
		case []any:
			for _, value := range values {
				out[k] = append(out[k], strings.TrimSpace(fmt.Sprint(value)))
			}
		case []string:
			out[k] = append([]string(nil), values...)
		default:
			out[k] = []string{strings.TrimSpace(fmt.Sprint(v))}
		}
	}
	return out
}

func truncateImportInput(s string) string {
	const max = 240
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
