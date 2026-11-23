import { useEffect, useState } from 'react'
import * as api from './api'
import type { InboundSettings, InboundType, OutboundSettings, OutboundType, TLSSettings, TransportSettings } from './types'

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
  outbound: JSONSchema
  route_rule: JSONSchema
}

function inboundMultiplexSubSchema(): JSONSchema {
  return {
    type: 'object',
    additionalProperties: true,
    properties: {
      enabled: { type: 'boolean', 'x-ui': { label: '启用多路复用', order: 1, widget: 'switch' } },
      padding: { type: 'boolean', 'x-ui': { label: '启用 Padding', order: 2, widget: 'switch' } },
      brutal: {
        type: 'object',
        additionalProperties: true,
        properties: {
          enabled: { type: 'boolean', 'x-ui': { label: '启用 Brutal 拥塞控制', order: 1, widget: 'switch' } },
          up_mbps: { type: 'integer', minimum: 0, 'x-ui': { label: '上行 Mbps', order: 2 } },
          down_mbps: { type: 'integer', minimum: 0, 'x-ui': { label: '下行 Mbps', order: 3 } },
        },
        'x-ui': { label: 'TCP Brutal', order: 3 },
      },
    },
    'x-ui': { label: '多路复用', group: '其他参数', advanced: true },
  }
}

function outboundMultiplexSubSchema(): JSONSchema {
  return {
    type: 'object',
    additionalProperties: true,
    properties: {
      enabled: { type: 'boolean', 'x-ui': { label: '启用多路复用', order: 1, widget: 'switch' } },
      protocol: { type: 'string', default: 'h2mux', enum: ['', 'h2mux', 'smux', 'yamux'], 'x-ui': { label: '复用协议', order: 2, widget: 'select' } },
      max_connections: { type: 'integer', minimum: 0, 'x-ui': { label: '最大物理连接数', order: 3 } },
      min_streams: { type: 'integer', minimum: 0, 'x-ui': { label: '最小并发流数', order: 4 } },
      max_streams: { type: 'integer', minimum: 0, 'x-ui': { label: '单连接最大流数', order: 5 } },
      padding: { type: 'boolean', 'x-ui': { label: '启用 Padding', order: 6, widget: 'switch' } },
      brutal: {
        type: 'object',
        additionalProperties: true,
        properties: {
          enabled: { type: 'boolean', 'x-ui': { label: '启用 Brutal 拥塞控制', order: 1, widget: 'switch' } },
          up_mbps: { type: 'integer', minimum: 0, 'x-ui': { label: '上行 Mbps', order: 2 } },
          down_mbps: { type: 'integer', minimum: 0, 'x-ui': { label: '下行 Mbps', order: 3 } },
        },
        'x-ui': { label: 'TCP Brutal', order: 7 },
      },
    },
    'x-ui': { label: '多路复用', group: '其他参数', advanced: true },
  }
}

function tlsInboundSubSchema(includeReality: boolean): JSONSchema {
  const props: Record<string, JSONSchema> = {
    enabled: { type: 'boolean', 'x-ui': { label: '启用 TLS', group: 'TLS', order: 1, widget: 'switch' } },
    server_name: { type: 'string', 'x-ui': { label: 'SNI', group: 'TLS', order: 2 } },
    alpn: { type: 'array', items: { type: 'string' }, 'x-ui': { label: 'ALPN', group: 'TLS', order: 3, widget: 'tags' } },
    min_version: { type: 'string', enum: ['', '1.0', '1.1', '1.2', '1.3'], 'x-ui': { label: '最低 TLS 版本', group: 'TLS', order: 4, widget: 'select', advanced: true } },
    max_version: { type: 'string', enum: ['', '1.0', '1.1', '1.2', '1.3'], 'x-ui': { label: '最高 TLS 版本', group: 'TLS', order: 5, widget: 'select', advanced: true } },
    cipher_suites: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '密码套件', group: 'TLS', order: 6, widget: 'tags', advanced: true } },
    curve_preferences: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '密钥交换曲线偏好', group: 'TLS', order: 7, widget: 'tags', advanced: true } },
    client_authentication: { type: 'string', enum: ['', 'no', 'request', 'require-any', 'verify-if-given', 'require-and-verify'], 'x-ui': { label: '客户端证书验证', group: 'TLS', order: 8, widget: 'select', advanced: true } },
    certificate_path: { type: 'string', 'x-ui': { label: '证书路径', group: 'TLS', order: 10 } },
    key_path: { type: 'string', 'x-ui': { label: '私钥路径', group: 'TLS', order: 11 } },
    ech: {
      type: 'object',
      additionalProperties: true,
      properties: {
        enabled: { type: 'boolean', 'x-ui': { label: '启用服务端 ECH', widget: 'switch' } },
        key: { type: 'array', items: { type: 'string' }, 'x-ui': { label: 'ECH 私钥 PEM', widget: 'lines', full_width: true } },
        key_path: { type: 'string', 'x-ui': { label: 'ECH 密钥路径' } },
      },
      'x-ui': { label: 'ECH', group: 'TLS', order: 16, advanced: true },
    },
  }
  if (includeReality) {
    props.reality = {
      type: 'object',
      additionalProperties: true,
      properties: {
        enabled: { type: 'boolean', 'x-ui': { label: '启用 REALITY', widget: 'switch' } },
        private_key: { type: 'string' },
        short_id: { type: 'array', items: { type: 'string' } },
        max_time_difference: { type: 'string' },
      },
    }
  }
  return { type: 'object', additionalProperties: true, properties: props }
}

function tlsOutboundSubSchema(includeReality: boolean): JSONSchema {
  const props: Record<string, JSONSchema> = {
    enabled: { type: 'boolean', 'x-ui': { label: '启用 TLS', group: 'TLS', order: 1, widget: 'switch' } },
    server_name: { type: 'string', 'x-ui': { label: 'SNI', group: 'TLS', order: 2 } },
    insecure: { type: 'boolean', 'x-ui': { label: '跳过证书验证', group: 'TLS', order: 3, widget: 'switch' } },
    alpn: { type: 'array', items: { type: 'string' }, 'x-ui': { label: 'ALPN', group: 'TLS', order: 4, widget: 'tags' } },
    min_version: { type: 'string', enum: ['', '1.0', '1.1', '1.2', '1.3'], 'x-ui': { label: '最低 TLS 版本', group: 'TLS', order: 5, widget: 'select', advanced: true } },
    max_version: { type: 'string', enum: ['', '1.0', '1.1', '1.2', '1.3'], 'x-ui': { label: '最高 TLS 版本', group: 'TLS', order: 6, widget: 'select', advanced: true } },
    cipher_suites: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '密码套件', group: 'TLS', order: 7, widget: 'tags', advanced: true } },
    curve_preferences: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '密钥交换曲线偏好', group: 'TLS', order: 8, widget: 'tags', advanced: true } },
    utls: {
      type: 'object',
      additionalProperties: true,
      properties: {
        enabled: { type: 'boolean', 'x-ui': { label: '启用 uTLS', widget: 'switch' } },
        fingerprint: { type: 'string', enum: ['', 'chrome', 'firefox', 'safari', 'ios', 'android', 'edge', '360', 'qq', 'random', 'randomized'], 'x-ui': { label: 'uTLS 指纹', widget: 'select' } },
      },
      'x-ui': { label: 'uTLS', group: 'TLS', order: 12 },
    },
    ech: {
      type: 'object',
      additionalProperties: true,
      properties: {
        enabled: { type: 'boolean', 'x-ui': { label: '启用 ECH', widget: 'switch' } },
        config: { type: 'array', items: { type: 'string' }, 'x-ui': { label: 'ECH 配置', widget: 'lines', full_width: true } },
        config_path: { type: 'string', 'x-ui': { label: 'ECH 配置路径' } },
        query_server_name: { type: 'string', 'x-ui': { label: 'ECH 查询域名' } },
        pq_signature_schemes_enabled: { type: 'boolean', 'x-ui': { label: '启用后量子签名', widget: 'switch' } },
        dynamic_record_sizing_disabled: { type: 'boolean', 'x-ui': { label: '禁用动态记录大小', widget: 'switch' } },
      },
      'x-ui': { label: 'ECH', group: 'TLS', order: 15, advanced: true },
    },
  }
  if (includeReality) {
    props.reality = {
      type: 'object',
      additionalProperties: true,
      properties: {
        enabled: { type: 'boolean', 'x-ui': { label: '启用 REALITY', widget: 'switch' } },
        public_key: { type: 'string', 'x-ui': { label: '公钥' } },
        short_id: { type: 'string', 'x-ui': { label: 'Short ID' } },
      },
    }
  }
  return { type: 'object', additionalProperties: true, properties: props }
}

const TRANSPORT_SUBSCHEMA: JSONSchema = {
  type: 'object',
  additionalProperties: true,
  properties: {
    type: { type: 'string', default: 'tcp', enum: ['tcp', 'ws', 'http', 'grpc', 'httpupgrade', 'quic'], 'x-ui': { label: '传输层', order: 1, widget: 'select' } },
    path: { type: 'string', 'x-ui': { label: '路径', order: 2, placeholder: '/ws' } },
    host: { type: 'string', 'x-ui': { label: 'Host', order: 3 } },
    method: { type: 'string', 'x-ui': { label: 'HTTP 方法', order: 4, advanced: true } },
    service_name: { type: 'string', 'x-ui': { label: 'gRPC Service Name', order: 6, placeholder: 'TunService' } },
    idle_timeout: { type: 'string', 'x-ui': { label: '空闲超时', order: 7, placeholder: '15s', advanced: true } },
    ping_timeout: { type: 'string', 'x-ui': { label: 'Ping 超时', order: 8, placeholder: '15s', advanced: true } },
    permit_without_stream: { type: 'boolean', 'x-ui': { label: '无流保持 Ping', order: 9, widget: 'switch', advanced: true } },
    max_early_data: { type: 'integer', minimum: 0, 'x-ui': { label: 'Early Data 长度', order: 10 } },
    early_data_header_name: { type: 'string', 'x-ui': { label: 'Early Data 头', order: 11 } },
  },
}

function inboundWithListen(tcp: boolean, udp: boolean, props: Record<string, JSONSchema>): JSONSchema {
  const merged: Record<string, JSONSchema> = {
    listen: { type: 'string', default: '::', 'x-ui': { label: '监听地址', group: '基本', order: 2 } },
    listen_port: { type: 'integer', minimum: 1, maximum: 65535, 'x-ui': { label: '端口', group: '基本', order: 3 } },
    detour: { type: 'string', 'x-ui': { label: '前置入站', group: '其他参数', order: 24, advanced: true } },
  }
  if (tcp) {
    merged.tcp_fast_open = { type: 'boolean', 'x-ui': { label: 'TCP Fast Open', group: '其他参数', order: 20, widget: 'switch', advanced: true } }
    merged.tcp_multi_path = { type: 'boolean', 'x-ui': { label: 'TCP Multi Path', group: '其他参数', order: 21, widget: 'switch', advanced: true } }
    merged.proxy_protocol = { type: 'boolean', 'x-ui': { label: 'Proxy Protocol', group: '其他参数', order: 25, widget: 'switch', advanced: true } }
    merged.proxy_protocol_accept_no_header = { type: 'boolean', 'x-ui': { label: '允许无 Proxy Protocol 头连接', group: '其他参数', order: 26, widget: 'switch', advanced: true } }
  }
  if (udp) {
    merged.udp_fragment = { type: 'boolean', 'x-ui': { label: 'UDP Fragment', group: '其他参数', order: 22, widget: 'switch', advanced: true } }
    merged.udp_timeout = { type: 'string', 'x-ui': { label: 'UDP 超时', group: '其他参数', order: 23, placeholder: '5m', advanced: true } }
  }
  return { type: 'object', additionalProperties: true, properties: { ...merged, ...props } }
}

export const DEFAULT_INBOUND_SCHEMA: JSONSchema = {
  type: 'object',
  additionalProperties: true,
  required: ['type'],
  'x-discriminator': 'type',
  properties: {
    type: {
      type: 'string',
      enum: ['vless', 'vmess', 'trojan', 'anytls', 'shadowsocks', 'tuic', 'hysteria2', 'snell', 'socks', 'http', 'mixed', 'tun', 'direct', 'redirect', 'tproxy'],
      'x-ui': { label: '协议', group: '基本', order: 1, widget: 'select' },
    },
    tag: { type: 'string', 'x-ui': { label: '标签', group: '基本', order: 2 } },
  },
  'x-variants': {
    shadowsocks: inboundWithListen(true, true, { network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络限制', group: '协议参数', order: 3, widget: 'select', advanced: true } }, multiplex: inboundMultiplexSubSchema() }),
    snell: inboundWithListen(true, false, {}),
    socks: inboundWithListen(true, true, {}),
    mixed: inboundWithListen(true, true, {}),
    http: inboundWithListen(true, false, { realm: { type: 'string', 'x-ui': { label: '认证域', group: '协议参数', order: 1, advanced: true } }, tls: tlsInboundSubSchema(false) }),
    tun: {
      type: 'object',
      additionalProperties: true,
      properties: {
        interface_name: { type: 'string', 'x-ui': { label: '网卡名称', group: '协议参数', order: 1, placeholder: 'tun0' } },
        address: { type: 'array', items: { type: 'string' }, 'x-ui': { label: 'TUN 地址 CIDR', group: '协议参数', order: 2, widget: 'tags', placeholder: '172.19.0.1/30' } },
        mtu: { type: 'integer', default: 9000, minimum: 576, maximum: 65535, 'x-ui': { label: 'MTU', group: '协议参数', order: 3 } },
        stack: { type: 'string', enum: ['', 'system', 'gvisor', 'mixed'], 'x-ui': { label: '网络栈', group: '协议参数', order: 4, widget: 'select' } },
        auto_route: { type: 'boolean', default: true, 'x-ui': { label: '自动路由', group: '协议参数', order: 5, widget: 'switch' } },
        strict_route: { type: 'boolean', 'x-ui': { label: '严格路由', group: '协议参数', order: 6, widget: 'switch' } },
        auto_redirect: { type: 'boolean', 'x-ui': { label: 'Auto Redirect', group: '协议参数', order: 7, widget: 'switch', advanced: true } },
        endpoint_independent_nat: { type: 'boolean', 'x-ui': { label: '独立端点 NAT', group: '协议参数', order: 8, widget: 'switch', advanced: true } },
        route_address: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '路由网段', group: '协议参数', order: 9, widget: 'tags', advanced: true } },
        route_exclude_address: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '排除网段', group: '协议参数', order: 10, widget: 'tags', advanced: true } },
        udp_timeout: { type: 'string', 'x-ui': { label: 'UDP 超时', group: '协议参数', order: 11, placeholder: '5m' } },
      },
    },
    direct: inboundWithListen(true, true, {
      network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络', group: '协议参数', order: 1, widget: 'select' } },
      override_address: { type: 'string', 'x-ui': { label: '覆盖目标地址', group: '协议参数', order: 2 } },
      override_port: { type: 'integer', minimum: 1, maximum: 65535, 'x-ui': { label: '覆盖目标端口', group: '协议参数', order: 3 } },
    }),
    redirect: inboundWithListen(true, false, {}),
    tproxy: inboundWithListen(true, true, { network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络', group: '协议参数', widget: 'select' } } }),
    tuic: inboundWithListen(false, true, { tls: tlsInboundSubSchema(false) }),
    trojan: inboundWithListen(true, true, { fallback_for_alpn: { type: 'object', additionalProperties: true, 'x-ui': { label: '按 ALPN 回落映射', group: '回落', widget: 'json', full_width: true, advanced: true } }, multiplex: inboundMultiplexSubSchema(), transport: TRANSPORT_SUBSCHEMA, tls: tlsInboundSubSchema(false) }),
    anytls: inboundWithListen(true, false, { padding_scheme: { type: 'array', items: { type: 'string' }, 'x-ui': { label: 'Padding 方案', group: '协议参数', widget: 'lines', full_width: true, advanced: true } }, tls: tlsInboundSubSchema(false) }),
    vless: inboundWithListen(true, true, { flow: { type: 'string', enum: ['', 'xtls-rprx-vision'] }, multiplex: inboundMultiplexSubSchema(), transport: TRANSPORT_SUBSCHEMA, tls: tlsInboundSubSchema(true) }),
    vmess: inboundWithListen(true, true, { multiplex: inboundMultiplexSubSchema(), transport: TRANSPORT_SUBSCHEMA, tls: tlsInboundSubSchema(true) }),
    hysteria2: inboundWithListen(false, true, {
      masquerade: { type: 'string', 'x-ui': { label: '伪装站点', group: '协议参数', order: 10, advanced: true } },
      bbr_profile: { type: 'string', enum: ['', 'conservative', 'standard', 'aggressive'], 'x-ui': { label: 'BBR 配置', group: '协议参数', order: 11, widget: 'select', advanced: true } },
      brutal_debug: { type: 'boolean', 'x-ui': { label: 'Brutal 调试日志', group: '协议参数', order: 12, widget: 'switch', advanced: true } },
      tls: tlsInboundSubSchema(false),
    }),
  },
}

export const DEFAULT_OUTBOUND_SCHEMA: JSONSchema = {
  type: 'object',
  additionalProperties: true,
  required: ['type'],
  'x-discriminator': 'type',
  properties: {
    type: {
      type: 'string',
      enum: ['vless', 'vmess', 'trojan', 'anytls', 'shadowsocks', 'tuic', 'hysteria2', 'snell', 'socks', 'http'],
      'x-ui': { label: '类型', group: '基本', order: 1, widget: 'select' },
    },
    tag: { type: 'string', 'x-ui': { label: '标签', group: '基本', order: 2 } },
    server: { type: 'string', 'x-ui': { label: '服务器地址', group: '基本', order: 3 } },
    server_port: { type: 'integer', minimum: 1, maximum: 65535, 'x-ui': { label: '端口', group: '基本', order: 4 } },
    detour: { type: 'string', 'x-ui': { label: '前置出站', group: '其他参数', order: 20, advanced: true } },
    connect_timeout: { type: 'string', 'x-ui': { label: '连接超时', group: '其他参数', order: 21, placeholder: '5s', advanced: true } },
    domain_resolver: { type: 'string', 'x-ui': { label: '域名解析器', group: '其他参数', order: 22, advanced: true } },
    bind_interface: { type: 'string', 'x-ui': { label: '绑定网卡', group: '其他参数', order: 23, advanced: true } },
    inet4_bind_address: { type: 'string', 'x-ui': { label: '绑定 IPv4 地址', group: '其他参数', order: 24, advanced: true } },
    inet6_bind_address: { type: 'string', 'x-ui': { label: '绑定 IPv6 地址', group: '其他参数', order: 25, advanced: true } },
    routing_mark: { type: 'integer', minimum: 0, 'x-ui': { label: '路由标记', group: '其他参数', order: 26, advanced: true } },
    tcp_fast_open: { type: 'boolean', 'x-ui': { label: 'TCP Fast Open', group: '其他参数', order: 27, widget: 'switch', advanced: true } },
    tcp_multi_path: { type: 'boolean', 'x-ui': { label: 'TCP Multi Path', group: '其他参数', order: 28, widget: 'switch', advanced: true } },
    udp_fragment: { type: 'boolean', 'x-ui': { label: 'UDP Fragment', group: '其他参数', order: 29, widget: 'switch', advanced: true } },
  },
  'x-variants': {
    shadowsocks: {
      type: 'object',
      additionalProperties: true,
      properties: {
        plugin: { type: 'string', enum: ['', 'obfs-local', 'v2ray-plugin'], 'x-ui': { label: 'SIP003 插件', group: '协议参数', order: 3, widget: 'select', advanced: true } },
        plugin_opts: { type: 'string', 'x-ui': { label: '插件参数', group: '协议参数', order: 4, advanced: true } },
        network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络限制', group: '协议参数', order: 5, widget: 'select', advanced: true } },
        udp_over_tcp: { type: 'boolean', 'x-ui': { label: 'UDP over TCP', group: '协议参数', order: 6, widget: 'switch', advanced: true } },
        multiplex: outboundMultiplexSubSchema(),
      },
    },
    snell: { type: 'object', additionalProperties: true, properties: {} },
    socks: {
      type: 'object',
      additionalProperties: true,
      properties: {
        version: { type: 'string', default: '5', enum: ['', '4', '4a', '5'], 'x-ui': { label: 'SOCKS 版本', group: '协议参数', order: 1, widget: 'select' } },
        network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络限制', group: '协议参数', order: 2, widget: 'select', advanced: true } },
        udp_over_tcp: { type: 'boolean', 'x-ui': { label: 'UDP over TCP', group: '协议参数', order: 3, widget: 'switch', advanced: true } },
      },
    },
    http: {
      type: 'object',
      additionalProperties: true,
      properties: {
        path: { type: 'string', 'x-ui': { label: '请求路径', group: '协议参数', order: 1, advanced: true } },
        headers: { type: 'object', additionalProperties: true, 'x-ui': { label: '自定义请求头', group: '协议参数', order: 2, widget: 'json', full_width: true, advanced: true } },
        tls: tlsOutboundSubSchema(false),
      },
    },
    tuic: {
      type: 'object',
      additionalProperties: true,
      properties: {
        udp_relay_mode: { type: 'string', enum: ['', 'native', 'quic'], 'x-ui': { label: 'UDP 中继模式', group: '协议参数', order: 2, widget: 'select' } },
        udp_over_stream: { type: 'boolean', 'x-ui': { label: 'UDP over Stream', group: '协议参数', order: 3, widget: 'switch', advanced: true } },
        zero_rtt_handshake: { type: 'boolean', 'x-ui': { label: '0-RTT 握手', group: '协议参数', order: 4, widget: 'switch' } },
        heartbeat: { type: 'string', default: '10s', 'x-ui': { label: '心跳间隔', group: '协议参数', order: 5 } },
        network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络限制', group: '协议参数', order: 6, widget: 'select', advanced: true } },
        tls: tlsOutboundSubSchema(false),
      },
    },
    trojan: {
      type: 'object',
      additionalProperties: true,
      properties: {
        network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络限制', group: '协议参数', order: 1, widget: 'select', advanced: true } },
        multiplex: outboundMultiplexSubSchema(),
        transport: TRANSPORT_SUBSCHEMA,
        tls: tlsOutboundSubSchema(false),
      },
    },
    anytls: {
      type: 'object',
      additionalProperties: true,
      properties: {
        idle_session_check_interval: { type: 'string', 'x-ui': { label: '空闲检查间隔', group: '协议参数', order: 1, placeholder: '30s', advanced: true } },
        idle_session_timeout: { type: 'string', 'x-ui': { label: '空闲会话超时', group: '协议参数', order: 2, placeholder: '30s', advanced: true } },
        min_idle_session: { type: 'integer', minimum: 0, 'x-ui': { label: '最小空闲会话数', group: '协议参数', order: 3, advanced: true } },
        tls: tlsOutboundSubSchema(false),
      },
    },
    vless: {
      type: 'object',
      additionalProperties: true,
      properties: {
        packet_encoding: { type: 'string', default: 'xudp', enum: ['', 'xudp', 'packetaddr'], 'x-ui': { label: 'UDP 封包编码', group: '协议参数', order: 2, widget: 'select' } },
        network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络限制', group: '协议参数', order: 3, widget: 'select', advanced: true } },
        multiplex: outboundMultiplexSubSchema(),
        transport: TRANSPORT_SUBSCHEMA,
        tls: tlsOutboundSubSchema(true),
      },
    },
    vmess: {
      type: 'object',
      additionalProperties: true,
      properties: {
        packet_encoding: { type: 'string', enum: ['', 'xudp', 'packetaddr'], 'x-ui': { label: 'UDP 封包编码', group: '协议参数', order: 3, widget: 'select', advanced: true } },
        global_padding: { type: 'boolean', 'x-ui': { label: '全局随机填充', group: '协议参数', order: 4, widget: 'switch', advanced: true } },
        authenticated_length: { type: 'boolean', 'x-ui': { label: '长度块认证', group: '协议参数', order: 5, widget: 'switch', advanced: true } },
        network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络限制', group: '协议参数', order: 6, widget: 'select', advanced: true } },
        multiplex: outboundMultiplexSubSchema(),
        transport: TRANSPORT_SUBSCHEMA,
        tls: tlsOutboundSubSchema(false),
      },
    },
    hysteria2: {
      type: 'object',
      additionalProperties: true,
      properties: {
        up_mbps: { type: 'integer', minimum: 0, 'x-ui': { label: '上行 Mbps', group: '协议参数', order: 1 } },
        down_mbps: { type: 'integer', minimum: 0, 'x-ui': { label: '下行 Mbps', group: '协议参数', order: 2 } },
        server_ports: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '端口跳跃', group: '协议参数', order: 3, widget: 'tags', placeholder: '20000:50000', advanced: true } },
        hop_interval: { type: 'string', 'x-ui': { label: '跳跃间隔', group: '协议参数', order: 4, placeholder: '30s', advanced: true } },
        network: { type: 'string', enum: ['', 'tcp', 'udp'], 'x-ui': { label: '网络限制', group: '协议参数', order: 5, widget: 'select', advanced: true } },
        tls: tlsOutboundSubSchema(false),
      },
    },
  },
}

export const DEFAULT_ROUTE_RULE_SCHEMA: JSONSchema = {
  type: 'object',
  additionalProperties: true,
  properties: {
    action: { type: 'string', default: 'route', enum: ['', 'route', 'reject', 'sniff', 'hijack-dns', 'resolve', 'route-options'], 'x-ui': { label: '动作', group: '动作', order: 1, widget: 'select' } },
    method: { type: 'string', default: 'default', enum: ['', 'default', 'drop', 'reply'], 'x-ui': { label: '拒绝方式', group: '动作', order: 2, widget: 'select', actions: ['reject'] } },
    sniffer: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '嗅探协议', group: '动作', order: 3, widget: 'multiselect', actions: ['sniff'] } },
    timeout: { type: 'string', 'x-ui': { label: '嗅探超时', group: '动作', order: 4, placeholder: '300ms', actions: ['sniff'], advanced: true } },
    server: { type: 'string', 'x-ui': { label: '解析 DNS 服务器', group: '动作', order: 5, actions: ['resolve'], advanced: true } },
    strategy: { type: 'string', enum: ['', 'prefer_ipv4', 'prefer_ipv6', 'ipv4_only', 'ipv6_only'], 'x-ui': { label: '解析策略', group: '动作', order: 6, widget: 'select', actions: ['resolve', 'route-options'], advanced: true } },
    inbound: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '入站来源', group: '来源与身份', order: 10, widget: 'multiselect' } },
    auth_user: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '认证用户', group: '来源与身份', order: 11, widget: 'multiselect', actions: ['route', 'reject', 'resolve', 'route-options'] } },
    rule_set: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '规则集', group: '规则集', order: 20, widget: 'multiselect', actions: ['route', 'reject', 'resolve', 'route-options'] } },
    rule_set_ip_cidr_match_source: { type: 'boolean', 'x-ui': { label: '规则集匹配来源 IP', group: '规则集', order: 21, widget: 'switch', actions: ['route', 'reject', 'resolve', 'route-options'] } },
    rule_set_ip_cidr_accept_empty: { type: 'boolean', 'x-ui': { label: '规则集空 IP 也视为匹配', group: '规则集', order: 22, widget: 'switch', actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    domain: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '完整域名', group: '域名匹配', order: 30, widget: 'lines', full_width: true, actions: ['route', 'reject', 'resolve', 'route-options'] } },
    domain_suffix: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '域名后缀', group: '域名匹配', order: 31, widget: 'lines', full_width: true, actions: ['route', 'reject', 'resolve', 'route-options'] } },
    domain_keyword: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '域名关键词', group: '域名匹配', order: 32, widget: 'lines', full_width: true, actions: ['route', 'reject', 'resolve', 'route-options'] } },
    domain_regex: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '域名正则', group: '域名匹配', order: 33, widget: 'lines', full_width: true, actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    ip_cidr: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '目标 IP / CIDR', group: 'IP 与端口', order: 40, widget: 'lines', full_width: true, actions: ['route', 'reject', 'resolve', 'route-options'] } },
    ip_is_private: { type: 'boolean', 'x-ui': { label: '目标为内网/私有 IP', group: 'IP 与端口', order: 41, widget: 'switch', actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    source_ip_cidr: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '来源 IP / CIDR', group: 'IP 与端口', order: 42, widget: 'lines', full_width: true, actions: ['route', 'reject', 'resolve', 'route-options'] } },
    source_ip_is_private: { type: 'boolean', 'x-ui': { label: '来源为内网/私有 IP', group: 'IP 与端口', order: 43, widget: 'switch', actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    port: { type: 'array', items: { type: 'integer', minimum: 1, maximum: 65535 }, 'x-ui': { label: '目标端口', group: 'IP 与端口', order: 44, widget: 'lines', actions: ['route', 'reject', 'resolve', 'route-options'] } },
    port_range: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '目标端口范围', group: 'IP 与端口', order: 45, widget: 'tags', placeholder: '1000:2000', actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    source_port: { type: 'array', items: { type: 'integer', minimum: 1, maximum: 65535 }, 'x-ui': { label: '来源端口', group: 'IP 与端口', order: 46, widget: 'lines', actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    source_port_range: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '来源端口范围', group: 'IP 与端口', order: 47, widget: 'tags', placeholder: '1000:2000', actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    network: { type: 'string', enum: ['', 'tcp', 'udp', 'icmp'], 'x-ui': { label: '网络类型', group: '协议与版本', order: 50, widget: 'select', actions: ['route', 'reject', 'resolve', 'route-options'] } },
    ip_version: { type: 'integer', enum: [4, 6], 'x-ui': { label: 'IP 版本', group: '协议与版本', order: 51, widget: 'select', actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    protocol: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '应用协议', group: '协议与版本', order: 52, widget: 'tags', actions: ['route', 'reject', 'resolve', 'route-options'] } },
    client: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '识别客户端', group: '协议与版本', order: 53, widget: 'tags', actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    process_name: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '进程名', group: '进程与环境', order: 60, widget: 'tags', actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    process_path: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '进程路径', group: '进程与环境', order: 61, widget: 'lines', full_width: true, actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    process_path_regex: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '进程路径正则', group: '进程与环境', order: 62, widget: 'lines', full_width: true, actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    package_name: { type: 'array', items: { type: 'string' }, 'x-ui': { label: '应用包名', group: '进程与环境', order: 63, widget: 'tags', actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    clash_mode: { type: 'string', 'x-ui': { label: 'Clash 模式', group: '进程与环境', order: 64, actions: ['route', 'reject', 'resolve', 'route-options'], advanced: true } },
    invert: { type: 'boolean', 'x-ui': { label: '匹配结果取反', group: '高级选项', order: 70, widget: 'switch', advanced: true } },
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
  outbound: DEFAULT_OUTBOUND_SCHEMA,
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
        const next = raw as unknown as Partial<SchemaBundle>
        if (next.inbound && next.route_rule) {
          const merged: SchemaBundle = {
            version: next.version || DEFAULT_BUNDLE.version,
            inbound: next.inbound,
            outbound: next.outbound ?? DEFAULT_OUTBOUND_SCHEMA,
            route_rule: next.route_rule,
          }
          cachedBundle = merged
          setBundle(merged)
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
  else delete base.tag
  if (type !== 'tun') {
    if (settings.listen || !base.listen) base.listen = settings.listen || '::'
    if (listenPort) base.listen_port = listenPort
  } else {
    delete base.listen
    delete base.listen_port
  }

  if (type === 'vless') {
    if (settings.flow) base.flow = settings.flow
    else delete base.flow
    // packet_encoding belongs to client subscriptions, not server-side VLESS inbound JSON.
    delete base.packet_encoding
  } else if (type === 'shadowsocks') {
    if (settings.method) base.method = settings.method
    if (settings.ss_server_psk) base.password = settings.ss_server_psk
  } else if (type === 'hysteria2') {
    if (settings.up_mbps) base.up_mbps = settings.up_mbps
    else delete base.up_mbps
    if (settings.down_mbps) base.down_mbps = settings.down_mbps
    else delete base.down_mbps
    if (settings.ignore_client_bandwidth) base.ignore_client_bandwidth = true
    else delete base.ignore_client_bandwidth
    if (settings.obfs_type || settings.obfs_password) {
      base.obfs = {
        ...(typeof base.obfs === 'object' && base.obfs ? (base.obfs as Record<string, unknown>) : {}),
        type: settings.obfs_type || 'salamander',
        ...(settings.obfs_password ? { password: settings.obfs_password } : {}),
        ...(settings.obfs_type === 'gecko' && settings.gecko_min_packet_size ? { min_packet_size: settings.gecko_min_packet_size } : {}),
        ...(settings.obfs_type === 'gecko' && settings.gecko_max_packet_size ? { max_packet_size: settings.gecko_max_packet_size } : {}),
      }
    } else {
      delete base.obfs
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
    else delete base.obfs_mode
    if (settings.snell_mode) base.mode = settings.snell_mode
    else delete base.mode
  } else if (type === 'trojan') {
    if (settings.trojan_fallback) base.fallback = { ...settings.trojan_fallback }
    else delete base.fallback
  }

  if (settings.transport?.type && settings.transport.type !== 'tcp') {
    const prevTr = (base.transport && typeof base.transport === 'object' ? base.transport : {}) as Record<string, unknown>
    const nextTr: Record<string, unknown> = {
      ...prevTr,
      type: settings.transport.type,
    }
    if (settings.transport.type === 'ws' || settings.transport.type === 'httpupgrade') {
      if (settings.transport.path) nextTr.path = settings.transport.path
      else delete nextTr.path
      if (settings.transport.headers && Object.keys(settings.transport.headers).length > 0) nextTr.headers = settings.transport.headers
      else delete nextTr.headers
      if (settings.transport.max_early_data) nextTr.max_early_data = settings.transport.max_early_data
      else delete nextTr.max_early_data
      if (settings.transport.early_data_header) nextTr.early_data_header_name = settings.transport.early_data_header
      else delete nextTr.early_data_header_name
    } else {
      if (settings.transport.path) nextTr.path = settings.transport.path
      if (settings.transport.headers) nextTr.headers = settings.transport.headers
    }
    base.transport = nextTr
  } else {
    delete base.transport
  }

  if (settings.tls?.enabled || settings.tls?.reality?.enabled || settings.tls?.acme_domain) {
    const prevTls = (base.tls && typeof base.tls === 'object' ? base.tls : {}) as Record<string, unknown>
    const tlsObj: Record<string, unknown> = { ...prevTls, enabled: true }
    // Client-only fields must never appear inside server inbound TLS JSON.
    delete tlsObj.insecure
    delete tlsObj.utls
    if (settings.tls.server_name) tlsObj.server_name = settings.tls.server_name
    else delete tlsObj.server_name
    if (settings.tls.alpn?.length) tlsObj.alpn = settings.tls.alpn
    else delete tlsObj.alpn
    if (settings.tls.certificate_path) tlsObj.certificate_path = settings.tls.certificate_path
    else delete tlsObj.certificate_path
    if (settings.tls.key_path) tlsObj.key_path = settings.tls.key_path
    else delete tlsObj.key_path
    if (settings.tls.acme_domain) {
      tlsObj.certificate_provider = {
        ...(typeof prevTls.certificate_provider === 'object' && prevTls.certificate_provider ? (prevTls.certificate_provider as Record<string, unknown>) : {}),
        type: 'acme',
        domain: [settings.tls.acme_domain],
        ...(settings.tls.acme_email ? { email: settings.tls.acme_email } : {}),
      }
    } else if (
      typeof tlsObj.certificate_provider === 'object' &&
      tlsObj.certificate_provider &&
      (tlsObj.certificate_provider as Record<string, unknown>).type === 'acme'
    ) {
      delete tlsObj.certificate_provider
    }
    if (settings.tls.reality?.enabled) {
      const prevReality = (prevTls.reality && typeof prevTls.reality === 'object' ? prevTls.reality : {}) as Record<string, unknown>
      const nextReality: Record<string, unknown> = {
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
      delete nextReality.public_key
      tlsObj.reality = nextReality
    } else {
      delete tlsObj.reality
    }
    base.tls = tlsObj
  } else {
    delete base.tls
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
    next.ipv6_only = rawObj.listen.includes(':') && rawObj.listen !== '::'
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
  if (rawObj.obfs && typeof rawObj.obfs === 'object' && !Array.isArray(rawObj.obfs)) {
    const obfs = rawObj.obfs as Record<string, unknown>
    if (typeof obfs.type === 'string') next.obfs_type = obfs.type
    if (typeof obfs.password === 'string') next.obfs_password = obfs.password
    if (typeof obfs.min_packet_size === 'number') next.gecko_min_packet_size = obfs.min_packet_size
    if (typeof obfs.max_packet_size === 'number') next.gecko_max_packet_size = obfs.max_packet_size
  } else if (rawObj.type === 'hysteria2') {
    delete next.obfs_type
    delete next.obfs_password
    delete next.gecko_min_packet_size
    delete next.gecko_max_packet_size
  }
  if (rawObj.fallback && typeof rawObj.fallback === 'object' && !Array.isArray(rawObj.fallback)) {
    const fb = rawObj.fallback as Record<string, unknown>
    if (typeof fb.server === 'string' && fb.server) {
      next.trojan_fallback = {
        server: fb.server,
        server_port: typeof fb.server_port === 'number' ? fb.server_port : 80,
      }
    } else {
      delete next.trojan_fallback
    }
  } else if (rawObj.type === 'trojan') {
    delete next.trojan_fallback
  }

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
        }
      }
    }
    if (nextTls.reality?.enabled || nextTls.acme_domain || nextTls.certificate_path) {
      delete nextTls.self_signed
    }
    next.tls = nextTls
  } else {
    delete next.tls
  }

  return next
}

/** Build a native sing-box outbound JSON object from stored OutboundSettings (for Schema/JSON editing). */
export function outboundSettingsToNativeJson(
  type: OutboundType,
  tag: string,
  settings: OutboundSettings,
): Record<string, unknown> {
  const inner = settings.settings || {}
  const base = cloneJson((inner.raw ?? {}) as Record<string, unknown>)
  base.type = type
  if (tag) base.tag = tag
  if (settings.server) base.server = settings.server
  if (settings.server_port) base.server_port = settings.server_port

  if (type === 'vless') {
    if (settings.uuid) base.uuid = settings.uuid
    if (inner.flow) base.flow = inner.flow
    else delete base.flow
    if (inner.packet_encoding && inner.packet_encoding !== 'none') base.packet_encoding = inner.packet_encoding
    else if (inner.packet_encoding === 'none') base.packet_encoding = ''
  } else if (type === 'vmess') {
    if (settings.uuid) base.uuid = settings.uuid
    if (inner.vmess_security) base.security = inner.vmess_security
    if (inner.vmess_alter_id !== undefined) base.alter_id = inner.vmess_alter_id
  } else if (type === 'trojan') {
    if (settings.password) base.password = settings.password
  } else if (type === 'shadowsocks') {
    if (inner.method) base.method = inner.method
    const pw = settings.password || inner.ss_server_psk
    if (pw) base.password = pw
  } else if (type === 'hysteria2') {
    if (settings.password) base.password = settings.password
    if (inner.up_mbps) base.up_mbps = inner.up_mbps
    else delete base.up_mbps
    if (inner.down_mbps) base.down_mbps = inner.down_mbps
    else delete base.down_mbps
    if (inner.obfs_type || inner.obfs_password) {
      base.obfs = {
        ...(typeof base.obfs === 'object' && base.obfs ? (base.obfs as Record<string, unknown>) : {}),
        type: inner.obfs_type || 'salamander',
        ...(inner.obfs_password ? { password: inner.obfs_password } : {}),
        ...(inner.obfs_type === 'gecko' && inner.gecko_min_packet_size ? { min_packet_size: inner.gecko_min_packet_size } : {}),
        ...(inner.obfs_type === 'gecko' && inner.gecko_max_packet_size ? { max_packet_size: inner.gecko_max_packet_size } : {}),
      }
    } else {
      delete base.obfs
    }
  } else if (type === 'tuic') {
    if (settings.uuid) base.uuid = settings.uuid
    if (settings.password) base.password = settings.password
    if (inner.congestion_control) base.congestion_control = inner.congestion_control
    if (inner.zero_rtt_handshake !== undefined) base.zero_rtt_handshake = inner.zero_rtt_handshake
    if (inner.heartbeat) base.heartbeat = inner.heartbeat
    else delete base.heartbeat
  } else if (type === 'anytls') {
    if (settings.password) base.password = settings.password
  } else if (type === 'snell') {
    const psk = settings.password || inner.snell_psk
    if (psk) base.psk = psk
    if (inner.snell_version) base.version = inner.snell_version
    if (inner.snell_network) base.network = inner.snell_network
    if (inner.snell_reuse) base.reuse = true
    else delete base.reuse
    if (inner.snell_version === 4 && inner.snell_obfs_mode && inner.snell_obfs_mode !== 'none') base.obfs_mode = inner.snell_obfs_mode
    else delete base.obfs_mode
    if (inner.snell_version === 4 && inner.snell_obfs_host) base.obfs_host = inner.snell_obfs_host
    else delete base.obfs_host
    if (inner.snell_version === 6 && inner.snell_mode) base.mode = inner.snell_mode
    else delete base.mode
  } else if (type === 'socks' || type === 'http') {
    const user = settings.username ?? inner.username
    const pw = settings.password ?? inner.password
    if (user) base.username = user
    else delete base.username
    if (pw) base.password = pw
    else delete base.password
    if (type === 'http') {
      if (inner.http_path) base.path = inner.http_path
      if (inner.http_headers && Object.keys(inner.http_headers).length > 0) base.headers = inner.http_headers
    }
  }

  if (inner.transport?.type && inner.transport.type !== 'tcp') {
    const prevTr = (base.transport && typeof base.transport === 'object' ? base.transport : {}) as Record<string, unknown>
    base.transport = {
      ...prevTr,
      type: inner.transport.type,
      ...(inner.transport.path ? { path: inner.transport.path } : {}),
      ...(inner.transport.headers ? { headers: inner.transport.headers } : {}),
    }
  }

  if (inner.tls?.enabled || inner.tls?.reality?.enabled) {
    const prevTls = (base.tls && typeof base.tls === 'object' ? base.tls : {}) as Record<string, unknown>
    const tlsObj: Record<string, unknown> = { ...prevTls, enabled: true }
    if (inner.tls.server_name) tlsObj.server_name = inner.tls.server_name
    if (inner.tls.insecure !== undefined) tlsObj.insecure = inner.tls.insecure
    if (inner.tls.alpn?.length) tlsObj.alpn = inner.tls.alpn
    if (inner.tls.fingerprint) {
      tlsObj.utls = {
        ...(typeof prevTls.utls === 'object' && prevTls.utls ? (prevTls.utls as Record<string, unknown>) : {}),
        enabled: true,
        fingerprint: inner.tls.fingerprint,
      }
    } else {
      delete tlsObj.utls
    }
    if (inner.tls.reality?.enabled) {
      tlsObj.reality = {
        ...(typeof prevTls.reality === 'object' && prevTls.reality ? (prevTls.reality as Record<string, unknown>) : {}),
        enabled: true,
        ...(inner.tls.reality.public_key ? { public_key: inner.tls.reality.public_key } : {}),
        ...(inner.tls.reality.short_id?.length ? { short_id: inner.tls.reality.short_id[0] } : {}),
      }
    } else {
      delete tlsObj.reality
    }
    base.tls = tlsObj
  } else {
    delete base.tls
  }

  return base
}

/** Convert an edited native sing-box outbound JSON object into stored OutboundSettings. */
export function nativeJsonToOutboundSettings(prev: OutboundSettings, rawObj: Record<string, unknown>): OutboundSettings {
  const outer: OutboundSettings = cloneJson(prev || {})
  const inner: InboundSettings = cloneJson(prev?.settings || {})
  inner.raw = cloneJson(rawObj)

  if (typeof rawObj.server === 'string') outer.server = rawObj.server
  if (typeof rawObj.server_port === 'number') outer.server_port = rawObj.server_port
  if (typeof rawObj.uuid === 'string') outer.uuid = rawObj.uuid
  if (typeof rawObj.username === 'string') {
    outer.username = rawObj.username
    inner.username = rawObj.username
  }
  if (typeof rawObj.password === 'string') {
    outer.password = rawObj.password
    if (rawObj.type === 'shadowsocks') inner.ss_server_psk = rawObj.password
    else if (rawObj.type === 'socks' || rawObj.type === 'http') inner.password = rawObj.password
  }
  if (typeof rawObj.psk === 'string') {
    outer.password = rawObj.psk
    inner.snell_psk = rawObj.psk
  }
  if (typeof rawObj.method === 'string') inner.method = rawObj.method
  if (typeof rawObj.flow === 'string') inner.flow = rawObj.flow
  if (typeof rawObj.packet_encoding === 'string') inner.packet_encoding = rawObj.packet_encoding
  if (typeof rawObj.security === 'string') inner.vmess_security = rawObj.security
  if (typeof rawObj.alter_id === 'number') inner.vmess_alter_id = rawObj.alter_id
  if (typeof rawObj.congestion_control === 'string') inner.congestion_control = rawObj.congestion_control
  if (typeof rawObj.zero_rtt_handshake === 'boolean') inner.zero_rtt_handshake = rawObj.zero_rtt_handshake
  if (typeof rawObj.heartbeat === 'string') inner.heartbeat = rawObj.heartbeat
  if (typeof rawObj.up_mbps === 'number') inner.up_mbps = rawObj.up_mbps
  if (typeof rawObj.down_mbps === 'number') inner.down_mbps = rawObj.down_mbps
  if (rawObj.obfs && typeof rawObj.obfs === 'object') {
    const obfs = rawObj.obfs as Record<string, unknown>
    if (typeof obfs.type === 'string') inner.obfs_type = obfs.type
    if (typeof obfs.password === 'string') inner.obfs_password = obfs.password
    if (typeof obfs.min_packet_size === 'number') inner.gecko_min_packet_size = obfs.min_packet_size
    if (typeof obfs.max_packet_size === 'number') inner.gecko_max_packet_size = obfs.max_packet_size
  }
  if (rawObj.type === 'snell') {
    if (typeof rawObj.version === 'number') inner.snell_version = rawObj.version
    if (typeof rawObj.network === 'string') inner.snell_network = rawObj.network
    if (typeof rawObj.reuse === 'boolean') inner.snell_reuse = rawObj.reuse
    if (typeof rawObj.obfs_mode === 'string') inner.snell_obfs_mode = rawObj.obfs_mode
    if (typeof rawObj.obfs_host === 'string') inner.snell_obfs_host = rawObj.obfs_host
    if (typeof rawObj.mode === 'string') inner.snell_mode = rawObj.mode
  }
  if (rawObj.type === 'http') {
    if (typeof rawObj.path === 'string') inner.http_path = rawObj.path
    if (rawObj.headers && typeof rawObj.headers === 'object') inner.http_headers = rawObj.headers as Record<string, string | string[]>
  }

  if (rawObj.transport && typeof rawObj.transport === 'object' && !Array.isArray(rawObj.transport)) {
    const tr = rawObj.transport as Record<string, unknown>
    if (typeof tr.type === 'string' && tr.type !== 'tcp') {
      inner.transport = {
        type: tr.type,
        ...(typeof tr.path === 'string' ? { path: tr.path } : {}),
        ...(tr.headers && typeof tr.headers === 'object' ? { headers: tr.headers as Record<string, string | string[]> } : {}),
      }
    } else {
      delete inner.transport
    }
  } else {
    delete inner.transport
  }

  if (rawObj.tls && typeof rawObj.tls === 'object' && !Array.isArray(rawObj.tls)) {
    const t = rawObj.tls as Record<string, unknown>
    const utls = (t.utls && typeof t.utls === 'object' ? t.utls : {}) as Record<string, unknown>
    const nextTls: TLSSettings = {
      enabled: t.enabled !== false,
      ...(typeof t.server_name === 'string' ? { server_name: t.server_name } : {}),
      ...(typeof t.insecure === 'boolean' ? { insecure: t.insecure } : {}),
      ...(Array.isArray(t.alpn) ? { alpn: t.alpn.map(String) } : {}),
      ...(typeof utls.fingerprint === 'string' ? { fingerprint: utls.fingerprint } : {}),
    }
    if (t.reality && typeof t.reality === 'object') {
      const r = t.reality as Record<string, unknown>
      if (r.enabled) {
        nextTls.reality = {
          enabled: true,
          ...(typeof r.public_key === 'string' ? { public_key: r.public_key } : {}),
          ...(typeof r.short_id === 'string' && r.short_id ? { short_id: [r.short_id] } : (Array.isArray(r.short_id) ? { short_id: r.short_id.map(String) } : {})),
        }
      }
    }
    inner.tls = nextTls
  } else {
    delete inner.tls
  }

  outer.settings = inner
  return outer
}

export function hasPopulatedValue(v: unknown): boolean {
  if (v === undefined || v === null || v === '' || v === false) return false
  if (Array.isArray(v)) return v.length > 0
  if (typeof v === 'object') return Object.values(v as Record<string, unknown>).some(hasPopulatedValue)
  return true
}

export function pickPopulatedSchemaProps(
  props: Record<string, JSONSchema>,
  values: Record<string, unknown> | undefined,
  alwaysIncludeKeys?: ReadonlySet<string>,
): Record<string, JSONSchema> {
  const out: Record<string, JSONSchema> = {}
  const data = values && typeof values === 'object' ? values : {}
  for (const [k, v] of Object.entries(props)) {
    if (alwaysIncludeKeys?.has(k) || hasPopulatedValue(data[k])) {
      out[k] = v
    }
  }
  return out
}
