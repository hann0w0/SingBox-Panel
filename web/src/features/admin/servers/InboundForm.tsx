import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Info } from 'lucide-react'
import { EditorModeToggle } from '../../../components/EditorModeToggle'
import { JsonCodeEditor } from '../../../components/JsonCodeEditor'
import { SchemaFieldList } from '../../../components/SchemaFormFields'
import { Alert, Button, Form, Input, InputNumber, Modal, Segmented, Select, Space, Switch, Tooltip, type FormInstance } from '../../../components/kit'
import { TooltipProvider } from '../../../components/ui/tooltip'
import { createInbound, updateInbound } from '../../../lib/api'
import { applyWithToast } from '../../../lib/configApply'
import {
  cloneJson,
  inboundSettingsToNativeJson,
  nativeJsonToInboundSettings,
  pickPopulatedSchemaProps,
  resolveVariantProperties,
  useSingboxSchemas,
  validateObjectBySchema,
  type JSONSchema,
} from '../../../lib/singboxSchema'
import type { Inbound, InboundSettings, InboundType, RealitySettings } from '../../../lib/types'
import { INBOUND_PROTOCOL_TYPES, toProtocolOptions } from '../../../lib/protocols'
import { isValidSS2022Key, randomBase64, randomHex, randomUUID, SHADOWSOCKS_METHODS, ss2022KeyLen } from '../../../lib/util'
import '../../../styles/forms.css'
import '../../../styles/inbound-editor.css'

export const TYPES: { value: InboundType; label: string }[] = toProtocolOptions(INBOUND_PROTOCOL_TYPES)

const WS_TYPES = new Set<InboundType>(['vless', 'vmess', 'trojan'])
const REALITY_TYPES = new Set<InboundType>(['vless', 'vmess'])
const TLS_REQUIRED = new Set<InboundType>(['trojan', 'hysteria2', 'tuic', 'anytls'])
const NO_TLS = new Set<InboundType>(['shadowsocks', 'snell', 'socks', 'mixed', 'tun', 'direct', 'redirect', 'tproxy'])
const HAS_BANDWIDTH = new Set<InboundType>(['hysteria2'])
const EXTENDED_TRANSPORTS = new Set(['ws', 'httpupgrade', 'grpc', 'http', 'quic'])
const SCHEMA_INBOUND_PRIMARY_PROTO_KEYS = new Set([
  'interface_name', 'address', 'mtu', 'stack', 'auto_route', 'strict_route',
  'network', 'override_address', 'override_port',
])
const SCHEMA_TLS_STANDARD_KEYS = new Set([
  'enabled', 'server_name', 'alpn', 'certificate_path', 'key_path', 'reality', 'acme', 'certificate_provider',
])
const SCHEMA_INBOUND_TOP_EXCLUDES = new Set([
  'type', 'tag', 'listen', 'listen_port', 'flow', 'packet_encoding', 'method', 'password',
  'up_mbps', 'down_mbps', 'ignore_client_bandwidth', 'obfs', 'congestion_control',
  'auth_timeout', 'zero_rtt_handshake', 'heartbeat', 'version', 'psk', 'obfs_mode', 'mode',
  'fallback', 'tls', 'transport',
  'sniff', 'sniff_override_destination', 'domain_strategy',
])

function compactMultiplexSchema(schema: JSONSchema): JSONSchema {
  if (!schema.properties) return schema
  const { enabled, padding } = schema.properties
  return {
    ...schema,
    properties: {
      ...(enabled ? { enabled } : {}),
      ...(padding ? { padding } : {}),
    },
  }
}
// Which credential a single-user inbound presents.
const UUID_TYPES = new Set<InboundType>(['vless', 'vmess', 'tuic'])
const PW_TYPES = new Set<InboundType>(['trojan', 'hysteria2', 'anytls', 'tuic'])
const MULTI_USER_TYPES = new Set<InboundType>(['shadowsocks', 'vless', 'anytls', 'vmess', 'tuic', 'trojan', 'hysteria2'])
const SS2022_CHACHA_METHOD = '2022-blake3-chacha20-poly1305'
const SS_MULTI_USER_ERROR = '此加密方式不支持多用户，请关闭“单端口多用户”或改用 SS2022 AES 加密方式'
const DURATION_RE = /^\+?(?:(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h))+$/
const DURATION_PART_RE = /(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g
const DURATION_SCALE: Record<string, number> = { ns: 1e-9, us: 1e-6, 'µs': 1e-6, 'μs': 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 }

const VMESS_SECURITIES = ['auto', 'none', 'zero', 'aes-128-gcm', 'chacha20-poly1305', 'aes-128-cfb']

type FormVals = Record<string, any>

function parseALPN(value: unknown): string[] | undefined {
  if (typeof value !== 'string') return undefined
  const values = value.split(',').map((item) => item.trim()).filter(Boolean)
  return values.length ? [...new Set(values)] : undefined
}

// validateDuration accepts the Go duration subset the panel stores.
export function validateDuration(value: unknown, allowZero = false): string | undefined {
  const text = typeof value === 'string' ? value.trim() : ''
  if (!text || (allowZero && text === '0')) return undefined
  if (!DURATION_RE.test(text)) return '请输入时长，例如 1m'
  let seconds = 0
  for (const match of text.matchAll(DURATION_PART_RE)) {
    seconds += Number(match[1]) * (DURATION_SCALE[match[2]] ?? 0)
  }
  return seconds > 0 || allowZero ? undefined : '时长必须大于 0，例如 1m'
}

function randomPort(): number {
  return Math.floor(Math.random() * 50000) + 10000
}

function supportsMultiUser(type: InboundType, method?: string): boolean {
  if (!MULTI_USER_TYPES.has(type)) return false
  if (type === 'shadowsocks') return method === '2022-blake3-aes-128-gcm' || method === '2022-blake3-aes-256-gcm'
  return true
}

export function toForm(ib: Inbound | null): FormVals {
  if (!ib) {
    return {
      type: 'shadowsocks',
      listen_port: randomPort(),
      server: '',
      client_port: undefined,
      listen_mode: 'all',
      enabled: true,
      flow: 'xtls-rprx-vision',
      method: '2022-blake3-aes-128-gcm',
      vmess_security: 'auto',
      vmess_alter_id: 0,
      congestion_control: 'cubic',
      auth_timeout: '3s',
      zero_rtt_handshake: false,
      heartbeat: '10s',
      tls_mode: 'reality',
      tls_insecure: true,
      // REALITY camouflage target is the operator's choice — do not prefill one.
      reality_handshake_server: undefined,
      reality_handshake_port: undefined,
      reality_max_time_difference: undefined,
      transport_type: 'tcp',
      snell_version: 5,
      snell_obfs_mode: 'none',
      snell_mode: '',
      multi_user: false,
    }
  }
  const s = ib.settings || {}
  const tls = s.tls || {}
  let tls_mode = 'none'
  if (tls.reality?.enabled) tls_mode = 'reality'
  else if (tls.acme_domain) tls_mode = 'acme'
  else if (tls.self_signed) tls_mode = 'self'
  else if (tls.enabled) tls_mode = 'tls'
  return {
    type: ib.type,
    tag: ib.tag,
    listen_port: ib.listen_port,
    server: s.server ?? '',
    client_port: s.client_port || undefined,
    listen_mode: s.ipv6_only ? 'ipv6' : s.listen === '0.0.0.0' ? 'ipv4' : s.listen === '127.0.0.1' || s.listen === '::1' ? 'local' : 'all',
    enabled: ib.enabled,
    // Keep invalid ChaCha opt-ins visible until the administrator fixes them.
    multi_user: !!s.multi_user && (supportsMultiUser(ib.type, s.method) || (ib.type === 'shadowsocks' && s.method === SS2022_CHACHA_METHOD)),
    username: s.username ?? '',
    uuid: s.uuid ?? '',
    password: s.password ?? '',
    ss_psk: s.ss_server_psk ?? '',
    flow: s.flow ?? '',
    method: s.method ?? '2022-blake3-aes-128-gcm',
    vmess_security: s.vmess_security ?? 'auto',
    vmess_alter_id: s.vmess_alter_id ?? 0,
    up_mbps: s.up_mbps,
    down_mbps: s.down_mbps,
    obfs_password: s.obfs_password,
    congestion_control: s.congestion_control ?? 'cubic',
    auth_timeout: s.auth_timeout ?? '3s',
    zero_rtt_handshake: s.zero_rtt_handshake ?? false,
    heartbeat: s.heartbeat ?? '10s',
    trojan_fallback_server: s.trojan_fallback?.server,
    trojan_fallback_port: s.trojan_fallback?.server_port,
    snell_version: s.snell_version ?? 5,
    snell_psk: s.snell_psk ?? '',
    snell_obfs_mode: s.snell_obfs_mode ?? 'none',
    snell_mode: s.snell_mode === 'default' ? '' : (s.snell_mode ?? ''),
    obfs_type: s.obfs_type ?? '',
    ignore_client_bandwidth: s.ignore_client_bandwidth ?? false,
    gecko_min_packet_size: s.gecko_min_packet_size ?? undefined,
    gecko_max_packet_size: s.gecko_max_packet_size ?? undefined,
    tls_mode,
    tls_alpn: tls.alpn?.join(', '),
    reality_handshake_server: tls.reality?.handshake_server || undefined,
    reality_handshake_port: tls.reality?.handshake_server_port || undefined,
    reality_max_time_difference: tls.reality?.max_time_difference ?? '',
    tls_server_name: tls.reality?.enabled && tls.server_name === tls.reality.handshake_server ? undefined : tls.server_name,
    tls_cert_path: tls.certificate_path,
    tls_key_path: tls.key_path,
    // Preserve explicit choice on existing certificate-file inbounds; default
    // to skipping client verification when switching another mode to cert files.
    tls_insecure: tls_mode === 'tls' ? !!tls.insecure : true,
    acme_domain: tls.acme_domain,
    acme_email: tls.acme_email,
    transport_type: EXTENDED_TRANSPORTS.has(s.transport?.type ?? '') ? s.transport!.type : 'tcp',
    ws_path: s.transport?.path,
    ws_host: transportHost(s.transport?.headers),
    max_early_data: s.transport?.max_early_data,
    early_data_header: s.transport?.early_data_header,
    tls_fingerprint: tls.fingerprint,
    packet_encoding: s.packet_encoding ?? 'xudp',
    anytls_udp_over_stream: s.anytls_udp_over_stream ?? false,
    }
}

function transportHost(headers: Record<string, string | string[]> | undefined): string {
  const value = Object.entries(headers ?? {}).find(([key]) => key.toLowerCase() === 'host')?.[1]
  return Array.isArray(value) ? value[0] ?? '' : value ?? ''
}

// Build settings, preserving generated secrets from `base` (edit case) and
// merging any schema-driven raw sing-box fields without losing complex parameters.
export function assembleSettings(
  base: InboundSettings,
  v: FormVals,
  type: InboundType,
  extraRaw?: Record<string, unknown>,
): InboundSettings {
  const s: InboundSettings = JSON.parse(JSON.stringify(base || {}))
  if (v.listen_mode !== undefined) {
    const originalMode = base.ipv6_only ? 'ipv6' : base.listen === '0.0.0.0' ? 'ipv4' : base.listen === '127.0.0.1' || base.listen === '::1' ? 'local' : 'all'
    s.listen = v.listen_mode === originalMode && base.listen ? base.listen : ({ all: '::', ipv4: '0.0.0.0', ipv6: '::', local: '127.0.0.1' } as Record<string, string>)[v.listen_mode] || '::'
    s.ipv6_only = v.listen_mode === 'ipv6'
  }
  if (extraRaw && typeof extraRaw.listen === 'string' && extraRaw.listen !== base.raw?.listen) {
    s.listen = extraRaw.listen
  }
  if (type === 'tun') {
    delete s.server
    delete s.client_port
  } else {
    if (typeof v.server === 'string') s.server = v.server.trim()
    const clientPort = Number(v.client_port)
    if (Number.isInteger(clientPort) && clientPort >= 1 && clientPort <= 65535) {
      s.client_port = clientPort
    } else {
      delete s.client_port
    }
  }

  // Multi-user is explicit and capability-gated. Snell and legacy
  // Shadowsocks always retain their stable shared credential.
  s.multi_user = !!v.multi_user && supportsMultiUser(type, v.method)
  s.single_user = !s.multi_user
  if (UUID_TYPES.has(type)) s.uuid = v.uuid || s.uuid
  if (PW_TYPES.has(type)) s.password = v.password || s.password
  if (type === 'shadowsocks') {
    const psk = typeof v.ss_psk === 'string' ? v.ss_psk.trim() : ''
    if (psk) s.ss_server_psk = psk
    else if (s.ss_server_psk && !isValidSS2022Key(v.method || s.method || '', s.ss_server_psk)) s.ss_server_psk = ''
  }

  if (type === 'vless') {
    s.flow = v.flow || ''
    s.packet_encoding = v.packet_encoding || 'xudp'
  }
  if (type === 'vmess') {
    s.vmess_security = v.vmess_security || 'auto'
    s.vmess_alter_id = v.vmess_alter_id ?? 0
  }
  if (type === 'shadowsocks') s.method = v.method
  if (HAS_BANDWIDTH.has(type)) {
    s.up_mbps = v.up_mbps || 0
    s.down_mbps = v.down_mbps || 0
    s.obfs_type = v.obfs_type === 'salamander' || v.obfs_type === 'gecko' ? v.obfs_type : ''
    s.obfs_password = s.obfs_type ? (v.obfs_password || '') : ''
    s.gecko_min_packet_size = s.obfs_type === 'gecko' ? (v.gecko_min_packet_size || 0) : 0
    s.gecko_max_packet_size = s.obfs_type === 'gecko' ? (v.gecko_max_packet_size || 0) : 0
    s.ignore_client_bandwidth = !!v.ignore_client_bandwidth
  }
  if (type === 'tuic') {
    s.congestion_control = v.congestion_control || 'cubic'
    s.auth_timeout = v.auth_timeout || '3s'
    s.zero_rtt_handshake = !!v.zero_rtt_handshake
    s.heartbeat = v.heartbeat || '10s'
    // Client relay mode: native | quic (legacy TUIC v4 values are rejected).
    delete s.tuic_udp_relay_mode
  }
  if (type === 'anytls') {
    // AnyTLS always relays UDP over the TLS stream; the flag only controls
    // whether the anytls:// share link advertises udp_over_stream=1.
    s.anytls_udp_over_stream = !!v.anytls_udp_over_stream
  }
  if (type === 'trojan') {
    const server = typeof v.trojan_fallback_server === 'string' ? v.trojan_fallback_server.trim() : ''
    if (server || v.trojan_fallback_port) {
      s.trojan_fallback = { server, server_port: v.trojan_fallback_port }
    } else {
      delete s.trojan_fallback
    }
  }
  if (type === 'snell') {
    s.snell_version = v.snell_version || 5
    s.snell_psk = v.snell_psk || ''
    // Hidden fields remain in the form when switching versions.
    s.snell_obfs_mode = s.snell_version === 5 ? (v.snell_obfs_mode || 'none') : 'none'
    s.snell_mode = s.snell_version === 6 ? (v.snell_mode || '') : ''
  }
  if (type === 'socks' || type === 'mixed' || type === 'http') {
    s.username = typeof v.username === 'string' ? v.username.trim() : ''
    s.password = typeof v.password === 'string' ? v.password : ''
  }
  // transport (vless/vmess/trojan)
  if (WS_TYPES.has(type) && EXTENDED_TRANSPORTS.has(v.transport_type)) {
    if (v.transport_type === 'ws' || v.transport_type === 'httpupgrade') {
      const headers = { ...(s.transport?.headers ?? {}) }
      const host = typeof v.ws_host === 'string' ? v.ws_host.trim() : ''
      if (host !== transportHost(headers)) {
        for (const key of Object.keys(headers)) if (key.toLowerCase() === 'host') delete headers[key]
        if (host) headers.Host = host
      }
      s.transport = {
        ...s.transport,
        type: v.transport_type,
        path: v.ws_path || '',
        headers: Object.keys(headers).length ? headers : undefined,
        max_early_data: v.max_early_data || 0,
        early_data_header: v.early_data_header || '',
      }
    } else {
      s.transport = { type: v.transport_type }
    }
  } else {
    delete s.transport
  }

  // tls
  if (NO_TLS.has(type)) {
    delete s.tls
  } else {
    const mode = v.tls_mode
    const alpn = parseALPN(v.tls_alpn)
    if (mode === 'none' || !mode) {
      delete s.tls
    } else if (mode === 'reality') {
      const prevReality = s.tls?.reality || {}
      const reality: RealitySettings = {
        ...prevReality,
        enabled: true,
        handshake_server: v.reality_handshake_server,
        handshake_server_port: v.reality_handshake_port || 443,
        max_time_difference: String(v.reality_max_time_difference || '').trim() || undefined,
      }
      s.tls = {
        enabled: true,
        server_name: String(v.tls_server_name || '').trim() || v.reality_handshake_server,
        alpn,
        fingerprint: v.tls_fingerprint,
        reality,
      }
    } else if (mode === 'self') {
      // Keep any PEM already issued so the cert stays stable for live clients;
      // the backend generates it on first save.
      s.tls = {
        enabled: true,
        self_signed: true,
        server_name: String(v.tls_server_name || '').trim() || undefined,
        alpn,
        fingerprint: v.tls_fingerprint,
        certificate: !v.tls_cert_path && !v.tls_key_path ? s.tls?.certificate : undefined,
        key: !v.tls_cert_path && !v.tls_key_path ? s.tls?.key : undefined,
        insecure: true,
      }
    } else if (mode === 'tls') {
      const certPath = String(v.tls_cert_path || '').trim()
      const keyPath = String(v.tls_key_path || '').trim()
      s.tls = {
        enabled: true,
        server_name: String(v.tls_server_name || '').trim() || undefined,
        alpn,
        fingerprint: v.tls_fingerprint,
        certificate_path: certPath || undefined,
        key_path: keyPath || undefined,
        // Preserve imported inline PEM even though this form exposes paths.
        // Otherwise an ordinary edit turns a valid inline-TLS inbound into an
        // invalid paths-only configuration.
        certificate: !certPath && !keyPath ? s.tls?.certificate : undefined,
        key: !certPath && !keyPath ? s.tls?.key : undefined,
        insecure: v.tls_insecure !== undefined ? !!v.tls_insecure : true,
      }
    } else if (mode === 'acme') {
      const acmeDomain = String(v.acme_domain || '').trim()
      const acmeEmail = String(v.acme_email || '').trim()
      s.tls = {
        enabled: true,
        acme_domain: acmeDomain,
        acme_email: acmeEmail || undefined,
        server_name: acmeDomain,
        alpn,
        fingerprint: v.tls_fingerprint,
      }
    }
  }

  const rawSource = extraRaw ?? base.raw
  if (rawSource && Object.keys(rawSource).length > 0) {
    const mergedRaw = inboundSettingsToNativeJson(type, String(v.tag || ''), v.listen_port, { ...s, raw: rawSource })
    if (!s.tls) delete mergedRaw.tls
    else if (mergedRaw.tls && typeof mergedRaw.tls === 'object') {
      const tlsRaw = mergedRaw.tls as Record<string, unknown>
      if (!s.tls.reality?.enabled) delete tlsRaw.reality
    }
    if (!s.transport || s.transport.type === 'tcp') delete mergedRaw.transport
    s.raw = mergedRaw
  }
  return s
}

function isSubscriptionServerOnlyEdit(inbound: Inbound, values: FormVals, raw: Record<string, unknown>): boolean {
  if (values.type !== inbound.type || values.tag !== inbound.tag || values.listen_port !== inbound.listen_port) return false
  const base = inbound.settings || {}
  const originalValues = toForm(inbound)
  const original = assembleSettings(base, originalValues, inbound.type)
  const draft = assembleSettings(base, { ...values, server: originalValues.server }, inbound.type, raw)
  return JSON.stringify(original) === JSON.stringify(draft)
}

export default function InboundForm({
  serverId,
  ipv6Available = false,
  inbound,
  prefill,
  preview = false,
  onPreview,
  open,
  onClose,
  onSaved,
}: {
  serverId: number
  ipv6Available?: boolean
  inbound: Inbound | null
  prefill?: Record<string, any>
  preview?: boolean
  onPreview?: (body: Record<string, unknown>) => void
  open: boolean
  onClose: () => void
  onSaved: () => void
}) {
  const [form] = Form.useForm()
  const [submitting, setSubmitting] = useState(false)
  const [editorMode, setEditorMode] = useState<'form' | 'json'>('form')
  const [rawExtra, setRawExtra] = useState<Record<string, unknown>>({})
  const [stickyTopKeys, setStickyTopKeys] = useState<Set<string>>(() => new Set())
  const [stickyTransportKeys, setStickyTransportKeys] = useState<Set<string>>(() => new Set())
  const [stickyTlsKeys, setStickyTlsKeys] = useState<Set<string>>(() => new Set())
  const [schemaJsonErrors, setSchemaJsonErrors] = useState<Record<string, string>>({})
  const [jsonText, setJsonText] = useState('')
  const [jsonError, setJsonError] = useState('')
  const schemas = useSingboxSchemas(open)
  const submitLock = useRef(false)
  const submitSessionRef = useRef(0)
  const initialJsonText = useRef('')
  const [dirty, setDirty] = useState(false)
  const type: InboundType = Form.useWatch('type', form) ?? 'shadowsocks'
  const showTLS = !NO_TLS.has(type)
  const watchedTlsMode = Form.useWatch('tls_mode', form)
  const tlsMode = watchedTlsMode || (showTLS ? (!inbound && type === 'vless' ? 'reality' : (TLS_REQUIRED.has(type) ? 'acme' : 'none')) : 'none')
  const transportType = Form.useWatch('transport_type', form)
  const snellVersion = Form.useWatch('snell_version', form)
  const obfsType = Form.useWatch('obfs_type', form)
  const method = Form.useWatch('method', form)
  const requestedMultiUser = !!Form.useWatch('multi_user', form)
  const multiUser = requestedMultiUser && supportsMultiUser(type, method)
  const invalidSSMultiUser = type === 'shadowsocks' && method === SS2022_CHACHA_METHOD && requestedMultiUser
  const listenMode = Form.useWatch('listen_mode', form)
  const listenPort = Form.useWatch('listen_port', form)
  const maxEarlyData = Form.useWatch('max_early_data', form)
  const earlyDataHeader = Form.useWatch('early_data_header', form)

  const variantProps = useMemo(
    () => resolveVariantProperties(schemas.inbound, type),
    [schemas.inbound, type],
  )
  const protocolExtraSchemaProps = useMemo(() => {
    const out: Record<string, JSONSchema> = {}
    for (const [k, v] of Object.entries(variantProps)) {
      if (SCHEMA_INBOUND_TOP_EXCLUDES.has(k)) continue
      if (type === 'shadowsocks' && k === 'network') continue
      if (SCHEMA_INBOUND_PRIMARY_PROTO_KEYS.has(k)) out[k] = v
    }
    return out
  }, [variantProps, type])
  const otherParamsSchemaProps = useMemo(() => {
    const candidates: Record<string, JSONSchema> = {}
    for (const [k, v] of Object.entries(variantProps)) {
      if (SCHEMA_INBOUND_TOP_EXCLUDES.has(k)) continue
      if (type !== 'shadowsocks' && SCHEMA_INBOUND_PRIMARY_PROTO_KEYS.has(k)) continue
      candidates[k] = k === 'multiplex' ? compactMultiplexSchema(v) : v
    }
    const alwaysInclude = new Set<string>(stickyTopKeys)
    if (type === 'hysteria2') alwaysInclude.add('masquerade')
    return pickPopulatedSchemaProps(candidates, rawExtra, alwaysInclude)
  }, [variantProps, type, rawExtra, stickyTopKeys])
  const transportSchemaProps = useMemo(() => {
    const trProps = variantProps.transport?.properties ?? {}
    const trRaw = (rawExtra.transport && typeof rawExtra.transport === 'object' ? rawExtra.transport : {}) as Record<string, unknown>
    if (transportType === 'grpc') {
      const candidates = {
        ...(trProps.service_name ? { service_name: trProps.service_name } : {}),
        ...(trProps.idle_timeout ? { idle_timeout: trProps.idle_timeout } : {}),
        ...(trProps.ping_timeout ? { ping_timeout: trProps.ping_timeout } : {}),
        ...(trProps.permit_without_stream ? { permit_without_stream: trProps.permit_without_stream } : {}),
      }
      return pickPopulatedSchemaProps(candidates, trRaw, new Set(['service_name', ...stickyTransportKeys]))
    }
    if (transportType === 'http') {
      const candidates = {
        ...(trProps.host ? { host: trProps.host } : {}),
        ...(trProps.path ? { path: trProps.path } : {}),
        ...(trProps.method ? { method: trProps.method } : {}),
        ...(trProps.headers ? { headers: trProps.headers } : {}),
        ...(trProps.idle_timeout ? { idle_timeout: trProps.idle_timeout } : {}),
        ...(trProps.ping_timeout ? { ping_timeout: trProps.ping_timeout } : {}),
      }
      return pickPopulatedSchemaProps(candidates, trRaw, new Set(['host', 'path', ...stickyTransportKeys]))
    }
    return {}
  }, [variantProps, transportType, rawExtra.transport, stickyTransportKeys])
  const tlsAdvSchemaProps = useMemo(() => {
    const tlsProps = variantProps.tls?.properties ?? {}
    const candidates: Record<string, JSONSchema> = {}
    for (const [k, v] of Object.entries(tlsProps)) {
      if (!SCHEMA_TLS_STANDARD_KEYS.has(k)) candidates[k] = v
    }
    return pickPopulatedSchemaProps(candidates, (rawExtra.tls && typeof rawExtra.tls === 'object' ? rawExtra.tls : {}) as Record<string, unknown>, stickyTlsKeys)
  }, [variantProps, rawExtra.tls, stickyTlsKeys])

  const seedStickyFromRaw = (targetType: InboundType, raw: Record<string, unknown>) => {
    const props = resolveVariantProperties(schemas.inbound, targetType)
    const topCandidates: Record<string, JSONSchema> = {}
    for (const [k, schema] of Object.entries(props)) {
      if (SCHEMA_INBOUND_TOP_EXCLUDES.has(k)) continue
      if (targetType !== 'shadowsocks' && SCHEMA_INBOUND_PRIMARY_PROTO_KEYS.has(k)) continue
      topCandidates[k] = k === 'multiplex' ? compactMultiplexSchema(schema) : schema
    }
    setStickyTopKeys(new Set(Object.keys(pickPopulatedSchemaProps(topCandidates, raw))))
    const trRaw = (raw.transport && typeof raw.transport === 'object' ? raw.transport : {}) as Record<string, unknown>
    setStickyTransportKeys(new Set(Object.keys(pickPopulatedSchemaProps((props.transport?.properties ?? {}), trRaw))))
    const tlsRaw = (raw.tls && typeof raw.tls === 'object' ? raw.tls : {}) as Record<string, unknown>
    const tlsCandidates: Record<string, JSONSchema> = {}
    for (const [k, schema] of Object.entries(props.tls?.properties ?? {})) {
      if (!SCHEMA_TLS_STANDARD_KEYS.has(k)) tlsCandidates[k] = schema
    }
    setStickyTlsKeys(new Set(Object.keys(pickPopulatedSchemaProps(tlsCandidates, tlsRaw))))
  }
  const updateRawExtra = (updater: Record<string, unknown> | ((prev: Record<string, unknown>) => Record<string, unknown>)) => {
    setDirty(true)
    setRawExtra((prev) => {
      const next = typeof updater === 'function' ? updater(prev) : updater
      const changed = Object.keys(next).filter((k) => next[k] !== prev[k])
      if (changed.length > 0) {
        setStickyTopKeys((s) => {
          if (changed.every((k) => s.has(k))) return s
          const copy = new Set(s)
          for (const k of changed) copy.add(k)
          return copy
        })
      }
      return next
    })
  }
  const updateRawTransport = (nextTr: Record<string, unknown>) => {
    setDirty(true)
    setRawExtra((prev) => {
      const prevTr = (prev.transport && typeof prev.transport === 'object' ? prev.transport : {}) as Record<string, unknown>
      const changed = Object.keys(nextTr).filter((k) => nextTr[k] !== prevTr[k])
      if (changed.length > 0) {
        setStickyTransportKeys((s) => {
          if (changed.every((k) => s.has(k))) return s
          const copy = new Set(s)
          for (const k of changed) copy.add(k)
          return copy
        })
      }
      return { ...prev, transport: nextTr }
    })
  }
  const updateRawTls = (nextTls: Record<string, unknown>) => {
    setDirty(true)
    setRawExtra((prev) => {
      const prevTls = (prev.tls && typeof prev.tls === 'object' ? prev.tls : {}) as Record<string, unknown>
      const changed = Object.keys(nextTls).filter((k) => nextTls[k] !== prevTls[k])
      if (changed.length > 0) {
        setStickyTlsKeys((s) => {
          if (changed.every((k) => s.has(k))) return s
          const copy = new Set(s)
          for (const k of changed) copy.add(k)
          return copy
        })
      }
      return { ...prev, tls: nextTls }
    })
  }

  useLayoutEffect(() => {
    submitSessionRef.current += 1
    submitLock.current = false
    setSubmitting(false)
    return () => { submitSessionRef.current += 1 }
  }, [open, serverId, inbound?.id])

  useEffect(() => {
    if (!open) return
    // Reset first: toForm() omits many keys (tag, credentials, TLS paths...),
    // and without a reset those survive from the previously opened inbound —
    // 新增协议 would inherit the last one's tag and secrets.
    form.resetFields()
    const initialVals = prefill ?? toForm(inbound)
    form.setFieldsValue(initialVals)
    const initialRaw = cloneJson((inbound?.settings?.raw ?? {}) as Record<string, unknown>)
    setRawExtra(initialRaw)
    seedStickyFromRaw((initialVals.type ?? inbound?.type ?? 'shadowsocks') as InboundType, initialRaw)
    setSchemaJsonErrors({})
    setEditorMode('form')
    setJsonError('')
    setDirty(false)
    setJsonText(
      inbound
        ? JSON.stringify(inboundSettingsToNativeJson(inbound.type, inbound.tag, inbound.listen_port, inbound.settings || {}), null, 2)
        : '',
    )
  }, [form, open, inbound, prefill])

  useEffect(() => {
    if (!open || !showTLS) return
    if (!form.getFieldValue('tls_mode')) {
      form.setFieldValue('tls_mode', !inbound && type === 'vless' ? 'reality' : (TLS_REQUIRED.has(type) ? 'acme' : 'none'))
    }
  }, [open, showTLS, type, inbound, form])

  const switchEditorMode = (nextMode: string) => {
    if (nextMode !== 'form' && nextMode !== 'json') return
    if (nextMode === editorMode) return
    setJsonError('')
    if (nextMode === 'json') {
      const v = form.getFieldsValue()
      const activeType = (v.type || type) as InboundType
      const settings = assembleSettings(inbound?.settings || {}, v, activeType, rawExtra)
      const nativeObj = inboundSettingsToNativeJson(activeType, String(v.tag || ''), Number(v.listen_port || 0) || undefined, settings)
      initialJsonText.current = JSON.stringify(nativeObj, null, 2)
      setJsonText(initialJsonText.current)
      setEditorMode('json')
      return
    }
    if (jsonText === initialJsonText.current) {
      setEditorMode('form')
      return
    }
    try {
      const parsed = JSON.parse(jsonText || '{}') as Record<string, unknown>
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        setJsonError('配置必须是 JSON 对象')
        return
      }
      const schemaErr = validateObjectBySchema(schemas.inbound, parsed)
      if (schemaErr) {
        setJsonError(schemaErr)
        return
      }
      const nextType = (typeof parsed.type === 'string' && TYPES.some((t) => t.value === parsed.type) ? parsed.type : type) as InboundType
      if (inbound && nextType !== inbound.type) {
        setJsonError(`编辑已有入站时不能修改协议类型（当前为 ${inbound.type}）`)
        return
      }
      const prevFormVals = form.getFieldsValue() as FormVals
      const activeMethod = typeof parsed.method === 'string' ? parsed.method : prevFormVals.method
      const isMultiUser = !!(prevFormVals.multi_user ?? inbound?.settings?.multi_user) && supportsMultiUser(nextType, activeMethod)
      if (!isMultiUser && Array.isArray(parsed.users) && parsed.users.length > 1) {
        setJsonError('当前处于单用户模式，不支持在 JSON 中配置多个 users；请开启“单端口多用户”或仅保留 1 条用户凭证')
        return
      }
      const prevSettings = assembleSettings(inbound?.settings || {}, prevFormVals, nextType, rawExtra)
      const nextSettings = nativeJsonToInboundSettings(prevSettings, parsed)
      const nextTag = typeof parsed.tag === 'string' ? parsed.tag : ''
      const nextPort = typeof parsed.listen_port === 'number' ? parsed.listen_port : (form.getFieldValue('listen_port') || 0)
      const nextRaw = cloneJson(nextSettings.raw ?? {})
      setRawExtra(nextRaw)
      seedStickyFromRaw(nextType, nextRaw)
      setSchemaJsonErrors({})
      form.setFieldsValue(
        toForm({
          ...(inbound ?? { id: 0, server_id: serverId, enabled: true, remark: '', created_at: '', updated_at: '' }),
          type: nextType,
          tag: nextTag,
          listen_port: nextPort,
          settings: nextSettings,
        }),
      )
      setEditorMode('form')
    } catch (err) {
      setJsonError(err instanceof Error ? `JSON 解析失败: ${err.message}` : 'JSON 格式错误')
    }
  }

  const tlsModeOptions = (t: InboundType = type) => {
    const opts: { value: string; label: string }[] = []
    if (!TLS_REQUIRED.has(t)) opts.push({ value: 'none', label: '无 TLS' })
    if (REALITY_TYPES.has(t)) opts.push({ value: 'reality', label: 'REALITY' })
    opts.push({ value: 'acme', label: 'ACME 自动证书' })
    opts.push({ value: 'tls', label: '证书文件' })
    if (inbound?.settings?.tls?.self_signed) opts.push({ value: 'self', label: '自签证书（旧）' })
    return opts
  }

  const readValues = async (): Promise<FormVals> => {
    return form.validateFields().catch((err) => {
      scrollToFirstError(form, err)
      throw err
    })
  }

  const handleSchemaJsonError = (fieldKey: string, err: string | null) => {
    setJsonError('')
    setSchemaJsonErrors((prev) => {
      if (!err) {
        if (!(fieldKey in prev)) return prev
        const next = { ...prev }
        delete next[fieldKey]
        return next
      }
      if (prev[fieldKey] === err) return prev
      return { ...prev, [fieldKey]: err }
    })
  }

  const submit = async () => {
    if (!open || submitLock.current) return
    submitLock.current = true
    const session = submitSessionRef.current
    setSubmitting(true)
    try {
      const values = form.getFieldsValue()
      const subscriptionServer = String(values.server ?? '')
      if (
        inbound && !preview && !prefill && subscriptionServer !== (inbound.settings?.server ?? '') &&
        (editorMode === 'form' || jsonText === initialJsonText.current) && !Object.keys(schemaJsonErrors).length &&
        isSubscriptionServerOnlyEdit(inbound, values, rawExtra)
      ) {
        const saved = await applyWithToast(`inbound-${inbound.id}`, () =>
          updateInbound(serverId, inbound.id, { settings: { server: subscriptionServer.trim() } }), '正在保存')
        if (saved && session === submitSessionRef.current) {
          onClose()
          onSaved()
        }
        return
      }
      let body: {
        type: InboundType
        tag: string
        listen_port: number
        settings: InboundSettings
      }
      if (editorMode === 'json') {
        setJsonError('')
        let parsed: Record<string, unknown>
        try {
          parsed = JSON.parse(jsonText || '{}') as Record<string, unknown>
        } catch (err) {
          setJsonError(err instanceof Error ? `JSON 解析失败: ${err.message}` : 'JSON 格式错误')
          return
        }
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          setJsonError('配置必须是 JSON 对象')
          return
        }
        const schemaErr = validateObjectBySchema(schemas.inbound, parsed)
        if (schemaErr) {
          setJsonError(schemaErr)
          return
        }
        const nextType = (typeof parsed.type === 'string' && TYPES.some((t) => t.value === parsed.type) ? parsed.type : type) as InboundType
        if (inbound && nextType !== inbound.type) {
          setJsonError(`编辑已有入站时不能修改协议类型（当前为 ${inbound.type}）`)
          return
        }
        const prevFormVals = form.getFieldsValue() as FormVals
        const activeMethod = typeof parsed.method === 'string' ? parsed.method : prevFormVals.method
        const isMultiUser = !!(prevFormVals.multi_user ?? inbound?.settings?.multi_user) && supportsMultiUser(nextType, activeMethod)
        if (!isMultiUser && Array.isArray(parsed.users) && parsed.users.length > 1) {
          setJsonError('当前处于单用户模式，不支持在 JSON 中配置多个 users；请开启“单端口多用户”或仅保留 1 条用户凭证')
          return
        }
        const prevSettings = assembleSettings(inbound?.settings || {}, prevFormVals, nextType, rawExtra)
        const nextSettings = nativeJsonToInboundSettings(prevSettings, parsed)
        body = {
          type: nextType,
          tag: typeof parsed.tag === 'string' ? parsed.tag : '',
          listen_port: nextType === 'tun' ? Number(parsed.listen_port || 0) : (Number(parsed.listen_port || 0) || randomPort()),
          settings: nextSettings,
        }
      } else {
        setJsonError('')
        const v = await readValues()
        if (session !== submitSessionRef.current) return
        const badJson = Object.values(schemaJsonErrors)[0]
        if (badJson) {
          setJsonError(badJson)
          return
        }
        const settings = assembleSettings(inbound?.settings || {}, v, v.type, rawExtra)
        const listenPort: number = v.type === 'tun' ? Number(v.listen_port || 0) : (v.listen_port || randomPort())
        if (v.type !== 'tun' && !v.listen_port && listenPort > 0) {
          form.setFieldValue('listen_port', listenPort)
        }
        const nativeObj = inboundSettingsToNativeJson(v.type as InboundType, String(v.tag || ''), v.type === 'tun' ? 0 : listenPort, settings)
        const schemaErr = validateObjectBySchema(schemas.inbound, nativeObj)
        if (schemaErr) {
          setJsonError(schemaErr)
          return
        }
        body = {
          type: v.type as InboundType,
          tag: v.tag,
          listen_port: listenPort,
          settings,
        }
      }
      if (preview) {
        onPreview?.({ ...body, enabled: inbound?.enabled ?? true })
        return
      }
      const editing = inbound
      if (editing && (body.settings.server ?? '') === (editing.settings?.server ?? '')) {
        delete body.settings.server
      }
      const saved = await applyWithToast(`inbound-${editing ? editing.id : 'new'}`, () =>
        editing ? updateInbound(serverId, editing.id, body) : createInbound(serverId, { ...body, enabled: true }),
        '正在保存',
      )
      if (!saved || session !== submitSessionRef.current) return
      onClose()
      onSaved()
    } finally {
      if (session === submitSessionRef.current) {
        submitLock.current = false
        setSubmitting(false)
      }
    }
  }

  const showTransport = WS_TYPES.has(type)
  const canMultiUser = supportsMultiUser(type, method)
  const hasProtocolSchemaExtras = Object.keys(protocolExtraSchemaProps).length > 0
  const hasInlineCert = !!(inbound?.settings?.tls?.certificate && inbound?.settings?.tls?.key)
  const showTLSFingerprint = showTLS && tlsMode !== 'none' && type !== 'hysteria2' && type !== 'tuic' && type !== 'http'
  const jsonField = (key: string, value: unknown) => {
    if (form.getFieldValue(key) === undefined) form.setFieldValue(key, value)
  }

  return (
    <Modal
      className="fm-modal inbound-compact"
      title={<span className="inbound-compact-heading">{!preview && inbound ? '编辑入站' : '新增入站'}</span>}
      open={open}
      onOk={submit}
      onCancel={onClose}
      maskClosable={!submitting && !dirty}
      confirmLoading={submitting}
      okText="保存"
      cancelText="取消"
      width={680}
    >
      <fieldset disabled={submitting} aria-busy={submitting} className="inbound-compact-fieldset">
      <div className="mb-4 flex items-center justify-between gap-3">
        <EditorModeToggle
          ariaLabel="入站编辑模式"
          value={editorMode}
          onChange={switchEditorMode}
          disabled={submitting}
        />
      </div>
      {editorMode === 'json' ? (
        <div className="space-y-3">
          {jsonError && <Alert type="error" showIcon message={jsonError} />}
          <JsonCodeEditor
            ariaLabel="inbound.json"
            value={jsonText}
            disabled={submitting}
            onChange={(val) => {
              setJsonText(val)
              setDirty(true)
              if (jsonError) setJsonError('')
            }}
          />
        </div>
      ) : (
      <Form form={form} layout="vertical" className="fm-form inbound-compact-form" onFinish={submit} onChange={() => setDirty(true)} onInput={() => setDirty(true)}>
        {jsonError && <Alert type="error" showIcon message={jsonError} className="mb-3" />}
        <div className="fm-grid" data-panel="inbound">
          <Form.Item name="type" label="协议" rules={[{ required: true }]} className="inbound-compact-type">
            <Select
              options={TYPES}
              disabled={!!inbound}
              onChange={(t: InboundType) => {
                setRawExtra({})
                setStickyTopKeys(new Set())
                setStickyTransportKeys(new Set())
                setStickyTlsKeys(new Set())
                setSchemaJsonErrors({})
                setJsonError('')
                const allowed = tlsModeOptions(t).map((o) => o.value)
                const currentTls = form.getFieldValue('tls_mode')
                if (!currentTls || !allowed.includes(currentTls) || (!inbound && t === 'vless' && currentTls === 'none')) {
                  form.setFieldValue('tls_mode', t === 'vless' && allowed.includes('reality') ? 'reality' : allowed[0])
                }
                jsonField('listen_port', randomPort())
                jsonField('flow', 'xtls-rprx-vision')
                jsonField('method', '2022-blake3-aes-128-gcm')
                jsonField('vmess_security', 'auto')
                jsonField('vmess_alter_id', 0)
                jsonField('congestion_control', 'cubic')
                jsonField('auth_timeout', '3s')
                jsonField('heartbeat', '10s')
                jsonField('transport_type', 'tcp')
                jsonField('snell_version', 6)
                jsonField('snell_obfs_mode', 'none')
                jsonField('snell_mode', '')
                jsonField('multi_user', false)
              }}
            />
          </Form.Item>
          {type !== 'tun' && (
            <>
              <Form.Item name="listen_mode" label="监听" getValueProps={(value) => ({ value: value === 'ipv6' && !ipv6Available ? null : value })} rules={[{ validator: (_, value) => {
                if (value === 'ipv6' && !ipv6Available) throw new Error('服务器当前没有可用的 IPv6 地址，请选择其他监听方式')
              } }]}>
                <Select options={[
                  { value: 'all', label: '双栈' },
                  { value: 'local', label: '本机' },
                  { value: 'ipv4', label: 'IPv4' },
                  ...(ipv6Available ? [{ value: 'ipv6', label: 'IPv6' }] : []),
                ]} placeholder={listenMode === 'ipv6' ? 'IPv6 不可用' : undefined} />
              </Form.Item>
              <TooltipProvider delayDuration={180}>
                <Form.Item
                  name="client_port"
                  label={(
                    <span className="inbound-label-hint">
                      连接端口
                      <Tooltip title="客户端订阅与节点链接使用的端口，留空则与监听端口一致">
                        <Info className="inbound-hint-icon" aria-hidden="true" />
                      </Tooltip>
                    </span>
                  )}
                >
                  <InputNumber
                    precision={0}
                    min={1}
                    max={65535}
                    placeholder={listenPort ? String(listenPort) : '同监听端口'}
                    className="mono"
                  />
                </Form.Item>
                <Form.Item
                  name="listen_port"
                  label={(
                    <span className="inbound-label-hint">
                      监听端口
                      <Tooltip title="服务端 sing-box 实际监听运行的端口">
                        <Info className="inbound-hint-icon" aria-hidden="true" />
                      </Tooltip>
                    </span>
                  )}
                >
                  <InputNumber precision={0} min={1} max={65535} className="mono" />
                </Form.Item>
                <Form.Item
                  name="server"
                  label={(
                    <span className="inbound-label-hint">
                      连接地址
                      <Tooltip title="客户端订阅与节点链接使用的域名或 IP，留空则使用服务器默认地址">
                        <Info className="inbound-hint-icon" aria-hidden="true" />
                      </Tooltip>
                    </span>
                  )}
                >
                  <Input allowClear spellCheck={false} className="mono" />
                </Form.Item>
              </TooltipProvider>
            </>
          )}
          <Form.Item name="tag" label="标签">
            <Input />
          </Form.Item>
          {type === 'shadowsocks' && (
            <Form.Item
              name="method"
              label="加密方式"
              rules={[{ required: true }]}
              className="fm-full"
              extra={method === SS2022_CHACHA_METHOD ? (
                invalidSSMultiUser
                  ? <span className="fm-warn">当前多用户配置无效：{SS_MULTI_USER_ERROR}</span>
                  : '仅支持单用户共享密码，切换到此加密方式会关闭多用户'
              ) : undefined}
            >
              <Select
                options={SHADOWSOCKS_METHODS.map((m) => ({ value: m, label: m }))}
                onChange={(value: string) => {
                  if (value !== method && !supportsMultiUser('shadowsocks', value)) form.setFieldValue('multi_user', false)
                  const required = ss2022KeyLen(value)
                  const currentPsk = String(form.getFieldValue('ss_psk') || '').trim()
                  if (required > 0 && currentPsk && !isValidSS2022Key(value, currentPsk)) {
                    form.setFieldValue('ss_psk', randomBase64(required))
                  }
                }}
              />
            </Form.Item>
          )}
          {(canMultiUser || invalidSSMultiUser) && (
            <div className="fm-switches">
              <Form.Item
                name="multi_user"
                label="单端口多用户"
                valuePropName="checked"
                className="fm-switch"
                dependencies={['method']}
                rules={[{
                  validator: (_, value) => !value || canMultiUser
                    ? Promise.resolve()
                    : Promise.reject(new Error(SS_MULTI_USER_ERROR)),
                }]}
              >
                <Switch />
              </Form.Item>
              {multiUser && inbound && !inbound.settings?.multi_user && (
                <div className="fm-warn">将更换该入站凭证，已分享的订阅需重新更新</div>
              )}
            </div>
          )}

          {!multiUser && UUID_TYPES.has(type) && (
            <Form.Item label="UUID" className="fm-full">
              <Space.Compact>
                <Form.Item name="uuid" noStyle>
                  <Input className="mono" placeholder={inbound ? '留空保持当前凭据' : '留空自动生成'} />
                </Form.Item>
                <Button onClick={() => form.setFieldValue('uuid', randomUUID())}>随机</Button>
              </Space.Compact>
            </Form.Item>
          )}
          {!multiUser && PW_TYPES.has(type) && (
            <Form.Item label="密码" className="fm-full">
              <Space.Compact>
                <Form.Item name="password" noStyle>
                  <Input className="mono" placeholder={inbound ? '留空保持当前凭据' : '留空自动生成'} />
                </Form.Item>
                <Button onClick={() => form.setFieldValue('password', randomHex(16))}>随机</Button>
              </Space.Compact>
            </Form.Item>
          )}
          {type === 'shadowsocks' && (
            <Form.Item label="密码 / PSK" className="fm-full">
              <Space.Compact>
                <Form.Item
                  name="ss_psk"
                  noStyle
                  dependencies={['method']}
                  rules={[{
                    validator: (_, value) => {
                      const raw = String(value || '').trim()
                      const m = form.getFieldValue('method') || ''
                      const required = ss2022KeyLen(m)
                      if (!raw || required === 0 || isValidSS2022Key(m, raw)) return Promise.resolve()
                      return Promise.reject(new Error(`此加密方式需要 ${required} 字节的 Base64 密钥，可留空自动生成或点击“随机”`))
                    },
                  }]}
                >
                  <Input className="mono" placeholder={inbound ? '留空保持当前凭据' : '留空自动生成'} />
                </Form.Item>
                <Button
                  onClick={() =>
                    form.setFieldValue('ss_psk', randomBase64(ss2022KeyLen(form.getFieldValue('method')) || 16))
                  }
                >
                  随机
                </Button>
              </Space.Compact>
            </Form.Item>
          )}
          {type === 'snell' && (
            <Form.Item label="PSK" className="fm-full">
              <Space.Compact>
                <Form.Item name="snell_psk" noStyle>
                  <Input className="mono" placeholder={inbound ? '留空保持当前凭据' : '留空自动生成'} />
                </Form.Item>
                <Button onClick={() => form.setFieldValue('snell_psk', randomHex(16))}>生成</Button>
              </Space.Compact>
            </Form.Item>
          )}
          {(type === 'socks' || type === 'mixed' || type === 'http') && !multiUser && (
            <>
              <Form.Item
                name="username"
                label="用户名"
                dependencies={['password']}
                rules={[
                  ({ getFieldValue }) => ({
                    validator(_, value) {
                      if (!getFieldValue('password') || value?.trim()) return Promise.resolve()
                      return Promise.reject(new Error('填写密码时必须同时填写用户名'))
                    },
                  }),
                ]}
              >
                <Input autoComplete="off" />
              </Form.Item>
              <Form.Item
                name="password"
                label="密码"
                dependencies={['username']}
                rules={[
                  ({ getFieldValue }) => ({
                    validator(_, value) {
                      if (!getFieldValue('username')?.trim() || value) return Promise.resolve()
                      return Promise.reject(new Error('填写用户名时必须同时填写密码'))
                    },
                  }),
                ]}
              >
                <Input.Password autoComplete="new-password" />
              </Form.Item>
            </>
          )}

          {type === 'vless' && (
            <Form.Item name="flow" label="Flow" className="fm-full">
              <Select
                options={[
                  { value: '', label: '无' },
                  { value: 'xtls-rprx-vision', label: 'xtls-rprx-vision' },
                ]}
              />
            </Form.Item>
          )}
          {type === 'vmess' && (
            <>
              <Form.Item name="vmess_security" label="客户端加密方式">
                <Select options={VMESS_SECURITIES.map((value) => ({ value, label: value }))} />
              </Form.Item>
              <Form.Item name="vmess_alter_id" label="Alter ID">
                <InputNumber min={0} precision={0} />
              </Form.Item>
            </>
          )}
          {HAS_BANDWIDTH.has(type) && (
            <>
              <Form.Item name="up_mbps" label="上行 Mbps">
                <InputNumber precision={0} min={0} />
              </Form.Item>
              <Form.Item name="down_mbps" label="下行 Mbps">
                <InputNumber precision={0} min={0} />
              </Form.Item>
              <Form.Item name="obfs_type" label="混淆类型" className={obfsType ? undefined : 'fm-full'}>
                <Select options={[
                  { value: '', label: '无' },
                  { value: 'salamander', label: 'salamander' },
                  { value: 'gecko', label: 'gecko' },
                ]} />
              </Form.Item>
              {obfsType === 'salamander' && (
                <Form.Item name="obfs_password" label="混淆密码">
                  <Input.Password />
                </Form.Item>
              )}
              {obfsType === 'gecko' && (
                <>
                  <Form.Item name="obfs_password" label="混淆密码" rules={[{ required: true, message: '请填写 gecko 混淆密码' }]}>
                    <Input.Password />
                  </Form.Item>
                  <Form.Item name="gecko_min_packet_size" label="最小包大小">
                    <InputNumber min={512} precision={0} />
                  </Form.Item>
                  <Form.Item name="gecko_max_packet_size" label="最大包大小">
                    <InputNumber min={512} precision={0} />
                  </Form.Item>
                </>
              )}
            </>
          )}
          {type === 'tuic' && (
            <>
              <Form.Item name="congestion_control" label="拥塞控制" rules={[{ required: true }]} className="fm-full">
                <Select options={['cubic', 'new_reno', 'bbr'].map((v) => ({ value: v, label: v }))} />
              </Form.Item>
              <Form.Item
                name="auth_timeout"
                label="认证超时"
                rules={[
                  { required: true },
                  { validator: (_, value) => {
                    const error = validateDuration(value, false)
                    return error ? Promise.reject(new Error(error)) : Promise.resolve()
                  } },
                ]}
              >
                <Input className="mono" />
              </Form.Item>
              <Form.Item
                name="heartbeat"
                label="心跳间隔"
                rules={[
                  { required: true },
                  { validator: (_, value) => {
                    const error = validateDuration(value, false)
                    return error ? Promise.reject(new Error(error)) : Promise.resolve()
                  } },
                ]}
              >
                <Input className="mono" />
              </Form.Item>
            </>
          )}
          {type === 'snell' && (
            <>
              <Form.Item name="snell_version" label="版本">
                <Select options={[{ value: 5, label: 'v5' }, { value: 6, label: 'v6' }]} />
              </Form.Item>
              {snellVersion === 6 ? (
                <Form.Item name="snell_mode" label="模式">
                  <Select
                    allowClear
                    options={[
                      { value: '', label: '默认' },
                      { value: 'unshaped', label: 'unshaped' },
                      { value: 'unsafe-raw', label: 'unsafe-raw' },
                    ]}
                  />
                </Form.Item>
              ) : (
                <Form.Item name="snell_obfs_mode" label="混淆">
                  <Select options={[{ value: 'none', label: '无' }, { value: 'http', label: 'http' }, { value: 'tls', label: 'tls' }]} />
                </Form.Item>
              )}
            </>
          )}
          {(type === 'tuic' || HAS_BANDWIDTH.has(type)) && (
            <div className="fm-switches">
              {type === 'tuic' && (
                <Form.Item name="zero_rtt_handshake" label="0-RTT 握手" valuePropName="checked" className="fm-switch">
                  <Switch />
                </Form.Item>
              )}
              {HAS_BANDWIDTH.has(type) && (
                <Form.Item name="ignore_client_bandwidth" label="忽略客户端带宽" valuePropName="checked" className="fm-switch">
                  <Switch />
                </Form.Item>
              )}
            </div>
          )}
          {hasProtocolSchemaExtras && (
            <SchemaFieldList
              properties={protocolExtraSchemaProps}
              value={rawExtra}
              onChange={updateRawExtra}
              idPrefix="inbound-proto"
              onJsonErrorChange={handleSchemaJsonError}
            />
          )}

          {showTransport && (
            <>
              <Form.Item name="transport_type" label="传输层" className="fm-full">
                <Select options={[
                  { value: 'tcp', label: 'TCP' },
                  { value: 'ws', label: 'WebSocket' },
                  { value: 'httpupgrade', label: 'HTTPUpgrade' },
                  { value: 'grpc', label: 'gRPC' },
                  { value: 'http', label: 'HTTP' },
                  { value: 'quic', label: 'QUIC' },
                ]} />
              </Form.Item>
              {(transportType === 'ws' || transportType === 'httpupgrade') && (
                <>
                  <Form.Item name="ws_path" label="路径">
                    <Input className="mono" />
                  </Form.Item>
                  <Form.Item name="ws_host" label="Host">
                    <Input className="mono" />
                  </Form.Item>
                  {transportType === 'ws' && (Boolean(maxEarlyData) || Boolean(earlyDataHeader)) && (
                    <>
                      <Form.Item name="max_early_data" label="Early Data 长度">
                        <InputNumber min={0} precision={0} />
                      </Form.Item>
                      <Form.Item name="early_data_header" label="Early Data 头">
                        <Input className="mono" />
                      </Form.Item>
                    </>
                  )}
                </>
              )}
              {Object.keys(transportSchemaProps).length > 0 && (
                <SchemaFieldList
                  properties={transportSchemaProps}
                  value={(rawExtra.transport && typeof rawExtra.transport === 'object' ? rawExtra.transport : {}) as Record<string, unknown>}
                  onChange={updateRawTransport}
                  idPrefix="inbound-tr"
                  onJsonErrorChange={handleSchemaJsonError}
                />
              )}
            </>
          )}

          {type === 'trojan' && (
            <>
              <Form.Item
                name="trojan_fallback_server"
                label="回落服务器"
                rules={[
                  ({ getFieldValue }) => ({
                    validator(_, value) {
                      if (!getFieldValue('trojan_fallback_port') || value?.trim()) return Promise.resolve()
                      return Promise.reject(new Error('填写回落端口时必须同时填写回落服务器'))
                    },
                  }),
                ]}
              >
                <Input className="mono" />
              </Form.Item>
              <Form.Item
                name="trojan_fallback_port"
                label="回落端口"
                rules={[
                  ({ getFieldValue }) => ({
                    validator(_, value) {
                      if (!getFieldValue('trojan_fallback_server')?.trim() || value) return Promise.resolve()
                      return Promise.reject(new Error('填写回落服务器时必须同时填写回落端口'))
                    },
                  }),
                ]}
              >
                <InputNumber min={1} max={65535} precision={0} className="mono" />
              </Form.Item>
            </>
          )}

          {showTLS && (
            <>
              <Form.Item
                name="tls_mode"
                label="TLS 模式"
                className="fm-full"
                rules={[{ required: true }]}
              >
                <Segmented aria-label="TLS 模式" options={tlsModeOptions()} />
              </Form.Item>
              {tlsMode === 'reality' && (
                <>
                  <Form.Item
                    name="reality_handshake_server"
                    label="握手域名"
                    rules={[{ required: true, whitespace: true, message: '请填写伪装目标域名' }]}
                  >
                    <Input className="mono" />
                  </Form.Item>
                  <Form.Item name="reality_handshake_port" label="握手端口">
                    <InputNumber precision={0} min={1} max={65535} className="mono" />
                  </Form.Item>
                  <Form.Item name="tls_server_name" label="SNI">
                    <Input className="mono" />
                  </Form.Item>
                  <Form.Item name="reality_max_time_difference" label="时间差限制" rules={[
                    { validator: (_, value) => {
                      const error = validateDuration(value, true)
                      return error ? Promise.reject(new Error(error)) : Promise.resolve()
                    } },
                  ]}>
                    <Input className="mono" />
                  </Form.Item>
                </>
              )}
              {tlsMode === 'acme' && (
                <>
                  <Form.Item
                    name="acme_domain"
                    label="ACME 域名"
                    rules={[{ required: true, whitespace: true, message: '请填写 ACME 域名' }]}
                  >
                    <Input className="mono" />
                  </Form.Item>
                  <Form.Item name="acme_email" label="ACME 邮箱">
                    <Input />
                  </Form.Item>
                  <Form.Item name="tls_alpn" label="ALPN" className="fm-full">
                    <Input className="mono" />
                  </Form.Item>
                </>
              )}
              {tlsMode === 'tls' && (
                <>
                  <Form.Item
                    name="tls_cert_path"
                    label="证书路径"
                    rules={hasInlineCert ? undefined : [{ required: true, whitespace: true, message: '请填写证书路径' }]}
                  >
                    <Input className="mono" />
                  </Form.Item>
                  <Form.Item
                    name="tls_key_path"
                    label="私钥路径"
                    rules={hasInlineCert ? undefined : [{ required: true, whitespace: true, message: '请填写私钥路径' }]}
                  >
                    <Input className="mono" />
                  </Form.Item>
                  <Form.Item name="tls_server_name" label="SNI">
                    <Input className="mono" />
                  </Form.Item>
                  <Form.Item name="tls_alpn" label="ALPN">
                    <Input className="mono" />
                  </Form.Item>
                  <div className="fm-switches">
                    <Form.Item name="tls_insecure" label="客户端跳过证书校验" valuePropName="checked" className="fm-switch">
                      <Switch />
                    </Form.Item>
                  </div>
                </>
              )}
              {tlsMode === 'self' && (
                <>
                  <Form.Item name="tls_server_name" label="SNI">
                    <Input className="mono" />
                  </Form.Item>
                  <Form.Item name="tls_alpn" label="ALPN">
                    <Input className="mono" />
                  </Form.Item>
                </>
              )}
            </>
          )}

          {showTLSFingerprint && (
            <Form.Item name="tls_fingerprint" label="订阅 uTLS 指纹" className="fm-full">
              <Select
                allowClear
                options={['chrome', 'firefox', 'safari', 'ios', 'random', 'randomized'].map((v) => ({ value: v, label: v }))}
              />
            </Form.Item>
          )}
          {(type === 'vless' || type === 'anytls') && (
            <div className="fm-switches">
              {type === 'vless' && (
                <Form.Item
                  name="packet_encoding"
                  label="订阅 UDP xudp"
                  valuePropName="checked"
                  className="fm-switch"
                  getValueFromEvent={(checked: boolean) => (checked ? 'xudp' : 'none')}
                  getValueProps={(value: string) => ({ checked: !value || value === 'xudp' })}
                >
                  <Switch />
                </Form.Item>
              )}
              {type === 'anytls' && (
                <Form.Item name="anytls_udp_over_stream" label="订阅 UDP over Stream" valuePropName="checked" className="fm-switch">
                  <Switch />
                </Form.Item>
              )}
            </div>
          )}

          {Object.keys(tlsAdvSchemaProps).length > 0 && (
            <SchemaFieldList
              properties={tlsAdvSchemaProps}
              value={(rawExtra.tls && typeof rawExtra.tls === 'object' ? rawExtra.tls : {}) as Record<string, unknown>}
              onChange={updateRawTls}
              idPrefix="inbound-tls"
              onJsonErrorChange={handleSchemaJsonError}
            />
          )}
          {Object.keys(otherParamsSchemaProps).length > 0 && (
            <SchemaFieldList
              properties={otherParamsSchemaProps}
              value={rawExtra}
              onChange={updateRawExtra}
              idPrefix="inbound-other"
              onJsonErrorChange={handleSchemaJsonError}
            />
          )}
        </div>
      </Form>
      )}
      </fieldset>
    </Modal>
  )
}

export function FormGroup({ title, extra, panel, children }: {
  title: ReactNode
  extra?: ReactNode
  panel?: 'inbound' | 'outbound'
  children: ReactNode
}) {
  if (title === '基本' || title === '动作') {
    return <section className="fm-group fm-group-basic">
      <h3 className="fm-group-title">{title}{extra && <small>{extra}</small>}</h3>
      <div className="fm-grid" data-panel={panel}>{children}</div>
    </section>
  }
  return <section className="fm-group fm-group-open">
    <h3 className="fm-group-title">{title}{extra && <small>{extra}</small>}</h3>
    <div className="fm-grid" data-panel={panel}>{children}</div>
  </section>
}

// Validation failed: bring the first invalid field into view inside the
// scrolling modal body.
export function scrollToFirstError(form: FormInstance, err: unknown) {
  const name = (err as { errorFields?: { name: (string | number)[] }[] })?.errorFields?.[0]?.name
  if (name) {
    // Fields remain mounted inside collapsed sections. Reveal them before
    // scrolling, including validation errors that have not painted yet.
    document.querySelectorAll<HTMLDetailsElement>('details.fm-group').forEach((group) => { group.open = true })
    form.scrollToField(name, { block: 'center', behavior: 'smooth' })
  }
}
