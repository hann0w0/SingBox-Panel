package singbox

import (
	"fmt"
	"math"
	"strings"
)

// SchemaVersion identifies the panel's embedded sing-box JSON Schema revision.
const SchemaVersion = "sing-box-1.14"

// UIMeta carries optional presentation hints alongside standard JSON Schema
// keywords so the frontend can render structured Chinese forms without
// hardcoding field lists per protocol.
type UIMeta struct {
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Group       string   `json:"group,omitempty"`
	Order       int      `json:"order,omitempty"`
	Widget      string   `json:"widget,omitempty"` // text | password | textarea | select | tags | multiselect | switch | number | lines | json
	Generator   string   `json:"generator,omitempty"`
	FullWidth   bool     `json:"full_width,omitempty"`
	Actions     []string `json:"actions,omitempty"` // for route_rule: actions where this field applies
	Advanced    bool     `json:"advanced,omitempty"`
}

// JSONSchema represents a subset of JSON Schema Draft 2020-12 extended with
// panel presentation metadata (x-ui) and protocol variants (x-variants).
type JSONSchema struct {
	Schema               string                 `json:"$schema,omitempty"`
	Title                string                 `json:"title,omitempty"`
	Type                 string                 `json:"type,omitempty"`
	Description          string                 `json:"description,omitempty"`
	Default              any                    `json:"default,omitempty"`
	Enum                 []any                  `json:"enum,omitempty"`
	Minimum              *float64               `json:"minimum,omitempty"`
	Maximum              *float64               `json:"maximum,omitempty"`
	Required             []string               `json:"required,omitempty"`
	Properties           map[string]*JSONSchema `json:"properties,omitempty"`
	Items                *JSONSchema            `json:"items,omitempty"`
	AdditionalProperties *bool                  `json:"additionalProperties,omitempty"`
	Discriminator        string                 `json:"x-discriminator,omitempty"`
	Variants             map[string]*JSONSchema `json:"x-variants,omitempty"`
	UI                   *UIMeta                `json:"x-ui,omitempty"`
}

// SchemaBundle is returned by GET /api/admin/schemas for frontend form
// rendering and client-side schema validation.
type SchemaBundle struct {
	Version   string      `json:"version"`
	Inbound   *JSONSchema `json:"inbound"`
	RouteRule *JSONSchema `json:"route_rule"`
}

func ptrFloat(v float64) *float64 { return &v }
func ptrBool(v bool) *bool       { return &v }

func strEnum(values ...string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func intEnum(values ...int) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func stringArraySchema(ui *UIMeta) *JSONSchema {
	return &JSONSchema{
		Type:  "array",
		Items: &JSONSchema{Type: "string"},
		UI:    ui,
	}
}

func intArraySchema(minV, maxV float64, ui *UIMeta) *JSONSchema {
	return &JSONSchema{
		Type: "array",
		Items: &JSONSchema{
			Type:    "integer",
			Minimum: ptrFloat(minV),
			Maximum: ptrFloat(maxV),
		},
		UI: ui,
	}
}

func tlsSubSchema(includeReality bool) *JSONSchema {
	props := map[string]*JSONSchema{
		"enabled": {
			Type: "boolean",
			UI:   &UIMeta{Label: "启用 TLS", Group: "TLS", Order: 1, Widget: "switch"},
		},
		"server_name": {
			Type: "string",
			UI:   &UIMeta{Label: "SNI", Group: "TLS", Order: 2, Placeholder: "example.com"},
		},
		"alpn": stringArraySchema(&UIMeta{Label: "ALPN", Group: "TLS", Order: 3, Widget: "tags", Placeholder: "h2, http/1.1"}),
		"min_version": {
			Type: "string",
			Enum: strEnum("", "1.0", "1.1", "1.2", "1.3"),
			UI:   &UIMeta{Label: "最低 TLS 版本", Group: "TLS", Order: 4, Widget: "select", Advanced: true},
		},
		"max_version": {
			Type: "string",
			Enum: strEnum("", "1.0", "1.1", "1.2", "1.3"),
			UI:   &UIMeta{Label: "最高 TLS 版本", Group: "TLS", Order: 5, Widget: "select", Advanced: true},
		},
		"cipher_suites":     stringArraySchema(&UIMeta{Label: "密码套件", Group: "TLS", Order: 6, Widget: "tags", Advanced: true}),
		"curve_preferences": stringArraySchema(&UIMeta{Label: "椭圆曲线偏好", Group: "TLS", Order: 7, Widget: "tags", Advanced: true}),
		"client_authentication": {
			Type: "string",
			Enum: strEnum("", "no", "request", "require-any", "verify-if-given", "require-and-verify"),
			UI:   &UIMeta{Label: "客户端证书验证", Group: "TLS", Order: 8, Widget: "select", Advanced: true},
		},
		"certificate_path": {
			Type: "string",
			UI:   &UIMeta{Label: "证书路径", Group: "TLS", Order: 10, Placeholder: "/etc/sing-box/cert.pem"},
		},
		"key_path": {
			Type: "string",
			UI:   &UIMeta{Label: "私钥路径", Group: "TLS", Order: 11, Placeholder: "/etc/sing-box/key.pem"},
		},
		"certificate": {
			Type: "array",
			Items: &JSONSchema{
				Type: "string",
			},
			UI: &UIMeta{Label: "内联证书 PEM", Group: "TLS", Order: 12, Widget: "lines", FullWidth: true, Advanced: true},
		},
		"key": {
			Type: "array",
			Items: &JSONSchema{
				Type: "string",
			},
			UI: &UIMeta{Label: "内联私钥 PEM", Group: "TLS", Order: 13, Widget: "lines", FullWidth: true, Advanced: true},
		},
		"insecure": {
			Type: "boolean",
			UI:   &UIMeta{Label: "客户端跳过证书验证", Group: "TLS", Order: 14, Widget: "switch"},
		},
		"utls": {
			Type:                 "object",
			AdditionalProperties: ptrBool(true),
			Properties: map[string]*JSONSchema{
				"enabled": {Type: "boolean", UI: &UIMeta{Label: "启用 uTLS", Widget: "switch"}},
				"fingerprint": {
					Type: "string",
					Enum: strEnum("", "chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized"),
					UI:   &UIMeta{Label: "uTLS 指纹", Widget: "select"},
				},
			},
			UI: &UIMeta{Label: "uTLS", Group: "TLS", Order: 15},
		},
		"ech": {
			Type:                 "object",
			AdditionalProperties: ptrBool(true),
			Properties: map[string]*JSONSchema{
				"enabled":                    {Type: "boolean", UI: &UIMeta{Label: "启用 ECH", Widget: "switch"}},
				"pq_signature_schemes_enabled": {Type: "boolean", UI: &UIMeta{Label: "启用后量子签名", Widget: "switch"}},
				"dynamic_record_sizing_disabled": {Type: "boolean", UI: &UIMeta{Label: "禁用动态记录大小", Widget: "switch"}},
				"key":                        stringArraySchema(&UIMeta{Label: "ECH 密钥", Widget: "lines"}),
				"key_path":                   {Type: "string", UI: &UIMeta{Label: "ECH 密钥路径"}},
			},
			UI: &UIMeta{Label: "ECH", Group: "TLS", Order: 16, Advanced: true},
		},
		"certificate_provider": {
			Type:                 "object",
			AdditionalProperties: ptrBool(true),
			Properties: map[string]*JSONSchema{
				"type":           {Type: "string", Default: "acme", Enum: strEnum("acme", "cloudflare", "tailscale")},
				"domain":         stringArraySchema(&UIMeta{Label: "ACME 域名", Widget: "tags"}),
				"email":          {Type: "string", UI: &UIMeta{Label: "ACME 邮箱"}},
				"data_directory": {Type: "string", UI: &UIMeta{Label: "ACME 数据目录"}},
				"provider":       {Type: "string", UI: &UIMeta{Label: "ACME CA"}},
			},
			UI: &UIMeta{Label: "证书提供者 (ACME)", Group: "TLS", Order: 17, Advanced: true},
		},
	}
	if includeReality {
		props["reality"] = &JSONSchema{
			Type:                 "object",
			AdditionalProperties: ptrBool(true),
			Properties: map[string]*JSONSchema{
				"enabled": {Type: "boolean", UI: &UIMeta{Label: "启用 REALITY", Widget: "switch"}},
				"handshake": {
					Type:                 "object",
					AdditionalProperties: ptrBool(true),
					Properties: map[string]*JSONSchema{
						"server":         {Type: "string", UI: &UIMeta{Label: "握手域名"}},
						"server_port":    {Type: "integer", Default: 443, Minimum: ptrFloat(1), Maximum: ptrFloat(65535), UI: &UIMeta{Label: "握手端口"}},
						"detour":         {Type: "string", UI: &UIMeta{Label: "握手出站 detour", Advanced: true}},
						"bind_interface": {Type: "string", UI: &UIMeta{Label: "绑定网卡", Advanced: true}},
						"tcp_fast_open":  {Type: "boolean", UI: &UIMeta{Label: "TCP Fast Open", Widget: "switch", Advanced: true}},
					},
				},
				"private_key":         {Type: "string", UI: &UIMeta{Label: "私钥", Generator: "reality-keypair"}},
				"short_id":            stringArraySchema(&UIMeta{Label: "Short ID", Generator: "short-id", Widget: "tags"}),
				"max_time_difference": {Type: "string", UI: &UIMeta{Label: "时间差限制", Placeholder: "1m"}},
			},
			UI: &UIMeta{Label: "REALITY", Group: "TLS", Order: 18},
		}
	}
	return &JSONSchema{
		Type:                 "object",
		AdditionalProperties: ptrBool(true),
		Properties:           props,
		UI:                   &UIMeta{Label: "TLS", Group: "TLS"},
	}
}

func transportSubSchema() *JSONSchema {
	return &JSONSchema{
		Type:                 "object",
		AdditionalProperties: ptrBool(true),
		Properties: map[string]*JSONSchema{
			"type": {
				Type:    "string",
				Default: "tcp",
				Enum:    strEnum("tcp", "ws", "http", "grpc", "httpupgrade", "quic"),
				UI:      &UIMeta{Label: "传输层", Order: 1, Widget: "select"},
			},
			"path": {
				Type: "string",
				UI:   &UIMeta{Label: "路径", Order: 2, Placeholder: "/ws"},
			},
			"host": {
				Type: "string",
				UI:   &UIMeta{Label: "Host", Order: 3},
			},
			"method": {
				Type: "string",
				UI:   &UIMeta{Label: "HTTP 方法", Order: 4, Placeholder: "GET", Advanced: true},
			},
			"headers": {
				Type:                 "object",
				AdditionalProperties: ptrBool(true),
				UI:                   &UIMeta{Label: "请求头", Order: 5, Widget: "json", FullWidth: true, Advanced: true},
			},
			"service_name": {
				Type: "string",
				UI:   &UIMeta{Label: "gRPC Service Name", Order: 6, Placeholder: "TunService"},
			},
			"idle_timeout": {
				Type: "string",
				UI:   &UIMeta{Label: "空闲超时", Order: 7, Placeholder: "15s", Advanced: true},
			},
			"ping_timeout": {
				Type: "string",
				UI:   &UIMeta{Label: "Ping 超时", Order: 8, Placeholder: "15s", Advanced: true},
			},
			"permit_without_stream": {
				Type: "boolean",
				UI:   &UIMeta{Label: "无流保持 Ping", Order: 9, Widget: "switch", Advanced: true},
			},
			"max_early_data": {
				Type:    "integer",
				Minimum: ptrFloat(0),
				UI:      &UIMeta{Label: "Early Data 长度", Order: 10},
			},
			"early_data_header_name": {
				Type: "string",
				UI:   &UIMeta{Label: "Early Data 头", Order: 11, Placeholder: "Sec-WebSocket-Protocol"},
			},
		},
		UI: &UIMeta{Label: "传输", Group: "传输"},
	}
}

func multiplexSubSchema() *JSONSchema {
	return &JSONSchema{
		Type:                 "object",
		AdditionalProperties: ptrBool(true),
		Properties: map[string]*JSONSchema{
			"enabled": {Type: "boolean", UI: &UIMeta{Label: "启用多路复用 (Multiplex)", Widget: "switch"}},
			"padding": {Type: "boolean", UI: &UIMeta{Label: "启用 Padding", Widget: "switch"}},
			"brutal": {
				Type:                 "object",
				AdditionalProperties: ptrBool(true),
				Properties: map[string]*JSONSchema{
					"enabled":   {Type: "boolean", UI: &UIMeta{Label: "启用 Brutal 拥塞控制", Widget: "switch"}},
					"up_mbps":   {Type: "integer", Minimum: ptrFloat(0), UI: &UIMeta{Label: "上行 Mbps"}},
					"down_mbps": {Type: "integer", Minimum: ptrFloat(0), UI: &UIMeta{Label: "下行 Mbps"}},
				},
			},
		},
		UI: &UIMeta{Label: "多路复用", Group: "高级网络", Advanced: true},
	}
}

func inboundVariant(props map[string]*JSONSchema) *JSONSchema {
	return &JSONSchema{
		Type:                 "object",
		AdditionalProperties: ptrBool(true),
		Properties:           props,
	}
}

// InboundSchema returns the JSON Schema for sing-box inbound configurations.
func InboundSchema() *JSONSchema {
	baseProps := map[string]*JSONSchema{
		"type": {
			Type: "string",
			Enum: strEnum(
				"vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic",
				"anytls", "snell", "socks", "mixed", "http", "tun",
				"direct", "redirect", "tproxy",
			),
			UI: &UIMeta{Label: "协议", Group: "基本", Order: 1, Widget: "select"},
		},
		"tag": {
			Type: "string",
			UI:   &UIMeta{Label: "标签", Group: "基本", Order: 2, Placeholder: "留空自动生成"},
		},
		"listen": {
			Type:    "string",
			Default: "::",
			UI:      &UIMeta{Label: "监听地址", Group: "高级网络", Order: 10, Placeholder: "::", Advanced: true},
		},
		"listen_port": {
			Type:    "integer",
			Minimum: ptrFloat(1),
			Maximum: ptrFloat(65535),
			UI:      &UIMeta{Label: "端口", Group: "基本", Order: 3},
		},
		"tcp_fast_open": {
			Type: "boolean",
			UI:   &UIMeta{Label: "TCP Fast Open", Group: "高级网络", Order: 20, Widget: "switch", Advanced: true},
		},
		"tcp_multi_path": {
			Type: "boolean",
			UI:   &UIMeta{Label: "TCP Multi Path", Group: "高级网络", Order: 21, Widget: "switch", Advanced: true},
		},
		"udp_fragment": {
			Type: "boolean",
			UI:   &UIMeta{Label: "UDP Fragment", Group: "高级网络", Order: 22, Widget: "switch", Advanced: true},
		},
		"udp_timeout": {
			Type: "string",
			UI:   &UIMeta{Label: "UDP 超时", Group: "高级网络", Order: 23, Placeholder: "5m", Advanced: true},
		},
		"detour": {
			Type: "string",
			UI:   &UIMeta{Label: "前置入站 (detour)", Group: "高级网络", Order: 24, Advanced: true},
		},
		"sniff": {
			Type: "boolean",
			UI:   &UIMeta{Label: "入站嗅探 (legacy)", Group: "高级网络", Order: 25, Widget: "switch", Advanced: true},
		},
		"sniff_override_destination": {
			Type: "boolean",
			UI:   &UIMeta{Label: "嗅探覆盖目标 (legacy)", Group: "高级网络", Order: 26, Widget: "switch", Advanced: true},
		},
		"domain_strategy": {
			Type: "string",
			Enum: strEnum("", "prefer_ipv4", "prefer_ipv6", "ipv4_only", "ipv6_only"),
			UI:   &UIMeta{Label: "域名解析策略 (legacy)", Group: "高级网络", Order: 27, Widget: "select", Advanced: true},
		},
	}

	variants := map[string]*JSONSchema{
		"vless": inboundVariant(map[string]*JSONSchema{
			"flow": {
				Type: "string",
				Enum: strEnum("", "xtls-rprx-vision"),
				UI:   &UIMeta{Label: "流控", Group: "协议参数", Order: 1, Widget: "select"},
			},
			"packet_encoding": {
				Type:    "string",
				Default: "xudp",
				Enum:    strEnum("", "none", "xudp", "packetaddr"),
				UI:      &UIMeta{Label: "UDP 封包编码", Group: "协议参数", Order: 2, Widget: "select"},
			},
			"multiplex": multiplexSubSchema(),
			"transport": transportSubSchema(),
			"tls":       tlsSubSchema(true),
		}),
		"vmess": inboundVariant(map[string]*JSONSchema{
			"multiplex": multiplexSubSchema(),
			"transport": transportSubSchema(),
			"tls":       tlsSubSchema(true),
		}),
		"trojan": inboundVariant(map[string]*JSONSchema{
			"fallback": {
				Type:                 "object",
				AdditionalProperties: ptrBool(true),
				Properties: map[string]*JSONSchema{
					"server":      {Type: "string", UI: &UIMeta{Label: "回落服务器"}},
					"server_port": {Type: "integer", Minimum: ptrFloat(1), Maximum: ptrFloat(65535), UI: &UIMeta{Label: "回落端口"}},
				},
				UI: &UIMeta{Label: "回落", Group: "回落"},
			},
			"multiplex": multiplexSubSchema(),
			"transport": transportSubSchema(),
			"tls":       tlsSubSchema(false),
		}),
		"shadowsocks": inboundVariant(map[string]*JSONSchema{
			"method": {
				Type: "string",
				Enum: strEnum(
					"2022-blake3-aes-128-gcm",
					"2022-blake3-aes-256-gcm",
					"2022-blake3-chacha20-poly1305",
					"aes-128-gcm",
					"aes-256-gcm",
					"chacha20-ietf-poly1305",
					"xchacha20-ietf-poly1305",
					"none",
				),
				UI: &UIMeta{Label: "加密方式", Group: "协议参数", Order: 1, Widget: "select"},
			},
			"password": {
				Type: "string",
				UI:   &UIMeta{Label: "密钥 / 密码", Group: "协议参数", Order: 2, Widget: "password", Generator: "ss2022-psk"},
			},
			"network": {
				Type: "string",
				Enum: strEnum("", "tcp", "udp"),
				UI:   &UIMeta{Label: "网络限制", Group: "协议参数", Order: 3, Widget: "select", Advanced: true},
			},
			"multiplex": multiplexSubSchema(),
		}),
		"hysteria2": inboundVariant(map[string]*JSONSchema{
			"up_mbps":                 {Type: "integer", Minimum: ptrFloat(0), UI: &UIMeta{Label: "上行 Mbps", Group: "协议参数", Order: 1}},
			"down_mbps":               {Type: "integer", Minimum: ptrFloat(0), UI: &UIMeta{Label: "下行 Mbps", Group: "协议参数", Order: 2}},
			"ignore_client_bandwidth": {Type: "boolean", UI: &UIMeta{Label: "忽略客户端带宽", Group: "协议参数", Order: 3, Widget: "switch"}},
			"obfs": {
				Type:                 "object",
				AdditionalProperties: ptrBool(true),
				Properties: map[string]*JSONSchema{
					"type":            {Type: "string", Enum: strEnum("", "salamander", "gecko"), UI: &UIMeta{Label: "混淆类型", Widget: "select"}},
					"password":        {Type: "string", UI: &UIMeta{Label: "混淆密码", Widget: "password"}},
					"min_packet_size": {Type: "integer", Minimum: ptrFloat(512), UI: &UIMeta{Label: "最小包大小"}},
					"max_packet_size": {Type: "integer", Minimum: ptrFloat(512), UI: &UIMeta{Label: "最大包大小"}},
				},
				UI: &UIMeta{Label: "混淆", Group: "协议参数"},
			},
			"masquerade": {
				Type: "string",
				UI:   &UIMeta{Label: "伪装站点 / 响应 (masquerade)", Group: "协议参数", Order: 10, Placeholder: "https://example.com", Advanced: true},
			},
			"bbr_profile": {
				Type: "string",
				Enum: strEnum("", "conservative", "standard", "aggressive"),
				UI:   &UIMeta{Label: "BBR 配置", Group: "协议参数", Order: 11, Widget: "select", Advanced: true},
			},
			"tls": tlsSubSchema(false),
		}),
		"tuic": inboundVariant(map[string]*JSONSchema{
			"congestion_control": {
				Type:    "string",
				Default: "cubic",
				Enum:    strEnum("cubic", "new_reno", "bbr"),
				UI:      &UIMeta{Label: "拥塞控制", Group: "协议参数", Order: 1, Widget: "select"},
			},
			"auth_timeout": {
				Type:    "string",
				Default: "3s",
				UI:      &UIMeta{Label: "认证超时", Group: "协议参数", Order: 2},
			},
			"heartbeat": {
				Type:    "string",
				Default: "10s",
				UI:      &UIMeta{Label: "心跳间隔", Group: "协议参数", Order: 3},
			},
			"zero_rtt_handshake": {
				Type: "boolean",
				UI:   &UIMeta{Label: "0-RTT 握手", Group: "协议参数", Order: 4, Widget: "switch"},
			},
			"tls": tlsSubSchema(false),
		}),
		"anytls": inboundVariant(map[string]*JSONSchema{
			"padding_scheme": stringArraySchema(&UIMeta{Label: "Padding 方案", Group: "协议参数", Widget: "lines", Advanced: true}),
			"tls":            tlsSubSchema(false),
		}),
		"snell": inboundVariant(map[string]*JSONSchema{
			"version": {
				Type:    "integer",
				Default: 5,
				Enum:    intEnum(5, 6),
				UI:      &UIMeta{Label: "版本", Group: "协议参数", Order: 1, Widget: "select"},
			},
			"psk": {
				Type: "string",
				UI:   &UIMeta{Label: "PSK", Group: "协议参数", Order: 2, Widget: "password", Generator: "psk"},
			},
			"obfs_mode": {
				Type: "string",
				Enum: strEnum("none", "http", "tls"),
				UI:   &UIMeta{Label: "v5 混淆", Group: "协议参数", Order: 3, Widget: "select"},
			},
			"mode": {
				Type: "string",
				Enum: strEnum("", "unshaped", "unsafe-raw"),
				UI:   &UIMeta{Label: "v6 模式", Group: "协议参数", Order: 4, Widget: "select"},
			},
		}),
		"socks": inboundVariant(map[string]*JSONSchema{
			"set_system_proxy": {Type: "boolean", UI: &UIMeta{Label: "设置系统代理", Group: "协议参数", Widget: "switch", Advanced: true}},
		}),
		"mixed": inboundVariant(map[string]*JSONSchema{
			"set_system_proxy": {Type: "boolean", UI: &UIMeta{Label: "设置系统代理", Group: "协议参数", Widget: "switch", Advanced: true}},
		}),
		"http": inboundVariant(map[string]*JSONSchema{
			"set_system_proxy": {Type: "boolean", UI: &UIMeta{Label: "设置系统代理", Group: "协议参数", Widget: "switch", Advanced: true}},
			"tls":              tlsSubSchema(false),
		}),
		"tun": inboundVariant(map[string]*JSONSchema{
			"interface_name":              {Type: "string", UI: &UIMeta{Label: "网卡名称", Group: "协议参数", Order: 1, Placeholder: "tun0"}},
			"address":                     stringArraySchema(&UIMeta{Label: "TUN 地址 CIDR", Group: "协议参数", Order: 2, Widget: "tags", Placeholder: "172.19.0.1/30"}),
			"mtu":                         {Type: "integer", Default: 9000, Minimum: ptrFloat(576), Maximum: ptrFloat(65535), UI: &UIMeta{Label: "MTU", Group: "协议参数", Order: 3}},
			"auto_route":                  {Type: "boolean", Default: true, UI: &UIMeta{Label: "自动路由", Group: "协议参数", Order: 4, Widget: "switch"}},
			"strict_route":                {Type: "boolean", UI: &UIMeta{Label: "严格路由", Group: "协议参数", Order: 5, Widget: "switch"}},
			"stack":                       {Type: "string", Enum: strEnum("", "system", "gvisor", "mixed"), UI: &UIMeta{Label: "网络栈", Group: "协议参数", Order: 6, Widget: "select"}},
			"auto_redirect":               {Type: "boolean", UI: &UIMeta{Label: "Auto Redirect", Group: "协议参数", Order: 7, Widget: "switch", Advanced: true}},
			"endpoint_independent_nat":    {Type: "boolean", UI: &UIMeta{Label: "独立端点 NAT", Group: "协议参数", Order: 8, Widget: "switch", Advanced: true}},
			"udp_timeout":                 {Type: "string", UI: &UIMeta{Label: "UDP 超时", Group: "协议参数", Order: 9, Placeholder: "5m"}},
		}),
		"direct": inboundVariant(map[string]*JSONSchema{
			"network":          {Type: "string", Enum: strEnum("", "tcp", "udp"), UI: &UIMeta{Label: "网络", Group: "协议参数", Order: 1, Widget: "select"}},
			"override_address": {Type: "string", UI: &UIMeta{Label: "覆盖目标地址", Group: "协议参数", Order: 2}},
			"override_port":    {Type: "integer", Minimum: ptrFloat(1), Maximum: ptrFloat(65535), UI: &UIMeta{Label: "覆盖目标端口", Group: "协议参数", Order: 3}},
		}),
		"redirect": inboundVariant(map[string]*JSONSchema{}),
		"tproxy": inboundVariant(map[string]*JSONSchema{
			"network": {Type: "string", Enum: strEnum("", "tcp", "udp"), UI: &UIMeta{Label: "网络", Group: "协议参数", Widget: "select"}},
		}),
		"naive": inboundVariant(map[string]*JSONSchema{
			"network": {Type: "string", Enum: strEnum("", "tcp", "udp"), UI: &UIMeta{Label: "网络", Group: "协议参数", Widget: "select"}},
			"tls":     tlsSubSchema(false),
		}),
		"hysteria": inboundVariant(map[string]*JSONSchema{
			"up_mbps":   {Type: "integer", Minimum: ptrFloat(1), UI: &UIMeta{Label: "上行 Mbps", Group: "协议参数"}},
			"down_mbps": {Type: "integer", Minimum: ptrFloat(1), UI: &UIMeta{Label: "下行 Mbps", Group: "协议参数"}},
			"obfs":      {Type: "string", UI: &UIMeta{Label: "混淆密钥", Group: "协议参数", Widget: "password"}},
			"tls":       tlsSubSchema(false),
		}),
		"shadowtls": inboundVariant(map[string]*JSONSchema{
			"version": {Type: "integer", Default: 3, Enum: intEnum(1, 2, 3), UI: &UIMeta{Label: "ShadowTLS 版本", Group: "协议参数", Widget: "select"}},
			"handshake": {
				Type: "object",
				Properties: map[string]*JSONSchema{
					"server":      {Type: "string", UI: &UIMeta{Label: "握手服务器"}},
					"server_port": {Type: "integer", Default: 443, Minimum: ptrFloat(1), Maximum: ptrFloat(65535), UI: &UIMeta{Label: "握手端口"}},
				},
			},
			"strict_mode": {Type: "boolean", UI: &UIMeta{Label: "严格模式", Group: "协议参数", Widget: "switch"}},
		}),
	}

	return &JSONSchema{
		Schema:               "https://json-schema.org/draft/2020-12/schema",
		Title:                "sing-box Inbound",
		Type:                 "object",
		AdditionalProperties: ptrBool(true),
		Required:             []string{"type"},
		Properties:           baseProps,
		Discriminator:        "type",
		Variants:             variants,
	}
}

// RouteRuleSchema returns the JSON Schema for sing-box route rule match objects.
func RouteRuleSchema() *JSONSchema {
	routeActions := []string{"route", "reject"}
	props := map[string]*JSONSchema{
		"action": {
			Type:    "string",
			Default: "route",
			Enum:    strEnum("route", "reject", "sniff", "hijack-dns", "resolve", "route-options"),
			UI:      &UIMeta{Label: "动作", Group: "动作", Order: 1, Widget: "select"},
		},
		"method": {
			Type:    "string",
			Default: "default",
			Enum:    strEnum("default", "drop", "reply"),
			UI:      &UIMeta{Label: "拒绝方式", Group: "动作", Order: 2, Widget: "select", Actions: []string{"reject"}},
		},
		"sniffer": stringArraySchema(&UIMeta{
			Label: "嗅探协议", Group: "动作", Order: 3, Widget: "multiselect", Actions: []string{"sniff"},
		}),
		"timeout": {
			Type: "string",
			UI:   &UIMeta{Label: "嗅探超时", Group: "动作", Order: 4, Placeholder: "300ms", Actions: []string{"sniff"}, Advanced: true},
		},
		"inbound": stringArraySchema(&UIMeta{
			Label: "入站来源", Group: "来源与身份", Order: 10, Widget: "multiselect",
		}),
		"auth_user": stringArraySchema(&UIMeta{
			Label: "认证用户", Group: "来源与身份", Order: 11, Widget: "multiselect", Actions: routeActions,
		}),
		"rule_set": stringArraySchema(&UIMeta{
			Label: "规则集", Group: "规则集", Order: 20, Widget: "multiselect", Actions: routeActions,
		}),
		"rule_set_ip_cidr_match_source": {
			Type: "boolean",
			UI:   &UIMeta{Label: "规则集匹配来源 IP", Group: "规则集", Order: 21, Widget: "switch", Actions: routeActions},
		},
		"rule_set_ip_cidr_accept_empty": {
			Type: "boolean",
			UI:   &UIMeta{Label: "规则集空 IP 也视为匹配", Group: "规则集", Order: 22, Widget: "switch", Actions: routeActions, Advanced: true},
		},
		"domain": stringArraySchema(&UIMeta{
			Label: "完整域名", Group: "域名匹配", Order: 30, Widget: "lines", FullWidth: true, Actions: routeActions,
		}),
		"domain_suffix": stringArraySchema(&UIMeta{
			Label: "域名后缀", Group: "域名匹配", Order: 31, Widget: "lines", FullWidth: true, Actions: routeActions,
		}),
		"domain_keyword": stringArraySchema(&UIMeta{
			Label: "域名关键词", Group: "域名匹配", Order: 32, Widget: "lines", FullWidth: true, Actions: routeActions,
		}),
		"domain_regex": stringArraySchema(&UIMeta{
			Label: "域名正则", Group: "域名匹配", Order: 33, Widget: "lines", FullWidth: true, Actions: routeActions, Advanced: true,
		}),
		"ip_cidr": stringArraySchema(&UIMeta{
			Label: "目标 IP / CIDR", Group: "IP 与端口", Order: 40, Widget: "lines", FullWidth: true, Actions: routeActions,
		}),
		"ip_is_private": {
			Type: "boolean",
			UI:   &UIMeta{Label: "目标为内网/私有 IP", Group: "IP 与端口", Order: 41, Widget: "switch", Actions: routeActions, Advanced: true},
		},
		"source_ip_cidr": stringArraySchema(&UIMeta{
			Label: "来源 IP / CIDR", Group: "IP 与端口", Order: 42, Widget: "lines", FullWidth: true, Actions: routeActions,
		}),
		"source_ip_is_private": {
			Type: "boolean",
			UI:   &UIMeta{Label: "来源为内网/私有 IP", Group: "IP 与端口", Order: 43, Widget: "switch", Actions: routeActions, Advanced: true},
		},
		"port": intArraySchema(1, 65535, &UIMeta{
			Label: "目标端口", Group: "IP 与端口", Order: 44, Widget: "lines", Actions: routeActions,
		}),
		"port_range": stringArraySchema(&UIMeta{
			Label: "目标端口范围", Group: "IP 与端口", Order: 45, Widget: "tags", Placeholder: "1000:2000", Actions: routeActions, Advanced: true,
		}),
		"source_port": intArraySchema(1, 65535, &UIMeta{
			Label: "来源端口", Group: "IP 与端口", Order: 46, Widget: "lines", Actions: routeActions, Advanced: true,
		}),
		"source_port_range": stringArraySchema(&UIMeta{
			Label: "来源端口范围", Group: "IP 与端口", Order: 47, Widget: "tags", Placeholder: "1000:2000", Actions: routeActions, Advanced: true,
		}),
		"network": {
			Type: "string",
			Enum: strEnum("", "tcp", "udp", "icmp"),
			UI:   &UIMeta{Label: "网络类型", Group: "协议与版本", Order: 50, Widget: "select", Actions: routeActions},
		},
		"ip_version": {
			Type: "integer",
			Enum: intEnum(4, 6),
			UI:   &UIMeta{Label: "IP 版本", Group: "协议与版本", Order: 51, Widget: "select", Actions: routeActions, Advanced: true},
		},
		"protocol": stringArraySchema(&UIMeta{
			Label: "应用协议", Group: "协议与版本", Order: 52, Widget: "tags", Actions: routeActions,
		}),
		"client": stringArraySchema(&UIMeta{
			Label: "识别客户端 (client)", Group: "协议与版本", Order: 53, Widget: "tags", Placeholder: "chromium, safari", Actions: routeActions, Advanced: true,
		}),
		"process_name": stringArraySchema(&UIMeta{
			Label: "进程名", Group: "进程与环境", Order: 60, Widget: "tags", Actions: routeActions, Advanced: true,
		}),
		"process_path": stringArraySchema(&UIMeta{
			Label: "进程路径", Group: "进程与环境", Order: 61, Widget: "lines", FullWidth: true, Actions: routeActions, Advanced: true,
		}),
		"process_path_regex": stringArraySchema(&UIMeta{
			Label: "进程路径正则", Group: "进程与环境", Order: 62, Widget: "lines", FullWidth: true, Actions: routeActions, Advanced: true,
		}),
		"package_name": stringArraySchema(&UIMeta{
			Label: "应用包名", Group: "进程与环境", Order: 63, Widget: "tags", Actions: routeActions, Advanced: true,
		}),
		"clash_mode": {
			Type: "string",
			UI:   &UIMeta{Label: "Clash 模式", Group: "进程与环境", Order: 64, Placeholder: "rule / global / direct", Actions: routeActions, Advanced: true},
		},
		"invert": {
			Type: "boolean",
			UI:   &UIMeta{Label: "匹配结果取反 (invert)", Group: "高级选项", Order: 70, Widget: "switch", Advanced: true},
		},
		"override_address": {
			Type: "string",
			UI:   &UIMeta{Label: "覆盖目标地址", Group: "高级选项", Order: 71, Actions: []string{"route", "route-options"}, Advanced: true},
		},
		"override_port": {
			Type:    "integer",
			Minimum: ptrFloat(1),
			Maximum: ptrFloat(65535),
			UI:      &UIMeta{Label: "覆盖目标端口", Group: "高级选项", Order: 72, Actions: []string{"route", "route-options"}, Advanced: true},
		},
		"udp_disable_domain_unmapping": {
			Type: "boolean",
			UI:   &UIMeta{Label: "禁用 UDP 域名反解", Group: "高级选项", Order: 73, Widget: "switch", Actions: []string{"route", "route-options"}, Advanced: true},
		},
		"udp_connect": {
			Type: "boolean",
			UI:   &UIMeta{Label: "UDP Connect", Group: "高级选项", Order: 74, Widget: "switch", Actions: []string{"route", "route-options"}, Advanced: true},
		},
		"udp_timeout": {
			Type: "string",
			UI:   &UIMeta{Label: "UDP 超时", Group: "高级选项", Order: 75, Placeholder: "5m", Actions: []string{"route", "route-options"}, Advanced: true},
		},
		"server": {
			Type: "string",
			UI:   &UIMeta{Label: "解析 DNS 服务器", Group: "动作", Order: 5, Actions: []string{"resolve"}, Advanced: true},
		},
		"strategy": {
			Type: "string",
			Enum: strEnum("", "prefer_ipv4", "prefer_ipv6", "ipv4_only", "ipv6_only"),
			UI:   &UIMeta{Label: "解析策略", Group: "动作", Order: 6, Widget: "select", Actions: []string{"resolve"}, Advanced: true},
		},
	}

	return &JSONSchema{
		Schema:               "https://json-schema.org/draft/2020-12/schema",
		Title:                "sing-box Route Rule",
		Type:                 "object",
		AdditionalProperties: ptrBool(true),
		Properties:           props,
	}
}

// GetSchemaBundle returns all sing-box JSON Schemas exposed by the panel.
func GetSchemaBundle() SchemaBundle {
	return SchemaBundle{
		Version:   SchemaVersion,
		Inbound:   InboundSchema(),
		RouteRule: RouteRuleSchema(),
	}
}

// ValidateJSONAgainstSchema validates an arbitrary JSON object against a
// JSONSchema (respecting discriminated x-variants and additionalProperties).
func ValidateJSONAgainstSchema(schema *JSONSchema, doc map[string]any) error {
	if schema == nil || doc == nil {
		return nil
	}
	return validateObjectAgainstSchema("", schema, doc)
}

func validateObjectAgainstSchema(path string, schema *JSONSchema, obj map[string]any) error {
	mergedProps := make(map[string]*JSONSchema, len(schema.Properties))
	for k, v := range schema.Properties {
		mergedProps[k] = v
	}
	if schema.Discriminator != "" && len(schema.Variants) > 0 {
		if disc, ok := obj[schema.Discriminator].(string); ok {
			if variant := schema.Variants[disc]; variant != nil {
				for k, v := range variant.Properties {
					mergedProps[k] = v
				}
			}
		}
	}
	for _, req := range schema.Required {
		val, exists := obj[req]
		if !exists || val == nil || val == "" {
			return fmt.Errorf("%s缺少必填字段 %s", prefixPath(path), req)
		}
	}
	for key, val := range obj {
		propSchema := mergedProps[key]
		if propSchema == nil {
			if schema.AdditionalProperties != nil && !*schema.AdditionalProperties {
				return fmt.Errorf("%s不支持字段 %s", prefixPath(path), key)
			}
			continue
		}
		fieldPath := key
		if path != "" {
			fieldPath = path + "." + key
		}
		if err := validateValueAgainstSchema(fieldPath, propSchema, val); err != nil {
			return err
		}
	}
	return nil
}

func validateValueAgainstSchema(path string, schema *JSONSchema, val any) error {
	if val == nil || schema == nil {
		return nil
	}
	switch schema.Type {
	case "string":
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("%s 必须是字符串", path)
		}
		if len(schema.Enum) > 0 && !matchesEnum(schema.Enum, s) {
			return fmt.Errorf("%s 的值 %q 不在允许范围内", path, s)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("%s 必须是布尔值", path)
		}
	case "integer", "number":
		num, ok := asFloat64(val)
		if !ok {
			return fmt.Errorf("%s 必须是数字", path)
		}
		if schema.Type == "integer" && math.Trunc(num) != num {
			return fmt.Errorf("%s 必须是整数", path)
		}
		if schema.Minimum != nil && num < *schema.Minimum {
			return fmt.Errorf("%s 不能小于 %v", path, *schema.Minimum)
		}
		if schema.Maximum != nil && num > *schema.Maximum {
			return fmt.Errorf("%s 不能大于 %v", path, *schema.Maximum)
		}
		if len(schema.Enum) > 0 && !matchesEnum(schema.Enum, num) {
			return fmt.Errorf("%s 的值 %v 不在允许范围内", path, num)
		}
	case "array":
		arr, ok := val.([]any)
		if !ok {
			return fmt.Errorf("%s 必须是数组", path)
		}
		if schema.Items != nil {
			for i, item := range arr {
				if err := validateValueAgainstSchema(fmt.Sprintf("%s[%d]", path, i), schema.Items, item); err != nil {
					return err
				}
			}
		}
	case "object":
		obj, ok := val.(map[string]any)
		if !ok {
			return fmt.Errorf("%s 必须是对象", path)
		}
		return validateObjectAgainstSchema(path, schema, obj)
	}
	return nil
}

func asFloat64(val any) (float64, bool) {
	switch v := val.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

func matchesEnum(enum []any, target any) bool {
	for _, candidate := range enum {
		if cStr, ok := candidate.(string); ok {
			if tStr, ok := target.(string); ok && cStr == tStr {
				return true
			}
		}
		if cNum, ok := asFloat64(candidate); ok {
			if tNum, ok := asFloat64(target); ok && cNum == tNum {
				return true
			}
		}
	}
	return false
}

func prefixPath(path string) string {
	if path == "" {
		return ""
	}
	return strings.TrimSuffix(path, ".") + ": "
}
