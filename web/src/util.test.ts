import { describe, expect, it } from 'vitest'

import { SHADOWSOCKS_METHODS, daysUntil, relativeTime, toCSV } from './util'

describe('Shadowsocks methods', () => {
  it('offers aes-128-gcm in every form through the shared method list', () => {
    expect(SHADOWSOCKS_METHODS).toContain('aes-128-gcm')
    expect(new Set(SHADOWSOCKS_METHODS).size).toBe(SHADOWSOCKS_METHODS.length)
  })
})

describe('usage helpers', () => {
  const now = Date.parse('2026-10-03T12:00:00Z')
  it('renders relative times', () => {
    expect(relativeTime(null, now)).toBe('—')
    expect(relativeTime('2026-10-03T11:59:30Z', now)).toBe('刚刚')
    expect(relativeTime('2026-10-03T11:15:00Z', now)).toBe('45 分钟前')
    expect(relativeTime('2026-10-03T07:00:00Z', now)).toBe('5 小时前')
    expect(relativeTime('2026-09-30T12:00:00Z', now)).toBe('3 天前')
  })
  it('counts days until expiry', () => {
    expect(daysUntil('2026-10-10T12:00:00Z', now)).toBe(7)
    expect(daysUntil('2026-10-02T12:00:00Z', now)).toBe(-1)
    expect(daysUntil(null, now)).toBeNull()
  })
  it('quotes CSV cells', () => {
    expect(toCSV([['a', 'b,c'], ['say "hi"', 1]])).toBe('\uFEFFa,"b,c"\r\n"say ""hi""",1')
  })
})
