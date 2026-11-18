import { describe, expect, it } from 'vitest'
import {
  EXTERNAL_NODE_PROTOCOL_TYPES,
  INBOUND_PROTOCOL_TYPES,
  OUTBOUND_PROTOCOL_TYPES,
  SHARED_PROXY_PROTOCOLS,
  formatProtocolLabel,
  toProtocolOptions,
} from './protocols'

describe('protocol ordering and naming consistency', () => {
  it('shares identical leading protocol order across inbound, outbound, and external node selectors', () => {
    expect(INBOUND_PROTOCOL_TYPES.slice(0, SHARED_PROXY_PROTOCOLS.length)).toEqual([...SHARED_PROXY_PROTOCOLS])
    expect([...OUTBOUND_PROTOCOL_TYPES]).toEqual([...SHARED_PROXY_PROTOCOLS])
    expect(EXTERNAL_NODE_PROTOCOL_TYPES.slice(0, SHARED_PROXY_PROTOCOLS.length)).toEqual([...SHARED_PROXY_PROTOCOLS])
    expect(INBOUND_PROTOCOL_TYPES.slice(SHARED_PROXY_PROTOCOLS.length)).toEqual(['mixed', 'tun', 'direct', 'redirect', 'tproxy'])
    expect(EXTERNAL_NODE_PROTOCOL_TYPES.slice(SHARED_PROXY_PROTOCOLS.length)).toEqual(['mixed', 'hysteria'])
  })

  it('uses canonical sing-box lowercase names and expands ss to shadowsocks', () => {
    expect(formatProtocolLabel('VLESS')).toBe('vless')
    expect(formatProtocolLabel('ss')).toBe('shadowsocks')
    expect(formatProtocolLabel('shadowsocks')).toBe('shadowsocks')
    expect(formatProtocolLabel('hy2')).toBe('hysteria2')
    expect(formatProtocolLabel('socks5')).toBe('socks')

    const opts = toProtocolOptions(INBOUND_PROTOCOL_TYPES)
    expect(opts.map((o) => o.label)).toEqual([
      'vless',
      'vmess',
      'trojan',
      'anytls',
      'shadowsocks',
      'tuic',
      'hysteria2',
      'snell',
      'socks',
      'http',
      'mixed',
      'tun',
      'direct',
      'redirect',
      'tproxy',
    ])
  })
})
