import type { InboundType, OutboundType } from './types'

/**
 * Shared proxy protocols displayed in identical order at the top of
 * Inbound, Outbound, and External Node selectors.
 */
export const SHARED_PROXY_PROTOCOLS = [
  'snell',
  'shadowsocks',
  'anytls',
  'vless',
  'vmess',
  'tuic',
  'trojan',
  'hysteria2',
  'socks',
  'http',
] as const

export const INBOUND_PROTOCOL_TYPES: readonly InboundType[] = [
  ...SHARED_PROXY_PROTOCOLS,
  'mixed',
  'tun',
  'direct',
  'redirect',
  'tproxy',
]

export const OUTBOUND_PROTOCOL_TYPES: readonly OutboundType[] = [
  ...SHARED_PROXY_PROTOCOLS,
]

export const EXTERNAL_NODE_PROTOCOL_TYPES = [
  ...SHARED_PROXY_PROTOCOLS,
  'mixed',
  'hysteria',
] as const

/** Canonical sing-box lowercase protocol display names (never abbreviating shadowsocks as ss). */
export const PROTOCOL_LABELS: Record<string, string> = {
  vless: 'vless',
  vmess: 'vmess',
  trojan: 'trojan',
  anytls: 'anytls',
  shadowsocks: 'shadowsocks',
  ss: 'shadowsocks',
  tuic: 'tuic',
  hysteria2: 'hysteria2',
  hy2: 'hysteria2',
  snell: 'snell',
  socks: 'socks',
  socks5: 'socks',
  http: 'http',
  https: 'http',
  mixed: 'mixed',
  hysteria: 'hysteria',
  hy1: 'hysteria',
  tun: 'tun',
  direct: 'direct',
  redirect: 'redirect',
  tproxy: 'tproxy',
  block: 'block',
  dns: 'dns',
  selector: 'selector',
  urltest: 'urltest',
  wireguard: 'wireguard',
}

export function formatProtocolLabel(type?: string | null): string {
  const key = (type ?? '').trim().toLowerCase()
  if (!key) return ''
  return PROTOCOL_LABELS[key] ?? key
}

export function toProtocolOptions<T extends string>(types: readonly T[]): { value: T; label: string }[] {
  return types.map((value) => ({
    value,
    label: formatProtocolLabel(value) || value,
  }))
}
