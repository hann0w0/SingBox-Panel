import type { InboundSettings, Server } from './types'

function trimBrackets(value: string) {
  return value.startsWith('[') && value.endsWith(']') ? value.slice(1, -1) : value
}

function isIPv6(value: string) {
  return value.includes(':')
}

/**
 * The host a subscriber dials for one inbound, mirroring the panel's export
 * rules: an explicit bind address wins; an IPv6-only inbound uses the server's
 * public IPv6; IPv4 uses public IPv4 (falling back to an explicit IPv4 domain
 * address); dual stack prefers the configured domain, then IPv4, then IPv6.
 * Loopback inbounds are not public nodes and produce an empty host.
 */
export function inboundConnectionHost(server: Server, settings?: InboundSettings | null): string {
  const listen = trimBrackets((settings?.listen || '::').trim())
  if (listen === '127.0.0.1' || listen === '::1') return ''
  const literal = listen !== '::' && listen !== '0.0.0.0'
  if (literal) return listen
  if (settings?.ipv6_only) return trimBrackets(server.public_ipv6 || '')
  const domain = trimBrackets(server.address || '')
  if (listen === '0.0.0.0') return server.public_ip || (domain && !isIPv6(domain) ? domain : '')
  return domain || server.public_ip || trimBrackets(server.public_ipv6 || '')
}
