export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || !n || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.min(units.length - 1, Math.max(0, Math.floor(Math.log(n) / Math.log(1024))))
  const v = n / Math.pow(1024, i)
  return `${v.toFixed(i === 0 ? 0 : 2)} ${units[i]}`
}

// randomHex returns a cryptographically-random hex string of 2*bytes chars.
export function randomHex(bytes: number): string {
  const a = new Uint8Array(bytes)
  crypto.getRandomValues(a)
  return Array.from(a, (b) => b.toString(16).padStart(2, '0')).join('')
}

// randomUUID returns a random UUIDv4 string.
export function randomUUID(): string {
  if (crypto.randomUUID) return crypto.randomUUID()
  const a = new Uint8Array(16)
  crypto.getRandomValues(a)
  a[6] = (a[6] & 0x0f) | 0x40
  a[8] = (a[8] & 0x3f) | 0x80
  const h = Array.from(a, (b) => b.toString(16).padStart(2, '0'))
  return `${h.slice(0, 4).join('')}-${h.slice(4, 6).join('')}-${h.slice(6, 8).join('')}-${h.slice(8, 10).join('')}-${h.slice(10, 16).join('')}`
}

// randomBase64 returns a standard-base64 string of `bytes` random bytes — used
// for Shadowsocks 2022 keys (16 or 32 bytes for the aes-128 / aes-256 methods).
export function randomBase64(bytes: number): string {
  const a = new Uint8Array(bytes)
  crypto.getRandomValues(a)
  let s = ''
  for (const b of a) s += String.fromCharCode(b)
  return btoa(s)
}

export const SHADOWSOCKS_METHODS = [
  '2022-blake3-aes-128-gcm',
  '2022-blake3-aes-256-gcm',
  '2022-blake3-chacha20-poly1305',
  'aes-128-gcm',
  'aes-256-gcm',
  'chacha20-ietf-poly1305',
] as const

// ss2022KeyLen returns the required key length (bytes) for an SS2022 method, or 0.
export function ss2022KeyLen(method: string): number {
  if (method === '2022-blake3-aes-128-gcm') return 16
  if (method === '2022-blake3-aes-256-gcm' || method === '2022-blake3-chacha20-poly1305') return 32
  return 0
}

export function formatDate(s: string | null): string {
  if (!s) return '—'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString()
}

// copyToClipboard copies text, falling back to execCommand for non-secure
// (plain http, non-localhost) contexts where navigator.clipboard is unavailable.
export async function copyToClipboard(text: string): Promise<void> {
  // The async Clipboard API can reject even when present (document not focused,
  // permissions policy, iOS quirks), so always fall back to execCommand rather
  // than only when the API is missing.
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return
    } catch {
      // fall through to the legacy path
    }
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.top = '0'
  ta.style.left = '0'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  try {
    ta.focus()
    ta.select()
    ta.setSelectionRange(0, ta.value.length) // iOS needs an explicit range
    if (!document.execCommand('copy')) {
      throw new Error('execCommand copy rejected')
    }
  } finally {
    document.body.removeChild(ta)
  }
}

// formatDuration renders a seconds count as a compact 天/小时/分 string.
export function formatDuration(sec: number): string {
  if (!sec || sec < 0) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d} 天 ${h} 小时`
  if (h > 0) return `${h} 小时 ${m} 分`
  return `${m} 分`
}

// relativeTime renders a past timestamp as 刚刚 / N 分钟前 / N 小时前 / N 天前,
// falling back to a date for anything older than 30 days.
export function relativeTime(value: string | null | undefined, now: number = Date.now()): string {
  if (!value) return '—'
  const t = new Date(value).getTime()
  if (!Number.isFinite(t) || t <= 0) return '—'
  const seconds = Math.max(0, Math.floor((now - t) / 1000))
  if (seconds < 60) return '刚刚'
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`
  if (seconds < 30 * 86400) return `${Math.floor(seconds / 86400)} 天前`
  return new Date(t).toLocaleDateString('zh-CN')
}

// daysUntil returns whole days until a timestamp (negative when past), or null.
export function daysUntil(value: string | null | undefined, now: number = Date.now()): number | null {
  if (!value) return null
  const t = new Date(value).getTime()
  if (!Number.isFinite(t)) return null
  return Math.floor((t - now) / 86400000)
}

// toCSV serializes rows with RFC 4180 quoting and a BOM so Excel keeps UTF-8.
export function toCSV(rows: (string | number)[][]): string {
  const escape = (cell: string | number) => {
    const text = String(cell)
    return /[",\n\r]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text
  }
  return '\uFEFF' + rows.map((row) => row.map(escape).join(',')).join('\r\n')
}

export function downloadText(filename: string, text: string, type = 'text/csv;charset=utf-8') {
  const url = URL.createObjectURL(new Blob([text], { type }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
