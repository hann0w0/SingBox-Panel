import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Plus, X } from 'lucide-react'
import { Alert, Button, Form, Input, InputNumber, Modal, Segmented, Select, Space, Switch } from '../../../components/kit'
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
import { OutboundImport, type NodeImportMode } from './OutboundImport'
import { Pills } from '../../../components/kit/Page'
import { EditorModeToggle } from '../../../components/EditorModeToggle'
import { JsonCodeEditor } from '../../../components/JsonCodeEditor'
import { SchemaFieldList } from '../../../components/SchemaFormFields'
import {
  cloneJson,
  pickPopulatedSchemaProps,
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
const SCHEMA_OUT_PRIMARY_PROTO_KEYS = new Set(['version', 'up_mbps', 'down_mbps'])
const SCHEMA_OUT_TLS_STANDARD_KEYS = new Set(['enabled', 'server_name', 'insecure', 'alpn', 'utls', 'reality'])
const SCHEMA_OUT_TOP_EXCLUDES = new Set([
  'type', 'tag', 'server', 'server_port', 'uuid', 'username', 'password', 'psk',
  'method', 'flow', 'security', 'alter_id', 'congestion_control',
  'zero_rtt_handshake', 'obfs', 'reuse', 'obfs_mode', 'obfs_host', 'mode',
  'tls', 'transport',
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
  const [rawExtra, setRawExtra] = useState<Record<string, unknown>>({})
  const schemas = useSingboxSchemas(open)
  const submitLock = useRef(false)
  const submitSessionRef = useRef(0)
  const type: OutboundType = Form.useWatch('type', form) ?? 'shadowsocks'
  const tlsMode = Form.useWatch('tls_mode', form) ?? 'none'
  const transportType = Form.useWatch('transport_type', form) ?? 'tcp'
  const snellVersion = Form.useWatch('snell_version', form) ?? 4
  const snellObfsMode = Form.useWatch('snell_obfs_mode', form) ?? 'none'
  const hysteriaObfsType = Form.useWatch('obfs_type', form) ?? ''

  const variantProps = useMemo(
    () => resolveVariantProperties(schemas.outbound, type),
    [schemas.outbound, type],
  )
  const protocolExtraSchemaProps = useMemo(() => {
    const out: Record<string, JSONSchema> = {}
    for (const [k, v] of Object.entries(variantProps)) {
      if (SCHEMA_OUT_TOP_EXCLUDES.has(k)) continue
      if (type !== 'vless' && k === 'packet_encoding') continue
      if (SCHEMA_OUT_PRIMARY_PROTO_KEYS.has(k)) out[k] = v
    }
    return out
  }, [variantProps, type])
  const otherParamsSchemaProps = useMemo(() => {
    const candidates: Record<string, JSONSchema> = {}
    for (const [k, v] of Object.entries(variantProps)) {
      if (SCHEMA_OUT_TOP_EXCLUDES.has(k)) continue
      if (type === 'vless' && k === 'packet_encoding') continue
      if (SCHEMA_OUT_PRIMARY_PROTO_KEYS.has(k)) continue
      candidates[k] = k === 'multiplex' ? compactOutboundMultiplexSchema(v) : v
    }
    return pickPopulatedSchemaProps(candidates, rawExtra)
  }, [variantProps, type, rawExtra])
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
      return pickPopulatedSchemaProps(candidates, trRaw, new Set(['service_name']))
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
      return pickPopulatedSchemaProps(candidates, trRaw, new Set(['host', 'path']))
    }
    return {}
  }, [variantProps, transportType, rawExtra.transport])
  const tlsAdvSchemaProps = useMemo(() => {
    const tlsProps = variantProps.tls?.properties ?? {}
    const candidates: Record<string, JSONSchema> = {}
    for (const [k, v] of Object.entries(tlsProps)) {
      if (!SCHEMA_OUT_TLS_STANDARD_KEYS.has(k)) candidates[k] = v
    }
    return pickPopulatedSchemaProps(candidates, (rawExtra.tls && typeof rawExtra.tls === 'object' ? rawExtra.tls : {}) as Record<string, unknown>)
  }, [variantProps, rawExtra.tls])

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
    form.resetFields()
    form.setFieldsValue(outboundToForm(outbound))
    setRawExtra(cloneJson((outbound?.settings?.settings?.raw ?? {}) as Record<string, unknown>))
  }, [open, outbound, form])

  const submit = async () => {
    if (!open || submitLock.current) return
    submitLock.current = true
    const session = submitSessionRef.current
    setSubmitting(true)
    try {
      const v = await form.validateFields().catch((err) => {
        scrollToFirstError(form, err)
        throw err
      })
      if (session !== submitSessionRef.current) return
      const body = buildOutboundBody(outbound, v as Record<string, any> & { type: OutboundType }, rawExtra)
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
          message="手动保存后将转为固定配置，不再跟随源节点。"
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
                  <Select options={[{ value: '', label: '无' }, { value: 'salamander', label: 'salamander' }, { value: 'gecko', label: 'gecko' }]} />
                </Form.Item>
                <Form.Item name="obfs_password" label="混淆密码">
                  <Input.Password />
                </Form.Item>
                {hysteriaObfsType === 'gecko' && (
                  <>
                    <Form.Item name="gecko_min_packet_size" label="最小包大小"><InputNumber min={512} precision={0} /></Form.Item>
                    <Form.Item name="gecko_max_packet_size" label="最大包大小"><InputNumber min={512} precision={0} /></Form.Item>
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
          </FormGroup>
        )}
        {(Object.keys(otherParamsSchemaProps).length > 0 || Object.keys(tlsAdvSchemaProps).length > 0) && (
          <FormGroup title="其他参数">
            {Object.keys(tlsAdvSchemaProps).length > 0 && (
              <div className="fm-full">
                <SchemaFieldList
                  properties={tlsAdvSchemaProps}
                  value={(rawExtra.tls && typeof rawExtra.tls === 'object' ? rawExtra.tls : {}) as Record<string, unknown>}
                  onChange={(nextTls) => setRawExtra((prev) => ({ ...prev, tls: nextTls }))}
                  idPrefix="outbound-tls"
                />
              </div>
            )}
            {Object.keys(otherParamsSchemaProps).length > 0 && (
              <div className="fm-full">
                <SchemaFieldList
                  properties={otherParamsSchemaProps}
                  value={rawExtra}
                  onChange={setRawExtra}
                  idPrefix="outbound-other"
                />
              </div>
            )}
          </FormGroup>
        )}
      </Form>
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

// 动作类型选项（官方 sing-box 参数 + 中文释义）
const ACTION_OPTIONS = [
  { value: 'route', label: 'route · 路由分流' },
  { value: 'reject', label: 'reject · 拒绝连接' },
  { value: 'sniff', label: 'sniff · 协议嗅探' },
  { value: 'hijack-dns', label: 'hijack-dns · 劫持 DNS' },
  { value: 'resolve', label: 'resolve · 解析域名' },
  { value: 'route-options', label: 'route-options · 路由选项' },
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
  'domain_regex',
  'ip_cidr',
  'source_ip_cidr',
  'ip_is_private',
  'port',
  'port_range',
  'protocol',
  'network',
  'sniffer',
  'method',
  'server',
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

type MatchField =
  | 'domain_suffix'
  | 'domain'
  | 'domain_keyword'
  | 'domain_regex'
  | 'rule_set'
  | 'ip_cidr'
  | 'source_ip_cidr'
  | 'port'
  | 'port_range'
  | 'ip_is_private'
  | 'inbound'
  | 'auth_user'
  | 'protocol'
  | 'network'

type ConditionRow = {
  id: string
  field: MatchField
  text: string
  list: string[]
  single: string
  ruleSetSource: boolean
}

const MATCH_FIELD_OPTIONS: { value: MatchField; label: string }[] = [
  { value: 'domain_suffix', label: 'domain_suffix · 域名后缀' },
  { value: 'domain', label: 'domain · 完整域名' },
  { value: 'domain_keyword', label: 'domain_keyword · 域名关键词' },
  { value: 'domain_regex', label: 'domain_regex · 域名正则' },
  { value: 'rule_set', label: 'rule_set · 规则集' },
  { value: 'ip_cidr', label: 'ip_cidr · 目标 IP / CIDR' },
  { value: 'source_ip_cidr', label: 'source_ip_cidr · 来源 IP / CIDR' },
  { value: 'port', label: 'port · 目标端口' },
  { value: 'port_range', label: 'port_range · 目标端口范围' },
  { value: 'ip_is_private', label: 'ip_is_private · 目标私有 IP' },
  { value: 'inbound', label: 'inbound · 入站标签' },
  { value: 'auth_user', label: 'auth_user · 认证用户' },
  { value: 'protocol', label: 'protocol · 嗅探协议' },
  { value: 'network', label: 'network · 传输层网络' },
]

function emptyConditionRow(field: MatchField, id: string): ConditionRow {
  return { id, field, text: '', list: [], single: '', ruleSetSource: false }
}

function matchToConditionRows(match: RuleMatch, act: string, nextId: () => string): ConditionRow[] {
  const rows: ConditionRow[] = []
  if (match.domain_suffix?.length) rows.push({ ...emptyConditionRow('domain_suffix', nextId()), text: joinLines(match.domain_suffix) })
  if (match.domain?.length) rows.push({ ...emptyConditionRow('domain', nextId()), text: joinLines(match.domain) })
  if (match.domain_keyword?.length) rows.push({ ...emptyConditionRow('domain_keyword', nextId()), text: joinLines(match.domain_keyword) })
  if (match.domain_regex?.length) rows.push({ ...emptyConditionRow('domain_regex', nextId()), text: joinLines(match.domain_regex) })
  if (match.rule_set?.length) rows.push({ ...emptyConditionRow('rule_set', nextId()), list: [...match.rule_set], ruleSetSource: !!match.rule_set_ip_cidr_match_source })
  if (match.ip_cidr?.length) rows.push({ ...emptyConditionRow('ip_cidr', nextId()), text: joinLines(match.ip_cidr) })
  if (match.source_ip_cidr?.length) rows.push({ ...emptyConditionRow('source_ip_cidr', nextId()), text: joinLines(match.source_ip_cidr) })
  if (match.port?.length) rows.push({ ...emptyConditionRow('port', nextId()), text: joinLines(match.port.map(String)) })
  if (match.port_range?.length) rows.push({ ...emptyConditionRow('port_range', nextId()), text: joinLines(match.port_range) })
  if (match.ip_is_private) rows.push(emptyConditionRow('ip_is_private', nextId()))
  if ((act === 'route' || act === 'reject' || act === 'route-options') && match.inbound?.length) {
    rows.push({ ...emptyConditionRow('inbound', nextId()), list: [...match.inbound] })
  }
  if (match.auth_user?.length) rows.push({ ...emptyConditionRow('auth_user', nextId()), list: [...match.auth_user] })
  if (act !== 'sniff' && act !== 'hijack-dns' && match.protocol?.length) {
    rows.push({ ...emptyConditionRow('protocol', nextId()), list: [...match.protocol] })
  }
  if (match.network) rows.push({ ...emptyConditionRow('network', nextId()), single: String(match.network) })
  return rows
}

function validateConditionRows(rows: ConditionRow[]): string | null {
  for (const row of rows) {
    if (row.field === 'port') {
      const ports = splitLines(row.text)
      if (ports.some((p) => !/^\d+$/.test(p) || Number(p) < 1 || Number(p) > 65535)) {
        return '目标端口必须是 1-65535 的整数'
      }
    }
    if (row.field === 'port_range') {
      const ranges = splitLines(row.text)
      if (ranges.some((r) => !/^\d+:\d+$/.test(r) && !/^\d+$/.test(r))) {
        return '端口范围格式应为 start:end，例如 1000:2000'
      }
    }
  }
  return null
}

function ruleToFormValues(m: Record<string, unknown>, outbound?: string) {
  const act = inferRuleAction(m, outbound)
  const match = m as RuleMatch
  return {
    action: act,
    outbound: act === 'route' ? (outbound || undefined) : undefined,
    sniffer: match.sniffer || (act === 'sniff' ? match.protocol : undefined) || [],
    method: (match.method as string) || 'default',
    inbound: match.inbound || [],
    server: match.server || '',
  }
}

function buildRulePayload(
  v: Record<string, any>,
  rows: ConditionRow[],
  extraMatch: Record<string, unknown>,
  schemaProps: Record<string, JSONSchema>,
): { match: RuleMatch; outbound: string } {
  const act = String(v.action || 'route') as NonNullable<RuleMatch['action']>
  const match: RuleMatch = { action: act }
  if ((act === 'sniff' || act === 'hijack-dns' || act === 'resolve') && v.inbound?.length) {
    match.inbound = v.inbound
  }
  if (act === 'resolve' && typeof v.server === 'string' && v.server.trim()) {
    match.server = v.server.trim()
  }
  if (act === 'route' || act === 'reject' || act === 'route-options') {
    for (const row of rows) {
      if (row.field === 'auth_user' && row.list.length) match.auth_user = row.list
      else if (row.field === 'inbound' && row.list.length) match.inbound = row.list
      else if (row.field === 'rule_set' && row.list.length) {
        match.rule_set = row.list
        if (row.ruleSetSource) match.rule_set_ip_cidr_match_source = true
      }
      else if (row.field === 'protocol' && row.list.length) match.protocol = row.list
      else if (row.field === 'network' && row.single) match.network = row.single
      else if (row.field === 'ip_is_private') match.ip_is_private = true
      else if (row.field === 'domain') {
        const items = splitLines(row.text)
        if (items.length) match.domain = items
      } else if (row.field === 'domain_suffix') {
        const items = splitLines(row.text)
        if (items.length) match.domain_suffix = items
      } else if (row.field === 'domain_keyword') {
        const items = splitLines(row.text)
        if (items.length) match.domain_keyword = items
      } else if (row.field === 'domain_regex') {
        const items = splitLines(row.text)
        if (items.length) match.domain_regex = items
      } else if (row.field === 'ip_cidr') {
        const items = splitLines(row.text)
        if (items.length) match.ip_cidr = items
      } else if (row.field === 'source_ip_cidr') {
        const items = splitLines(row.text)
        if (items.length) match.source_ip_cidr = items
      } else if (row.field === 'port') {
        const ports = splitLines(row.text).map((x) => Number(x))
        if (ports.length) match.port = ports
      } else if (row.field === 'port_range') {
        const items = splitLines(row.text)
        if (items.length) match.port_range = items
      }
    }
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
  const [conditions, setConditions] = useState<ConditionRow[]>([])
  const [conditionError, setConditionError] = useState('')
  const [extraMatch, setExtraMatch] = useState<Record<string, unknown>>({})
  const [jsonText, setJsonText] = useState('')
  const [jsonError, setJsonError] = useState('')
  const schemas = useSingboxSchemas(open)
  const submitLock = useRef(false)
  const submitSessionRef = useRef(0)
  const rowSeqRef = useRef(0)
  const nextRowId = () => `cond-${++rowSeqRef.current}`
  const action = Form.useWatch('action', form) || 'route'
  const showConditions = action === 'route' || action === 'reject' || action === 'route-options'

  const schemaProps = useMemo(() => schemas.route_rule.properties ?? {}, [schemas.route_rule])
  const advancedSchemaProps = useMemo(() => {
    const candidates: Record<string, JSONSchema> = {}
    for (const [k, v] of Object.entries(schemaProps)) {
      if (STANDARD_RULE_MATCH_KEYS.has(k)) continue
      const actions = v['x-ui']?.actions
      if (actions && !actions.includes(action)) continue
      candidates[k] = v
    }
    const alwaysInclude = action === 'resolve' ? new Set(['strategy']) : undefined
    return pickPopulatedSchemaProps(candidates, extraMatch, alwaysInclude)
  }, [schemaProps, action, extraMatch])
  const hasAdvancedSchemaProps = Object.keys(advancedSchemaProps).length > 0

  const usedFields = useMemo(() => new Set(conditions.map((c) => c.field)), [conditions])
  const nextAvailableField = MATCH_FIELD_OPTIONS.find((opt) => !usedFields.has(opt.value))?.value

  const addConditionRow = () => {
    if (!nextAvailableField) return
    setConditionError('')
    setConditions((prev) => [...prev, emptyConditionRow(nextAvailableField, nextRowId())])
  }

  const updateConditionRow = (id: string, patch: Partial<ConditionRow>) => {
    setConditionError('')
    setConditions((prev) => prev.map((row) => (row.id === id ? { ...row, ...patch } : row)))
  }

  const removeConditionRow = (id: string) => {
    setConditionError('')
    setConditions((prev) => prev.filter((row) => row.id !== id))
  }

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
    setConditionError('')
    if (rule) {
      const m = (rule.match || {}) as Record<string, unknown>
      const act = inferRuleAction(m, rule.outbound)
      form.setFieldsValue(ruleToFormValues(m, rule.outbound))
      setConditions(matchToConditionRows(m as RuleMatch, act, nextRowId))
      setExtraMatch(cloneMatchExtra(m))
      setJsonText(JSON.stringify(ruleToNativeJson(rule.match || {}, rule.outbound), null, 2))
    } else {
      form.setFieldsValue(ruleToFormValues({}, undefined))
      setConditions([emptyConditionRow('domain_suffix', nextRowId())])
      setExtraMatch({})
      setJsonText('')
    }
  }, [open, rule, form])

  const switchEditorMode = (nextMode: string) => {
    if (nextMode !== 'form' && nextMode !== 'json') return
    if (nextMode === editorMode) return
    setJsonError('')
    setConditionError('')
    if (nextMode === 'json') {
      const v = form.getFieldsValue()
      const { match, outbound } = buildRulePayload(v, conditions, extraMatch, schemaProps)
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
      const act = inferRuleAction(parsed, outboundStr)
      form.setFieldsValue(ruleToFormValues(parsed, outboundStr))
      setConditions(matchToConditionRows(parsed as RuleMatch, act, nextRowId))
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
        setConditionError('')
        const v = await form.validateFields().catch((err) => {
          scrollToFirstError(form, err)
          throw err
        })
        if (showConditions) {
          const condErr = validateConditionRows(conditions)
          if (condErr) {
            setConditionError(condErr)
            return
          }
        }
        if (session !== submitSessionRef.current) return
        const built = buildRulePayload(v, conditions, extraMatch, schemaProps)
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

  const renderConditionValue = (row: ConditionRow) => {
    if (row.field === 'auth_user') {
      return (
        <Select
          mode="multiple"
          allowClear
          aria-label="用户"
          value={row.list}
          onChange={(list: string[]) => updateConditionRow(row.id, { list })}
          options={users.map((user) => ({ value: user.auth_user, label: user.name }))}
        />
      )
    }
    if (row.field === 'inbound') {
      return (
        <Select
          mode="multiple"
          allowClear
          aria-label="入站来源"
          value={row.list}
          onChange={(list: string[]) => updateConditionRow(row.id, { list })}
          options={inboundTags.map((t) => ({ value: t, label: t }))}
        />
      )
    }
    if (row.field === 'rule_set') {
      return (
        <div className="space-y-1.5">
          <Select
            mode="multiple"
            allowClear
            aria-label="规则集"
            value={row.list}
            onChange={(list: string[]) => updateConditionRow(row.id, { list })}
            options={(ruleSets || []).map((rs) => ({ label: rs.tag, value: rs.tag }))}
          />
          <div className="flex items-center justify-between gap-2 pt-0.5">
            <span className="text-xs text-muted-foreground">规则集 IP 方向</span>
            <Segmented
              aria-label="规则集 IP 匹配方向"
              value={row.ruleSetSource ? 'source' : 'target'}
              onChange={(val) => updateConditionRow(row.id, { ruleSetSource: val === 'source' })}
              options={[
                { value: 'target', label: '目标 IP' },
                { value: 'source', label: '来源 IP' },
              ]}
            />
          </div>
        </div>
      )
    }
    if (row.field === 'protocol') {
      return (
        <Select
          mode="tags"
          allowClear
          aria-label="应用协议"
          value={row.list}
          onChange={(list: string[]) => updateConditionRow(row.id, { list })}
          options={[
            { value: 'dns', label: 'DNS' },
            { value: 'http', label: 'HTTP' },
            { value: 'tls', label: 'TLS' },
            { value: 'quic', label: 'QUIC' },
            { value: 'bittorrent', label: 'BitTorrent' },
            { value: 'stun', label: 'STUN' },
          ]}
        />
      )
    }
    if (row.field === 'network') {
      return (
        <Select
          allowClear
          aria-label="网络类型"
          value={row.single}
          onChange={(single: string) => updateConditionRow(row.id, { single: single || '' })}
          options={[
            { value: 'tcp', label: 'TCP' },
            { value: 'udp', label: 'UDP' },
            { value: 'icmp', label: 'ICMP' },
          ]}
        />
      )
    }
    if (row.field === 'ip_is_private') {
      return (
        <div className="flex h-9 items-center rounded-[7px] border border-[var(--inbound-line-strong)] bg-[var(--inbound-field)] px-3 text-[13px] text-muted-foreground">
          true · 匹配私有 / 局域网 IP
        </div>
      )
    }
    const label = MATCH_FIELD_OPTIONS.find((opt) => opt.value === row.field)?.label || row.field
    const placeholderMap: Partial<Record<MatchField, string>> = {
      domain_suffix: 'openai.com, anthropic.com',
      domain: 'api.openai.com',
      domain_keyword: 'google',
      domain_regex: '^api\\..*',
      ip_cidr: '10.0.0.0/8',
      source_ip_cidr: '192.168.1.0/24',
      port: '80, 443',
      port_range: '1000:2000',
    }
    return (
      <Input
        aria-label={label}
        value={row.text}
        placeholder={placeholderMap[row.field]}
        onChange={(e) => updateConditionRow(row.id, { text: e.target.value })}
        className="mono"
      />
    )
  }

  return (
    <Modal
      className="fm-modal inbound-compact"
      title={<span className="inbound-compact-heading">{rule ? '编辑路由规则' : '新增路由规则'}</span>}
      open={open}
      onOk={submit}
      onCancel={onClose}
      confirmLoading={submitting}
      okText={rule ? '保存' : '创建'}
      cancelText="取消"
      width={680}
    >
      <fieldset disabled={submitting} aria-busy={submitting} className="inbound-compact-fieldset">
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
      <Form form={form} layout="vertical" className="fm-form inbound-compact-form">
        <FormGroup title="基本" panel="inbound">
          <Form.Item name="action" label="动作" rules={[{ required: true, message: '请选择规则动作' }]}>
            <Select aria-label="规则动作" options={ACTION_OPTIONS} />
          </Form.Item>
          {action === 'route' && (
            <Form.Item name="outbound" label="目标出站" rules={[{ required: true, message: '请选择目标出站' }]}>
              <Select
                allowClear
                aria-label="目标出站"
                options={[
                  { value: 'direct', label: 'direct' },
                  ...outboundTags.filter((t) => t !== 'direct').map((t) => ({ value: t, label: t })),
                ]}
              />
            </Form.Item>
          )}
          {action === 'reject' && (
            <Form.Item name="method" label="拒绝方式">
              <Select
                aria-label="拒绝方式"
                options={[
                  { value: 'default', label: 'default · 拒绝响应' },
                  { value: 'drop', label: 'drop · 静默丢弃' },
                ]}
              />
            </Form.Item>
          )}
          {action === 'sniff' && (
            <Form.Item name="sniffer" label="嗅探协议">
              <Select
                mode="multiple"
                allowClear
                aria-label="嗅探协议"
                options={[
                  { value: 'tls', label: 'TLS' },
                  { value: 'http', label: 'HTTP' },
                  { value: 'quic', label: 'QUIC' },
                  { value: 'dns', label: 'DNS' },
                ]}
              />
            </Form.Item>
          )}
          {action === 'resolve' && (
            <Form.Item name="server" label="DNS 服务器">
              <Input aria-label="DNS 服务器" className="mono" />
            </Form.Item>
          )}
          {(action === 'sniff' || action === 'hijack-dns' || action === 'resolve') && (
            <Form.Item name="inbound" label="限定入站" className="fm-full">
              <Select
                mode="multiple"
                allowClear
                aria-label="限定入站"
                options={inboundTags.map((t) => ({ value: t, label: t }))}
              />
            </Form.Item>
          )}
        </FormGroup>

        {showConditions && (
          <FormGroup
            title="匹配条件"
            panel="inbound"
            extra={nextAvailableField ? (
              <Button size="small" type="text" icon={<Plus className="size-3.5" />} onClick={addConditionRow}>
                添加条件
              </Button>
            ) : undefined}
          >
            <div className="fm-full space-y-2.5 pb-1">
              {conditionError && <Alert type="error" showIcon message={conditionError} />}
              {conditions.length === 0 ? (
                <div className="rounded-[7px] border border-dashed border-[var(--inbound-line-strong)] px-3 py-2.5 text-[13px] text-muted-foreground">
                  未设置条件，将匹配全部流量
                </div>
              ) : (
                conditions.map((row) => (
                  <div key={row.id} className="flex items-start gap-2">
                    <div className="w-[228px] max-w-[48%] shrink-0">
                      <Select
                        aria-label="条件字段"
                        value={row.field}
                        onChange={(nextField: MatchField) => {
                          updateConditionRow(row.id, {
                            field: nextField,
                            text: '',
                            list: [],
                            single: '',
                            ruleSetSource: false,
                          })
                        }}
                        options={MATCH_FIELD_OPTIONS.map((opt) => ({
                          ...opt,
                          disabled: opt.value !== row.field && usedFields.has(opt.value),
                        }))}
                      />
                    </div>
                    <div className="min-w-0 flex-1">{renderConditionValue(row)}</div>
                    <Button
                      type="text"
                      size="small"
                      aria-label={`移除${row.field}`}
                      className="h-9 w-9 shrink-0 px-0 text-muted-foreground hover:text-foreground"
                      icon={<X className="size-4" />}
                      onClick={() => removeConditionRow(row.id)}
                    />
                  </div>
                ))
              )}
            </div>
          </FormGroup>
        )}

        {hasAdvancedSchemaProps && (
          <FormGroup title="其他参数" panel="inbound">
            <div className="fm-full">
              <SchemaFieldList
                properties={advancedSchemaProps}
                value={extraMatch}
                onChange={setExtraMatch}
                idPrefix="rule-adv"
              />
            </div>
          </FormGroup>
        )}
      </Form>
      )}
      </fieldset>
    </Modal>
  )
}
