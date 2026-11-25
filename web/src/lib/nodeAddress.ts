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
 * Loopback inbounds stay local unless a separate public client_port is set.
 */
export function inboundConnectionHost(server: Server, settings?: InboundSettings | null): string {
  const listen = trimBrackets((settings?.listen || '::').trim())
  if (listen === '127.0.0.1' || listen === '::1') {
    if (!settings?.client_port) return ''
    return trimBrackets(server.address || '') || server.public_ip || trimBrackets(server.public_ipv6 || '')
  }
  const literal = listen !== '::' && listen !== '0.0.0.0'
  if (literal) return listen
  if (settings?.ipv6_only) return trimBrackets(server.public_ipv6 || '')
  const domain = trimBrackets(server.address || '')
  if (listen === '0.0.0.0') return server.public_ip || (domain && !isIPv6(domain) ? domain : '')
  return domain || server.public_ip || trimBrackets(server.public_ipv6 || '')
}

/** The port a subscriber dials for one inbound (client_port override or listen_port). */
export function inboundConnectionPort(inbound: { listen_port: number; settings?: InboundSettings | null }): number {
  return inbound.settings?.client_port && inbound.settings.client_port > 0
    ? inbound.settings.client_port
    : inbound.listen_port
}

/** Format host and optional port, bracketing literal IPv6 addresses when a port is shown. */
export function formatHostPort(host?: string | null, port?: number | string | null, emptyFallback = ''): string {
  const raw = (host ?? '').trim()
  if (!raw) return emptyFallback
  const clean = trimBrackets(raw)
  const portText = port !== undefined && port !== null && port !== 0 ? String(port).trim() : ''
  if (!portText) return clean
  return isIPv6(clean) ? `[${clean}]:${portText}` : `${clean}:${portText}`
}
