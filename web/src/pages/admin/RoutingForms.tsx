import { useEffect } from 'react'
import { Alert, Button, Divider, Form, Input, InputNumber, Modal, Segmented, Select, Space, Switch } from 'antd'
import {
  createOutbound,
  createRule,
  updateOutbound,
  updateRule,
} from '../../api'
import { applyWithToast } from '../../configApply'
import type { Outbound, OutboundType, RouteRule, RuleMatch, RuleSet } from '../../types'
import { randomBase64, randomHex, randomUUID, ss2022KeyLen } from '../../util'

const OUT_TYPES: { value: OutboundType; label: string }[] = [
  { value: 'shadowsocks', label: 'Shadowsocks' },
  { value: 'socks', label: 'SOCKS5' },
  { value: 'vless', label: 'VLESS' },
  { value: 'vmess', label: 'VMess' },
  { value: 'trojan', label: 'Trojan' },
  { value: 'hysteria2', label: 'Hysteria2' },
  { value: 'tuic', label: 'TUIC' },
]

const SS_METHODS = [
  '2022-blake3-aes-128-gcm',
  '2022-blake3-aes-256-gcm',
  '2022-blake3-chacha20-poly1305',
  'aes-256-gcm',
  'chacha20-ietf-poly1305',
]

const UUID_OUT = new Set<OutboundType>(['vless', 'vmess', 'tuic'])
const PW_OUT = new Set<OutboundType>(['trojan', 'hysteria2', 'tuic'])

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
  const type: OutboundType = Form.useWatch('type', form) ?? 'shadowsocks'

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
        ss_psk: inner.ss_server_psk,
        tls: !!inner.tls?.enabled,
        sni: inner.tls?.server_name,
        insecure: inner.tls?.insecure,
        flow: inner.flow,
      })
    } else {
      form.setFieldsValue({
        type: 'shadowsocks',
        method: '2022-blake3-aes-128-gcm',
        server_port: 443,
        tls: false,
      })
    }
  }, [open, outbound])

  const submit = async () => {
    const v = await form.validateFields()
    const inner: Record<string, unknown> = {}
    if (type === 'shadowsocks') {
      inner.method = v.method
      inner.ss_server_psk = v.ss_psk || ''
    }
    if (type === 'vless' && v.flow) inner.flow = v.flow
    if (UUID_OUT.has(type) || type === 'trojan' || type === 'hysteria2') {
      if (v.tls) inner.tls = { enabled: true, server_name: v.sni || '', insecure: !!v.insecure }
    }
    const body = {
      tag: v.tag,
      type: v.type as OutboundType,
      settings: {
        server: v.server,
        server_port: v.server_port,
        username: v.username || undefined,
        uuid: v.uuid,
        password: v.password,
        settings: inner,
      },
    }
    const editing = outbound
    // 关闭弹窗后在后台下发，避免确认按钮卡顿
    onClose()
    await applyWithToast(`outbound-${editing ? editing.id : 'new'}`, () =>
      editing ? updateOutbound(serverId, editing.id, body) : createOutbound(serverId, body),
    )
    onSaved()
  }

  return (
    <Modal
      title={outbound ? '编辑出站' : '新增出站'}
      open={open}
      onOk={submit}
      onCancel={onClose}
      width={600}
      centered
      destroyOnClose
    >
      <Form form={form} layout="vertical">
        <Form.Item name="tag" label="标签" rules={[{ required: true }]}>
          <Input placeholder="hkbn-out" />
        </Form.Item>
        <Form.Item name="type" label="类型" rules={[{ required: true }]}>
          <Select options={OUT_TYPES} />
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
              <Select options={SS_METHODS.map((m) => ({ value: m, label: m }))} />
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

        {type !== 'shadowsocks' && type !== 'socks' && (
          <>
            <Divider orientation="left">TLS</Divider>
            <Form.Item name="tls" label="启用 TLS" valuePropName="checked">
              <Switch />
            </Form.Item>
            <Form.Item name="sni" label="SNI / server_name">
              <Input placeholder="example.com" />
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

type MatchField = 'inbound' | 'rule_set' | 'domain_suffix' | 'ip_cidr' | 'port' | 'protocol' | 'network'

const MATCH_FIELD_OPTIONS: { value: MatchField; label: string }[] = [
  { value: 'inbound', label: '限定入站' },
  { value: 'rule_set', label: '规则集' },
  { value: 'domain_suffix', label: '域名后缀' },
  { value: 'ip_cidr', label: '目标 IP / CIDR' },
  { value: 'port', label: '目标端口' },
  { value: 'protocol', label: '应用协议' },
  { value: 'network', label: '网络类型' },
]

function activeMatchFields(match: RuleMatch): MatchField[] {
  const fields: MatchField[] = []
  if (match.inbound?.length) fields.push('inbound')
  if (match.rule_set?.length) fields.push('rule_set')
  if (match.domain_suffix?.length) fields.push('domain_suffix')
  if (match.ip_cidr?.length) fields.push('ip_cidr')
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
        match_fields: activeMatchFields(m),
        rule_set: m.rule_set || [],
        inbound: m.inbound || [],
        domain_suffix: joinLines(m.domain_suffix),
        ip_cidr: joinLines(m.ip_cidr),
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
        match_fields: [],
        rule_set: [],
        inbound: [],
        domain_suffix: '',
        ip_cidr: '',
        port: '',
        network: '',
        enabled: true,
      })
    }
  }, [open, rule])

  const submit = async () => {
    const v = await form.validateFields()
    const match: RuleMatch = { action: v.action }
    const selectedFields = new Set<MatchField>(v.match_fields || [])
    if ((v.action === 'sniff' || v.action === 'hijack-dns' || selectedFields.has('inbound')) && v.inbound?.length) {
      match.inbound = v.inbound
    }
    if (v.action === 'route' || v.action === 'reject') {
      if (selectedFields.has('rule_set') && v.rule_set?.length) match.rule_set = v.rule_set
      if (selectedFields.has('protocol') && v.protocol?.length) match.protocol = v.protocol
      if (selectedFields.has('domain_suffix')) {
        const domains = splitLines(v.domain_suffix)
        if (domains.length) match.domain_suffix = domains
      }
      if (selectedFields.has('ip_cidr')) {
        const cidrs = splitLines(v.ip_cidr)
        if (cidrs.length) match.ip_cidr = cidrs
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
      if (v.sniffer?.length) {
        match.sniffer = v.sniffer
      }
    } else if (v.action === 'reject') {
      targetOutbound = 'block'
      match.method = v.method || 'default'
    } else if (v.action === 'hijack-dns') {
      targetOutbound = 'hijack-dns'
      match.protocol = ['dns']
    }

    const body = { match, outbound: targetOutbound, enabled: v.enabled, sort: rule?.sort }
    const editing = rule
    // 关闭弹窗后在后台下发，避免确认按钮卡顿
    onClose()
    await applyWithToast(`rule-${editing ? editing.id : 'new'}`, () =>
      editing ? updateRule(serverId, editing.id, body) : createRule(serverId, body),
    )
    onSaved()
  }

  return (
    <Modal
      title={rule ? '编辑路由规则' : '新增路由规则'}
      open={open}
      onOk={submit}
      onCancel={onClose}
      width={560}
      centered
      destroyOnClose
    >
      <Form form={form} layout="vertical" style={{ marginTop: 8 }}>
        {/* ── ❶ 规则动作 ── */}
        <Form.Item
          name="action"
          label="规则动作"
          rules={[{ required: true, message: '请选择规则动作' }]}
          style={{ marginBottom: 4 }}
        >
          <Segmented
            block
            options={ACTION_OPTIONS}
            style={{ fontWeight: 500 }}
          />
        </Form.Item>
        <div style={{ fontSize: 12, color: '#8c8c8c', marginBottom: 16, paddingLeft: 2 }}>
          {ACTION_DESC[action] || ''}
        </div>

        {/* 路由动作：选择目标出站 */}
        {action === 'route' && (
          <Form.Item name="outbound" label="目标出站" rules={[{ required: true, message: '请选择目标出站' }]}>
            <Select
              allowClear
              placeholder="请选择目标出站"
              options={[
                { value: 'direct', label: 'direct（内置直连）' },
                ...outboundTags.map((t) => ({ value: t, label: t })),
              ]}
            />
          </Form.Item>
        )}

        {/* 嗅探动作：精简设置（仅嗅探协议与限定入站） */}
        {action === 'sniff' && (
          <>
            <Form.Item name="sniffer" label="嗅探协议" extra="留空 = 嗅探 TLS/HTTP 等全部应用协议">
              <Select
                mode="multiple"
                allowClear
                placeholder="留空 = 嗅探全部协议"
                options={[
                  { value: 'tls', label: 'TLS (HTTPS)' },
                  { value: 'http', label: 'HTTP' },
                  { value: 'quic', label: 'QUIC (HTTP/3)' },
                  { value: 'dns', label: 'DNS' },
                ]}
              />
            </Form.Item>
            <Form.Item name="inbound" label="限定入站" extra="留空 = 对全部入站流量开启嗅探">
              <Select
                mode="multiple"
                allowClear
                placeholder="留空 = 全部入站"
                options={inboundTags.map((t) => ({ value: t, label: t }))}
              />
            </Form.Item>
          </>
        )}

        {/* DNS 劫持固定匹配 DNS，只允许按入站缩小范围。 */}
        {action === 'hijack-dns' && (
          <Form.Item name="inbound" label="限定入站（可选）" extra="留空时处理全部入站的 DNS 请求">
            <Select
              mode="multiple"
              allowClear
              placeholder="全部入站"
              options={inboundTags.map((t) => ({ value: t, label: t }))}
            />
          </Form.Item>
        )}

        {/* 拒绝动作：选择拦截方式 */}
        {action === 'reject' && (
          <Form.Item name="method" label="拦截方式">
            <Select
              options={[
                { value: 'default', label: '快速拒绝（TCP RST / ICMP 不可达）' },
                { value: 'drop', label: '静默丢弃（不返回响应）' },
              ]}
            />
          </Form.Item>
        )}

        {/* 分流与拒绝规则按需添加匹配条件，避免一次展示全部高级字段。 */}
        {(action === 'route' || action === 'reject') && (
          <>
            <Divider style={{ margin: '12px 0', fontSize: 13, color: '#64748b' }}>
              匹配范围
            </Divider>
            <Alert
              type={matchFields.length ? 'info' : 'warning'}
              showIcon
              style={{ marginBottom: 14 }}
              message={
                matchFields.length
                  ? '同一项中的多个值满足任意一个；不同条件通常需要同时满足。'
                  : action === 'reject'
                    ? '未添加条件：这会拒绝全部流量。'
                    : '未添加条件：这是一条匹配全部流量的兜底规则。'
              }
            />

            <Form.Item
              name="match_fields"
              label="添加匹配条件"
              extra="只选择需要的条件；域名与 IP 同属目标地址，二者满足其一即可。"
            >
              <Select
                mode="multiple"
                allowClear
                placeholder="不选择 = 全部流量"
                options={MATCH_FIELD_OPTIONS}
              />
            </Form.Item>

            {showMatchField('inbound') && (
              <Form.Item name="inbound" label="限定入站" rules={[{ required: true, message: '请选择至少一个入站' }]}>
                <Select
                  mode="multiple"
                  allowClear
                  placeholder="选择流量来自哪些入站"
                  options={inboundTags.map((t) => ({ value: t, label: t }))}
                />
              </Form.Item>
            )}

            {showMatchField('rule_set') && (
              <Form.Item name="rule_set" label="规则集" rules={[{ required: true, message: '请选择至少一个规则集' }]}>
                <Select
                  mode="multiple"
                  allowClear
                  placeholder="选择规则集"
                  options={(ruleSets || []).map((rs) => ({ label: rs.tag, value: rs.tag }))}
                />
              </Form.Item>
            )}

            {showMatchField('domain_suffix') && (
              <Form.Item name="domain_suffix" label="域名后缀" rules={[{ required: true, message: '请输入至少一个域名后缀' }]}>
                <Input.TextArea
                  autoSize={{ minRows: 1, maxRows: 3 }}
                  placeholder="example.com, google.com"
                />
              </Form.Item>
            )}

            {showMatchField('ip_cidr') && (
              <Form.Item name="ip_cidr" label="目标 IP / CIDR" rules={[{ required: true, message: '请输入至少一个 IP 或 CIDR' }]}>
                <Input.TextArea
                  autoSize={{ minRows: 1, maxRows: 3 }}
                  placeholder="8.8.8.8, 10.0.0.0/8"
                />
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
                <Input placeholder="80, 443" />
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
          </>
        )}

        {/* ── ❸ 启用开关 ── */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16 }}>
          <Form.Item name="enabled" valuePropName="checked" style={{ marginBottom: 0 }}>
            <Switch />
          </Form.Item>
          <span style={{ fontSize: 13, color: '#595959' }}>启用此规则</span>
        </div>
      </Form>
    </Modal>
  )
}
