package singbox

import "encoding/json"

// OutboundInput describes one outbound (a landing/forward target or direct).
type OutboundInput struct {
	Tag  string
	Type string // direct | vless | vmess | trojan | shadowsocks | hysteria2 | tuic | socks

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
	Action        string   `json:"action,omitempty"`
	Method        string   `json:"method,omitempty"`
	Inbound       []string `json:"inbound,omitempty"` // match by which inbound received the traffic
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	IPCIDR        []string `json:"ip_cidr,omitempty"`
	SourceIPCIDR  []string `json:"source_ip_cidr,omitempty"`
	Port          []int    `json:"port,omitempty"`
	Protocol      []string `json:"protocol,omitempty"`
	Sniffer       []string `json:"sniffer,omitempty"`
	Network       string   `json:"network,omitempty"`  // "" | tcp | udp
	RuleSet       []string `json:"rule_set,omitempty"` // referenced rule-set tags
	Outbound      string   `json:"-"`                  // target tag; "block"/"reject" => reject
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

// buildRouteRule renders one route rule object.
func buildRouteRule(r RuleInput) map[string]any {
	m := map[string]any{}
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

	act := r.Action
	if act == "" {
		if r.Outbound == "block" || r.Outbound == "reject" {
			act = "reject"
		} else if r.Outbound == "sniff" {
			act = "sniff"
		} else if r.Outbound == "hijack-dns" {
			act = "hijack-dns"
		} else {
			act = "route"
		}
	}
	// The panel intentionally exposes only the meaningful match fields for these
	// action rules. Dropping stale hidden fields also keeps an edited route rule
	// from carrying domain/IP/port conditions after it is changed to sniff/DNS.
	if act == "sniff" {
		for key := range m {
			if key != "inbound" {
				delete(m, key)
			}
		}
	} else if act == "hijack-dns" {
		for key := range m {
			if key != "inbound" && key != "protocol" {
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
	if rs.DownloadDetour != "" {
		m["download_detour"] = rs.DownloadDetour
	}
	if rs.UpdateInterval != "" {
		m["update_interval"] = rs.UpdateInterval
	}
	return m
}
