import { describe, expect, it } from 'vitest'
import { formatHostPort } from './nodeAddress'

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
