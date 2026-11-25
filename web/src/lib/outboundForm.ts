import { outboundSettingsToNativeJson } from './singboxSchema'
import type { InboundSettings, Outbound, OutboundSettings, OutboundType, TransportSettings } from './types'

export const TLS_REQUIRED_OUT = new Set<OutboundType>(['trojan', 'hysteria2', 'tuic', 'anytls'])
export const TRANSPORT_OUT = new Set<OutboundType>(['vless', 'vmess', 'trojan'])
const EXTENDED_TRANSPORT_OUT = new Set(['ws', 'httpupgrade', 'grpc', 'http', 'quic'])

type OutboundFormValues = Record<string, any> & { type: OutboundType }

function cloneJSON<T>(value: T): T {
  return JSON.parse(JSON.stringify(value ?? {})) as T
}

function splitValues(value: unknown): string[] {
  if (typeof value !== 'string') return []
  return value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean)
}

export function transportHost(headers: TransportSettings['headers']): string | undefined {
  const value = Object.entries(headers ?? {}).find(([key]) => key.toLowerCase() === 'host')?.[1]
  if (Array.isArray(value)) return value[0]
  return value
}

function setHostHeader(transport: TransportSettings, value: unknown) {
  // An omitted form value means the compact form did not expose the existing
  // header. Preserve it rather than treating the omission as an explicit clear.
  if (value === undefined || value === null) return

  const host = typeof value === 'string' ? value.trim() : ''
  if (host === (transportHost(transport.headers) ?? '')) return

  const headers = { ...(transport.headers ?? {}) }
  const existingHost = Object.keys(headers).find((key) => key.toLowerCase() === 'host')
  if (existingHost) delete headers[existingHost]
  if (host) headers.Host = host
  if (Object.keys(headers).length) transport.headers = headers
  else delete transport.headers
}

// Builds an outbound update without destroying settings the compact form does
// not expose. Imported configs can contain REALITY, plugins, early-data,
// repeated transport headers and protocol-specific beta fields; editing a tag
// or one visible field must leave those values intact.
function encodeBase64UTF8(text: string): string {
  const bytes = new TextEncoder().encode(text)
  let binary = ''
  for (let i = 0; i < bytes.length; i += 1) {
    binary += String.fromCharCode(bytes[i])
  }
  return btoa(binary)
}

export function buildOutboundBody(
  outbound: Outbound | null,
  values: OutboundFormValues,
  extraRaw?: Record<string, unknown>,
) {
  const type = values.type
  const sameProtocol = outbound?.type === type
  const outer: OutboundSettings = sameProtocol ? cloneJSON(outbound?.settings ?? {}) : {}
  const inner: InboundSettings = sameProtocol ? cloneJSON(outbound?.settings?.settings ?? {}) : {}

  outer.server = typeof values.server === 'string' ? values.server.trim() : values.server
  outer.server_port = values.server_port

  if (type === 'socks' || type === 'http') {
    outer.username = typeof values.username === 'string' ? values.username.trim() : ''
    outer.password = values.password || ''
    // Empty fields explicitly remove authentication, including imported
    // fallback copies that the renderer would otherwise reuse.
    delete inner.username
    delete inner.password
  }
  if (type === 'vless' || type === 'vmess' || type === 'tuic') outer.uuid = values.uuid || ''
  if (type === 'trojan' || type === 'hysteria2' || type === 'tuic' || type === 'anytls' || type === 'snell') {
    outer.password = values.password || ''
  }

  if (type === 'shadowsocks') {
    inner.method = values.method
    inner.ss_server_psk = values.ss_psk || ''
    // buildOutbound prefers the top-level password. Remove an imported copy so
    // a password edited in this form cannot be shadowed by stale data.
    delete outer.password
  }
  if (type === 'vless') {
    inner.flow = values.flow || ''
    if (values.packet_encoding) inner.packet_encoding = values.packet_encoding
  }
  if (type === 'vmess') {
    inner.vmess_security = values.vmess_security || 'auto'
    inner.vmess_alter_id = values.vmess_alter_id ?? 0
  }
  if (type === 'tuic') {
    inner.congestion_control = values.congestion_control || 'cubic'
    if (extraRaw) {
      if (typeof extraRaw.heartbeat === 'string' && extraRaw.heartbeat) inner.heartbeat = extraRaw.heartbeat
      else delete inner.heartbeat
      if (typeof extraRaw.zero_rtt_handshake === 'boolean') inner.zero_rtt_handshake = extraRaw.zero_rtt_handshake
      else delete inner.zero_rtt_handshake
    }
  }
  if (type === 'hysteria2') {
    inner.obfs_type = values.obfs_type === 'salamander' || values.obfs_type === 'gecko' ? values.obfs_type : ''
    inner.obfs_password = inner.obfs_type ? (values.obfs_password || '') : ''
    inner.gecko_min_packet_size = inner.obfs_type === 'gecko' ? (values.gecko_min_packet_size || 0) : 0
    inner.gecko_max_packet_size = inner.obfs_type === 'gecko' ? (values.gecko_max_packet_size || 0) : 0
    if (extraRaw) {
      if (typeof extraRaw.up_mbps === 'number' && extraRaw.up_mbps > 0) inner.up_mbps = extraRaw.up_mbps
      else delete inner.up_mbps
      if (typeof extraRaw.down_mbps === 'number' && extraRaw.down_mbps > 0) inner.down_mbps = extraRaw.down_mbps
      else delete inner.down_mbps
    }
  }
  if (type === 'http') {
    if (extraRaw) {
      if (typeof extraRaw.path === 'string' && extraRaw.path) inner.http_path = extraRaw.path
      else delete inner.http_path
      if (extraRaw.headers && typeof extraRaw.headers === 'object' && !Array.isArray(extraRaw.headers)) {
        inner.http_headers = { ...(extraRaw.headers as Record<string, string>) }
      } else {
        delete inner.http_headers
      }
    }
    // buildClientOutbound only derives Basic auth when username is nonempty;
    // preserve the password-only form through an explicit header after merging
    // custom headers so extraRaw.headers cannot wipe it out.
    const headers = { ...(inner.http_headers ?? {}) }
    const hasAuth = Object.keys(headers).some((key) => key.toLowerCase() === 'proxy-authorization')
    if (!outer.username && outer.password && !hasAuth) {
      headers['Proxy-Authorization'] = 'Basic ' + encodeBase64UTF8(':' + outer.password)
      inner.http_headers = headers
    } else if (Object.keys(headers).length === 0) {
      delete inner.http_headers
    }
  }
  if (type === 'snell') {
    // Imported configs duplicated the PSK here. The editable outer password
    // is authoritative; a stale inner PSK otherwise wins during rendering.
    delete inner.snell_psk
    inner.snell_version = values.snell_version || 4
    inner.snell_reuse = !!values.snell_reuse
    inner.snell_network = values.snell_network || 'tcp'
    if (inner.snell_version === 4) {
      inner.snell_obfs_mode = values.snell_obfs_mode || 'none'
      inner.snell_obfs_host = String(values.snell_obfs_host || '').trim()
      inner.snell_mode = ''
    } else {
      inner.snell_obfs_mode = 'none'
      inner.snell_obfs_host = ''
      inner.snell_mode = values.snell_mode || ''
    }
  }

  if (TRANSPORT_OUT.has(type)) {
    if (values.transport_type === 'ws' || values.transport_type === 'httpupgrade') {
      const transport: TransportSettings = sameProtocol ? cloneJSON(inner.transport ?? {}) : {}
      transport.type = values.transport_type
      transport.path = values.ws_path || ''
      setHostHeader(transport, values.ws_host)
      inner.transport = transport
    } else if (EXTENDED_TRANSPORT_OUT.has(values.transport_type)) {
      const transport: TransportSettings = sameProtocol ? cloneJSON(inner.transport ?? {}) : {}
      transport.type = values.transport_type
      inner.transport = transport
    } else {
      delete inner.transport
    }
  }

  if (type === 'vless') {
    if (values.tls_mode === 'reality') {
      const tls = sameProtocol ? cloneJSON(inner.tls ?? {}) : {}
      const reality = sameProtocol ? cloneJSON(tls.reality ?? {}) : {}
      tls.enabled = true
      tls.server_name = String(values.reality_server_name || '').trim()
      tls.alpn = splitValues(values.tls_alpn)
      if (values.tls_fingerprint === '') delete tls.fingerprint
      else tls.fingerprint = values.tls_fingerprint || tls.fingerprint || 'chrome'
      tls.reality = {
        ...reality,
        enabled: true,
        public_key: String(values.reality_public_key || '').trim(),
        short_id: splitValues(values.reality_short_id),
      }
      inner.tls = tls
    } else if (values.tls_mode === 'tls') {
      const tls = sameProtocol ? cloneJSON(inner.tls ?? {}) : {}
      delete tls.reality
      tls.enabled = true
      tls.server_name = String(values.sni || '').trim()
      tls.alpn = splitValues(values.tls_alpn)
      if (values.tls_fingerprint === '') delete tls.fingerprint
      else tls.fingerprint = values.tls_fingerprint || tls.fingerprint || 'chrome'
      tls.insecure = !!values.insecure
      inner.tls = tls
    } else {
      delete inner.tls
    }
  } else if (type === 'vmess' || type === 'trojan' || type === 'hysteria2' || type === 'tuic' || type === 'anytls' || type === 'http') {
    if (TLS_REQUIRED_OUT.has(type) || values.tls) {
      const tls = sameProtocol ? cloneJSON(inner.tls ?? {}) : {}
      tls.enabled = true
      tls.server_name = String(values.sni || '').trim()
      tls.alpn = splitValues(values.tls_alpn)
      if (typeof values.tls_fingerprint === 'string') {
        if (values.tls_fingerprint === '') delete tls.fingerprint
        else tls.fingerprint = values.tls_fingerprint
      }
      tls.insecure = !!values.insecure
      inner.tls = tls
    } else {
      delete inner.tls
    }
  }

  const rawSource = extraRaw ?? (sameProtocol ? inner.raw : undefined)
  if (rawSource && Object.keys(rawSource).length > 0) {
    const mergedRaw = outboundSettingsToNativeJson(type, String(values.tag || ''), {
      ...outer,
      settings: { ...inner, raw: rawSource },
    })
    if (!inner.tls) delete mergedRaw.tls
    if (!inner.transport || inner.transport.type === 'tcp') delete mergedRaw.transport
    inner.raw = mergedRaw
  } else {
    delete inner.raw
  }

  outer.settings = inner
  return {
    tag: typeof values.tag === 'string' ? values.tag.trim() : values.tag,
    type,
    settings: outer,
  }
}
