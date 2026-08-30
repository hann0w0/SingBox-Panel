import { useEffect, useRef, useState } from 'react'
import { Button, Checkbox, Divider, Form, Input, InputNumber, Modal, Segmented, Select, Space, Switch } from 'antd'
import {
  createOutbound,
  createRule,
  updateOutbound,
  updateRule,
} from '../../../api'
import { applyWithToast } from '../../../configApply'
import type { Outbound, OutboundType, RouteRule, RuleMatch, RuleSet } from '../../../types'
import { randomBase64, randomHex, randomUUID, SHADOWSOCKS_METHODS, ss2022KeyLen } from '../../../util'
import { buildOutboundBody, TLS_REQUIRED_OUT, TRANSPORT_OUT } from '../../../outboundForm'

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
  const [submitting, setSubmitting] = useState(false)
  const submitLock = useRef(false)
  const type: OutboundType = Form.useWatch('type', form) ?? 'shadowsocks'
  const tlsMode = Form.useWatch('tls_mode', form) ?? 'none'
  const transportType = Form.useWatch('transport_type', form) ?? 'tcp'
  const snellVersion = Form.useWatch('snell_version', form) ?? 4
  const snellObfsMode = Form.useWatch('snell_obfs_mode', form) ?? 'none'
  const hysteriaObfsType = Form.useWatch('obfs_type', form) ?? ''

  useEffect(() => {
    if (!open) return
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
        password: s.password,
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
        ws_host: inner.transport?.headers?.Host,
      })
    } else {
      form.setFieldsValue({
        type: 'shadowsocks',
        method: '2022-blake3-aes-128-gcm',
        server_port: 443,
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
    if (submitLock.current) return
    submitLock.current = true
    setSubmitting(true)
    try {
      const v = await form.validateFields()
      const body = buildOutboundBody(outbound, v)
      const editing = outbound
      // 关闭弹窗后在后台下发，避免确认按钮卡顿
      onClose()
      await applyWithToast(`outbound-${editing ? editing.id : 'new'}`, () =>
        editing ? updateOutbound(serverId, editing.id, body) : createOutbound(serverId, body),
      )
      onSaved()
    } finally {
      submitLock.current = false
      setSubmitting(false)
    }
  }

  return (
    <Modal
      title={outbound ? '编辑出站' : '新增出站'}
      open={open}
      onOk={submit}
      onCancel={onClose}
      confirmLoading={submitting}
      width={600}
      centered
      destroyOnClose
    >
      <Form form={form} layout="vertical">
        <Form.Item name="tag" label="标签" rules={[{ required: true }]}>
          <Input placeholder="hkbn-out" />
        </Form.Item>
        <Form.Item name="type" label="类型" rules={[{ required: true }]}>
          <Select
            options={OUT_TYPES}
            onChange={(next: OutboundType) => {
              if (TLS_REQUIRED_OUT.has(next)) form.setFieldValue('tls', true)
            }}
          />
        </Form.Item>
        <Space style={{ display: 'flex' }} align="baseline">
          <Form.Item name="server" label="服务器地址" rules={[{ required: true }]} style={{ flex: 1 }}>
            <Input placeholder="hg.example.com" />
          </Form.Item>
          <Form.Item name="server_port" label="端口" rules={[{ required: true }]}>
            <InputNumber min={1} max={65535} />
          </Form.Item>
        </Space>

        {type === 'shadowsocks' && (
          <>
            <Form.Item name="method" label="加密方式" rules={[{ required: true }]}>
              <Select options={SHADOWSOCKS_METHODS.map((m) => ({ value: m, label: m }))} />
            </Form.Item>
            <Form.Item label="密码 / 凭证">
              <Space.Compact style={{ width: '100%' }}>
                <Form.Item name="ss_psk" noStyle rules={[{ required: true, message: '必填' }]}>
                  <Input placeholder="服务器密钥" />
                </Form.Item>
                <Button onClick={() => form.setFieldValue('ss_psk', randomBase64(ss2022KeyLen(form.getFieldValue('method')) || 16))}>随机</Button>
              </Space.Compact>
            </Form.Item>
          </>
        )}

        {UUID_OUT.has(type) && (
          <Form.Item label="UUID">
            <Space.Compact style={{ width: '100%' }}>
              <Form.Item name="uuid" noStyle rules={[{ required: true, message: '必填' }]}>
                <Input placeholder="对方提供的 UUID" />
              </Form.Item>
              <Button onClick={() => form.setFieldValue('uuid', randomUUID())}>随机</Button>
            </Space.Compact>
          </Form.Item>
        )}
        {PW_OUT.has(type) && (
          <Form.Item label="密码">
            <Space.Compact style={{ width: '100%' }}>
              <Form.Item name="password" noStyle rules={[{ required: true, message: '必填' }]}>
                <Input placeholder="对方提供的密码" />
              </Form.Item>
              <Button onClick={() => form.setFieldValue('password', randomHex(16))}>随机</Button>
            </Space.Compact>
          </Form.Item>
        )}
        {type === 'socks' && (
          <>
            <Form.Item
              name="username"
              label="用户名（可选）"
              extra="用户名和密码同时留空表示无需认证"
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
              <Input placeholder="对方 SOCKS5 用户名（留空则免登录）" />
            </Form.Item>
            <Form.Item
              name="password"
              label="密码（可选）"
              extra="用户名和密码同时留空表示无需认证"
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
              <Input.Password placeholder="对方 SOCKS5 密码（留空则免登录）" />
            </Form.Item>
          </>
        )}
        {type === 'vless' && (
          <Form.Item name="flow" label="Flow">
            <Input placeholder="xtls-rprx-vision" />
          </Form.Item>
        )}

        {type === 'vmess' && (
          <>
            <Form.Item name="vmess_security" label="客户端加密" rules={[{ required: true }]}>
              <Select options={['auto', 'none', 'zero', 'aes-128-gcm', 'chacha20-poly1305', 'aes-128-cfb'].map((m) => ({ value: m, label: m }))} />
            </Form.Item>
            <Form.Item name="vmess_alter_id" label="Alter ID" rules={[{ required: true }]}>
              <InputNumber min={0} precision={0} style={{ width: '100%' }} />
            </Form.Item>
          </>
        )}

        {type === 'tuic' && (
          <Form.Item name="congestion_control" label="拥塞控制" rules={[{ required: true }]}>
            <Select options={TUIC_CC.map((v) => ({ value: v, label: v }))} />
          </Form.Item>
        )}

        {type === 'hysteria2' && (
          <>
            <Form.Item name="obfs_type" label="混淆类型">
              <Select options={[{ value: '', label: '无' }, { value: 'salamander', label: 'salamander' }, { value: 'gecko', label: 'gecko' }]} />
            </Form.Item>
            <Form.Item name="obfs_password" label="混淆密码" extra="留空表示不启用混淆">
              <Input.Password placeholder="与服务器一致的混淆密码" />
            </Form.Item>
            {hysteriaObfsType === 'gecko' && (
              <Space style={{ display: 'flex' }} align="baseline">
                <Form.Item name="gecko_min_packet_size" label="最小包大小" style={{ flex: 1 }}><InputNumber min={512} precision={0} style={{ width: '100%' }} /></Form.Item>
                <Form.Item name="gecko_max_packet_size" label="最大包大小" style={{ flex: 1 }}><InputNumber min={512} precision={0} style={{ width: '100%' }} /></Form.Item>
              </Space>
            )}
          </>
        )}

        {type === 'snell' && (
          <>
            <Form.Item name="snell_version" label="Snell 版本" rules={[{ required: true }]}>
              <Select options={[{ value: 4, label: 'v4' }, { value: 6, label: 'v6' }]} />
            </Form.Item>
            <Form.Item
              label="服务器 PSK"
            >
              <Space.Compact style={{ width: '100%' }}>
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
                  <Input placeholder="对方提供的 PSK" />
                </Form.Item>
                <Button onClick={() => form.setFieldValue('password', randomHex(16))}>随机</Button>
              </Space.Compact>
            </Form.Item>
            <Space align="baseline" style={{ display: 'flex' }}>
              <Form.Item name="snell_network" label="network" style={{ flex: 1 }}>
                <Select options={[
                  { value: 'tcp', label: 'TCP' },
                  { value: 'udp', label: 'UDP' },
                ]} />
              </Form.Item>
              <Form.Item name="snell_reuse" label="reuse" valuePropName="checked">
                <Switch />
              </Form.Item>
            </Space>
            {snellVersion === 4 ? (
              <>
                <Form.Item name="snell_obfs_mode" label="obfs_mode">
                  <Select options={[{ value: 'none', label: 'none' }, { value: 'http', label: 'http' }, { value: 'tls', label: 'tls' }]} />
                </Form.Item>
                {(snellObfsMode === 'http' || snellObfsMode === 'tls') && (
                  <Form.Item name="snell_obfs_host" label="obfs_host">
                    <Input placeholder="默认 bing.com" />
                  </Form.Item>
                )}
              </>
            ) : (
              <Form.Item name="snell_mode" label="mode">
                <Select options={[
                  { value: '', label: 'default' },
                  { value: 'unshaped', label: 'unshaped' },
                  { value: 'unsafe-raw', label: 'unsafe-raw' },
                ]} />
              </Form.Item>
            )}
          </>
        )}

        {TRANSPORT_OUT.has(type) && (
          <>
            <Divider orientation="left">传输</Divider>
            <Form.Item name="transport_type" label="传输层">
              <Select options={[
                { value: 'tcp', label: 'TCP' },
                { value: 'ws', label: 'WebSocket' },
                { value: 'httpupgrade', label: 'HTTPUpgrade' },
              ]} />
            </Form.Item>
            {(transportType === 'ws' || transportType === 'httpupgrade') && (
              <>
                <Form.Item name="ws_path" label={transportType === 'ws' ? 'WS 路径' : 'HTTPUpgrade 路径'}>
                  <Input placeholder="/ws" />
                </Form.Item>
                <Form.Item name="ws_host" label="Host 头">
                  <Input placeholder="example.com" />
                </Form.Item>
              </>
            )}
          </>
        )}

        {type === 'vless' && (
          <>
            <Divider orientation="left">安全连接</Divider>
            <Form.Item name="tls_mode" label="TLS 模式">
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
                <Form.Item
                  name="reality_server_name"
                  label="伪装域名 / SNI"
                  rules={[{ required: true, message: 'REALITY 需要伪装域名' }]}
                  extra="例如 www.microsoft.com；客户端会连接节点地址，但使用此域名完成握手。"
                >
                  <Input placeholder="www.microsoft.com" />
                </Form.Item>
                <Form.Item
                  name="reality_public_key"
                  label="REALITY 公钥"
                  rules={[{ required: true, message: '请输入 REALITY 公钥' }]}
                  extra="从服务端 REALITY 密钥对生成结果中复制 public key。"
                >
                  <Input placeholder="Base64URL 公钥" />
                </Form.Item>
                <Form.Item name="reality_short_id" label="Short ID（可选）" extra="多个值用逗号分隔；留空表示不指定。">
                  <Input placeholder="例如 0123456789abcdef" />
                </Form.Item>
                <Form.Item name="tls_alpn" label="ALPN（可选）">
                  <Input placeholder="例如 h2, http/1.1" />
                </Form.Item>
                <Form.Item name="tls_fingerprint" label="uTLS 指纹">
                  <Select options={['chrome', 'firefox', 'safari', 'ios', 'random', 'randomized'].map((v) => ({ value: v, label: v }))} />
                </Form.Item>
              </>
            )}

            {tlsMode === 'tls' && (
              <>
                <Form.Item name="sni" label="SNI / server_name">
                  <Input placeholder="example.com" />
                </Form.Item>
                <Form.Item name="tls_alpn" label="ALPN（可选）">
                  <Input placeholder="例如 h2, http/1.1" />
                </Form.Item>
                <Form.Item name="tls_fingerprint" label="uTLS 指纹">
                  <Select options={['chrome', 'firefox', 'safari', 'ios', 'random', 'randomized'].map((v) => ({ value: v, label: v }))} />
                </Form.Item>
                <Form.Item name="insecure" label="跳过证书校验" valuePropName="checked">
                  <Switch />
                </Form.Item>
              </>
            )}
          </>
        )}

        {type !== 'shadowsocks' && type !== 'socks' && type !== 'vless' && type !== 'snell' && (
          <>
            <Divider orientation="left">TLS</Divider>
            <Form.Item
              name="tls"
              label="启用 TLS"
              valuePropName="checked"
              extra={TLS_REQUIRED_OUT.has(type) ? '该协议必须使用 TLS，不能关闭。' : undefined}
            >
              <Switch disabled={TLS_REQUIRED_OUT.has(type)} />
            </Form.Item>
            <Form.Item name="sni" label="SNI / server_name">
              <Input placeholder="example.com" />
            </Form.Item>
            <Form.Item name="tls_alpn" label="ALPN（可选）" extra="QUIC 协议（TUIC/Hysteria2）建议填 h3；TCP 协议如 h2, http/1.1">
              <Input placeholder={type === 'tuic' || type === 'hysteria2' ? 'h3' : 'h2, http/1.1'} />
            </Form.Item>
            <Form.Item name="insecure" label="跳过证书校验" valuePropName="checked">
              <Switch />
            </Form.Item>
          </>
        )}

      </Form>
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

// 动作类型中文描述映射
const ACTION_DESC: Record<string, string> = {
  route: '匹配后将流量交给指定出站，并结束本条连接的规则匹配',
  reject: '匹配后立即拒绝或静默丢弃流量',
  sniff: '识别连接中的域名和应用协议，完成后继续匹配后续规则',
  'hijack-dns': '只截获 DNS 流量并交给 sing-box DNS 模块处理',
}

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
  const action = Form.useWatch('action', form) || 'route'
  const matchFields = (Form.useWatch('match_fields', form) || []) as MatchField[]
  const showMatchField = (field: MatchField) => matchFields.includes(field)

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
    if (submitLock.current) return
    submitLock.current = true
    setSubmitting(true)
    try {
      const v = await form.validateFields()
      const match: RuleMatch = { action: v.action }
      const selectedFields = new Set<MatchField>(v.match_fields || [])
      if ((v.action === 'sniff' || v.action === 'hijack-dns' || selectedFields.has('inbound')) && v.inbound?.length) {
        match.inbound = v.inbound
      }
      if (v.action === 'route' || v.action === 'reject') {
        if (selectedFields.has('rule_set') && v.rule_set?.length) match.rule_set = v.rule_set
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
      // 关闭弹窗后在后台下发，避免确认按钮卡顿
      onClose()
      await applyWithToast(`rule-${editing ? editing.id : 'new'}`, () =>
        editing ? updateRule(serverId, editing.id, body) : createRule(serverId, body),
      )
      onSaved()
    } finally {
      submitLock.current = false
      setSubmitting(false)
    }
  }

  return (
    <Modal
      className="route-rule-modal"
      title={rule ? '编辑路由规则' : '新增路由规则'}
      open={open}
      onOk={submit}
      onCancel={onClose}
      confirmLoading={submitting}
      okText={rule ? '保存规则' : '创建规则'}
      cancelText="取消"
      width={680}
      centered
      destroyOnClose
    >
      <Form form={form} layout="vertical" className="route-rule-form">
        <section className="route-rule-section">
          <div className="route-rule-section-heading">
            <span className="route-rule-step">1</span>
            <div>
              <strong>规则动作</strong>
              <div className="route-rule-section-subtitle">先决定匹配后要执行的动作，下面只显示相关设置。</div>
            </div>
          </div>
          <Form.Item name="action" rules={[{ required: true, message: '请选择规则动作' }]} style={{ marginBottom: 8 }}>
            <Segmented block options={ACTION_OPTIONS} className="route-rule-actions" />
          </Form.Item>
          <div className="route-rule-action-description">{ACTION_DESC[action] || ''}</div>
        </section>

        {action === 'route' && (
          <section className="route-rule-compact-section">
            <div className="route-rule-compact-heading">3. 执行分流</div>
            <Form.Item name="outbound" label="目标出站" rules={[{ required: true, message: '请选择目标出站' }]} style={{ marginBottom: 4 }}>
              <Select
                allowClear
                placeholder="选择要承载流量的出站"
                options={[
                  { value: 'direct', label: 'direct · 内置直连' },
                  ...outboundTags.filter((t) => t !== 'direct').map((t) => ({ value: t, label: t })),
                ]}
              />
            </Form.Item>
            <div className="route-rule-card-hint">匹配成功后，流量将交给这个出站处理。</div>
          </section>
        )}

        {action === 'reject' && (
          <section className="route-rule-compact-section">
            <div className="route-rule-compact-heading">3. 拒绝方式</div>
            <Form.Item name="method" label="拦截方式" style={{ marginBottom: 4 }}>
              <Select
                options={[
                  { value: 'default', label: '快速拒绝 · TCP RST / ICMP 不可达' },
                  { value: 'drop', label: '静默丢弃 · 不返回响应' },
                ]}
              />
            </Form.Item>
          </section>
        )}

        {action === 'sniff' && (
          <section className="route-rule-compact-section">
            <div className="route-rule-compact-heading">2. 嗅探范围</div>
            <div className="route-rule-fields">
              <Form.Item name="sniffer" label="嗅探协议" extra="留空 = 嗅探全部支持的协议">
                <Select
                  mode="multiple"
                  allowClear
                  placeholder="留空 = 全部协议"
                  options={[
                    { value: 'tls', label: 'TLS (HTTPS)' },
                    { value: 'http', label: 'HTTP' },
                    { value: 'quic', label: 'QUIC (HTTP/3)' },
                    { value: 'dns', label: 'DNS' },
                  ]}
                />
              </Form.Item>
              <Form.Item name="inbound" label="限定入站" extra="留空 = 全部入站">
                <Select
                  mode="multiple"
                  allowClear
                  placeholder="全部入站"
                  options={inboundTags.map((t) => ({ value: t, label: t }))}
                />
              </Form.Item>
            </div>
          </section>
        )}

        {action === 'hijack-dns' && (
          <section className="route-rule-compact-section">
            <div className="route-rule-compact-heading">2. DNS 劫持范围</div>
            <Form.Item name="inbound" label="限定入站（可选）" extra="留空时处理全部入站的 DNS 请求" style={{ marginBottom: 4 }}>
              <Select
                mode="multiple"
                allowClear
                placeholder="全部入站"
                options={inboundTags.map((t) => ({ value: t, label: t }))}
              />
            </Form.Item>
          </section>
        )}

        {(action === 'route' || action === 'reject') && (
          <section className="route-rule-compact-section route-rule-match-section">
            <div className="route-rule-compact-heading">
              <span>2. 匹配条件</span>
              <span className="route-rule-condition-count">{matchFields.length ? `已选 ${matchFields.length} 项` : '匹配全部'}</span>
            </div>
            <Form.Item
              name="match_fields"
              label="选择匹配条件"
              extra="可同时选择多个条件；未选择条件 = 匹配全部流量。"
              style={{ marginBottom: 16 }}
            >
              <Checkbox.Group className="route-rule-condition-picker">
                {MATCH_FIELD_OPTIONS.map((option) => (
                  <Checkbox value={option.value} key={option.value} className="route-rule-condition-option">
                    {option.label}
                  </Checkbox>
                ))}
              </Checkbox.Group>
            </Form.Item>

            <div className="route-rule-fields">
              {showMatchField('inbound') && (
                <Form.Item name="inbound" label="入站来源" rules={[{ required: true, message: '请选择至少一个入站' }]}>
                  <Select mode="multiple" allowClear placeholder="选择入站" options={inboundTags.map((t) => ({ value: t, label: t }))} />
                </Form.Item>
              )}

              {showMatchField('rule_set') && (
                <Form.Item name="rule_set" label="规则集" rules={[{ required: true, message: '请选择至少一个规则集' }]}>
                  <Select
                    mode="multiple"
                    allowClear
                    placeholder={ruleSets?.length ? '选择规则集' : '暂无可用规则集'}
                    options={(ruleSets || []).map((rs) => ({ label: rs.tag, value: rs.tag }))}
                  />
                </Form.Item>
              )}

              {showMatchField('domain') && (
                <Form.Item name="domain" label="完整域名" rules={[{ required: true, message: '请输入至少一个完整域名' }]}>
                  <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} placeholder="例如 www.example.com" />
                </Form.Item>
              )}

              {showMatchField('domain_suffix') && (
                <Form.Item name="domain_suffix" label="域名后缀" rules={[{ required: true, message: '请输入至少一个域名后缀' }]}>
                  <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} placeholder="例如 example.com, google.com" />
                </Form.Item>
              )}

              {showMatchField('domain_keyword') && (
                <Form.Item name="domain_keyword" label="域名关键词" rules={[{ required: true, message: '请输入至少一个域名关键词' }]}>
                  <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} placeholder="例如 google, youtube" />
                </Form.Item>
              )}

              {showMatchField('ip_cidr') && (
                <Form.Item name="ip_cidr" label="目标 IP / CIDR" rules={[{ required: true, message: '请输入至少一个 IP 或 CIDR' }]}>
                  <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} placeholder="例如 8.8.8.8, 10.0.0.0/8" />
                </Form.Item>
              )}

              {showMatchField('source_ip_cidr') && (
                <Form.Item name="source_ip_cidr" label="来源 IP / CIDR" rules={[{ required: true, message: '请输入至少一个来源 IP 或 CIDR' }]}>
                  <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} placeholder="例如 192.168.1.0/24" />
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
                  <Input placeholder="例如 80, 443" />
                </Form.Item>
              )}

              {showMatchField('protocol') && (
                <Form.Item name="protocol" label="应用协议" rules={[{ required: true, message: '请选择至少一个应用协议' }]}>
                  <Select
                    mode="tags"
                    allowClear
                    placeholder="如 dns、http、tls、quic"
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
                    placeholder="选择 TCP、UDP 或 ICMP"
                    options={[
                      { value: 'tcp', label: 'TCP' },
                      { value: 'udp', label: 'UDP' },
                      { value: 'icmp', label: 'ICMP（仅 TUN / WireGuard / Tailscale）' },
                    ]}
                  />
                </Form.Item>
              )}
            </div>
          </section>
        )}

        <div className="route-rule-options">
          <Form.Item name="remark" label="备注（可选）" extra="用于在规则列表中快速识别用途" className="route-rule-remark">
            <Input placeholder="例如：国内网站直连" maxLength={120} />
          </Form.Item>
          <div className="route-rule-enabled">
            <Form.Item name="enabled" valuePropName="checked" style={{ marginBottom: 0 }}>
              <Switch />
            </Form.Item>
            <div>
              <strong>启用此规则</strong>
              <div className="route-rule-card-hint">关闭后保留规则，但不会写入下发配置。</div>
            </div>
          </div>
        </div>
      </Form>
    </Modal>
  )
}
