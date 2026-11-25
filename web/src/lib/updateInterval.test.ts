import { describe, expect, it } from 'vitest'
import { buildUpdateIntervalMinuteOptions, parseDurationToMinutes } from './updateInterval'

describe('updateInterval helpers', () => {
  it('parses sing-box and plain-minute intervals into minutes', () => {
    expect(parseDurationToMinutes('1d')).toBe(1440)
    expect(parseDurationToMinutes('12h')).toBe(720)
    expect(parseDurationToMinutes('30m')).toBe(30)
    expect(parseDurationToMinutes('1d12h')).toBe(2160)
    expect(parseDurationToMinutes('90')).toBe(90)
    expect(parseDurationToMinutes('')).toBe(1440)
    expect(parseDurationToMinutes('invalid', 60)).toBe(60)
  })

  it('includes custom saved minute values in sorted preset options', () => {
    const options = buildUpdateIntervalMinuteOptions(90)
    expect(options.some((item) => item.value === 90 && item.label === '90 分钟')).toBe(true)
    const values = options.map((item) => item.value)
    expect(values).toEqual([...values].sort((a, b) => a - b))
  })
})
