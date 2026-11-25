import { describe, expect, it } from 'vitest'
import {
  formatIntervalDuration,
  intervalValueToMinutes,
  minutesToIntervalValue,
  parseDurationToIntervalValue,
  parseDurationToMinutes,
  validateUpdateInterval,
} from './updateInterval'

describe('updateInterval helpers', () => {
  it('parses duration strings into minutes and structured quantity+unit pairs', () => {
    expect(parseDurationToMinutes('1d')).toBe(1440)
    expect(parseDurationToMinutes('12h')).toBe(720)
    expect(parseDurationToMinutes('30m')).toBe(30)
    expect(parseDurationToMinutes('1d12h')).toBe(2160)
    expect(parseDurationToIntervalValue('1d')).toEqual({ amount: 1, unit: 'd' })
    expect(parseDurationToIntervalValue('12h')).toEqual({ amount: 12, unit: 'h' })
    expect(parseDurationToIntervalValue('45m')).toEqual({ amount: 45, unit: 'm' })
    expect(parseDurationToIntervalValue('1d12h')).toEqual({ amount: 36, unit: 'h' })
    expect(parseDurationToIntervalValue('')).toEqual({ amount: 1, unit: 'd' })
  })

  it('converts between minutes and compact unit pairs', () => {
    expect(minutesToIntervalValue(60)).toEqual({ amount: 1, unit: 'h' })
    expect(minutesToIntervalValue(1440)).toEqual({ amount: 1, unit: 'd' })
    expect(minutesToIntervalValue(45)).toEqual({ amount: 45, unit: 'm' })
    expect(intervalValueToMinutes({ amount: 3, unit: 'h' })).toBe(180)
    expect(intervalValueToMinutes({ amount: 2, unit: 'd' })).toBe(2880)
    expect(formatIntervalDuration({ amount: 6, unit: 'h' })).toBe('6h')
    expect(formatIntervalDuration({ amount: 30, unit: 'm' })).toBe('30m')
  })

  it('validates min/max bounds and non-integer inputs', () => {
    expect(validateUpdateInterval({ amount: null, unit: 'h' }, { minMinutes: 15, maxMinutes: 10080 })).toBe('请填写更新间隔')
    expect(validateUpdateInterval({ amount: 0, unit: 'm' }, { minMinutes: 15, maxMinutes: 10080 })).toBe('请输入大于 0 的整数')
    expect(validateUpdateInterval({ amount: 10, unit: 'm' }, { minMinutes: 15, maxMinutes: 10080 })).toBe('更新间隔不能小于 15 分钟')
    expect(validateUpdateInterval({ amount: 8, unit: 'd' }, { minMinutes: 15, maxMinutes: 10080 })).toBe('更新间隔不能超过 7 天')
    expect(validateUpdateInterval({ amount: 15, unit: 'm' }, { minMinutes: 15, maxMinutes: 10080 })).toBeNull()
    expect(validateUpdateInterval({ amount: 7, unit: 'd' }, { minMinutes: 15, maxMinutes: 10080 })).toBeNull()
  })
})
