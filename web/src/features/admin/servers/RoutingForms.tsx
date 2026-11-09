import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { Button, Checkbox, Form, Input, InputNumber, Modal, Segmented, Select, Space, Switch } from '../../../ui'
import {
  createOutbound,
  createRule,
  updateOutbound,
  updateRule,
} from '../../../api'
import { applyWithToast } from '../../../configApply'
import type { Outbound, OutboundType, RouteRule, RuleMatch, RuleSet } from '../../../types'
import { randomBase64, randomHex, randomUUID, SHADOWSOCKS_METHODS, ss2022KeyLen } from '../../../util'
import { buildOutboundBody, TLS_REQUIRED_OUT, TRANSPORT_OUT, transportHost } from '../../../outboundForm'
import { FormGroup, scrollToFirstError } from './InboundForm'
import '../../../styles/forms.css'
import { MODAL_MD } from '../../../ui/modal'
import { OutboundImport } from './OutboundImport'
import { Pills } from '../../../ui/Page'

const OUT_TYPES: { value: OutboundType; label: string }[] = [
  { value: 'shadowsocks', label: 'Shadowsocks' },
  { value: 'socks', label: 'SOCKS5' },
  { value: 'vless', label: 'VLESS' },
  { value: 'vmess', label: 'VMess' },
  { value: 'trojan', label: 'Trojan' },
  { value: 'hysteria2', label: 'Hysteria2' },
  { value: 'tuic', label: 'TUIC' },
  { value: 'anytls', label: 'AnyTLS' },
  { value: 'snell', label: 'Snell' },
]

const UUID_OUT = new Set<OutboundType>(['vless', 'vmess', 'tuic'])
const PW_OUT = new Set<OutboundType>(['trojan', 'hysteria2', 'tuic', 'anytls'])
const TUIC_CC = ['cubic', 'new_reno', 'bbr']

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
  const [source, setSource] = useState<'manual' | 'uri'>('manual')
  const [submitting, setSubmitting] = useState(false)
  const submitLock = useRef(false)
  const submitSessionRef = useRef(0)
  const type: OutboundType = Form.useWatch('type', form) ?? 'shadowsocks'
  const tlsMode = Form.useWatch('tls_mode', form) ?? 'none'
  const transportType = Form.useWatch('transport_type', form) ?? 'tcp'
  const snellVersion = Form.useWatch('snell_version', form) ?? 4
  const snellObfsMode = Form.useWatch('snell_obfs_mode', form) ?? 'none'
  const hysteriaObfsType = Form.useWatch('obfs_type', form) ?? ''

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
    // Reset first: keys omitted below would otherwise leak from the previously
    // opened outbound (e.g. a credential typed for a different landing).
    form.resetFields()
    if (outbound) {
      const s = outbound.settings || {}
      const inner = s.settings || {}
      form.setFieldsValue({
        tag: outbound.tag,
        type: outbound.type,
        server: s.server,
        server_port: s.server_port,
        username: s.username ?? s.settings?.username,
        uuid: s.uuid,
        password: s.password ?? (outbound.type === 'snell' ? inner.snell_psk : outbound.type === 'socks' ? inner.password : undefined),
        method: inner.method ?? '2022-blake3-aes-128-gcm',
        ss_psk: inner.ss_server_psk ?? s.password,
        tls: TLS_REQUIRED_OUT.has(outbound.type) || !!inner.tls?.enabled,
        sni: inner.tls?.server_name,
        insecure: inner.tls?.insecure,
        flow: inner.flow,
        tls_mode: inner.tls?.reality?.enabled ? 'reality' : (inner.tls?.enabled ? 'tls' : 'none'),
        tls_alpn: inner.tls?.alpn?.join(', '),
        tls_fingerprint: inner.tls?.fingerprint ?? 'chrome',
        reality_server_name: inner.tls?.server_name || '',
        reality_public_key: inner.tls?.reality?.public_key || '',
        reality_short_id: inner.tls?.reality?.short_id?.join(', '),
        vmess_security: inner.vmess_security ?? 'auto',
        vmess_alter_id: inner.vmess_alter_id ?? 0,
        congestion_control: inner.congestion_control ?? 'cubic',
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
        transport_type: inner.transport?.type === 'ws' || inner.transport?.type === 'httpupgrade' ? inner.transport.type : 'tcp',
        ws_path: inner.transport?.path,
        ws_host: transportHost(inner.transport?.headers),
      })
    } else {
      form.setFieldsValue({
        type: 'shadowsocks',
        method: '2022-blake3-aes-128-gcm',
        // Landing port must match the remote server — never guess it.
        server_port: undefined,
        tls: false,
        tls_mode: 'none',
        tls_alpn: '',
        tls_fingerprint: 'chrome',
        reality_server_name: '',
        reality_public_key: '',
        reality_short_id: '',
        vmess_security: 'auto',
        vmess_alter_id: 0,
        congestion_control: 'cubic',
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
      })
    }
  }, [open, outbound])

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
      const body = buildOutboundBody(outbound, v)
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
  const hasParams = type === 'vless' || type === 'vmess' || type === 'tuic' || type === 'hysteria2' || type === 'snell'

  return (
    <Modal
      className="fm-modal"
      title={<div className="ui-dialog-heading"><span>{outbound ? '编辑出站' : '新增出站'}</span><small>连接目标与转发方式</small></div>}
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
      width={MODAL_MD}
    >
      {!outbound && <fieldset className="fm-source-picker" disabled={submitting}>
        <Pills
          label="出站来源"
          active={source}
          items={[{ key: 'manual', label: '手动配置' }, { key: 'uri', label: 'URI 导入' }]}
          onChange={(value) => { setImportReady(false); setSource(value as typeof source) }}
        />
      </fieldset>}
      {!outbound && source !== 'manual' ? (
        <OutboundImport key={serverId} serverId={serverId} onClose={onClose} onSaved={onSaved} onSavingChange={setSubmitting} formId={importFormId} onReadyChange={setImportReady} />
      ) : <fieldset disabled={submitting} aria-busy={submitting} className="min-w-0 border-0 p-0 m-0">
      <Form form={form} layout="vertical" className="fm-form">
        <FormGroup title="基本">
          <Form.Item name="tag" label="标签" rules={[{ required: true }]}>
            <Input placeholder="hk-out" />
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
            <Input placeholder="hk.example.com" className="mono" />
          </Form.Item>
          <Form.Item name="server_port" label="端口" rules={[{ required: true, message: '请填写落地服务器的端口' }]}>
            <InputNumber precision={0} min={1} max={65535} placeholder="443" className="mono" />
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
          {type === 'socks' && (
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
              <Form.Item name="flow" label="Flow" className="fm-full">
                <Input placeholder="xtls-rprx-vision" className="mono" />
              </Form.Item>
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
                        <Input placeholder="bing.com" className="mono" />
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
          </FormGroup>
        )}

        {TRANSPORT_OUT.has(type) && (
          <FormGroup title="传输">
            <Form.Item name="transport_type" label="传输层" className="fm-full">
              <Select options={[
                { value: 'tcp', label: 'TCP' },
                { value: 'ws', label: 'WebSocket' },
                { value: 'httpupgrade', label: 'HTTPUpgrade' },
              ]} />
            </Form.Item>
            {(transportType === 'ws' || transportType === 'httpupgrade') && (
              <>
                <Form.Item name="ws_path" label="路径">
                  <Input placeholder="/ws" className="mono" />
                </Form.Item>
                <Form.Item name="ws_host" label="Host">
                  <Input placeholder="example.com" className="mono" />
                </Form.Item>
              </>
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
                  <Input placeholder="www.microsoft.com" className="mono" />
                </Form.Item>
                <Form.Item name="reality_public_key" label="公钥" rules={[{ required: true, message: '请输入 REALITY 公钥' }]} className="fm-full">
                  <Input className="mono" />
                </Form.Item>
                <Form.Item name="reality_short_id" label="Short ID">
                  <Input placeholder="0123456789abcdef" className="mono" />
                </Form.Item>
                <Form.Item name="tls_alpn" label="ALPN">
                  <Input placeholder="h2, http/1.1" className="mono" />
                </Form.Item>
                <Form.Item name="tls_fingerprint" label="uTLS 指纹">
                  <Select options={['chrome', 'firefox', 'safari', 'ios', 'random', 'randomized'].map((v) => ({ value: v, label: v }))} />
                </Form.Item>
              </>
            )}

            {tlsMode === 'tls' && (
              <>
                <Form.Item name="sni" label="SNI">
                  <Input placeholder="example.com" className="mono" />
                </Form.Item>
                <Form.Item name="tls_alpn" label="ALPN">
                  <Input placeholder="h2, http/1.1" className="mono" />
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
              <Input placeholder="example.com" className="mono" />
            </Form.Item>
            <Form.Item name="tls_alpn" label="ALPN">
              <Input placeholder={type === 'tuic' || type === 'hysteria2' ? 'h3' : 'h2, http/1.1'} className="mono" />
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

// 动作类型选项
const ACTION_OPTIONS = [
  { value: 'route', label: '分流' },
  { value: 'reject', label: '拒绝' },
  { value: 'sniff', label: '嗅探' },
  { value: 'hijack-dns', label: 'DNS 劫持' },
]

type MatchField =
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
  ruleSets,
  open,
  onClose,
  onSaved,
}: {
  serverId: number
  rule: RouteRule | null
  outboundTags: string[]
  inboundTags: string[]
  ruleSets?: RuleSet[]
  open: boolean
  onClose: () => void
  onSaved: () => void
}) {
  const [form] = Form.useForm()
  const [submitting, setSubmitting] = useState(false)
  const submitLock = useRef(false)
  const submitSessionRef = useRef(0)
  const action = Form.useWatch('action', form) || 'route'
  const matchFields = (Form.useWatch('match_fields', form) || []) as MatchField[]
  const showMatchField = (field: MatchField) => matchFields.includes(field)

  useLayoutEffect(() => {
    submitSessionRef.current += 1
    submitLock.current = false
    setSubmitting(false)
    return () => { submitSessionRef.current += 1 }
  }, [open, serverId, rule?.id])

  useEffect(() => {
    if (!open) return
    form.resetFields()
    if (rule) {
      const m = rule.match || {}
      // 根据 outbound 值推导 action 类型
      let act = (m.action as string) || ''
      if (!act) {
        if (rule.outbound === 'block' || rule.outbound === 'reject') {
          act = 'reject'
        } else if (rule.outbound === 'sniff') {
          act = 'sniff'
        } else if (rule.outbound === 'hijack-dns') {
          act = 'hijack-dns'
        } else {
          act = 'route'
        }
      }
      form.setFieldsValue({
        action: act,
        outbound: rule.outbound || undefined,
        sniffer: m.sniffer || (act === 'sniff' ? m.protocol : null) || [],
        protocol: act === 'hijack-dns' ? (m.protocol?.length ? m.protocol : ['dns']) : (act !== 'sniff' ? (m.protocol || []) : []),
         method: (m.method as string) || 'default',
         remark: rule.remark || '',
         match_fields: activeMatchFields(m),
         rule_set: m.rule_set || [],
         rule_set_ip_direction: m.rule_set_ip_cidr_match_source ? 'source' : 'target',
         inbound: m.inbound || [],
         domain: joinLines(m.domain),
         domain_suffix: joinLines(m.domain_suffix),
         domain_keyword: joinLines(m.domain_keyword),
         ip_cidr: joinLines(m.ip_cidr),
         source_ip_cidr: joinLines(m.source_ip_cidr),
         port: joinLines(m.port?.map(String)),
        network: m.network || '',
        enabled: rule.enabled ?? true,
      })
    } else {
      form.setFieldsValue({
        action: 'route',
        outbound: undefined,
        sniffer: [],
         protocol: [],
         method: 'default',
         remark: '',
         match_fields: [],
         rule_set: [],
         rule_set_ip_direction: 'target',
         inbound: [],
         domain: '',
         domain_suffix: '',
         domain_keyword: '',
         ip_cidr: '',
         source_ip_cidr: '',
         port: '',
        network: '',
        enabled: true,
      })
    }
  }, [open, rule])

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
      const match: RuleMatch = { action: v.action }
      const selectedFields = new Set<MatchField>(v.match_fields || [])
      if ((v.action === 'sniff' || v.action === 'hijack-dns' || selectedFields.has('inbound')) && v.inbound?.length) {
        match.inbound = v.inbound
      }
      if (v.action === 'route' || v.action === 'reject') {
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

      // 根据动作类型设置目标出站
      let targetOutbound = v.outbound
      if (v.action === 'sniff') {
        targetOutbound = 'sniff'
        if (v.sniffer?.length) match.sniffer = v.sniffer
      } else if (v.action === 'reject') {
        targetOutbound = 'block'
        match.method = v.method || 'default'
      } else if (v.action === 'hijack-dns') {
        targetOutbound = 'hijack-dns'
        match.protocol = ['dns']
      }

      const body = { match, outbound: targetOutbound, enabled: v.enabled, remark: v.remark || '', sort: rule?.sort }
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
      <Form form={form} layout="vertical" className="fm-form">
        <FormGroup title="动作">
          <Form.Item name="action" rules={[{ required: true, message: '请选择规则动作' }]} className="fm-full">
            <Segmented block options={ACTION_OPTIONS} className="fm-actions" aria-label="规则动作" />
          </Form.Item>
          {action === 'route' && (
            <Form.Item name="outbound" label="目标出站" rules={[{ required: true, message: '请选择目标出站' }]} className="fm-full">
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
          {action === 'hijack-dns' && (
            <Form.Item name="inbound" label="限定入站" className="fm-full">
              <Select
                mode="multiple"
                allowClear
                placeholder="全部入站"
                options={inboundTags.map((t) => ({ value: t, label: t }))}
              />
            </Form.Item>
          )}
        </FormGroup>

        {(action === 'route' || action === 'reject') && (
          <FormGroup title="匹配条件" extra={matchFields.length ? `已选 ${matchFields.length} 项` : '匹配全部'}>
            <Form.Item name="match_fields" className="fm-full">
              <Checkbox.Group className="fm-chips" options={MATCH_FIELD_OPTIONS} />
            </Form.Item>

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
                <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} placeholder="www.example.com" className="mono" />
              </Form.Item>
            )}

            {showMatchField('domain_suffix') && (
              <Form.Item name="domain_suffix" label="域名后缀" rules={[{ required: true, message: '请输入至少一个域名后缀' }]} className="fm-full">
                <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} placeholder="example.com, google.com" className="mono" />
              </Form.Item>
            )}

            {showMatchField('domain_keyword') && (
              <Form.Item name="domain_keyword" label="域名关键词" rules={[{ required: true, message: '请输入至少一个域名关键词' }]} className="fm-full">
                <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} placeholder="google, youtube" className="mono" />
              </Form.Item>
            )}

            {showMatchField('ip_cidr') && (
              <Form.Item name="ip_cidr" label="目标 IP / CIDR" rules={[{ required: true, message: '请输入至少一个 IP 或 CIDR' }]} className="fm-full">
                <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} placeholder="8.8.8.8, 10.0.0.0/8" className="mono" />
              </Form.Item>
            )}

            {showMatchField('source_ip_cidr') && (
              <Form.Item name="source_ip_cidr" label="来源 IP / CIDR" rules={[{ required: true, message: '请输入至少一个来源 IP 或 CIDR' }]} className="fm-full">
                <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} placeholder="192.168.1.0/24" className="mono" />
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
                <Input placeholder="80, 443" className="mono" />
              </Form.Item>
            )}

            {showMatchField('protocol') && (
              <Form.Item name="protocol" label="应用协议" rules={[{ required: true, message: '请选择至少一个应用协议' }]}>
                <Select
                  mode="tags"
                  allowClear
                  placeholder="dns, tls"
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
                  placeholder="TCP / UDP"
                  options={[
                    { value: 'tcp', label: 'TCP' },
                    { value: 'udp', label: 'UDP' },
                    { value: 'icmp', label: 'ICMP' },
                  ]}
                />
              </Form.Item>
            )}
          </FormGroup>
        )}

        <FormGroup title="其他">
          <Form.Item name="remark" label="备注" className="fm-full">
            <Input placeholder="国内直连" maxLength={120} />
          </Form.Item>
          <div className="fm-switches">
            <Form.Item name="enabled" label="启用" valuePropName="checked" className="fm-switch">
              <Switch />
            </Form.Item>
          </div>
        </FormGroup>
      </Form>
      </fieldset>
    </Modal>
  )
}
