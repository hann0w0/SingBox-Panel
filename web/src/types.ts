// Types mirror the Go API responses (internal/model + handlers).

export type Role = 'admin' | 'user'

export interface User {
  id: number
  email: string
  role: Role
  server_ids: number[]
  inbound_ids: number[]
  expire_at: string | null
  enabled: boolean
  sub_token: string
  created_at: string
  updated_at: string
}

export interface Server {
  id: number
  name: string
  address: string
  remark: string
  region: string
  online: boolean
  last_seen: string | null
  hostname: string
  os: string
  arch: string
  kernel: string
  public_ip: string
  agent_version: string
  singbox_installed: boolean
  singbox_version: string
  singbox_active: boolean
  uptime: number
  load1: number
  mem_used: number
  mem_total: number
  final_outbound: string
  config_mode: 'managed' | 'raw' | ''
  agent_url: string
  inbounds?: Inbound[]
  created_at: string
  updated_at: string
}

export interface TrafficPoint {
  time: string
  upload: number
  download: number
}

export type TrafficRange = '1h' | '12h' | '24h' | '7d' | '30d'

export interface TrafficSeries {
  available: boolean
  range: TrafficRange
  step_seconds: number
  updated_at: string | null
  points: TrafficPoint[]
}

export type InboundType =
  | 'shadowsocks'
  | 'snell'
  | 'vless'
  | 'anytls'
  | 'vmess'
  | 'tuic'
  | 'trojan'
  | 'hysteria2'
  | 'socks'

export interface RealitySettings {
  enabled?: boolean
  handshake_server?: string
  handshake_server_port?: number
  private_key?: string
  public_key?: string
  short_id?: string[]
}

export interface TLSSettings {
  enabled?: boolean
  server_name?: string
  alpn?: string[]
  self_signed?: boolean
  certificate?: string
  key?: string
  certificate_path?: string
  key_path?: string
  acme_domain?: string
  acme_email?: string
  reality?: RealitySettings
  insecure?: boolean
}

export interface TransportSettings {
  type?: string
  path?: string
  headers?: Record<string, string>
}

export interface FallbackSettings {
  server?: string
  server_port?: number
}

export interface InboundSettings {
  method?: string
  ss_server_psk?: string
  flow?: string
  vmess_security?: string
  vmess_alter_id?: number
  up_mbps?: number
  down_mbps?: number
  obfs_password?: string
  congestion_control?: string
  auth_timeout?: string
  zero_rtt_handshake?: boolean
  heartbeat?: string
  trojan_fallback?: FallbackSettings
  snell_version?: number
  snell_psk?: string
  snell_obfs_mode?: string
  snell_mode?: string
  // single-credential mode
  single_user?: boolean
  multi_user?: boolean
  username?: string
  uuid?: string
  password?: string
  transport?: TransportSettings
  tls?: TLSSettings
}

// OutboundType is a routing target protocol (plus "direct").
export type OutboundType =
  | 'direct'
  | 'vless'
  | 'vmess'
  | 'trojan'
  | 'shadowsocks'
  | 'hysteria2'
  | 'tuic'
  | 'socks'

export interface OutboundSettings {
  server?: string
  server_port?: number
  username?: string
  uuid?: string
  password?: string
  settings?: InboundSettings
}

export interface Outbound {
  id: number
  server_id: number
  tag: string
  type: OutboundType
  settings: OutboundSettings
  remark: string
  sort: number
}

// RuleMatch mirrors singbox.RuleInput match fields.
export interface RuleMatch {
  action?: 'route' | 'sniff' | 'reject' | 'hijack-dns'
  inbound?: string[]
  domain?: string[]
  domain_suffix?: string[]
  domain_keyword?: string[]
  ip_cidr?: string[]
  source_ip_cidr?: string[]
  port?: number[]
  protocol?: string[]
  sniffer?: string[]
  method?: 'default' | 'drop'
  network?: string
  rule_set?: string[]
}

export interface RouteRule {
  id: number
  server_id: number
  sort: number
  match: RuleMatch
  outbound: string
  remark: string
  enabled: boolean
}

export interface RuleSet {
  id: number
  server_id: number
  tag: string
  type: 'remote' | 'local'
  format: 'binary' | 'source'
  url?: string
  path?: string
  download_detour?: string
  update_interval?: string
  created_at?: string
  updated_at?: string
}

export interface Inbound {
  id: number
  server_id: number
  tag: string
  type: InboundType
  listen_port: number
  enabled: boolean
  settings: InboundSettings
  remark: string
  created_at: string
  updated_at: string
}

export interface NodeBrief {
  id: number
  name: string
  region: string
  online: boolean
  singbox_version: string
  singbox_installed: boolean
  load1: number
  mem_used: number
  mem_total: number
  inbounds: number
  outbounds: number
  rules: number
}

export interface Overview {
  servers_total: number
  servers_online: number
  users_total: number
  users_enabled: number
  users_expiring: number
  inbounds_total: number
  outbounds_total: number
  rules_total: number
  cpu_percent: number
  mem_used: number
  mem_total: number
  mem_percent: number
  uptime_seconds: number
  nodes: NodeBrief[] | null
}

export interface InboundBrief {
  tag: string
  type: string
  listen_port: number
}

export interface StatusData {
  singbox_installed: boolean
  singbox_version: string
  service_active: boolean
  service_enabled: boolean
  has_config?: boolean
  inbounds?: InboundBrief[]
  hostname?: string
  os?: string
  arch?: string
  kernel?: string
  public_ip?: string
  load1?: number
  mem_used?: number
  mem_total?: number
  uptime?: number
}

export interface RemoteConfig {
  path: string
  exists: boolean
  raw: string
  inbounds?: InboundBrief[]
}
