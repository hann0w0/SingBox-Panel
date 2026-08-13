import 'flag-icons/css/flag-icons.min.css'

const regionalFlagPattern = /([\u{1F1E6}-\u{1F1FF}])([\u{1F1E6}-\u{1F1FF}])/u

export function regionCodeFromFlag(value?: string): string {
  const match = regionalFlagPattern.exec(value || '')
  if (!match) return ''
  const base = 0x1F1E6
  return [match[1], match[2]]
    .map((part) => String.fromCharCode(65 + (part.codePointAt(0)! - base)))
    .join('')
}

export function removeRegionFlag(value?: string): string {
  return (value || '').replace(regionalFlagPattern, '').replace(/\s{2,}/g, ' ').trim()
}

export function RegionFlag({ code, size = 16, fallback = true }: {
  code?: string
  size?: number
  fallback?: boolean
}) {
  const normalized = (code || '').trim().toUpperCase()
  if (!/^[A-Z]{2}$/.test(normalized)) {
    return fallback ? <span aria-label="未知地区" style={{ fontSize: size, lineHeight: 1 }}>🌐</span> : null
  }
  return (
    <span
      className={`fi fi-${normalized.toLowerCase()}`}
      role="img"
      aria-label={normalized}
      title={normalized}
      style={{
        width: Math.round(size * 4 / 3),
        height: size,
        flex: '0 0 auto',
        borderRadius: 2,
        overflow: 'hidden',
        verticalAlign: 'middle',
      }}
    />
  )
}
