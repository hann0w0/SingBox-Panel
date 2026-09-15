import type { CustomNode } from './api'

type FormValues = Record<string, unknown>

const PARAM_FIELDS: Record<string, readonly string[]> = {
  vless: ['uuid', 'flow'],
  vmess: ['uuid'],
  trojan: ['password'],
  anytls: ['password', 'udp_over_stream'],
  tuic: ['uuid', 'password', 'congestion_control', 'udp_relay_mode'],
  hysteria2: ['password', 'obfs', 'obfs_password', 'gecko_min_packet_size', 'gecko_max_packet_size', 'up_mbps', 'down_mbps'],
  hysteria: ['password', 'obfs', 'obfs_password', 'up_mbps', 'down_mbps'],
  shadowsocks: ['password', 'method', 'ss_plugin'],
  snell: ['psk', 'snell_version', 'snell_obfs_mode', 'snell_obfs_host', 'snell_mode'],
  socks: ['username', 'password'],
  mixed: ['username', 'password'],
}

const PARAM_NAMES: Record<string, string> = {
  tls_mode: 'tls', snell_version: 'version', snell_obfs_mode: 'obfs_mode',
  snell_obfs_host: 'obfs_host', snell_mode: 'mode',
}

// The form exposes only part of an imported node. Keep the original definition
// for same-protocol edits, and replace only fields present in validateFields().
export function buildCustomNodeParams(node: CustomNode | null, values: FormValues): Record<string, unknown> {
  const protocol = String(values.protocol || '')
  const sameProtocol = node?.protocol === protocol && !node.link?.trim()
  const params: Record<string, unknown> = sameProtocol ? JSON.parse(JSON.stringify(node.params ?? {})) : {}
  const fields = [...(PARAM_FIELDS[protocol] ?? [])]
  if (['vless', 'vmess', 'trojan', 'anytls', 'tuic', 'hysteria2', 'hysteria'].includes(protocol)) {
    fields.push('tls_mode', 'sni', 'insecure')
  }
  if (['vless', 'vmess', 'trojan'].includes(protocol)) {
    fields.push('alpn', 'fingerprint', 'pbk', 'sid', 'transport', 'path', 'host')
  }
  for (const field of fields) {
    if (!Object.prototype.hasOwnProperty.call(values, field)) continue
    const key = PARAM_NAMES[field] ?? field
    if (values[field] === undefined) delete params[key]
    else params[key] = values[field]
  }

  if (protocol === 'snell' && Object.prototype.hasOwnProperty.call(values, 'snell_version')) {
    if (Number(values.snell_version) === 6) {
      delete params.obfs_mode
      delete params.obfs_host
      delete params.obfs
    } else {
      delete params.mode
    }
  }

  // A cleared Host field must not fall back to the original headers object.
  // Leave repeated/case-preserved headers intact when the visible Host did not change.
  if (Object.prototype.hasOwnProperty.call(values, 'host') && values.host !== node?.params?.host && params.headers && typeof params.headers === 'object') {
    const headers = { ...(params.headers as Record<string, unknown>) }
    for (const key of Object.keys(headers)) if (key.toLowerCase() === 'host') delete headers[key]
    if (Object.keys(headers).length) params.headers = headers
    else delete params.headers
  }
  return params
}
