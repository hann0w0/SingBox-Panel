import { PROTOCOL_LABELS } from '../../../lib/protocols'

export function protocolOf(link: string): string {
  const m = /^([a-z0-9+.-]+):\/\//i.exec(link.trim())
  return m ? m[1].toLowerCase() : '-'
}

export const nodeTypeLabel: Record<string, string> = PROTOCOL_LABELS

export const MAX_VISIBLE_SUBSCRIPTIONS = 6
const MANAGEMENT_TABLE_ROW_HEIGHT = 53
const MANAGEMENT_TABLE_HEAD_HEIGHT = 42
export const SUBSCRIPTION_TABLE_SCROLL_HEIGHT = MANAGEMENT_TABLE_HEAD_HEIGHT + MAX_VISIBLE_SUBSCRIPTIONS * MANAGEMENT_TABLE_ROW_HEIGHT

export const HAS_UUID = ['vless', 'vmess', 'tuic']
export const HAS_PASSWORD = ['trojan', 'anytls', 'tuic', 'hysteria2', 'hysteria', 'shadowsocks']
export const HAS_AUTH = ['http', 'socks', 'mixed']
export const HAS_TLS = ['vless', 'vmess', 'trojan', 'anytls', 'tuic', 'hysteria2', 'hysteria', 'http']
export const TLS_REALITY = ['vless', 'vmess', 'trojan']
export const HAS_TRANSPORT = ['vless', 'vmess', 'trojan']
export const HAS_BANDWIDTH = ['hysteria2', 'hysteria']
