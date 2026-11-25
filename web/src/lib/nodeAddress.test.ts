import { describe, expect, it } from 'vitest'
import type { Server } from './types'
import { formatHostPort, inboundConnectionHost, inboundConnectionPort } from './nodeAddress'

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
    [{ listen: '127.0.0.1', client_port: 443 }, 'v4.example.com'],
    [{ listen: '::1', client_port: 443 }, 'v4.example.com'],
  ])('maps %o to %s', (settings, expected) => {
    expect(inboundConnectionHost(server, settings)).toBe(expected)
  })

  it('uses public addresses when no domain is configured', () => {
    const bare = { ...server, address: '' } as Server
    expect(inboundConnectionHost(bare, { listen: '::' })).toBe('133.200.223.98')
    expect(inboundConnectionHost(bare, { listen: '::', ipv6_only: true })).toBe('2404:7a80:df62:8604::fde')
    expect(inboundConnectionHost(bare, { listen: '127.0.0.1', client_port: 443 })).toBe('133.200.223.98')
  })

  it('treats a legacy inbound without settings as dual stack', () => {
    expect(inboundConnectionHost(server, undefined)).toBe('v4.example.com')
  })
})

describe('inboundConnectionPort', () => {
  it('prefers client_port over listen_port when set', () => {
    expect(inboundConnectionPort({ listen_port: 1443, settings: { client_port: 443 } })).toBe(443)
    expect(inboundConnectionPort({ listen_port: 1443, settings: {} })).toBe(1443)
    expect(inboundConnectionPort({ listen_port: 1443, settings: undefined })).toBe(1443)
  })
})

describe('formatHostPort', () => {
  it('brackets IPv6 literals when a port is present', () => {
    expect(formatHostPort('2001:db8::1', 443)).toBe('[2001:db8::1]:443')
    expect(formatHostPort('[2001:db8::1]', 443)).toBe('[2001:db8::1]:443')
    expect(formatHostPort('2001:db8::1')).toBe('2001:db8::1')
  })

  it('formats IPv4 and domains normally and handles empty host', () => {
    expect(formatHostPort('1.2.3.4', 8443)).toBe('1.2.3.4:8443')
    expect(formatHostPort('example.com', 443)).toBe('example.com:443')
    expect(formatHostPort('', 443, '—')).toBe('—')
    expect(formatHostPort(undefined, undefined, '—')).toBe('—')
  })
})
