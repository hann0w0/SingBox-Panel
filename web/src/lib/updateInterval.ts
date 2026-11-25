export type UpdateIntervalUnit = 'm' | 'h' | 'd'

export interface UpdateIntervalValue {
  amount: number | null
  unit: UpdateIntervalUnit
}

export const UPDATE_INTERVAL_UNIT_OPTIONS: { value: UpdateIntervalUnit; label: string }[] = [
  { value: 'm', label: '分钟' },
  { value: 'h', label: '小时' },
  { value: 'd', label: '天' },
]

export const UPDATE_INTERVAL_UNIT_MINUTES: Record<UpdateIntervalUnit, number> = {
  m: 1,
  h: 60,
  d: 1440,
}

export function parseDurationToMinutes(raw?: string | null, fallback = 1440): number {
  const text = (raw ?? '').trim()
  if (!text) return fallback
  if (/^\d+$/.test(text)) {
    const mins = Number(text)
    return Number.isFinite(mins) && mins >= 1 ? Math.round(mins) : fallback
  }
  const tokenRe = /(\d+(?:\.\d+)?)(d|h|m|s)/gi
  let totalSeconds = 0
  let matchedLength = 0
  let match: RegExpExecArray | null
  while ((match = tokenRe.exec(text)) !== null) {
    matchedLength += match[0].length
    const amount = Number(match[1])
    const unit = match[2].toLowerCase()
    if (unit === 'd') totalSeconds += amount * 86400
    else if (unit === 'h') totalSeconds += amount * 3600
    else if (unit === 'm') totalSeconds += amount * 60
    else if (unit === 's') totalSeconds += amount
  }
  if (matchedLength !== text.length || totalSeconds <= 0) return fallback
  return Math.max(1, Math.round(totalSeconds / 60))
}

export function minutesToIntervalValue(
  minutes?: number | null,
  fallback: UpdateIntervalValue = { amount: 1, unit: 'h' },
): UpdateIntervalValue {
  if (typeof minutes !== 'number' || !Number.isFinite(minutes) || minutes < 1) {
    return { ...fallback }
  }
  const rounded = Math.round(minutes)
  if (rounded % 1440 === 0) return { amount: rounded / 1440, unit: 'd' }
  if (rounded % 60 === 0) return { amount: rounded / 60, unit: 'h' }
  return { amount: rounded, unit: 'm' }
}

export function parseDurationToIntervalValue(
  raw?: string | null,
  fallback: UpdateIntervalValue = { amount: 1, unit: 'd' },
): UpdateIntervalValue {
  const text = (raw ?? '').trim()
  if (!text) return { ...fallback }
  const simple = /^(\d+)\s*([dhm])$/i.exec(text)
  if (simple) {
    const amount = Number(simple[1])
    const unit = simple[2].toLowerCase() as UpdateIntervalUnit
    if (Number.isFinite(amount) && amount >= 1) {
      return { amount, unit }
    }
  }
  const minutes = parseDurationToMinutes(text, 0)
  if (minutes < 1) return { ...fallback }
  return minutesToIntervalValue(minutes, fallback)
}

export function intervalValueToMinutes(value?: UpdateIntervalValue | null): number | null {
  if (!value || value.amount === null || value.amount === undefined) return null
  const amount = Number(value.amount)
  if (!Number.isFinite(amount) || !Number.isInteger(amount) || amount < 1) return null
  const multiplier = UPDATE_INTERVAL_UNIT_MINUTES[value.unit]
  if (!multiplier) return null
  return amount * multiplier
}

export function formatIntervalDuration(value?: UpdateIntervalValue | null, fallback = '1d'): string {
  if (intervalValueToMinutes(value) === null || !value) return fallback
  return `${Math.round(Number(value.amount))}${value.unit}`
}

function formatMinutesLabel(minutes: number): string {
  if (minutes % 1440 === 0) return `${minutes / 1440} 天`
  if (minutes % 60 === 0) return `${minutes / 60} 小时`
  return `${minutes} 分钟`
}

export function validateUpdateInterval(
  value: UpdateIntervalValue | null | undefined,
  opts: { minMinutes: number; maxMinutes: number },
): string | null {
  if (!value || value.amount === null || value.amount === undefined) {
    return '请填写更新间隔'
  }
  const amount = Number(value.amount)
  if (!Number.isFinite(amount) || !Number.isInteger(amount) || amount < 1) {
    return '请输入大于 0 的整数'
  }
  const multiplier = UPDATE_INTERVAL_UNIT_MINUTES[value.unit]
  if (!multiplier) return '请选择有效的时间单位'
  const totalMinutes = amount * multiplier
  if (totalMinutes < opts.minMinutes) {
    return `更新间隔不能小于 ${formatMinutesLabel(opts.minMinutes)}`
  }
  if (totalMinutes > opts.maxMinutes) {
    return `更新间隔不能超过 ${formatMinutesLabel(opts.maxMinutes)}`
  }
  return null
}
