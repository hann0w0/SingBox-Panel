import { describe, expect, it } from 'vitest'
import type { Server } from './types'
import { inboundConnectionHost } from './nodeAddress'

const server = {
  address: 'v4.example.com', public_ip: '133.200.223.98', public_ipv6: '2404:7a80:df62:8604::fde',
} as Server

describe('inboundConnectionHost', () => {
  it.each([
    [{ listen: '::' }, 'v4.example.com'],
    [{ listen: '0.0.0.0' }, '133.200.223.98'],
    [{ listen: '::', ipv6_only: true }, '2404:7a80:df62:8604::fde'],
    [{ listen: '2001:db8::9' }, '2001:db8::9'],
    [{ listen: '127.0.0.1' }, ''],
    [{ listen: '::1' }, ''],
  ])('maps %o to %s', (settings, expected) => {
    expect(inboundConnectionHost(server, settings)).toBe(expected)
  })

  it('uses public addresses when no domain is configured', () => {
    const bare = { ...server, address: '' } as Server
    expect(inboundConnectionHost(bare, { listen: '::' })).toBe('133.200.223.98')
    expect(inboundConnectionHost(bare, { listen: '::', ipv6_only: true })).toBe('2404:7a80:df62:8604::fde')
  })

  it('treats a legacy inbound without settings as dual stack', () => {
    expect(inboundConnectionHost(server, undefined)).toBe('v4.example.com')
  })
})
