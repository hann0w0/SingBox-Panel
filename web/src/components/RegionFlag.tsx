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
    return fallback ? (
      <span role="img" aria-label="未知地区" title="未知地区" style={{ display: 'inline-flex', width: Math.round(size * 1.5), height: size, alignItems: 'center', justifyContent: 'center', fontSize: size, lineHeight: 1, flex: '0 0 auto' }}>🌐</span>
    ) : null
  }
  const flag = Array.from(normalized)
    .map((letter) => String.fromCodePoint(letter.charCodeAt(0) + 127397))
    .join('')
  return (
    <span
      role="img"
      aria-label={normalized}
      title={normalized}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        width: Math.round(size * 1.5),
        height: size,
        flex: '0 0 auto',
        fontSize: size,
        lineHeight: 1,
        verticalAlign: 'middle',
      }}
    >
      {flag}
    </span>
  )
}
