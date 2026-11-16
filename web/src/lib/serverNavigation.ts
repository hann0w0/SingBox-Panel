export function parseServerId(value: string | null | undefined): number | null {
  if (!value || !/^\d+$/.test(value)) return null
  const id = Number(value)
  return Number.isSafeInteger(id) && id > 0 ? id : null
}

export function serverIdFromLocation(pathname: string, search: string): number | null {
  const detail = pathname.match(/^\/admin\/servers\/(\d+)(?:\/|$)/)
  if (detail) return parseServerId(detail[1])
  return pathname === '/admin/traffic' || pathname === '/admin/logs'
    ? parseServerId(new URLSearchParams(search).get('server')) : null
}
