import { describe, expect, it } from 'vitest'

import { SHADOWSOCKS_METHODS } from './util'

describe('Shadowsocks methods', () => {
  it('offers aes-128-gcm in every form through the shared method list', () => {
    expect(SHADOWSOCKS_METHODS).toContain('aes-128-gcm')
    expect(new Set(SHADOWSOCKS_METHODS).size).toBe(SHADOWSOCKS_METHODS.length)
  })
})
