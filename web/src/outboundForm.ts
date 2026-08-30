import type { InboundSettings, Outbound, OutboundSettings, OutboundType, TransportSettings } from './types'

export const TLS_REQUIRED_OUT = new Set<OutboundType>(['trojan', 'hysteria2', 'tuic', 'anytls'])
export const TRANSPORT_OUT = new Set<OutboundType>(['vless', 'vmess', 'trojan'])

type OutboundFormValues = Record<string, any> & { type: OutboundType }

function cloneJSON<T>(value: T): T {
  return JSON.parse(JSON.stringify(value ?? {})) as T
}

function splitValues(value: unknown): string[] {
  if (typeof value !== 'string') return []
  return value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean)
}

function setHostHeader(transport: TransportSettings, value: unknown) {
  const headers = { ...(transport.headers ?? {}) }
  const existingHost = Object.keys(headers).find((key) => key.toLowerCase() === 'host')
  if (existingHost) delete headers[existingHost]
  const host = typeof value === 'string' ? value.trim() : ''
  if (host) headers.Host = host
  if (Object.keys(headers).length) transport.headers = headers
  else delete transport.headers
}

// Builds an outbound update without destroying settings the compact form does
// not expose. Imported configs can contain REALITY, plugins, early-data,
// repeated transport headers and protocol-specific beta fields; editing a tag
// or one visible field must leave those values intact.
export function buildOutboundBody(outbound: Outbound | null, values: OutboundFormValues) {
  const type = values.type
  const sameProtocol = outbound?.type === type
  const outer: OutboundSettings = sameProtocol ? cloneJSON(outbound?.settings ?? {}) : {}
  const inner: InboundSettings = sameProtocol ? cloneJSON(outbound?.settings?.settings ?? {}) : {}

  outer.server = typeof values.server === 'string' ? values.server.trim() : values.server
  outer.server_port = values.server_port

  if (type === 'socks') {
    outer.username = typeof values.username === 'string' ? values.username.trim() : ''
    outer.password = values.password || ''
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
  if (type === 'vless') inner.flow = values.flow || ''
  if (type === 'vmess') {
    inner.vmess_security = values.vmess_security || 'auto'
    inner.vmess_alter_id = values.vmess_alter_id ?? 0
  }
  if (type === 'tuic') inner.congestion_control = values.congestion_control || 'cubic'
  if (type === 'hysteria2') {
    inner.obfs_type = values.obfs_type === 'salamander' || values.obfs_type === 'gecko' ? values.obfs_type : ''
    inner.obfs_password = inner.obfs_type ? (values.obfs_password || '') : ''
    inner.gecko_min_packet_size = inner.obfs_type === 'gecko' ? (values.gecko_min_packet_size || 0) : 0
    inner.gecko_max_packet_size = inner.obfs_type === 'gecko' ? (values.gecko_max_packet_size || 0) : 0
  }
  if (type === 'snell') {
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
      tls.fingerprint = values.tls_fingerprint || 'chrome'
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
      tls.fingerprint = values.tls_fingerprint || 'chrome'
      tls.insecure = !!values.insecure
      inner.tls = tls
    } else {
      delete inner.tls
    }
  } else if (type === 'vmess' || type === 'trojan' || type === 'hysteria2' || type === 'tuic' || type === 'anytls') {
    if (TLS_REQUIRED_OUT.has(type) || values.tls) {
      const tls = sameProtocol ? cloneJSON(inner.tls ?? {}) : {}
      tls.enabled = true
      tls.server_name = String(values.sni || '').trim()
      tls.alpn = splitValues(values.tls_alpn)
      tls.insecure = !!values.insecure
      inner.tls = tls
    } else {
      delete inner.tls
    }
  }

  outer.settings = inner
  return {
    tag: typeof values.tag === 'string' ? values.tag.trim() : values.tag,
    type,
    settings: outer,
  }
}
