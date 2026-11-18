import { useEffect, useState } from 'react'
import * as api from './api'
import type { InboundSettings, InboundType, TLSSettings, TransportSettings } from './types'

export type UIMeta = {
  label?: string
  description?: string
  placeholder?: string
  group?: string
  order?: number
  widget?: string
  generator?: string
  full_width?: boolean
  actions?: string[]
  advanced?: boolean
}

export type JSONSchema = {
  $schema?: string
  title?: string
  type?: string
  description?: string
  default?: unknown
  enum?: unknown[]
  minimum?: number
  maximum?: number
  required?: string[]
  properties?: Record<string, JSONSchema>
  items?: JSONSchema
  additionalProperties?: boolean
  'x-discriminator'?: string
  'x-variants'?: Record<string, JSONSchema>
  'x-ui'?: UIMeta
}

export type SchemaBundle = {
  version: string
  inbound: JSONSchema
  route_rule: JSONSchema
}

const TLS_SUBSCHEMA: JSONSchema = {
  type: 'object',
  additionalProperties: true,
  properties: {
    enabled: { type: 'boolean', 'x-ui': { label: '启用 TLS', group: 'TLS', order: 1, widget: 'switch' } },
    server_name: { type: 'string', 'x-ui': { label: 'SNI', group: 'TLS', order: 2, placeholder: 'example.com' } },
    alpn: { type: 'array', items: { type: 'string' }, 'x-ui': { label: 'ALPN', group: 'TLS', order: 3, widget: 'tags' } },
    min_version: { type: 'string', enum: ['', '1.0', '1.1', '1.2', '1.3'], 'x-ui': { label: '最低 TLS 版本', group: 'TLS', order: 4, widget: 'select', advanced: true } },
    max_version: { type: 'string', enum: ['', '1.0', '1.1', '1.2', '1.3'], 'x-ui': { label: '最高 TLS 版本', group: 'TLS', order: 5, widget: 'select', advanced: true } },
    cipher_suites: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '密码套件', group: 'TLS', order: 6, widget: 'tags', advanced: true } },
    curve_preferences: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '椭圆曲线偏好', group: 'TLS', order: 7, widget: 'tags', advanced: true } },
    client_authentication: { type: 'string', enum: ['', 'no', 'request', 'require-any', 'verify-if-given', 'require-and-verify'], 'x-ui': { label: '客户端证书验证', group: 'TLS', order: 8, widget: 'select', advanced: true } },
    certificate_path: { type: 'string', 'x-ui': { label: '证书路径', group: 'TLS', order: 10 } },
    key_path: { type: 'string', 'x-ui': { label: '私钥路径', group: 'TLS', order: 11 } },
    insecure: { type: 'boolean', 'x-ui': { label: '客户端跳过证书验证', group: 'TLS', order: 14, widget: 'switch' } },
    ech: {
      type: 'object',
      additionalProperties: true,
      properties: {
        enabled: { type: 'boolean', 'x-ui': { label: '启用 ECH', widget: 'switch' } },
        pq_signature_schemes_enabled: { type: 'boolean', 'x-ui': { label: '启用后量子签名', widget: 'switch' } },
        dynamic_record_sizing_disabled: { type: 'boolean', 'x-ui': { label: '禁用动态记录大小', widget: 'switch' } },
        key_path: { type: 'string', 'x-ui': { label: 'ECH 密钥路径' } },
      },
      'x-ui': { label: 'ECH', group: 'TLS', order: 16, advanced: true },
    },
  },
}

const TRANSPORT_SUBSCHEMA: JSONSchema = {
  type: 'object',
  additionalProperties: true,
  properties: {
    type: { type: 'string', default: 'tcp', enum: ['tcp', 'ws', 'http', 'grpc', 'httpupgrade', 'quic'], 'x-ui': { label: '传输层', order: 1, widget: 'select' } },
    path: { type: 'string', 'x-ui': { label: '路径', order: 2, placeholder: '/ws' } },
    host: { type: 'string', 'x-ui': { label: 'Host', order: 3 } },
    method: { type: 'string', 'x-ui': { label: 'HTTP 方法', order: 4, placeholder: 'GET', advanced: true } },
    service_name: { type: 'string', 'x-ui': { label: 'gRPC Service Name', order: 6, placeholder: 'TunService' } },
    idle_timeout: { type: 'string', 'x-ui': { label: '空闲超时', order: 7, placeholder: '15s', advanced: true } },
    ping_timeout: { type: 'string', 'x-ui': { label: 'Ping 超时', order: 8, placeholder: '15s', advanced: true } },
    permit_without_stream: { type: 'boolean', 'x-ui': { label: '无流保持 Ping', order: 9, widget: 'switch', advanced: true } },
    max_early_data: { type: 'integer', minimum: 0, 'x-ui': { label: 'Early Data 长度', order: 10 } },
    early_data_header_name: { type: 'string', 'x-ui': { label: 'Early Data 头', order: 11 } },
  },
}

const MULTIPLEX_SUBSCHEMA: JSONSchema = {
  type: 'object',
  additionalProperties: true,
  properties: {
    enabled: { type: 'boolean', 'x-ui': { label: '启用多路复用 (Multiplex)', widget: 'switch' } },
    padding: { type: 'boolean', 'x-ui': { label: '启用 Padding', widget: 'switch' } },
    brutal: {
      type: 'object',
      additionalProperties: true,
      properties: {
        enabled: { type: 'boolean', 'x-ui': { label: '启用 Brutal 拥塞控制', widget: 'switch' } },
        up_mbps: { type: 'integer', minimum: 0, 'x-ui': { label: '上行 Mbps' } },
        down_mbps: { type: 'integer', minimum: 0, 'x-ui': { label: '下行 Mbps' } },
      },
    },
  },
  'x-ui': { label: '多路复用', group: '高级网络', advanced: true },
}

export const DEFAULT_INBOUND_SCHEMA: JSONSchema = {
  type: 'object',
  additionalProperties: true,
  required: ['type'],
  'x-discriminator': 'type',
  properties: {
    type: {
      type: 'string',
      enum: ['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria2', 'tuic', 'anytls', 'snell', 'socks', 'mixed', 'http', 'tun', 'direct', 'redirect', 'tproxy'],
      'x-ui': { label: '协议', group: '基本', order: 1, widget: 'select' },
    },
    tag: { type: 'string', 'x-ui': { label: '标签', group: '基本', order: 2 } },
    listen: { type: 'string', default: '::', 'x-ui': { label: '监听地址', group: '高级网络', order: 10, placeholder: '::', advanced: true } },
    listen_port: { type: 'integer', minimum: 1, maximum: 65535, 'x-ui': { label: '端口', group: '基本', order: 3 } },
    tcp_fast_open: { type: 'boolean', 'x-ui': { label: 'TCP Fast Open', group: '高级网络', order: 20, widget: 'switch', advanced: true } },
    tcp_multi_path: { type: 'boolean', 'x-ui': { label: 'TCP Multi Path', group: '高级网络', order: 21, widget: 'switch', advanced: true } },
    udp_fragment: { type: 'boolean', 'x-ui': { label: 'UDP Fragment', group: '高级网络', order: 22, widget: 'switch', advanced: true } },
    udp_timeout: { type: 'string', 'x-ui': { label: 'UDP 超时', group: '高级网络', order: 23, placeholder: '5m', advanced: true } },
    detour: { type: 'string', 'x-ui': { label: '前置入站 (detour)', group: '高级网络', order: 24, advanced: true } },
    sniff: { type: 'boolean', 'x-ui': { label: '入站嗅探 (legacy)', group: '高级网络', order: 25, widget: 'switch', advanced: true } },
    sniff_override_destination: { type: 'boolean', 'x-ui': { label: '嗅探覆盖目标 (legacy)', group: '高级网络', order: 26, widget: 'switch', advanced: true } },
    domain_strategy: { type: 'string', enum: ['', 'prefer_ipv4', 'prefer_ipv6', 'ipv4_only', 'ipv6_only'], 'x-ui': { label: '域名解析策略 (legacy)', group: '高级网络', order: 27, widget: 'select', advanced: true } },
  },
  'x-variants': {
    vless: { type: 'object', additionalProperties: true, properties: { flow: { type: 'string', enum: ['', 'xtls-rprx-vision'] }, packet_encoding: { type: 'string', enum: ['', 'none', 'xudp', 'packetaddr'] }, multiplex: MULTIPLEX_SUBSCHEMA, transport: TRANSPORT_SUBSCHEMA, tls: TLS_SUBSCHEMA } },
    vmess: { type: 'object', additionalProperties: true, properties: { multiplex: MULTIPLEX_SUBSCHEMA, transport: TRANSPORT_SUBSCHEMA, tls: TLS_SUBSCHEMA } },
    trojan: { type: 'object', additionalProperties: true, properties: { multiplex: MULTIPLEX_SUBSCHEMA, transport: TRANSPORT_SUBSCHEMA, tls: TLS_SUBSCHEMA } },
    shadowsocks: { type: 'object', additionalProperties: true, properties: { network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络限制', group: '协议参数', order: 3, widget: 'select', advanced: true } }, multiplex: MULTIPLEX_SUBSCHEMA } },
    hysteria2: { type: 'object', additionalProperties: true, properties: { masquerade: { type: 'string', 'x-ui': { label: '伪装站点 / 响应 (masquerade)', group: '协议参数', order: 10, placeholder: 'https://example.com', advanced: true } }, bbr_profile: { type: 'string', enum: ['', 'conservative', 'standard', 'aggressive'], 'x-ui': { label: 'BBR 配置', group: '协议参数', order: 11, widget: 'select', advanced: true } }, tls: TLS_SUBSCHEMA } },
    tuic: { type: 'object', additionalProperties: true, properties: { tls: TLS_SUBSCHEMA } },
    anytls: { type: 'object', additionalProperties: true, properties: { padding_scheme: { type: 'array', items: { type: 'string' }, 'x-ui': { label: 'Padding 方案', group: '协议参数', widget: 'lines', full_width: true, advanced: true } }, tls: TLS_SUBSCHEMA } },
    snell: { type: 'object', additionalProperties: true, properties: {} },
    socks: { type: 'object', additionalProperties: true, properties: { set_system_proxy: { type: 'boolean', 'x-ui': { label: '设置系统代理', group: '协议参数', widget: 'switch', advanced: true } } } },
    mixed: { type: 'object', additionalProperties: true, properties: { set_system_proxy: { type: 'boolean', 'x-ui': { label: '设置系统代理', group: '协议参数', widget: 'switch', advanced: true } } } },
    http: { type: 'object', additionalProperties: true, properties: { set_system_proxy: { type: 'boolean', 'x-ui': { label: '设置系统代理', group: '协议参数', widget: 'switch', advanced: true } }, tls: TLS_SUBSCHEMA } },
    tun: {
      type: 'object',
      additionalProperties: true,
      properties: {
        interface_name: { type: 'string', 'x-ui': { label: '网卡名称', group: '协议参数', order: 1, placeholder: 'tun0' } },
        address: { type: 'array', items: { type: 'string' }, 'x-ui': { label: 'TUN 地址 CIDR', group: '协议参数', order: 2, widget: 'tags', placeholder: '172.19.0.1/30' } },
        mtu: { type: 'integer', default: 9000, minimum: 576, maximum: 65535, 'x-ui': { label: 'MTU', group: '协议参数', order: 3 } },
        auto_route: { type: 'boolean', default: true, 'x-ui': { label: '自动路由', group: '协议参数', order: 4, widget: 'switch' } },
        strict_route: { type: 'boolean', 'x-ui': { label: '严格路由', group: '协议参数', order: 5, widget: 'switch' } },
        stack: { type: 'string', enum: ['', 'system', 'gvisor', 'mixed'], 'x-ui': { label: '网络栈', group: '协议参数', order: 6, widget: 'select' } },
        auto_redirect: { type: 'boolean', 'x-ui': { label: 'Auto Redirect', group: '协议参数', order: 7, widget: 'switch', advanced: true } },
        endpoint_independent_nat: { type: 'boolean', 'x-ui': { label: '独立端点 NAT', group: '协议参数', order: 8, widget: 'switch', advanced: true } },
      },
    },
    direct: {
      type: 'object',
      additionalProperties: true,
      properties: {
        network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络', group: '协议参数', order: 1, widget: 'select' } },
        override_address: { type: 'string', 'x-ui': { label: '覆盖目标地址', group: '协议参数', order: 2 } },
        override_port: { type: 'integer', minimum: 1, maximum: 65535, 'x-ui': { label: '覆盖目标端口', group: '协议参数', order: 3 } },
      },
    },
    redirect: { type: 'object', additionalProperties: true, properties: {} },
    tproxy: { type: 'object', additionalProperties: true, properties: { network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络', group: '协议参数', widget: 'select' } } } },
  },
}

export const DEFAULT_ROUTE_RULE_SCHEMA: JSONSchema = {
  type: 'object',
  additionalProperties: true,
  properties: {
    action: { type: 'string', default: 'route', enum: ['route', 'reject', 'sniff', 'hijack-dns', 'resolve', 'route-options'], 'x-ui': { label: '动作', group: '动作', order: 1, widget: 'select' } },
    method: { type: 'string', default: 'default', enum: ['default', 'drop', 'reply'], 'x-ui': { label: '拒绝方式', group: '动作', order: 2, widget: 'select', actions: ['reject'] } },
    sniffer: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '嗅探协议', group: '动作', order: 3, widget: 'multiselect', actions: ['sniff'] } },
    timeout: { type: 'string', 'x-ui': { label: '嗅探超时', group: '动作', order: 4, placeholder: '300ms', actions: ['sniff'], advanced: true } },
    server: { type: 'string', 'x-ui': { label: '解析 DNS 服务器', group: '动作', order: 5, actions: ['resolve'], advanced: true } },
    strategy: { type: 'string', enum: ['', 'prefer_ipv4', 'prefer_ipv6', 'ipv4_only', 'ipv6_only'], 'x-ui': { label: '解析策略', group: '动作', order: 6, widget: 'select', actions: ['resolve'], advanced: true } },
    inbound: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '入站来源', group: '来源与身份', order: 10, widget: 'multiselect' } },
    auth_user: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '认证用户', group: '来源与身份', order: 11, widget: 'multiselect', actions: ['route', 'reject'] } },
    rule_set: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '规则集', group: '规则集', order: 20, widget: 'multiselect', actions: ['route', 'reject'] } },
    rule_set_ip_cidr_match_source: { type: 'boolean', 'x-ui': { label: '规则集匹配来源 IP', group: '规则集', order: 21, widget: 'switch', actions: ['route', 'reject'] } },
    rule_set_ip_cidr_accept_empty: { type: 'boolean', 'x-ui': { label: '规则集空 IP 也视为匹配', group: '规则集', order: 22, widget: 'switch', actions: ['route', 'reject'], advanced: true } },
    domain: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '完整域名', group: '域名匹配', order: 30, widget: 'lines', full_width: true, actions: ['route', 'reject'] } },
    domain_suffix: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '域名后缀', group: '域名匹配', order: 31, widget: 'lines', full_width: true, actions: ['route', 'reject'] } },
    domain_keyword: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '域名关键词', group: '域名匹配', order: 32, widget: 'lines', full_width: true, actions: ['route', 'reject'] } },
    domain_regex: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '域名正则', group: '域名匹配', order: 33, widget: 'lines', full_width: true, actions: ['route', 'reject'], advanced: true } },
    ip_cidr: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '目标 IP / CIDR', group: 'IP 与端口', order: 40, widget: 'lines', full_width: true, actions: ['route', 'reject'] } },
    ip_is_private: { type: 'boolean', 'x-ui': { label: '目标为内网/私有 IP', group: 'IP 与端口', order: 41, widget: 'switch', actions: ['route', 'reject'], advanced: true } },
    source_ip_cidr: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '来源 IP / CIDR', group: 'IP 与端口', order: 42, widget: 'lines', full_width: true, actions: ['route', 'reject'] } },
    source_ip_is_private: { type: 'boolean', 'x-ui': { label: '来源为内网/私有 IP', group: 'IP 与端口', order: 43, widget: 'switch', actions: ['route', 'reject'], advanced: true } },
    port: { type: 'array', items: { type: 'integer', minimum: 1, maximum: 65535 }, 'x-ui': { label: '目标端口', group: 'IP 与端口', order: 44, widget: 'lines', actions: ['route', 'reject'] } },
    port_range: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '目标端口范围', group: 'IP 与端口', order: 45, widget: 'tags', placeholder: '1000:2000', actions: ['route', 'reject'], advanced: true } },
    source_port: { type: 'array', items: { type: 'integer', minimum: 1, maximum: 65535 }, 'x-ui': { label: '来源端口', group: 'IP 与端口', order: 46, widget: 'lines', actions: ['route', 'reject'], advanced: true } },
    source_port_range: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '来源端口范围', group: 'IP 与端口', order: 47, widget: 'tags', placeholder: '1000:2000', actions: ['route', 'reject'], advanced: true } },
    network: { type: 'string', enum: ['', 'tcp', 'udp', 'icmp'], 'x-ui': { label: '网络类型', group: '协议与版本', order: 50, widget: 'select', actions: ['route', 'reject'] } },
    ip_version: { type: 'integer', enum: [4, 6], 'x-ui': { label: 'IP 版本', group: '协议与版本', order: 51, widget: 'select', actions: ['route', 'reject'], advanced: true } },
    protocol: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '应用协议', group: '协议与版本', order: 52, widget: 'tags', actions: ['route', 'reject'] } },
    client: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '识别客户端 (client)', group: '协议与版本', order: 53, widget: 'tags', placeholder: 'chromium, safari', actions: ['route', 'reject'], advanced: true } },
    process_name: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '进程名', group: '进程与环境', order: 60, widget: 'tags', actions: ['route', 'reject'], advanced: true } },
    process_path: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '进程路径', group: '进程与环境', order: 61, widget: 'lines', full_width: true, actions: ['route', 'reject'], advanced: true } },
    process_path_regex: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '进程路径正则', group: '进程与环境', order: 62, widget: 'lines', full_width: true, actions: ['route', 'reject'], advanced: true } },
    package_name: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '应用包名', group: '进程与环境', order: 63, widget: 'tags', actions: ['route', 'reject'], advanced: true } },
    clash_mode: { type: 'string', 'x-ui': { label: 'Clash 模式', group: '进程与环境', order: 64, placeholder: 'rule / global / direct', actions: ['route', 'reject'], advanced: true } },
    invert: { type: 'boolean', 'x-ui': { label: '匹配结果取反 (invert)', group: '高级选项', order: 70, widget: 'switch', advanced: true } },
    override_address: { type: 'string', 'x-ui': { label: '覆盖目标地址', group: '高级选项', order: 71, actions: ['route', 'route-options'], advanced: true } },
    override_port: { type: 'integer', minimum: 1, maximum: 65535, 'x-ui': { label: '覆盖目标端口', group: '高级选项', order: 72, actions: ['route', 'route-options'], advanced: true } },
    udp_disable_domain_unmapping: { type: 'boolean', 'x-ui': { label: '禁用 UDP 域名反解', group: '高级选项', order: 73, widget: 'switch', actions: ['route', 'route-options'], advanced: true } },
    udp_connect: { type: 'boolean', 'x-ui': { label: 'UDP Connect', group: '高级选项', order: 74, widget: 'switch', actions: ['route', 'route-options'], advanced: true } },
    udp_timeout: { type: 'string', 'x-ui': { label: 'UDP 超时', group: '高级选项', order: 75, placeholder: '5m', actions: ['route', 'route-options'], advanced: true } },
  },
}

const DEFAULT_BUNDLE: SchemaBundle = {
  version: 'sing-box-1.14',
  inbound: DEFAULT_INBOUND_SCHEMA,
  route_rule: DEFAULT_ROUTE_RULE_SCHEMA,
}

let cachedBundle: SchemaBundle | null = null

export function useSingboxSchemas(active = true): SchemaBundle {
  const [bundle, setBundle] = useState<SchemaBundle>(() => cachedBundle ?? DEFAULT_BUNDLE)

  useEffect(() => {
    if (!active || cachedBundle) return
    let loader: ((signal?: AbortSignal) => Promise<Record<string, unknown>>) | undefined
    try {
      loader = (api as { getSchemas?: (signal?: AbortSignal) => Promise<Record<string, unknown>> }).getSchemas
    } catch {
      return
    }
    if (typeof loader !== 'function') return
    const controller = new AbortController()
    loader(controller.signal)
      .then((raw) => {
        if (controller.signal.aborted || !raw || typeof raw !== 'object') return
        const next = raw as unknown as SchemaBundle
        if (next.inbound && next.route_rule) {
          cachedBundle = next
          setBundle(next)
        }
      })
      .catch(() => {})
    return () => controller.abort()
  }, [active])

  return bundle
}

export function resolveVariantProperties(schema: JSONSchema, variantKey: string): Record<string, JSONSchema> {
  const base = schema.properties ?? {}
  const variant = schema['x-variants']?.[variantKey]?.properties ?? {}
  return { ...base, ...variant }
}

export function validateObjectBySchema(schema: JSONSchema, obj: Record<string, unknown>, path = ''): string | null {
  const props = schema['x-discriminator'] && typeof obj[schema['x-discriminator']] === 'string'
    ? resolveVariantProperties(schema, String(obj[schema['x-discriminator']]))
    : (schema.properties ?? {})

  for (const req of schema.required ?? []) {
    const v = obj[req]
    if (v === undefined || v === null || v === '') {
      return `${path ? `${path}: ` : ''}缺少必填字段 ${req}`
    }
  }

  for (const [key, val] of Object.entries(obj)) {
    if (val === undefined || val === null) continue
    const prop = props[key]
    if (!prop) {
      if (schema.additionalProperties === false) {
        return `${path ? `${path}.` : ''}${key} 不被支持`
      }
      continue
    }
    const fieldPath = path ? `${path}.${key}` : key
    const err = validateValueBySchema(prop, val, fieldPath)
    if (err) return err
  }
  return null
}

function validateValueBySchema(schema: JSONSchema, val: unknown, path: string): string | null {
  if (val === undefined || val === null) return null
  switch (schema.type) {
    case 'string':
      if (typeof val !== 'string') return `${path} 必须是字符串`
      if (schema.enum?.length && !schema.enum.includes(val)) return `${path} 的值 ${val} 不在允许范围内`
      return null
    case 'boolean':
      if (typeof val !== 'boolean') return `${path} 必须是布尔值`
      return null
    case 'integer':
    case 'number':
      if (typeof val !== 'number' || Number.isNaN(val)) return `${path} 必须是数字`
      if (schema.type === 'integer' && !Number.isInteger(val)) return `${path} 必须是整数`
      if (schema.minimum !== undefined && val < schema.minimum) return `${path} 不能小于 ${schema.minimum}`
      if (schema.maximum !== undefined && val > schema.maximum) return `${path} 不能大于 ${schema.maximum}`
      if (schema.enum?.length && !schema.enum.includes(val)) return `${path} 的值 ${val} 不在允许范围内`
      return null
    case 'array':
      if (!Array.isArray(val)) return `${path} 必须是数组`
      if (schema.items) {
        for (let i = 0; i < val.length; i += 1) {
          const itemErr = validateValueBySchema(schema.items, val[i], `${path}[${i}]`)
          if (itemErr) return itemErr
        }
      }
      return null
    case 'object':
      if (typeof val !== 'object' || Array.isArray(val)) return `${path} 必须是对象`
      return validateObjectBySchema(schema, val as Record<string, unknown>, path)
    default:
      return null
  }
}

export function cloneJson<T>(value: T): T {
  return JSON.parse(JSON.stringify(value ?? {})) as T
}

export function deepMergeObjects(target: Record<string, unknown>, source: Record<string, unknown>): Record<string, unknown> {
  const out = cloneJson(target)
  for (const [k, v] of Object.entries(source)) {
    if (v === undefined) continue
    if (v && typeof v === 'object' && !Array.isArray(v) && out[k] && typeof out[k] === 'object' && !Array.isArray(out[k])) {
      out[k] = deepMergeObjects(out[k] as Record<string, unknown>, v as Record<string, unknown>)
    } else {
      out[k] = cloneJson(v)
    }
  }
  return out
}

/** Build a native sing-box inbound JSON object from stored InboundSettings (for Schema/JSON editing). */
export function inboundSettingsToNativeJson(
  type: InboundType,
  tag: string,
  listenPort: number | undefined,
  settings: InboundSettings,
): Record<string, unknown> {
  const base = cloneJson((settings.raw ?? {}) as Record<string, unknown>)
  base.type = type
  if (tag) base.tag = tag
  if (type !== 'tun') {
    if (settings.listen || !base.listen) base.listen = settings.listen || '::'
    if (listenPort) base.listen_port = listenPort
  }

  if (type === 'vless') {
    if (settings.flow) base.flow = settings.flow
    if (settings.packet_encoding) base.packet_encoding = settings.packet_encoding
  } else if (type === 'shadowsocks') {
    if (settings.method) base.method = settings.method
    if (settings.ss_server_psk) base.password = settings.ss_server_psk
  } else if (type === 'hysteria2') {
    if (settings.up_mbps) base.up_mbps = settings.up_mbps
    if (settings.down_mbps) base.down_mbps = settings.down_mbps
    if (settings.ignore_client_bandwidth) base.ignore_client_bandwidth = true
    if (settings.obfs_type || settings.obfs_password) {
      base.obfs = {
        ...(typeof base.obfs === 'object' && base.obfs ? (base.obfs as Record<string, unknown>) : {}),
        type: settings.obfs_type || 'salamander',
        ...(settings.obfs_password ? { password: settings.obfs_password } : {}),
        ...(settings.obfs_type === 'gecko' && settings.gecko_min_packet_size ? { min_packet_size: settings.gecko_min_packet_size } : {}),
        ...(settings.obfs_type === 'gecko' && settings.gecko_max_packet_size ? { max_packet_size: settings.gecko_max_packet_size } : {}),
      }
    }
  } else if (type === 'tuic') {
    if (settings.congestion_control) base.congestion_control = settings.congestion_control
    if (settings.auth_timeout) base.auth_timeout = settings.auth_timeout
    if (settings.zero_rtt_handshake !== undefined) base.zero_rtt_handshake = settings.zero_rtt_handshake
    if (settings.heartbeat) base.heartbeat = settings.heartbeat
  } else if (type === 'snell') {
    if (settings.snell_version) base.version = settings.snell_version
    if (settings.snell_psk) base.psk = settings.snell_psk
    if (settings.snell_obfs_mode && settings.snell_obfs_mode !== 'none') base.obfs_mode = settings.snell_obfs_mode
    if (settings.snell_mode) base.mode = settings.snell_mode
  } else if (type === 'trojan' && settings.trojan_fallback) {
    base.fallback = { ...settings.trojan_fallback }
  }

  if (settings.transport?.type && settings.transport.type !== 'tcp') {
    const prevTr = (base.transport && typeof base.transport === 'object' ? base.transport : {}) as Record<string, unknown>
    base.transport = {
      ...prevTr,
      type: settings.transport.type,
      ...(settings.transport.path ? { path: settings.transport.path } : {}),
      ...(settings.transport.headers ? { headers: settings.transport.headers } : {}),
      ...(settings.transport.max_early_data ? { max_early_data: settings.transport.max_early_data } : {}),
      ...(settings.transport.early_data_header ? { early_data_header_name: settings.transport.early_data_header } : {}),
    }
  }

  if (settings.tls?.enabled || settings.tls?.reality?.enabled || settings.tls?.acme_domain) {
    const prevTls = (base.tls && typeof base.tls === 'object' ? base.tls : {}) as Record<string, unknown>
    const tlsObj: Record<string, unknown> = { ...prevTls, enabled: true }
    if (settings.tls.server_name) tlsObj.server_name = settings.tls.server_name
    if (settings.tls.alpn?.length) tlsObj.alpn = settings.tls.alpn
    if (settings.tls.certificate_path) tlsObj.certificate_path = settings.tls.certificate_path
    if (settings.tls.key_path) tlsObj.key_path = settings.tls.key_path
    if (settings.tls.acme_domain) {
      tlsObj.certificate_provider = {
        ...(typeof prevTls.certificate_provider === 'object' && prevTls.certificate_provider ? (prevTls.certificate_provider as Record<string, unknown>) : {}),
        type: 'acme',
        domain: [settings.tls.acme_domain],
        ...(settings.tls.acme_email ? { email: settings.tls.acme_email } : {}),
      }
    }
    if (settings.tls.reality?.enabled) {
      const prevReality = (prevTls.reality && typeof prevTls.reality === 'object' ? prevTls.reality : {}) as Record<string, unknown>
      tlsObj.reality = {
        ...prevReality,
        enabled: true,
        handshake: {
          ...(typeof prevReality.handshake === 'object' && prevReality.handshake ? (prevReality.handshake as Record<string, unknown>) : {}),
          server: settings.tls.reality.handshake_server || '',
          server_port: settings.tls.reality.handshake_server_port || 443,
        },
        ...(settings.tls.reality.private_key ? { private_key: settings.tls.reality.private_key } : {}),
        ...(settings.tls.reality.short_id?.length ? { short_id: settings.tls.reality.short_id } : {}),
        ...(settings.tls.reality.max_time_difference ? { max_time_difference: settings.tls.reality.max_time_difference } : {}),
      }
    }
    base.tls = tlsObj
  }
  return base
}

/** Merge an edited native sing-box JSON inbound object back into InboundSettings, keeping panel metadata intact. */
export function nativeJsonToInboundSettings(prev: InboundSettings, rawObj: Record<string, unknown>): InboundSettings {
  const next = cloneJson(prev || {})
  const cleanRaw = cloneJson(rawObj)
  delete cleanRaw.users
  next.raw = cleanRaw

  if (typeof rawObj.listen === 'string') {
    next.listen = rawObj.listen
  }
  if (typeof rawObj.flow === 'string') next.flow = rawObj.flow
  if (typeof rawObj.packet_encoding === 'string') next.packet_encoding = rawObj.packet_encoding
  if (typeof rawObj.method === 'string') next.method = rawObj.method
  if (typeof rawObj.password === 'string' && rawObj.type === 'shadowsocks') next.ss_server_psk = rawObj.password
  if (typeof rawObj.up_mbps === 'number') next.up_mbps = rawObj.up_mbps
  if (typeof rawObj.down_mbps === 'number') next.down_mbps = rawObj.down_mbps
  if (typeof rawObj.ignore_client_bandwidth === 'boolean') next.ignore_client_bandwidth = rawObj.ignore_client_bandwidth
  if (typeof rawObj.congestion_control === 'string') next.congestion_control = rawObj.congestion_control
  if (typeof rawObj.auth_timeout === 'string') next.auth_timeout = rawObj.auth_timeout
  if (typeof rawObj.zero_rtt_handshake === 'boolean') next.zero_rtt_handshake = rawObj.zero_rtt_handshake
  if (typeof rawObj.heartbeat === 'string') next.heartbeat = rawObj.heartbeat
  if (typeof rawObj.version === 'number') next.snell_version = rawObj.version
  if (typeof rawObj.psk === 'string') next.snell_psk = rawObj.psk
  if (typeof rawObj.obfs_mode === 'string') next.snell_obfs_mode = rawObj.obfs_mode
  if (typeof rawObj.mode === 'string') next.snell_mode = rawObj.mode

  if (rawObj.transport && typeof rawObj.transport === 'object' && !Array.isArray(rawObj.transport)) {
    const tr = rawObj.transport as Record<string, unknown>
    if (typeof tr.type === 'string' && tr.type !== 'tcp') {
      const nextTr: TransportSettings = { type: tr.type }
      if (typeof tr.path === 'string') nextTr.path = tr.path
      if (tr.headers && typeof tr.headers === 'object') nextTr.headers = tr.headers as Record<string, string | string[]>
      if (typeof tr.max_early_data === 'number') nextTr.max_early_data = tr.max_early_data
      if (typeof tr.early_data_header_name === 'string') nextTr.early_data_header = tr.early_data_header_name
      next.transport = nextTr
    } else {
      delete next.transport
    }
  } else {
    delete next.transport
  }

  if (rawObj.tls && typeof rawObj.tls === 'object' && !Array.isArray(rawObj.tls)) {
    const t = rawObj.tls as Record<string, unknown>
    const nextTls: TLSSettings = {
      enabled: t.enabled !== false,
      ...(typeof t.server_name === 'string' ? { server_name: t.server_name } : {}),
      ...(Array.isArray(t.alpn) ? { alpn: t.alpn.map(String) } : {}),
      ...(typeof t.certificate_path === 'string' ? { certificate_path: t.certificate_path } : {}),
      ...(typeof t.key_path === 'string' ? { key_path: t.key_path } : {}),
      ...(prev.tls?.self_signed ? { self_signed: true } : {}),
      ...(prev.tls?.insecure !== undefined ? { insecure: prev.tls.insecure } : {}),
      ...(prev.tls?.fingerprint ? { fingerprint: prev.tls.fingerprint } : {}),
    }
    if (t.certificate_provider && typeof t.certificate_provider === 'object') {
      const cp = t.certificate_provider as Record<string, unknown>
      if (cp.type === 'acme' && Array.isArray(cp.domain) && cp.domain.length > 0) {
        nextTls.acme_domain = String(cp.domain[0])
        if (typeof cp.email === 'string') nextTls.acme_email = cp.email
      }
    }
    if (t.reality && typeof t.reality === 'object') {
      const r = t.reality as Record<string, unknown>
      if (r.enabled) {
        const hs = (r.handshake && typeof r.handshake === 'object' ? r.handshake : {}) as Record<string, unknown>
        nextTls.reality = {
          enabled: true,
          handshake_server: typeof hs.server === 'string' ? hs.server : '',
          handshake_server_port: typeof hs.server_port === 'number' ? hs.server_port : 443,
          private_key: typeof r.private_key === 'string' ? r.private_key : (prev.tls?.reality?.private_key ?? ''),
          ...(prev.tls?.reality?.public_key ? { public_key: prev.tls.reality.public_key } : {}),
          ...(Array.isArray(r.short_id) ? { short_id: r.short_id.map(String) } : {}),
          ...(typeof r.max_time_difference === 'string' ? { max_time_difference: r.max_time_difference } : {}),
          ...(prev.tls?.reality?.guard ? { guard: prev.tls.reality.guard } : {}),
        }
      }
    }
    next.tls = nextTls
  } else {
    delete next.tls
  }

  return next
}
