import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Alert, Button, Checkbox, Form, Input, InputNumber, Modal, Segmented, Select, Space, Switch } from '../../../components/kit'
import {
  createOutbound,
  createRule,
  listSharedRuleSets,
  updateOutbound,
  updateRule,
} from '../../../lib/api'
import { applyWithToast } from '../../../lib/configApply'
import type { Outbound, OutboundType, RouteRule, RuleMatch, RuleSet } from '../../../lib/types'
import { randomBase64, randomHex, randomUUID, SHADOWSOCKS_METHODS, ss2022KeyLen } from '../../../lib/util'
import { buildOutboundBody, TLS_REQUIRED_OUT, TRANSPORT_OUT, transportHost } from '../../../lib/outboundForm'
import { FormGroup, scrollToFirstError } from './InboundForm'
import '../../../styles/forms.css'
import { MODAL_MD } from '../../../components/kit/modal'
import { OutboundImport, type NodeImportMode } from './OutboundImport'
import { Pills } from '../../../components/kit/Page'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { EditorModeToggle } from '../../../components/EditorModeToggle'
import { JsonCodeEditor } from '../../../components/JsonCodeEditor'
import { SchemaFieldList } from '../../../components/SchemaFormFields'
import {
  cloneJson,
  nativeJsonToOutboundSettings,
  outboundSettingsToNativeJson,
  resolveVariantProperties,
  useSingboxSchemas,
  validateObjectBySchema,
  type JSONSchema,
} from '../../../lib/singboxSchema'

const OUT_TYPES: { value: OutboundType; label: string }[] = [
  { value: 'shadowsocks', label: 'ss' },
  { value: 'snell', label: 'snell' },
  { value: 'socks', label: 'socks' },
  { value: 'http', label: 'http' },
  { value: 'tuic', label: 'tuic' },
  { value: 'trojan', label: 'trojan' },
  { value: 'anytls', label: 'anytls' },
  { value: 'vless', label: 'vless' },
  { value: 'vmess', label: 'vmess' },
  { value: 'hysteria2', label: 'hysteria2' },
]

type OutboundSourceMode = 'manual' | NodeImportMode

const UUID_OUT = new Set<OutboundType>(['vless', 'vmess', 'tuic'])
const PW_OUT = new Set<OutboundType>(['trojan', 'hysteria2', 'tuic', 'anytls'])
const TUIC_CC = ['cubic', 'new_reno', 'bbr']
const EXTENDED_OUT_TRANSPORTS = new Set(['ws', 'httpupgrade', 'grpc', 'http', 'quic'])
const SCHEMA_OUT_OTHER_KEYS = new Set([
  'detour', 'connect_timeout', 'domain_resolver', 'tcp_fast_open', 'bind_interface',
  'multiplex', 'plugin', 'plugin_opts', 'network', 'udp_over_tcp',
  'path', 'idle_session_check_interval', 'idle_session_timeout', 'min_idle_session',
  'server_ports', 'hop_interval', 'heartbeat', 'udp_over_stream',
])
const SCHEMA_OUT_TLS_ADV_KEYS = new Set(['min_version', 'max_version'])
const SCHEMA_OUT_PROTO_EXCLUDES = new Set([
  'type', 'tag', 'server', 'server_port', 'uuid', 'username', 'password', 'psk',
  'method', 'flow', 'packet_encoding', 'security', 'alter_id', 'congestion_control',
  'udp_relay_mode', 'zero_rtt_handshake', 'up_mbps', 'down_mbps', 'obfs',
  'version', 'reuse', 'obfs_mode', 'obfs_host', 'mode', 'tls', 'transport',
  'headers', 'global_padding', 'authenticated_length',
  'routing_mark', 'inet4_bind_address', 'inet6_bind_address', 'tcp_multi_path', 'udp_fragment',
  ...SCHEMA_OUT_OTHER_KEYS,
])

function compactOutboundMultiplexSchema(schema: JSONSchema): JSONSchema {
  if (!schema.properties) return schema
  const { enabled, protocol, padding } = schema.properties
  return {
    ...schema,
    properties: {
      ...(enabled ? { enabled } : {}),
      ...(protocol ? { protocol } : {}),
      ...(padding ? { padding } : {}),
    },
  }
}

function outboundToForm(outbound: Outbound | null) {
  if (!outbound) {
    return {
      type: 'shadowsocks' as OutboundType,
      method: '2022-blake3-aes-128-gcm',
      server_port: undefined,
      tls: false,
      tls_mode: 'none',
      tls_alpn: '',
      tls_fingerprint: 'chrome',
      reality_server_name: '',
      reality_public_key: '',
      reality_short_id: '',
      flow: '',
      packet_encoding: 'xudp',
      vmess_security: 'auto',
      vmess_alter_id: 0,
      congestion_control: 'cubic',
      tuic_udp_relay_mode: 'native',
      zero_rtt_handshake: false,
      snell_version: 4,
      obfs_type: '',
      gecko_min_packet_size: undefined,
      gecko_max_packet_size: undefined,
      snell_reuse: false,
      snell_network: 'tcp',
      snell_obfs_mode: 'none',
      snell_obfs_host: '',
      snell_mode: '',
      transport_type: 'tcp',
    }
  }
  const s = outbound.settings || {}
  const inner = s.settings || {}
  return {
    tag: outbound.tag,
    type: outbound.type,
    server: s.server,
    server_port: s.server_port,
    username: s.username ?? inner.username,
    uuid: s.uuid,
    password: s.password ?? (outbound.type === 'snell' ? inner.snell_psk : (outbound.type === 'socks' || outbound.type === 'http') ? inner.password : undefined),
    method: inner.method ?? '2022-blake3-aes-128-gcm',
    ss_psk: inner.ss_server_psk ?? s.password,
    tls: TLS_REQUIRED_OUT.has(outbound.type) || !!inner.tls?.enabled,
    sni: inner.tls?.server_name,
    insecure: inner.tls?.insecure,
    flow: inner.flow ?? '',
    packet_encoding: inner.packet_encoding ?? 'xudp',
    tls_mode: inner.tls?.reality?.enabled ? 'reality' : (inner.tls?.enabled ? 'tls' : 'none'),
    tls_alpn: inner.tls?.alpn?.join(', '),
    tls_fingerprint: inner.tls?.fingerprint ?? 'chrome',
    reality_server_name: inner.tls?.server_name || '',
    reality_public_key: inner.tls?.reality?.public_key || '',
    reality_short_id: inner.tls?.reality?.short_id?.join(', '),
    vmess_security: inner.vmess_security ?? 'auto',
    vmess_alter_id: inner.vmess_alter_id ?? 0,
    congestion_control: inner.congestion_control ?? 'cubic',
    tuic_udp_relay_mode: (inner.raw?.udp_relay_mode as string) ?? inner.tuic_udp_relay_mode ?? 'native',
    zero_rtt_handshake: !!inner.zero_rtt_handshake,
    obfs_type: inner.obfs_type ?? (inner.obfs_password ? 'salamander' : ''),
    obfs_password: inner.obfs_password,
    gecko_min_packet_size: inner.gecko_min_packet_size,
    gecko_max_packet_size: inner.gecko_max_packet_size,
    snell_version: inner.snell_version === 6 ? 6 : 4,
    snell_reuse: !!inner.snell_reuse,
    snell_network: inner.snell_network ?? 'tcp',
    snell_obfs_mode: inner.snell_obfs_mode ?? 'none',
    snell_obfs_host: inner.snell_obfs_host ?? '',
    snell_mode: inner.snell_mode ?? '',
    transport_type: inner.transport?.type && EXTENDED_OUT_TRANSPORTS.has(inner.transport.type) ? inner.transport.type : 'tcp',
    ws_path: inner.transport?.path,
    ws_host: transportHost(inner.transport?.headers),
  }
}

// ---------------- Outbound 出站 ----------------

export function OutboundForm({
  serverId,
  outbound,
  open,
  onClose,
  onSaved,
}: {
  serverId: number
  outbound: Outbound | null
  open: boolean
  onClose: () => void
  onSaved: () => void
}) {
  const [form] = Form.useForm()
  const importFormId = useId()
  const [importReady, setImportReady] = useState(false)
  const [source, setSource] = useState<OutboundSourceMode>('manual')
  const [submitting, setSubmitting] = useState(false)
  const [editorMode, setEditorMode] = useState<'form' | 'json'>('form')
  const [rawExtra, setRawExtra] = useState<Record<string, unknown>>({})
  const [jsonText, setJsonText] = useState('')
  const [jsonError, setJsonError] = useState('')
  const schemas = useSingboxSchemas(open)
  const submitLock = useRef(false)
  const submitSessionRef = useRef(0)
  const type: OutboundType = Form.useWatch('type', form) ?? 'shadowsocks'
  const tlsMode = Form.useWatch('tls_mode', form) ?? 'none'
  const transportType = Form.useWatch('transport_type', form) ?? 'tcp'
  const snellVersion = Form.useWatch('snell_version', form) ?? 4
  const snellObfsMode = Form.useWatch('snell_obfs_mode', form) ?? 'none'
  const hysteriaObfsType = Form.useWatch('obfs_type', form) ?? ''
  const tlsChecked = !!Form.useWatch('tls', form)

  const variantProps = useMemo(
    () => resolveVariantProperties(schemas.outbound, type),
    [schemas.outbound, type],
  )
  const otherParamsSchemaProps = useMemo(() => {
    if (type === 'snell') return {}
    const out: Record<string, JSONSchema> = {}
    for (const [k, v] of Object.entries(variantProps)) {
      if (SCHEMA_OUT_OTHER_KEYS.has(k)) {
        out[k] = k === 'multiplex' ? compactOutboundMultiplexSchema(v) : v
      }
    }
    return out
  }, [variantProps, type])
  const protocolExtraSchemaProps = useMemo(() => {
    const out: Record<string, JSONSchema> = {}
    for (const [k, v] of Object.entries(variantProps)) {
      if (!SCHEMA_OUT_PROTO_EXCLUDES.has(k)) out[k] = v
    }
    if (type === 'socks' && variantProps.version) out.version = variantProps.version
    return out
  }, [variantProps, type])
  const transportSchemaProps = useMemo(() => {
    const trProps = variantProps.transport?.properties ?? {}
    if (transportType === 'grpc') {
      return {
        ...(trProps.service_name ? { service_name: trProps.service_name } : {}),
        ...(trProps.idle_timeout ? { idle_timeout: trProps.idle_timeout } : {}),
        ...(trProps.ping_timeout ? { ping_timeout: trProps.ping_timeout } : {}),
        ...(trProps.permit_without_stream ? { permit_without_stream: trProps.permit_without_stream } : {}),
      }
    }
    if (transportType === 'http') {
      return {
        ...(trProps.method ? { method: trProps.method } : {}),
        ...(trProps.host ? { host: trProps.host } : {}),
        ...(trProps.path ? { path: trProps.path } : {}),
        ...(trProps.headers ? { headers: trProps.headers } : {}),
        ...(trProps.idle_timeout ? { idle_timeout: trProps.idle_timeout } : {}),
        ...(trProps.ping_timeout ? { ping_timeout: trProps.ping_timeout } : {}),
      }
    }
    return {}
  }, [variantProps, transportType])
  const tlsAdvSchemaProps = useMemo(() => {
    const tlsProps = variantProps.tls?.properties ?? {}
    const out: Record<string, JSONSchema> = {}
    for (const [k, v] of Object.entries(tlsProps)) {
      if (SCHEMA_OUT_TLS_ADV_KEYS.has(k)) out[k] = v
    }
    return out
  }, [variantProps])

  useLayoutEffect(() => {
    submitSessionRef.current += 1
    submitLock.current = false
    setSubmitting(false)
    return () => { submitSessionRef.current += 1 }
  }, [open, serverId, outbound?.id])

  useEffect(() => {
    if (!open) return
    setSource('manual')
    setImportReady(false)
    setEditorMode('form')
    setJsonError('')
    form.resetFields()
    form.setFieldsValue(outboundToForm(outbound))
    setRawExtra(cloneJson((outbound?.settings?.settings?.raw ?? {}) as Record<string, unknown>))
    setJsonText(
      outbound
        ? JSON.stringify(outboundSettingsToNativeJson(outbound.type, outbound.tag, outbound.settings || {}), null, 2)
        : '',
    )
  }, [open, outbound, form])

  const switchEditorMode = (nextMode: string) => {
    if (nextMode !== 'form' && nextMode !== 'json') return
    if (nextMode === editorMode) return
    setJsonError('')
    if (nextMode === 'json') {
      const v = form.getFieldsValue()
      const activeType = (v.type || type) as OutboundType
      const built = buildOutboundBody(outbound, { ...v, type: activeType }, rawExtra)
      setJsonText(JSON.stringify(outboundSettingsToNativeJson(activeType, String(built.tag || ''), built.settings), null, 2))
      setEditorMode('json')
      return
    }
    try {
      const parsed = JSON.parse(jsonText || '{}') as Record<string, unknown>
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        setJsonError('配置必须是 JSON 对象')
        return
      }
      const schemaErr = validateObjectBySchema(schemas.outbound, parsed)
      if (schemaErr) {
        setJsonError(schemaErr)
        return
      }
      const nextType = (typeof parsed.type === 'string' && OUT_TYPES.some((t) => t.value === parsed.type) ? parsed.type : type) as OutboundType
      const nextSettings = nativeJsonToOutboundSettings(outbound?.settings || {}, parsed)
      const nextTag = typeof parsed.tag === 'string' ? parsed.tag : ''
      setRawExtra(cloneJson((nextSettings.settings?.raw ?? {}) as Record<string, unknown>))
      form.setFieldsValue(
        outboundToForm({
          ...(outbound ?? { id: 0, server_id: serverId, remark: '', sort: 0 }),
          tag: nextTag,
          type: nextType,
          settings: nextSettings,
        }),
      )
      setEditorMode('form')
    } catch (err) {
      setJsonError(err instanceof Error ? `JSON 解析失败: ${err.message}` : 'JSON 格式错误')
    }
  }

  const submit = async () => {
    if (!open || submitLock.current) return
    submitLock.current = true
    const session = submitSessionRef.current
    setSubmitting(true)
    try {
      let body: { tag: string; type: OutboundType; settings: Outbound['settings'] }
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
        const schemaErr = validateObjectBySchema(schemas.outbound, parsed)
        if (schemaErr) {
          setJsonError(schemaErr)
          return
        }
        const nextType = (typeof parsed.type === 'string' && OUT_TYPES.some((t) => t.value === parsed.type) ? parsed.type : type) as OutboundType
        body = {
          tag: typeof parsed.tag === 'string' ? parsed.tag.trim() : '',
          type: nextType,
          settings: nativeJsonToOutboundSettings(outbound?.settings || {}, parsed),
        }
      } else {
        const v = await form.validateFields().catch((err) => {
          scrollToFirstError(form, err)
          throw err
        })
        if (session !== submitSessionRef.current) return
        body = buildOutboundBody(outbound, v as Record<string, any> & { type: OutboundType }, rawExtra)
      }
      const editing = outbound
      const saved = await applyWithToast(`outbound-${editing ? editing.id : 'new'}`, () =>
        editing ? updateOutbound(serverId, editing.id, body) : createOutbound(serverId, body),
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

  const showGenericTLS = type !== 'shadowsocks' && type !== 'socks' && type !== 'vless' && type !== 'snell'
  const hasProtocolSchemaExtras = Object.keys(protocolExtraSchemaProps).length > 0
  const hasParams = type === 'vless' || type === 'vmess' || type === 'tuic' || type === 'hysteria2' || type === 'snell' || hasProtocolSchemaExtras

  return (
    <Modal
      className="fm-modal inbound-compact"
      title={outbound ? '编辑出站' : '新增出站'}
      open={open}
      onOk={submit}
      onCancel={() => { if (!submitting) onClose() }}
      closable={!submitting}
      maskClosable={!submitting}
      keyboard={!submitting}
      footer={!outbound && source !== 'manual' ? <>
        <Button disabled={submitting} onClick={onClose}>取消</Button>
        <Button type="primary" htmlType="submit" form={importFormId} disabled={!importReady} loading={submitting}>保存出站</Button>
      </> : undefined}
      confirmLoading={submitting}
      width={680}
    >
      {outbound?.source_kind && (
        <Alert
          type="info"
          showIcon
          className="fm-edit-follow-hint"
          message={
            outbound.source_kind === 'inbound'
              ? '该出站由面板节点导入，会跟随源节点自动更新；在此手动保存后将改为固定配置，不再跟随。'
              : '该出站由外部节点导入；在此手动保存后将改为固定配置，不再跟随。'
          }
        />
      )}
      {!outbound && <fieldset className="fm-source-picker" disabled={submitting}>
        <Pills
          label="出站来源"
          active={source}
          items={[
            { key: 'manual', label: '手动配置' },
            { key: 'panel', label: '面板节点' },
            { key: 'external', label: '外部节点' },
            { key: 'uri', label: 'URI 导入' },
          ]}
          onChange={(value) => { setImportReady(false); setSource(value as OutboundSourceMode) }}
        />
      </fieldset>}
      {!outbound && source !== 'manual' ? (
        <OutboundImport key={`${serverId}-${source}`} serverId={serverId} mode={source} onClose={onClose} onSaved={onSaved} onSavingChange={setSubmitting} formId={importFormId} onReadyChange={setImportReady} />
      ) : <fieldset disabled={submitting} aria-busy={submitting} className="min-w-0 border-0 p-0 m-0">
      <div className="mb-4 flex items-center justify-between gap-3">
        <EditorModeToggle
          ariaLabel="出站编辑模式"
          value={editorMode}
          onChange={switchEditorMode}
          disabled={submitting}
        />
      </div>
      {editorMode === 'json' ? (
        <div className="space-y-3">
          {jsonError && <Alert type="error" showIcon message={jsonError} />}
          <JsonCodeEditor
            ariaLabel="outbound.json"
            value={jsonText}
            disabled={submitting}
            onChange={(val) => {
              setJsonText(val)
              if (jsonError) setJsonError('')
            }}
          />
        </div>
      ) : (
      <Form form={form} layout="vertical" className="fm-form">
        <FormGroup title="基本">
          <Form.Item name="tag" label="标签" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="type" label="类型" rules={[{ required: true }]}>
            <Select
              options={OUT_TYPES}
              onChange={(next: OutboundType) => {
                if (TLS_REQUIRED_OUT.has(next)) form.setFieldValue('tls', true)
              }}
            />
          </Form.Item>
          <Form.Item name="server" label="服务器地址" rules={[{ required: true }]}>
            <Input className="mono" />
          </Form.Item>
          <Form.Item name="server_port" label="端口" rules={[{ required: true, message: '请填写落地服务器的端口' }]}>
            <InputNumber precision={0} min={1} max={65535} className="mono" />
          </Form.Item>
        </FormGroup>

        <FormGroup title="认证">
          {type === 'shadowsocks' && (
            <>
              <Form.Item name="method" label="加密方式" rules={[{ required: true }]} className="fm-full">
                <Select options={SHADOWSOCKS_METHODS.map((m) => ({ value: m, label: m }))} />
              </Form.Item>
              <Form.Item label="密码 / PSK" required className="fm-full">
                <Space.Compact>
                  <Form.Item name="ss_psk" noStyle rules={[{ required: true, message: '必填' }]}>
                    <Input className="mono" />
                  </Form.Item>
                  <Button onClick={() => form.setFieldValue('ss_psk', randomBase64(ss2022KeyLen(form.getFieldValue('method')) || 16))}>随机</Button>
                </Space.Compact>
              </Form.Item>
            </>
          )}

          {UUID_OUT.has(type) && (
            <Form.Item label="UUID" required className="fm-full">
              <Space.Compact>
                <Form.Item name="uuid" noStyle rules={[{ required: true, message: '必填' }]}>
                  <Input className="mono" />
                </Form.Item>
                <Button onClick={() => form.setFieldValue('uuid', randomUUID())}>随机</Button>
              </Space.Compact>
            </Form.Item>
          )}
          {PW_OUT.has(type) && (
            <Form.Item label="密码" required className="fm-full">
              <Space.Compact>
                <Form.Item name="password" noStyle rules={[{ required: true, message: '必填' }]}>
                  <Input className="mono" />
                </Form.Item>
                <Button onClick={() => form.setFieldValue('password', randomHex(16))}>随机</Button>
              </Space.Compact>
            </Form.Item>
          )}
          {type === 'snell' && (
            <Form.Item label="PSK" required className="fm-full">
              <Space.Compact>
                <Form.Item
                  name="password"
                  noStyle
                  dependencies={['snell_version']}
                  rules={[{ required: true, message: '必填' }, ({ getFieldValue }) => ({
                    validator(_, value) {
                      const bytes = value ? new TextEncoder().encode(String(value)).length : 0
                      if (getFieldValue('snell_version') === 6 && bytes > 0 && (bytes < 12 || bytes > 255)) {
                        return Promise.reject(new Error('Snell v6 的 PSK 长度必须在 12-255 字节之间'))
                      }
                      return Promise.resolve()
                    },
                  })]}
                >
                  <Input className="mono" />
                </Form.Item>
                <Button onClick={() => form.setFieldValue('password', randomHex(16))}>随机</Button>
              </Space.Compact>
            </Form.Item>
          )}
          {(type === 'socks' || type === 'http') && (
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
                <Input placeholder="可选" autoComplete="off" />
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
                <Input.Password placeholder="可选" autoComplete="new-password" />
              </Form.Item>
            </>
          )}
        </FormGroup>

        {hasParams && (
          <FormGroup title="协议参数">
            {type === 'vless' && (
              <>
                <Form.Item name="flow" label="Flow">
                  <Select
                    options={[
                      { value: '', label: '无' },
                      { value: 'xtls-rprx-vision', label: 'xtls-rprx-vision' },
                    ]}
                  />
                </Form.Item>
                <Form.Item name="packet_encoding" label="UDP 封包编码">
                  <Select
                    options={[
                      { value: 'xudp', label: 'xudp' },
                      { value: 'packetaddr', label: 'packetaddr' },
                      { value: 'none', label: '不启用' },
                    ]}
                  />
                </Form.Item>
              </>
            )}

            {type === 'vmess' && (
              <>
                <Form.Item name="vmess_security" label="客户端加密" rules={[{ required: true }]}>
                  <Select options={['auto', 'none', 'zero', 'aes-128-gcm', 'chacha20-poly1305', 'aes-128-cfb'].map((m) => ({ value: m, label: m }))} />
                </Form.Item>
                <Form.Item name="vmess_alter_id" label="Alter ID" rules={[{ required: true }]}>
                  <InputNumber min={0} precision={0} />
                </Form.Item>
              </>
            )}

            {type === 'tuic' && (
              <Form.Item name="congestion_control" label="拥塞控制" rules={[{ required: true }]} className="fm-full">
                <Select options={TUIC_CC.map((v) => ({ value: v, label: v }))} />
              </Form.Item>
            )}

            {type === 'hysteria2' && (
              <>
                <Form.Item name="obfs_type" label="混淆类型">
                  <Select placeholder="无" options={[{ value: '', label: '无' }, { value: 'salamander', label: 'salamander' }, { value: 'gecko', label: 'gecko' }]} />
                </Form.Item>
                <Form.Item name="obfs_password" label="混淆密码">
                  <Input.Password />
                </Form.Item>
                {hysteriaObfsType === 'gecko' && (
                  <>
                    <Form.Item name="gecko_min_packet_size" label="最小包大小"><InputNumber min={512} precision={0} placeholder="默认" /></Form.Item>
                    <Form.Item name="gecko_max_packet_size" label="最大包大小"><InputNumber min={512} precision={0} placeholder="默认" /></Form.Item>
                  </>
                )}
              </>
            )}

            {type === 'snell' && (
              <>
                <Form.Item name="snell_version" label="版本" rules={[{ required: true }]}>
                  <Select options={[{ value: 4, label: 'v4' }, { value: 6, label: 'v6' }]} />
                </Form.Item>
                <Form.Item name="snell_network" label="网络">
                  <Select options={[
                    { value: 'tcp', label: 'TCP' },
                    { value: 'udp', label: 'UDP' },
                  ]} />
                </Form.Item>
                {snellVersion === 4 ? (
                  <>
                    <Form.Item name="snell_obfs_mode" label="混淆" className={snellObfsMode === 'http' || snellObfsMode === 'tls' ? undefined : 'fm-full'}>
                      <Select options={[{ value: 'none', label: 'none' }, { value: 'http', label: 'http' }, { value: 'tls', label: 'tls' }]} />
                    </Form.Item>
                    {(snellObfsMode === 'http' || snellObfsMode === 'tls') && (
                      <Form.Item name="snell_obfs_host" label="混淆 Host">
                        <Input className="mono" />
                      </Form.Item>
                    )}
                  </>
                ) : (
                  <Form.Item name="snell_mode" label="模式" className="fm-full">
                    <Select options={[
                      { value: '', label: 'default' },
                      { value: 'unshaped', label: 'unshaped' },
                      { value: 'unsafe-raw', label: 'unsafe-raw' },
                    ]} />
                  </Form.Item>
                )}
                <div className="fm-switches">
                  <Form.Item name="snell_reuse" label="连接复用" valuePropName="checked" className="fm-switch">
                    <Switch />
                  </Form.Item>
                </div>
              </>
            )}
            {hasProtocolSchemaExtras && (
              <div className="fm-full">
                <SchemaFieldList
                  properties={protocolExtraSchemaProps}
                  value={rawExtra}
                  onChange={setRawExtra}
                  idPrefix="outbound-proto"
                />
              </div>
            )}
          </FormGroup>
        )}

        {TRANSPORT_OUT.has(type) && (
          <FormGroup title="传输">
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
              </>
            )}
            {Object.keys(transportSchemaProps).length > 0 && (
              <div className="fm-full">
                <SchemaFieldList
                  properties={transportSchemaProps}
                  value={(rawExtra.transport && typeof rawExtra.transport === 'object' ? rawExtra.transport : {}) as Record<string, unknown>}
                  onChange={(nextTr) => setRawExtra((prev) => ({ ...prev, transport: nextTr }))}
                  idPrefix="outbound-tr"
                />
              </div>
            )}
          </FormGroup>
        )}

        {type === 'vless' && (
          <FormGroup title="TLS">
            <Form.Item name="tls_mode" label="模式" className={tlsMode === 'none' ? 'fm-full' : undefined}>
              <Select
                options={[
                  { value: 'none', label: '不使用 TLS' },
                  { value: 'tls', label: 'TLS' },
                  { value: 'reality', label: 'REALITY' },
                ]}
              />
            </Form.Item>

            {tlsMode === 'reality' && (
              <>
                <Form.Item name="reality_server_name" label="SNI" rules={[{ required: true, message: 'REALITY 需要伪装域名' }]}>
                  <Input className="mono" />
                </Form.Item>
                <Form.Item name="reality_public_key" label="公钥" rules={[{ required: true, message: '请输入 REALITY 公钥' }]} className="fm-full">
                  <Input className="mono" />
                </Form.Item>
                <Form.Item name="reality_short_id" label="Short ID">
                  <Input className="mono" />
                </Form.Item>
                <Form.Item name="tls_alpn" label="ALPN">
                  <Input className="mono" />
                </Form.Item>
                <Form.Item name="tls_fingerprint" label="uTLS 指纹">
                  <Select options={['chrome', 'firefox', 'safari', 'ios', 'random', 'randomized'].map((v) => ({ value: v, label: v }))} />
                </Form.Item>
              </>
            )}

            {tlsMode === 'tls' && (
              <>
                <Form.Item name="sni" label="SNI">
                  <Input className="mono" />
                </Form.Item>
                <Form.Item name="tls_alpn" label="ALPN">
                  <Input className="mono" />
                </Form.Item>
                <Form.Item name="tls_fingerprint" label="uTLS 指纹">
                  <Select options={['chrome', 'firefox', 'safari', 'ios', 'random', 'randomized'].map((v) => ({ value: v, label: v }))} />
                </Form.Item>
                <div className="fm-switches">
                  <Form.Item name="insecure" label="跳过证书校验" valuePropName="checked" className="fm-switch">
                    <Switch />
                  </Form.Item>
                </div>
              </>
            )}
            {tlsMode && tlsMode !== 'none' && Object.keys(tlsAdvSchemaProps).length > 0 && (
              <div className="fm-full">
                <SchemaFieldList
                  properties={tlsAdvSchemaProps}
                  value={(rawExtra.tls && typeof rawExtra.tls === 'object' ? rawExtra.tls : {}) as Record<string, unknown>}
                  onChange={(nextTls) => setRawExtra((prev) => ({ ...prev, tls: nextTls }))}
                  idPrefix="outbound-tls"
                />
              </div>
            )}
          </FormGroup>
        )}

        {showGenericTLS && (
          <FormGroup title="TLS">
            <Form.Item name="sni" label="SNI">
              <Input className="mono" />
            </Form.Item>
            <Form.Item name="tls_alpn" label="ALPN">
              <Input className="mono" />
            </Form.Item>
            <div className="fm-switches">
              <Form.Item name="tls" label="启用 TLS" valuePropName="checked" className="fm-switch">
                <Switch disabled={TLS_REQUIRED_OUT.has(type)} />
              </Form.Item>
              <Form.Item name="insecure" label="跳过证书校验" valuePropName="checked" className="fm-switch">
                <Switch />
              </Form.Item>
            </div>
            {(TLS_REQUIRED_OUT.has(type) || tlsChecked) && Object.keys(tlsAdvSchemaProps).length > 0 && (
              <div className="fm-full">
                <SchemaFieldList
                  properties={tlsAdvSchemaProps}
                  value={(rawExtra.tls && typeof rawExtra.tls === 'object' ? rawExtra.tls : {}) as Record<string, unknown>}
                  onChange={(nextTls) => setRawExtra((prev) => ({ ...prev, tls: nextTls }))}
                  idPrefix="outbound-tls-generic"
                />
              </div>
            )}
          </FormGroup>
        )}
        {Object.keys(otherParamsSchemaProps).length > 0 && (
          <FormGroup title="其他参数">
            <div className="fm-full">
              <SchemaFieldList
                properties={otherParamsSchemaProps}
                value={rawExtra}
                onChange={setRawExtra}
                idPrefix="outbound-other"
              />
            </div>
          </FormGroup>
        )}
      </Form>
      )}
      </fieldset>}
    </Modal>
  )
}


// ---------------- Route rule (分流规则) ----------------

// splitLines 将 "a, b\nc" 拆分为 ["a","b","c"]（去首尾空白、过滤空值）
function splitLines(s: string | undefined): string[] {
  if (!s) return []
  return s
    .split(/[\n,]/)
    .map((x) => x.trim())
    .filter(Boolean)
}
const joinLines = (a?: string[]) => (a || []).join(', ')

// 动作类型选项
const ACTION_OPTIONS = [
  { value: 'route', label: '分流' },
  { value: 'reject', label: '拒绝' },
  { value: 'sniff', label: '嗅探' },
  { value: 'hijack-dns', label: 'DNS 劫持' },
  { value: 'resolve', label: 'DNS 解析' },
  { value: 'route-options', label: '路由选项' },
]

const STANDARD_RULE_MATCH_KEYS = new Set([
  'action',
  'outbound',
  'auth_user',
  'inbound',
  'rule_set',
  'rule_set_ip_cidr_match_source',
  'domain',
  'domain_suffix',
  'domain_keyword',
  'ip_cidr',
  'source_ip_cidr',
  'port',
  'protocol',
  'network',
  'sniffer',
  'method',
])

function cloneMatchExtra(m: Record<string, unknown> | undefined): Record<string, unknown> {
  if (!m) return {}
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(m)) {
    if (STANDARD_RULE_MATCH_KEYS.has(k) || v === undefined || v === null) continue
    out[k] = structuredClone(v)
  }
  return out
}

function inferRuleAction(m: Record<string, unknown>, outbound?: string): string {
  const act = typeof m.action === 'string' ? m.action : ''
  if (act) return act
  if (outbound === 'block' || outbound === 'reject') return 'reject'
  if (outbound === 'sniff') return 'sniff'
  if (outbound === 'hijack-dns') return 'hijack-dns'
  if (outbound === 'resolve') return 'resolve'
  if (outbound === 'route-options') return 'route-options'
  return 'route'
}

function ruleToFormValues(m: Record<string, unknown>, outbound?: string) {
  const act = inferRuleAction(m, outbound)
  const match = m as RuleMatch
  return {
    action: act,
    outbound: act === 'route' ? (outbound || undefined) : undefined,
    sniffer: match.sniffer || (act === 'sniff' ? match.protocol : undefined) || [],
    protocol: act === 'hijack-dns' ? (match.protocol?.length ? match.protocol : ['dns']) : (act !== 'sniff' ? (match.protocol || []) : []),
    method: (match.method as string) || 'default',
    match_fields: activeMatchFields(match),
    auth_user: match.auth_user || [],
    rule_set: match.rule_set || [],
    rule_set_ip_direction: match.rule_set_ip_cidr_match_source ? 'source' : 'target',
    inbound: match.inbound || [],
    domain: joinLines(match.domain),
    domain_suffix: joinLines(match.domain_suffix),
    domain_keyword: joinLines(match.domain_keyword),
    ip_cidr: joinLines(match.ip_cidr),
    source_ip_cidr: joinLines(match.source_ip_cidr),
    port: joinLines(match.port?.map(String)),
    network: match.network || '',
  }
}

function buildRulePayload(
  v: Record<string, any>,
  extraMatch: Record<string, unknown>,
  schemaProps: Record<string, JSONSchema>,
): { match: RuleMatch; outbound: string } {
  const act = String(v.action || 'route') as NonNullable<RuleMatch['action']>
  const match: RuleMatch = { action: act }
  const selectedFields = new Set<MatchField>(v.match_fields || [])
  if (selectedFields.has('auth_user') && v.auth_user?.length) match.auth_user = v.auth_user
  if ((act === 'sniff' || act === 'hijack-dns' || act === 'resolve' || act === 'route-options' || selectedFields.has('inbound')) && v.inbound?.length) {
    match.inbound = v.inbound
  }
  if (act === 'route' || act === 'reject' || act === 'route-options') {
    if (selectedFields.has('rule_set') && v.rule_set?.length) {
      match.rule_set = v.rule_set
      if (v.rule_set_ip_direction === 'source') match.rule_set_ip_cidr_match_source = true
    }
    if (selectedFields.has('protocol') && v.protocol?.length) match.protocol = v.protocol
    if (selectedFields.has('domain')) {
      const domains = splitLines(v.domain)
      if (domains.length) match.domain = domains
    }
    if (selectedFields.has('domain_suffix')) {
      const domains = splitLines(v.domain_suffix)
      if (domains.length) match.domain_suffix = domains
    }
    if (selectedFields.has('domain_keyword')) {
      const keywords = splitLines(v.domain_keyword)
      if (keywords.length) match.domain_keyword = keywords
    }
    if (selectedFields.has('ip_cidr')) {
      const cidrs = splitLines(v.ip_cidr)
      if (cidrs.length) match.ip_cidr = cidrs
    }
    if (selectedFields.has('source_ip_cidr')) {
      const cidrs = splitLines(v.source_ip_cidr)
      if (cidrs.length) match.source_ip_cidr = cidrs
    }
    if (selectedFields.has('port')) {
      const ports = splitLines(v.port).map((x) => Number(x))
      if (ports.length) match.port = ports
    }
    if (selectedFields.has('network') && v.network) match.network = v.network
  }

  for (const [k, val] of Object.entries(extraMatch)) {
    if (STANDARD_RULE_MATCH_KEYS.has(k) || val === undefined || val === null || val === '') continue
    if (Array.isArray(val) && val.length === 0) continue
    const prop = schemaProps[k]
    const allowedActions = prop?.['x-ui']?.actions
    if (allowedActions && !allowedActions.includes(act)) continue
    match[k] = structuredClone(val)
  }

  let targetOutbound = String(v.outbound || 'direct')
  if (act === 'sniff') {
    targetOutbound = 'sniff'
    if (v.sniffer?.length) match.sniffer = v.sniffer
  } else if (act === 'reject') {
    targetOutbound = 'block'
    match.method = v.method || 'default'
  } else if (act === 'hijack-dns') {
    targetOutbound = 'hijack-dns'
    match.protocol = ['dns']
  } else if (act === 'resolve') {
    targetOutbound = 'resolve'
  } else if (act === 'route-options') {
    targetOutbound = 'route-options'
  }
  return { match, outbound: targetOutbound }
}

function ruleToNativeJson(match: RuleMatch, outbound: string): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(match)) {
    if (v === undefined || v === null) continue
    out[k] = structuredClone(v)
  }
  const act = String(match.action || 'route')
  if (act === 'route' && outbound) {
    out.outbound = outbound
  } else {
    delete out.outbound
  }
  if (!out.action) out.action = act
  return out
}

function nativeJsonToRulePayload(parsed: Record<string, unknown>): { match: RuleMatch; outbound: string } {
  const outboundRaw = typeof parsed.outbound === 'string' ? parsed.outbound : ''
  const act = inferRuleAction(parsed, outboundRaw) as NonNullable<RuleMatch['action']>
  const match: RuleMatch = { action: act }
  for (const [k, v] of Object.entries(parsed)) {
    if (k === 'outbound' || v === undefined || v === null) continue
    match[k] = structuredClone(v)
  }
  let targetOutbound = outboundRaw || 'direct'
  if (act === 'sniff') targetOutbound = 'sniff'
  else if (act === 'reject') targetOutbound = 'block'
  else if (act === 'hijack-dns') targetOutbound = 'hijack-dns'
  else if (act === 'resolve') targetOutbound = 'resolve'
  else if (act === 'route-options') targetOutbound = 'route-options'
  return { match, outbound: targetOutbound }
}

type MatchField =
  | 'auth_user'
  | 'inbound'
  | 'rule_set'
  | 'domain'
  | 'domain_suffix'
  | 'domain_keyword'
  | 'ip_cidr'
  | 'source_ip_cidr'
  | 'port'
  | 'protocol'
  | 'network'

const MATCH_FIELD_OPTIONS: { value: MatchField; label: string }[] = [
  { value: 'auth_user', label: '用户' },
  { value: 'inbound', label: '入站来源' },
  { value: 'rule_set', label: '规则集' },
  { value: 'domain', label: '完整域名' },
  { value: 'domain_suffix', label: '域名后缀' },
  { value: 'domain_keyword', label: '域名关键词' },
  { value: 'ip_cidr', label: '目标 IP / CIDR' },
  { value: 'source_ip_cidr', label: '来源 IP / CIDR' },
  { value: 'port', label: '目标端口' },
  { value: 'protocol', label: '应用协议' },
  { value: 'network', label: '网络类型' },
]

function activeMatchFields(match: RuleMatch): MatchField[] {
  const fields: MatchField[] = []
  if (match.auth_user?.length) fields.push('auth_user')
  if (match.inbound?.length) fields.push('inbound')
  if (match.rule_set?.length) fields.push('rule_set')
  if (match.domain?.length) fields.push('domain')
  if (match.domain_suffix?.length) fields.push('domain_suffix')
  if (match.domain_keyword?.length) fields.push('domain_keyword')
  if (match.ip_cidr?.length) fields.push('ip_cidr')
  if (match.source_ip_cidr?.length) fields.push('source_ip_cidr')
  if (match.port?.length) fields.push('port')
  if (match.protocol?.length) fields.push('protocol')
  if (match.network) fields.push('network')
  return fields
}

export function RuleForm({
  serverId,
  rule,
  outboundTags,
  inboundTags,
  users = [],
  ruleSets: assignedRuleSets,
  open,
  onClose,
  onSaved,
}: {
  serverId: number
  rule: RouteRule | null
  outboundTags: string[]
  inboundTags: string[]
  users?: { id: number; name: string; auth_user: string }[]
  ruleSets?: RuleSet[]
  open: boolean
  onClose: () => void
  onSaved: () => void
}) {
  const [form] = Form.useForm()
  const [submitting, setSubmitting] = useState(false)
  const [ruleSets, setRuleSets] = useState<RuleSet[]>(assignedRuleSets ?? [])
  const [editorMode, setEditorMode] = useState<'form' | 'json'>('form')
  const [extraMatch, setExtraMatch] = useState<Record<string, unknown>>({})
  const [jsonText, setJsonText] = useState('')
  const [jsonError, setJsonError] = useState('')
  const schemas = useSingboxSchemas(open)
  const submitLock = useRef(false)
  const submitSessionRef = useRef(0)
  const action = Form.useWatch('action', form) || 'route'
  const matchFields = (Form.useWatch('match_fields', form) || []) as MatchField[]
  const showMatchField = (field: MatchField) => matchFields.includes(field)

  const schemaProps = useMemo(() => schemas.route_rule.properties ?? {}, [schemas.route_rule])
  const advancedSchemaProps = useMemo(() => {
    const out: Record<string, JSONSchema> = {}
    for (const [k, v] of Object.entries(schemaProps)) {
      if (STANDARD_RULE_MATCH_KEYS.has(k)) continue
      const actions = v['x-ui']?.actions
      if (actions && !actions.includes(action)) continue
      out[k] = v
    }
    return out
  }, [schemaProps, action])
  const hasAdvancedSchemaProps = Object.keys(advancedSchemaProps).length > 0

  useLayoutEffect(() => {
    submitSessionRef.current += 1
    submitLock.current = false
    setSubmitting(false)
    return () => { submitSessionRef.current += 1 }
  }, [open, serverId, rule?.id])

  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    listSharedRuleSets(controller.signal)
      .then((rows) => { if (!controller.signal.aborted) setRuleSets(rows) })
      .catch(() => {})
    return () => controller.abort()
  }, [open])

  useEffect(() => {
    if (!open) return
    form.resetFields()
    setEditorMode('form')
    setJsonError('')
    if (rule) {
      const m = (rule.match || {}) as Record<string, unknown>
      form.setFieldsValue(ruleToFormValues(m, rule.outbound))
      setExtraMatch(cloneMatchExtra(m))
      setJsonText(JSON.stringify(ruleToNativeJson(rule.match || {}, rule.outbound), null, 2))
    } else {
      form.setFieldsValue(ruleToFormValues({}, undefined))
      setExtraMatch({})
      setJsonText('')
    }
  }, [open, rule, form])

  const switchEditorMode = (nextMode: string) => {
    if (nextMode !== 'form' && nextMode !== 'json') return
    if (nextMode === editorMode) return
    setJsonError('')
    if (nextMode === 'json') {
      const v = form.getFieldsValue()
      const { match, outbound } = buildRulePayload(v, extraMatch, schemaProps)
      setJsonText(JSON.stringify(ruleToNativeJson(match, outbound), null, 2))
      setEditorMode('json')
      return
    }
    try {
      const parsed = JSON.parse(jsonText || '{}') as Record<string, unknown>
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        setJsonError('配置必须是 JSON 对象')
        return
      }
      const schemaErr = validateObjectBySchema(schemas.route_rule, parsed)
      if (schemaErr) {
        setJsonError(schemaErr)
        return
      }
      const outboundStr = typeof parsed.outbound === 'string' ? parsed.outbound : rule?.outbound
      form.setFieldsValue(ruleToFormValues(parsed, outboundStr))
      setExtraMatch(cloneMatchExtra(parsed))
      setEditorMode('form')
    } catch (err) {
      setJsonError(err instanceof Error ? `JSON 解析失败: ${err.message}` : 'JSON 格式错误')
    }
  }

  const submit = async () => {
    if (!open || submitLock.current) return
    submitLock.current = true
    const session = submitSessionRef.current
    setSubmitting(true)
    try {
      let match: RuleMatch
      let targetOutbound: string
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
        const schemaErr = validateObjectBySchema(schemas.route_rule, parsed)
        if (schemaErr) {
          setJsonError(schemaErr)
          return
        }
        const built = nativeJsonToRulePayload(parsed)
        match = built.match
        targetOutbound = built.outbound
      } else {
        const v = await form.validateFields().catch((err) => {
          scrollToFirstError(form, err)
          throw err
        })
        if (session !== submitSessionRef.current) return
        const built = buildRulePayload(v, extraMatch, schemaProps)
        match = built.match
        targetOutbound = built.outbound
      }

      // 备注与启用不再在表单里编辑：保留已有值，新建规则默认启用。
      const body = { match, outbound: targetOutbound, enabled: rule ? rule.enabled : true, remark: rule?.remark || '', sort: rule?.sort }
      const editing = rule
      const saved = await applyWithToast(`rule-${editing ? editing.id : 'new'}`, () =>
        editing ? updateRule(serverId, editing.id, body) : createRule(serverId, body),
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

  return (
    <Modal
      className="fm-modal"
      title={rule ? '编辑路由规则' : '新增路由规则'}
      open={open}
      onOk={submit}
      onCancel={onClose}
      confirmLoading={submitting}
      okText={rule ? '保存' : '创建'}
      cancelText="取消"
      width={MODAL_MD}
    >
      <fieldset disabled={submitting} aria-busy={submitting} className="min-w-0 border-0 p-0 m-0">
      <div className="mb-4 flex items-center justify-between gap-3">
        <EditorModeToggle
          ariaLabel="规则编辑模式"
          value={editorMode}
          onChange={switchEditorMode}
          disabled={submitting}
        />
      </div>
      {editorMode === 'json' ? (
        <div className="space-y-3">
          {jsonError && <Alert type="error" showIcon message={jsonError} />}
          <JsonCodeEditor
            ariaLabel="rule.json"
            value={jsonText}
            disabled={submitting}
            onChange={(val) => {
              setJsonText(val)
              if (jsonError) setJsonError('')
            }}
          />
        </div>
      ) : (
      <Form form={form} layout="vertical" fieldLayout className="fm-form">
        <FieldGroup className="gap-5">
          <Field>
            <FieldLabel htmlFor="rule-action">动作</FieldLabel>
            <Form.Item name="action" noStyle rules={[{ required: true, message: '请选择规则动作' }]}>
              <Segmented block options={ACTION_OPTIONS} className="fm-actions" aria-label="规则动作" />
            </Form.Item>
          </Field>
          {action === 'route' && (
            <Form.Item name="outbound" label="目标出站" rules={[{ required: true, message: '请选择目标出站' }]}>
              <Select
                allowClear
                placeholder="选择出站"
                options={[
                  { value: 'direct', label: 'direct' },
                  ...outboundTags.filter((t) => t !== 'direct').map((t) => ({ value: t, label: t })),
                ]}
              />
            </Form.Item>
          )}
          {action === 'reject' && (
            <Form.Item name="method" label="拒绝方式" className="fm-full">
              <Select
                options={[
                  { value: 'default', label: '快速拒绝' },
                  { value: 'drop', label: '静默丢弃' },
                ]}
              />
            </Form.Item>
          )}
          {action === 'sniff' && (
            <>
              <Form.Item name="sniffer" label="嗅探协议">
                <Select
                  mode="multiple"
                  allowClear
                  placeholder="全部协议"
                  options={[
                    { value: 'tls', label: 'TLS' },
                    { value: 'http', label: 'HTTP' },
                    { value: 'quic', label: 'QUIC' },
                    { value: 'dns', label: 'DNS' },
                  ]}
                />
              </Form.Item>
              <Form.Item name="inbound" label="限定入站">
                <Select
                  mode="multiple"
                  allowClear
                  placeholder="全部入站"
                  options={inboundTags.map((t) => ({ value: t, label: t }))}
                />
              </Form.Item>
            </>
          )}
          {(action === 'hijack-dns' || action === 'resolve' || action === 'route-options') && (
            <Form.Item name="inbound" label="限定入站" className="fm-full">
              <Select
                mode="multiple"
                allowClear
                placeholder="全部入站"
                options={inboundTags.map((t) => ({ value: t, label: t }))}
              />
            </Form.Item>
          )}
        </FieldGroup>

        {(action === 'route' || action === 'reject' || action === 'route-options') && (
          <FieldGroup className="gap-5 mt-6">
            <Field>
              <div className="flex items-baseline justify-between gap-3">
                <FieldLabel>匹配条件</FieldLabel>
                <span className="text-sm text-muted-foreground">
                  {matchFields.length ? `已选 ${matchFields.length} 项` : '匹配全部'}
                </span>
              </div>
              <Form.Item name="match_fields" noStyle>
                <Checkbox.Group className="fm-chips" options={MATCH_FIELD_OPTIONS} />
              </Form.Item>
            </Field>

            {showMatchField('auth_user') && (
              <Form.Item name="auth_user" label="用户" rules={[{ required: true, message: '请选择至少一个用户' }]} className="fm-full">
                <Select mode="multiple" allowClear placeholder="选择用户" options={users.map((user) => ({ value: user.auth_user, label: user.name }))} />
              </Form.Item>
            )}

            {showMatchField('inbound') && (
              <Form.Item name="inbound" label="入站来源" rules={[{ required: true, message: '请选择至少一个入站' }]} className="fm-full">
                <Select mode="multiple" allowClear placeholder="选择入站" options={inboundTags.map((t) => ({ value: t, label: t }))} />
              </Form.Item>
            )}

            {showMatchField('rule_set') && (
              <>
                <Form.Item name="rule_set" label="规则集" rules={[{ required: true, message: '请选择至少一个规则集' }]} className="fm-full">
                  <Select
                    mode="multiple"
                    allowClear
                    placeholder={ruleSets?.length ? '选择规则集' : '暂无规则集'}
                    options={(ruleSets || []).map((rs) => ({ label: rs.tag, value: rs.tag }))}
                  />
                </Form.Item>
                <Form.Item
                  name="rule_set_ip_direction"
                  label="规则集 IP 匹配方向"
                  extra="仅影响规则集中的 IP 网段。来源 IP 为入站看到的连接来源；开启 REALITY 前置 SNI 过滤时无法匹配真实客户端 IP。"
                  className="fm-full"
                >
                  <Segmented aria-label="规则集 IP 匹配方向" options={[
                    { value: 'target', label: '目标 IP' },
                    { value: 'source', label: '来源 IP' },
                  ]} />
                </Form.Item>
              </>
            )}

            {showMatchField('domain') && (
              <Form.Item name="domain" label="完整域名" rules={[{ required: true, message: '请输入至少一个完整域名' }]} className="fm-full">
                <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} className="mono" />
              </Form.Item>
            )}

            {showMatchField('domain_suffix') && (
              <Form.Item name="domain_suffix" label="域名后缀" rules={[{ required: true, message: '请输入至少一个域名后缀' }]} className="fm-full">
                <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} className="mono" />
              </Form.Item>
            )}

            {showMatchField('domain_keyword') && (
              <Form.Item name="domain_keyword" label="域名关键词" rules={[{ required: true, message: '请输入至少一个域名关键词' }]} className="fm-full">
                <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} className="mono" />
              </Form.Item>
            )}

            {showMatchField('ip_cidr') && (
              <Form.Item name="ip_cidr" label="目标 IP / CIDR" rules={[{ required: true, message: '请输入至少一个 IP 或 CIDR' }]} className="fm-full">
                <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} className="mono" />
              </Form.Item>
            )}

            {showMatchField('source_ip_cidr') && (
              <Form.Item name="source_ip_cidr" label="来源 IP / CIDR" rules={[{ required: true, message: '请输入至少一个来源 IP 或 CIDR' }]} className="fm-full">
                <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} className="mono" />
              </Form.Item>
            )}

            {showMatchField('port') && (
              <Form.Item
                name="port"
                label="目标端口"
                rules={[
                  { required: true, message: '请输入至少一个端口' },
                  {
                    validator: (_, value) => {
                      const ports = splitLines(value)
                      const invalid = ports.some((port) => !/^\d+$/.test(port) || Number(port) < 1 || Number(port) > 65535)
                      return invalid ? Promise.reject(new Error('端口必须是 1-65535 的整数')) : Promise.resolve()
                    },
                  },
                ]}
              >
                <Input className="mono" />
              </Form.Item>
            )}

            {showMatchField('protocol') && (
              <Form.Item name="protocol" label="应用协议" rules={[{ required: true, message: '请选择至少一个应用协议' }]}>
                <Select
                  mode="tags"
                  allowClear
                  options={[
                    { value: 'dns', label: 'DNS' },
                    { value: 'http', label: 'HTTP' },
                    { value: 'tls', label: 'TLS' },
                    { value: 'quic', label: 'QUIC' },
                    { value: 'bittorrent', label: 'BitTorrent' },
                    { value: 'stun', label: 'STUN' },
                  ]}
                />
              </Form.Item>
            )}

            {showMatchField('network') && (
              <Form.Item name="network" label="网络类型" rules={[{ required: true, message: '请选择网络类型' }]}>
                <Select
                  allowClear
                  options={[
                    { value: 'tcp', label: 'TCP' },
                    { value: 'udp', label: 'UDP' },
                    { value: 'icmp', label: 'ICMP' },
                  ]}
                />
              </Form.Item>
            )}
          </FieldGroup>
        )}
        {hasAdvancedSchemaProps && (
          <FormGroup title="高级匹配与路由参数">
            <SchemaFieldList
              properties={advancedSchemaProps}
              value={extraMatch}
              onChange={setExtraMatch}
              idPrefix="rule-adv"
            />
          </FormGroup>
        )}
      </Form>
      )}
      </fieldset>
    </Modal>
  )
}
