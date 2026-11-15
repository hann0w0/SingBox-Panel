const IPV4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/

function expandIPv6(address: string): string[] | null {
  if (!/^[0-9a-fA-F:]+$/.test(address) || !address.includes(':')) return null
  const halves = address.split('::')
  if (halves.length > 2) return null
  const left = halves[0] ? halves[0].split(':') : []
  const right = halves.length === 2 && halves[1] ? halves[1].split(':') : []
  if (halves.length === 1) {
    if (left.length !== 8) return null
    return left.map(normalizeGroup)
  }
  const missing = 8 - left.length - right.length
  if (missing < 1) return null
  return [...left.map(normalizeGroup), ...Array<string>(missing).fill('0'), ...right.map(normalizeGroup)]
}

function normalizeGroup(group: string): string {
  return Number.parseInt(group, 16).toString(16)
}

function compressIPv6(groups: string[]): string {
  let start = -1
  let length = 0
  for (let index = 0; index < groups.length;) {
    if (groups[index] !== '0') {
      index += 1
      continue
    }
    let end = index
    while (end < groups.length && groups[end] === '0') end += 1
    if (end - index > length) {
      start = index
      length = end - index
    }
    index = end
  }
  if (length < 2) return groups.join(':')
  return `${groups.slice(0, start).join(':')}::${groups.slice(start + length).join(':')}`
}

/** Masks the middle of an IP: IPv4 `a.*.*.d`, IPv6 keeps its first and last groups. */
export function maskAddress(address: string): string {
  const trimmed = address.trim()
  const ipv4 = IPV4.exec(trimmed)
  if (ipv4) return `${ipv4[1]}.*.*.${ipv4[4]}`
  const groups = expandIPv6(trimmed)
  if (!groups) return trimmed
  groups[1] = '*'
  groups[2] = '*'
  return compressIPv6(groups)
}
