import { describe, expect, it } from 'vitest'
import { maskAddress } from './addressPrivacy'

describe('maskAddress', () => {
  it.each([
    ['133.200.223.98', '133.*.*.98'],
    ['10.0.0.102', '10.*.*.102'],
    ['2404:7a80:df62:8604::fde', '2404:*:*:8604::fde'],
    ['2001:db8::1', '2001:*:*::1'],
    ['2001:0db8:0000:0000:0000:0000:0000:0001', '2001:*:*::1'],
    ['fe80::18bf:5887:1516:2b0a', 'fe80:*:*:0:18bf:5887:1516:2b0a'],
    ['::1', '0:*:*::1'],
  ])('masks the middle of %s', (address, expected) => {
    expect(maskAddress(address)).toBe(expected)
  })

  it('keeps host names and non-IP text unchanged', () => {
    for (const value of ['hk01.example.com', '未设置地址', '']) expect(maskAddress(value)).toBe(value)
  })
})
