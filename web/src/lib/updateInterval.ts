export const UPDATE_INTERVAL_MINUTE_PRESETS = [
  15,
  30,
  60,
  120,
  180,
  360,
  720,
  1440,
  4320,
  10080,
] as const

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

export function buildUpdateIntervalMinuteOptions(currentMinutes?: number): { value: number; label: string }[] {
  const values = new Set<number>(UPDATE_INTERVAL_MINUTE_PRESETS)
  if (typeof currentMinutes === 'number' && Number.isFinite(currentMinutes) && currentMinutes >= 1) {
    values.add(Math.round(currentMinutes))
  }
  return Array.from(values)
    .sort((a, b) => a - b)
    .map((minutes) => ({ value: minutes, label: `${minutes} 分钟` }))
}
