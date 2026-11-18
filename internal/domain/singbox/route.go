package singbox

import "encoding/json"

// OutboundInput describes one outbound (a landing/forward target or direct).
type OutboundInput struct {
	Tag  string
	Type string // direct | vless | vmess | trojan | shadowsocks | hysteria2 | tuic | anytls | snell | socks

	// Proxy-type fields (reuses the client-outbound builder).
	Server     string
	ServerPort int
	Username   string
	UUID       string
	Password   string
	Settings   InboundSettings // method/flow/tls/transport/etc.
}

// RuleInput describes one route rule: match conditions → an outbound (or reject).
// The match fields carry json tags so a stored rule's Match blob unmarshals
// straight into this struct; Outbound is set separately from its own column.
type RuleInput struct {
	Action                   string   `json:"action,omitempty"`
	Method                   string   `json:"method,omitempty"`
	AuthUser                 []string `json:"auth_user,omitempty"`
	Inbound                  []string `json:"inbound,omitempty"` // match by which inbound received the traffic
	Domain                   []string `json:"domain,omitempty"`
	DomainSuffix             []string `json:"domain_suffix,omitempty"`
	DomainKeyword            []string `json:"domain_keyword,omitempty"`
	IPCIDR                   []string `json:"ip_cidr,omitempty"`
	SourceIPCIDR             []string `json:"source_ip_cidr,omitempty"`
	Port                     []int    `json:"port,omitempty"`
	Protocol                 []string `json:"protocol,omitempty"`
	Sniffer                  []string `json:"sniffer,omitempty"`
	Network                  string   `json:"network,omitempty"`  // "" | tcp | udp
	RuleSet                  []string       `json:"rule_set,omitempty"` // referenced rule-set tags
	RuleSetIPCIDRMatchSource bool           `json:"rule_set_ip_cidr_match_source,omitempty"`
	Outbound                 string         `json:"-"` // target tag; "block"/"reject" => reject
	Extra                    map[string]any `json:"-"` // arbitrary sing-box rule fields preserved losslessly
}

var knownRuleJSONKeys = map[string]bool{
	"action": true, "method": true, "auth_user": true, "inbound": true,
	"domain": true, "domain_suffix": true, "domain_keyword": true,
	"ip_cidr": true, "source_ip_cidr": true, "port": true, "protocol": true,
	"sniffer": true, "network": true, "rule_set": true,
	"rule_set_ip_cidr_match_source": true, "outbound": true,
}

type ruleInputAlias RuleInput

// UnmarshalJSON decodes known typed fields while preserving any additional
// sing-box route rule fields (such as domain_regex, process_name, invert) in Extra.
func (r *RuleInput) UnmarshalJSON(data []byte) error {
	var alias ruleInputAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*r = RuleInput(alias)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var extra map[string]any
	for key, val := range raw {
		if knownRuleJSONKeys[key] {
			continue
		}
		if extra == nil {
			extra = make(map[string]any)
		}
		extra[key] = val
	}
	r.Extra = extra
	return nil
}

// MarshalJSON encodes typed fields together with any preserved Extra keys.
func (r RuleInput) MarshalJSON() ([]byte, error) {
	encoded, err := json.Marshal(ruleInputAlias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return encoded, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(encoded, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		obj = make(map[string]any, len(r.Extra))
	}
	for key, val := range r.Extra {
		if knownRuleJSONKeys[key] {
			continue
		}
		if _, taken := obj[key]; !taken {
			obj[key] = val
		}
	}
	return json.Marshal(obj)
}

// RuleSetInput describes a local or remote rule-set used by route rules.
type RuleSetInput struct {
	Tag            string
	Type           string // local | remote
	URL            string
	Path           string
	Format         string // binary | source
	DownloadDetour string
	UpdateInterval string
}

// buildOutbound renders one outbound object.
func buildOutbound(o OutboundInput) (json.RawMessage, error) {
	if o.Type == "" || o.Type == "direct" {
		return json.Marshal(map[string]any{"type": "direct", "tag": o.Tag})
	}
	if o.Type == "shadowsocks" {
		pw := o.Password
		if pw == "" {
			pw = o.Settings.SSServerPSK
		}
		m := map[string]any{
			"type": "shadowsocks", "tag": o.Tag,
			"server": o.Server, "server_port": o.ServerPort,
			"method": o.Settings.Method, "password": pw,
		}
		if plugin, opts := ShadowsocksPluginFields(o.Settings.SSPlugin); plugin != "" {
			m["plugin"] = plugin
			if opts != "" {
				m["plugin_opts"] = opts
			}
		}
		return json.Marshal(m)
	}
	if o.Type == "socks" {
		m := map[string]any{
			"type": "socks", "tag": o.Tag,
			"server": o.Server, "server_port": o.ServerPort,
		}
		user := o.Username
		if user == "" {
			user = o.Settings.Username
		}
		if user != "" {
			m["username"] = user
		}
		pw := o.Password
		if pw == "" {
			pw = o.Settings.Password
		}
		if pw != "" {
			m["password"] = pw
		}
		return json.Marshal(m)
	}
	return BuildClientOutbound(ClientNode{
		Name:       o.Tag,
		Server:     o.Server,
		ServerPort: o.ServerPort,
		Type:       o.Type,
		Settings:   o.Settings,
		User:       ProxyUser{UUID: o.UUID, Password: o.Password},
	})
}

// ruleNeedsSniff reports whether a rule matches on a sniffed domain or protocol.
func ruleNeedsSniff(r RuleInput) bool {
	return len(r.Domain) > 0 || len(r.DomainSuffix) > 0 || len(r.DomainKeyword) > 0 || len(r.Protocol) > 0
}

func routeRuleAction(r RuleInput) string {
	if r.Action != "" {
		return r.Action
	}
	switch r.Outbound {
	case "block", "reject":
		return "reject"
	case "sniff":
		return "sniff"
	case "hijack-dns":
		return "hijack-dns"
	case "resolve":
		return "resolve"
	case "route-options":
		return "route-options"
	default:
		return "route"
	}
}

// buildRouteRule renders one route rule object.
func buildRouteRule(r RuleInput) map[string]any {
	m := deepCloneMap(r.Extra)
	if m == nil {
		m = map[string]any{}
	}
	if len(r.AuthUser) > 0 {
		m["auth_user"] = r.AuthUser
	}
	if len(r.Inbound) > 0 {
		m["inbound"] = r.Inbound
	}
	if len(r.Domain) > 0 {
		m["domain"] = r.Domain
	}
	if len(r.DomainSuffix) > 0 {
		m["domain_suffix"] = r.DomainSuffix
	}
	if len(r.DomainKeyword) > 0 {
		m["domain_keyword"] = r.DomainKeyword
	}
	if len(r.IPCIDR) > 0 {
		m["ip_cidr"] = r.IPCIDR
	}
	if len(r.SourceIPCIDR) > 0 {
		m["source_ip_cidr"] = r.SourceIPCIDR
	}
	if len(r.Port) > 0 {
		m["port"] = r.Port
	}
	if len(r.Protocol) > 0 {
		m["protocol"] = r.Protocol
	}
	if r.Network != "" {
		m["network"] = r.Network
	}
	if len(r.RuleSet) > 0 {
		m["rule_set"] = r.RuleSet
	}
	if r.RuleSetIPCIDRMatchSource {
		m["rule_set_ip_cidr_match_source"] = true
	}

	act := routeRuleAction(r)
	// The panel intentionally exposes only the meaningful match fields for these
	// action rules. Dropping stale hidden fields also keeps an edited route rule
	// from carrying domain/IP/port conditions after it is changed to sniff/DNS.
	if act == "sniff" {
		for key := range m {
			if key != "inbound" && key != "auth_user" && key != "timeout" && key != "invert" {
				delete(m, key)
			}
		}
	} else if act == "hijack-dns" {
		for key := range m {
			if key != "inbound" && key != "protocol" && key != "auth_user" && key != "invert" {
				delete(m, key)
			}
		}
	}

	if act == "sniff" {
		m["action"] = "sniff"
		if len(r.Sniffer) > 0 {
			m["sniffer"] = r.Sniffer
		} else if len(r.Protocol) > 0 {
			m["sniffer"] = r.Protocol
		}
	} else if act == "reject" {
		m["action"] = "reject"
		if r.Method != "" && r.Method != "default" {
			m["method"] = r.Method
		}
	} else if act == "hijack-dns" {
		m["action"] = "hijack-dns"
		m["protocol"] = []string{"dns"}
	} else if act == "resolve" || act == "route-options" {
		m["action"] = act
	} else {
		m["action"] = "route"
		m["outbound"] = r.Outbound
	}
	return m
}

func buildRuleSet(rs RuleSetInput) map[string]any {
	typ := rs.Type
	if typ == "" {
		typ = "remote"
	}
	format := rs.Format
	if format == "" {
		format = "binary"
	}
	m := map[string]any{
		"type":   typ,
		"tag":    rs.Tag,
		"format": format,
	}
	if typ == "local" {
		m["path"] = rs.Path
		return m
	}
	m["url"] = rs.URL
	// Download routing is emitted by BuildServerConfig as an explicit
	// http_client: the legacy download_detour and the implicit default HTTP
	// client were deprecated in sing-box 1.14 and are fatal from 1.15 on.
	if rs.UpdateInterval != "" {
		m["update_interval"] = rs.UpdateInterval
	}
	return m
}

// Tags of the HTTP clients the panel generates for remote rule-set downloads.
const (
	HTTPClientDirectTag    = "http-direct"
	httpClientDetourPrefix = "http-via-"
)

// ruleSetHTTPClients assigns each remote rule-set an explicit top-level HTTP
// client, one per distinct download route, in first-use order.
type ruleSetHTTPClients struct {
	final      string
	directTags map[string]bool
	byTag      map[string]bool
	list       []map[string]any
}

func newRuleSetHTTPClients(final string, directTags map[string]bool) *ruleSetHTTPClients {
	return &ruleSetHTTPClients{final: final, directTags: directTags, byTag: map[string]bool{}}
}

// tagFor returns the HTTP client tag that downloads through detour. An empty
// detour keeps the pre-1.14 meaning (the default outbound, i.e. route.final).
// A direct outbound becomes a client without detour: sing-box rejects a dial
// detour to an empty direct outbound, and no detour already dials directly.
func (c *ruleSetHTTPClients) tagFor(detour string) string {
	if detour == "" {
		detour = c.final
	}
	tag := HTTPClientDirectTag
	client := map[string]any{"tag": tag}
	if detour != "" && !c.directTags[detour] {
		tag = httpClientDetourPrefix + detour
		client = map[string]any{"tag": tag, "detour": detour}
	}
	if !c.byTag[tag] {
		c.byTag[tag] = true
		c.list = append(c.list, client)
	}
	return tag
}
