import { useEffect } from 'react'
import { Alert, Button, Divider, Form, Input, InputNumber, Modal, Select, Space, Switch } from 'antd'
import { createInbound, updateInbound } from '../../api'
import { applyWithToast } from '../../configApply'
import type { Inbound, InboundSettings, InboundType } from '../../types'
import { randomBase64, randomHex, randomUUID, ss2022KeyLen } from '../../util'

const TYPES: { value: InboundType; label: string }[] = [
  { value: 'shadowsocks', label: 'Shadowsocks' },
  { value: 'mixed', label: 'Mixed (HTTP+SOCKS5)' },
  { value: 'snell', label: 'Snell' },
  { value: 'vless', label: 'VLESS' },
  { value: 'anytls', label: 'AnyTLS' },
  { value: 'vmess', label: 'VMess' },
  { value: 'tuic', label: 'TUIC' },
  { value: 'trojan', label: 'Trojan' },
  { value: 'hysteria2', label: 'Hysteria2' },
]

const SS_METHODS = [
  '2022-blake3-aes-128-gcm',
  '2022-blake3-aes-256-gcm',
  '2022-blake3-chacha20-poly1305',
  'aes-256-gcm',
  'chacha20-ietf-poly1305',
]

const WS_TYPES = new Set<InboundType>(['vless', 'vmess', 'trojan'])
const REALITY_TYPES = new Set<InboundType>(['vless', 'vmess'])
const TLS_REQUIRED = new Set<InboundType>(['trojan', 'hysteria2', 'tuic', 'anytls'])
const NO_TLS = new Set<InboundType>(['shadowsocks', 'snell', 'mixed'])
const HAS_BANDWIDTH = new Set<InboundType>(['hysteria2'])
// Which credential a single-user inbound presents.
const UUID_TYPES = new Set<InboundType>(['vless', 'vmess', 'tuic'])
const PW_TYPES = new Set<InboundType>(['trojan', 'hysteria2', 'anytls', 'tuic'])
const MULTI_USER_TYPES = new Set<InboundType>(['shadowsocks', 'mixed', 'vless', 'anytls', 'vmess', 'tuic', 'trojan', 'hysteria2'])

const VMESS_SECURITIES = ['auto', 'aes-128-gcm', 'chacha20-poly1305']

type FormVals = Record<string, any>

function parseALPN(value: unknown): string[] | undefined {
  if (typeof value !== 'string') return undefined
  const values = value.split(',').map((item) => item.trim()).filter(Boolean)
  return values.length ? [...new Set(values)] : undefined
}

function randomPort(): number {
  return Math.floor(Math.random() * 50000) + 10000
}

function supportsMultiUser(type: InboundType, method?: string): boolean {
  if (!MULTI_USER_TYPES.has(type)) return false
  if (type === 'shadowsocks') return (method || '').startsWith('2022-')
  return true
}

function toForm(ib: Inbound | null): FormVals {
  if (!ib) {
    return {
      type: 'shadowsocks',
      listen_port: randomPort(),
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
      reality_handshake_server: 'www.microsoft.com',
      reality_handshake_port: 443,
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
    enabled: ib.enabled,
    multi_user: s.multi_user ?? false,
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
    udp_relay_mode: s.tuic_udp_relay_mode ?? '',
    trojan_fallback_server: s.trojan_fallback?.server,
    trojan_fallback_port: s.trojan_fallback?.server_port,
    snell_version: s.snell_version ?? 5,
    snell_psk: s.snell_psk ?? '',
    snell_obfs_mode: s.snell_obfs_mode ?? 'none',
    snell_mode: s.snell_mode === 'default' ? '' : (s.snell_mode ?? ''),
    obfs_type: s.obfs_type ?? '',
    ignore_client_bandwidth: s.ignore_client_bandwidth ?? false,
    tls_mode,
    tls_alpn: tls.alpn?.join(', '),
    reality_handshake_server: tls.reality?.handshake_server ?? 'www.microsoft.com',
    reality_handshake_port: tls.reality?.handshake_server_port ?? 443,
    tls_server_name: tls.server_name,
    tls_cert_path: tls.certificate_path,
    tls_key_path: tls.key_path,
    tls_insecure: tls.insecure,
    acme_domain: tls.acme_domain,
    acme_email: tls.acme_email,
    transport_type: s.transport?.type === 'ws' || s.transport?.type === 'httpupgrade' ? s.transport.type : 'tcp',
    ws_path: s.transport?.path,
    ws_host: s.transport?.headers?.Host,
    max_early_data: s.transport?.max_early_data,
    early_data_header: s.transport?.early_data_header,
    tls_fingerprint: tls.fingerprint,
    packet_encoding: s.packet_encoding ?? 'xudp',
    anytls_udp_over_stream: s.anytls_udp_over_stream ?? false,
    }
}

// Build settings, preserving generated secrets from `base` (edit case).
function assembleSettings(base: InboundSettings, v: FormVals, type: InboundType): InboundSettings {
  const s: InboundSettings = JSON.parse(JSON.stringify(base || {}))

  // Multi-user is explicit and capability-gated. Snell and legacy
  // Shadowsocks always retain their stable shared credential.
  s.multi_user = !!v.multi_user && supportsMultiUser(type, v.method)
  s.single_user = !s.multi_user
  if (UUID_TYPES.has(type)) s.uuid = v.uuid || s.uuid
  if (PW_TYPES.has(type)) s.password = v.password || s.password
  if (type === 'shadowsocks' && v.ss_psk) s.ss_server_psk = v.ss_psk

  if (type === 'vless') {
    s.flow = v.flow || ''
    s.packet_encoding = v.packet_encoding === 'none' ? '' : 'xudp'
  }
  if (type === 'vmess') {
    s.vmess_security = v.vmess_security || 'auto'
    s.vmess_alter_id = v.vmess_alter_id ?? 0
  }
  if (type === 'shadowsocks') s.method = v.method
  if (HAS_BANDWIDTH.has(type)) {
    s.up_mbps = v.up_mbps || 0
    s.down_mbps = v.down_mbps || 0
    s.obfs_type = v.obfs_type === 'salamander' ? 'salamander' : ''
    s.obfs_password = v.obfs_type === 'salamander' ? (v.obfs_password || '') : ''
    s.ignore_client_bandwidth = !!v.ignore_client_bandwidth
  }
  if (type === 'tuic') {
    s.congestion_control = v.congestion_control || 'cubic'
    s.auth_timeout = v.auth_timeout || '3s'
    s.zero_rtt_handshake = !!v.zero_rtt_handshake
    s.heartbeat = v.heartbeat || '10s'
    // Client relay mode: native | quic (legacy TUIC v4 values are rejected).
    s.tuic_udp_relay_mode = v.udp_relay_mode === 'quic' ? 'quic' : v.udp_relay_mode === 'native' ? 'native' : ''
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
    s.snell_obfs_mode = v.snell_obfs_mode || 'none'
    s.snell_mode = v.snell_mode || ''
  }
  if ((type as string) === 'socks' || type === 'mixed') {
    if (s.multi_user) {
      // Do not reuse a previously shared login as the internal lockout
      // credential when switching to multi-user mode. The backend generates a
      // private fallback that is never included in subscriptions.
      if (!base.multi_user) {
        s.username = ''
        s.password = ''
      }
    } else {
      s.username = typeof v.username === 'string' ? v.username.trim() : ''
      s.password = typeof v.password === 'string' ? v.password.trim() : ''
    }
  }
  // transport (ws/httpupgrade only for vless/vmess/trojan)
  if (WS_TYPES.has(type) && (v.transport_type === 'ws' || v.transport_type === 'httpupgrade')) {
    s.transport = {
      type: v.transport_type,
      path: v.ws_path || '',
      headers: v.ws_host ? { Host: v.ws_host } : undefined,
      max_early_data: v.max_early_data || 0,
      early_data_header: v.early_data_header || '',
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
      s.tls = {
        enabled: true,
        server_name: v.reality_handshake_server,
        alpn,
        fingerprint: v.tls_fingerprint,
        reality: {
          ...prevReality,
          enabled: true,
          handshake_server: v.reality_handshake_server,
          handshake_server_port: v.reality_handshake_port || 443,
        },
      }
    } else if (mode === 'self') {
      // Keep any PEM already issued so the cert stays stable for live clients;
      // the backend generates it on first save.
      s.tls = {
        enabled: true,
        self_signed: true,
        server_name: v.tls_server_name,
        alpn,
        certificate: s.tls?.certificate,
        key: s.tls?.key,
        insecure: true,
      }
    } else if (mode === 'tls') {
      s.tls = {
        enabled: true,
        server_name: v.tls_server_name,
        alpn,
        fingerprint: v.tls_fingerprint,
        certificate_path: v.tls_cert_path,
        key_path: v.tls_key_path,
        insecure: !!v.tls_insecure,
      }
    } else if (mode === 'acme') {
      s.tls = {
        enabled: true,
        acme_domain: v.acme_domain,
        acme_email: v.acme_email,
        server_name: v.acme_domain,
        alpn,
      }
    }
  }
  return s
}

export default function InboundForm({
  serverId,
  inbound,
  open,
  onClose,
  onSaved,
}: {
  serverId: number
  inbound: Inbound | null
  open: boolean
  onClose: () => void
  onSaved: () => void
}) {
  const [form] = Form.useForm()
  const type: InboundType = Form.useWatch('type', form) ?? 'shadowsocks'
  const tlsMode = Form.useWatch('tls_mode', form)
  const transportType = Form.useWatch('transport_type', form)
  const snellVersion = Form.useWatch('snell_version', form)
  const obfsType = Form.useWatch('obfs_type', form)
  const method = Form.useWatch('method', form)
  const multiUser = !!Form.useWatch('multi_user', form) && supportsMultiUser(type, method)

  useEffect(() => {
    if (!open) return
    // Reset first: toForm() omits many keys (tag, credentials, TLS paths...),
    // and without a reset those survive from the previously opened inbound —
    // 新增协议 would inherit the last one's tag and secrets.
    form.resetFields()
    form.setFieldsValue(toForm(inbound))
  }, [open, inbound])

  const tlsModeOptions = (t: InboundType = type) => {
    const opts: { value: string; label: string }[] = []
    if (!TLS_REQUIRED.has(t)) opts.push({ value: 'none', label: '无 TLS' })
    if (REALITY_TYPES.has(t)) opts.push({ value: 'reality', label: 'REALITY' })
    opts.push({ value: 'self', label: '自签证书　面板自动生成' })
    opts.push({ value: 'tls', label: 'TLS 证书　自己准备的文件' })
    opts.push({ value: 'acme', label: 'ACME 自动证书' })
    return opts
  }

  const submit = async () => {
    const v = await form.validateFields()
    const settings = assembleSettings(inbound?.settings || {}, v, v.type)
    const body = {
      type: v.type as InboundType,
      tag: v.tag,
      listen_port: v.listen_port || randomPort(),
      enabled: v.enabled,
      settings,
    }
    const editing = inbound
    // Close the modal immediately; the config is delivered in the background
    // behind a "配置下发中" spinner toast, so the form never appears to hang.
    onClose()
    await applyWithToast(`inbound-${editing ? editing.id : 'new'}`, () =>
      editing ? updateInbound(serverId, editing.id, body) : createInbound(serverId, body),
    )
    onSaved()
  }

  const showTLS = !NO_TLS.has(type)
  const showTransport = WS_TYPES.has(type)

  return (
    <Modal
      title={inbound ? '编辑协议入站' : '新增协议入站'}
      open={open}
      onOk={submit}
      onCancel={onClose}
      width={620}
      destroyOnHidden
    >
      <Form form={form} layout="vertical">
        <Form.Item name="type" label="协议" rules={[{ required: true }]}>
          <Select
            options={TYPES}
            disabled={!!inbound}
            onChange={(t: InboundType) => {
              const allowed = tlsModeOptions(t).map((o) => o.value)
              if (!allowed.includes(form.getFieldValue('tls_mode'))) {
                form.setFieldValue('tls_mode', allowed[0])
              }
            }}
          />
        </Form.Item>

        <Form.Item name="tag" label="标签" extra="留空时自动生成协议名加随机字符，例如 socks-a1b2c3">
          <Input placeholder="留空自动生成" />
        </Form.Item>
        <Form.Item name="listen_port" label="端口" extra="已全自动生成随机端口，亦可留空或手动自定义修改">
          <InputNumber
            min={1}
            max={65535}
            placeholder="自动随机分配"
            style={{ width: '100%' }}
          />
        </Form.Item>

        {supportsMultiUser(type, method) ? (
          <Form.Item
            name="multi_user"
            label="单端口多用户"
            valuePropName="checked"
            extra="开启后，每个获授权用户使用独立凭证；停用、到期或取消授权会自动从节点配置移除。"
          >
            <Switch />
          </Form.Item>
        ) : (
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 16 }}
            message={type === 'snell' ? 'Snell 固定使用单凭证模式' : '当前加密方式只支持单凭证'}
          />
        )}

        {multiUser && (
          <Alert
            type="success"
            showIcon
            style={{ marginBottom: 16 }}
            message="用户凭证由面板自动生成"
            description="节点级固定 UUID/密码在多用户模式下不会使用，请通过具体用户的订阅获取有效节点。"
          />
        )}

        {!multiUser && UUID_TYPES.has(type) && (
          <Form.Item label="UUID">
            <Space.Compact style={{ width: '100%' }}>
              <Form.Item name="uuid" noStyle>
                <Input placeholder="留空自动生成" />
              </Form.Item>
              <Button onClick={() => form.setFieldValue('uuid', randomUUID())}>随机</Button>
            </Space.Compact>
          </Form.Item>
        )}
        {!multiUser && PW_TYPES.has(type) && (
          <Form.Item label={type === 'snell' ? 'userkey' : '密码'}>
            <Space.Compact style={{ width: '100%' }}>
              <Form.Item name="password" noStyle>
                <Input placeholder="留空自动生成" />
              </Form.Item>
              <Button onClick={() => form.setFieldValue('password', randomHex(16))}>随机</Button>
            </Space.Compact>
          </Form.Item>
        )}
        {type === 'shadowsocks' && (
          <Form.Item label="密码 / PSK" extra="2022 算法需要 Base64 格式密钥">
            <Space.Compact style={{ width: '100%' }}>
              <Form.Item name="ss_psk" noStyle>
                <Input placeholder="留空自动生成" />
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

        {type === 'anytls' && (
          <Form.Item
            name="anytls_udp_over_stream"
            label="UDP 走 TLS 流"
            valuePropName="checked"
            extra="AnyTLS 的 UDP 始终通过 TLS 流传输；开启后在 anytls:// 链接中输出 udp_over_stream=1 供客户端识别"
          >
            <Switch />
          </Form.Item>
        )}

        {type === 'vless' && (
          <Form.Item name="flow" label="Flow">
            <Select
              options={[
                { value: '', label: '无' },
                { value: 'xtls-rprx-vision', label: 'xtls-rprx-vision' },
              ]}
            />
          </Form.Item>
        )}
        {type === 'vless' && (
          <Form.Item
            name="packet_encoding"
            label="UDP 编码"
            valuePropName="checked"
            extra="xudp 是更可靠的 UDP 传输编码，防止游戏/通话丢包（默认开启）"
            getValueFromEvent={(checked: boolean) => (checked ? 'xudp' : 'none')}
            getValueProps={(value: string) => ({ checked: value !== 'none' })}
          >
            <Switch checkedChildren="xudp" unCheckedChildren="tcp" />
          </Form.Item>
        )}
        {type === 'vmess' && (
          <>
            <Form.Item
              name="vmess_security"
              label="客户端加密"
              rules={[{ required: true }]}
            >
              <Select options={VMESS_SECURITIES.map((value) => ({ value, label: value }))} />
            </Form.Item>
            <Form.Item
              name="vmess_alter_id"
              label="Alter ID"
              rules={[{ required: true }]}
            >
              <InputNumber min={0} precision={0} style={{ width: '100%' }} />
            </Form.Item>
          </>
        )}
        {type === 'shadowsocks' && (
          <Form.Item name="method" label="加密方式" rules={[{ required: true }]}>
            <Select
              options={SS_METHODS.map((m) => ({ value: m, label: m }))}
              onChange={(value: string) => {
                if (!value.startsWith('2022-')) form.setFieldValue('multi_user', false)
              }}
            />
          </Form.Item>
        )}
        {((type as string) === 'socks' || type === 'mixed') && !multiUser && (
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
              <Input placeholder={type === 'mixed' ? 'Mixed 用户名（留空则免登录）' : 'SOCKS5 用户名（留空则免登录）'} />
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
              <Input.Password placeholder={type === 'mixed' ? 'Mixed 密码（留空则免登录）' : 'SOCKS5 密码（留空则免登录）'} />
            </Form.Item>
          </>
        )}
        {HAS_BANDWIDTH.has(type) && (
          <>
            <Form.Item name="up_mbps" label="上行 Mbps">
              <InputNumber min={0} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="down_mbps" label="下行 Mbps">
              <InputNumber min={0} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="ignore_client_bandwidth" label="忽略客户端带宽" valuePropName="checked" extra="开启后始终使用上方设置的速度，忽略客户端上报值">
              <Switch />
            </Form.Item>
            <Form.Item name="obfs_type" label="混淆类型">
              <Select options={[
                { value: '', label: '无' },
                { value: 'salamander', label: 'salamander' },
              ]} />
            </Form.Item>
            {obfsType === 'salamander' && (
              <Form.Item name="obfs_password" label="混淆密码">
                <Input.Password placeholder="salamander 混淆密码" />
              </Form.Item>
            )}
          </>
        )}
        {type === 'tuic' && (
          <>
            <Form.Item name="congestion_control" label="拥塞控制" rules={[{ required: true }]}>
              <Select options={['cubic', 'new_reno', 'bbr'].map((v) => ({ value: v, label: v }))} />
            </Form.Item>
            <Form.Item
              name="auth_timeout"
              label="认证超时"
              rules={[{ required: true }]}
              extra="时长格式，例如 3s、1500ms"
            >
              <Input placeholder="3s" />
            </Form.Item>
            <Form.Item
              name="heartbeat"
              label="心跳间隔"
              rules={[{ required: true }]}
              extra="时长格式，例如 10s"
            >
              <Input placeholder="10s" />
            </Form.Item>
            <Form.Item
              name="zero_rtt_handshake"
              label="启用 0-RTT"
              valuePropName="checked"
            >
              <Switch />
            </Form.Item>
            <Form.Item
              name="udp_relay_mode"
              label="UDP 中继模式"
              extra="native = UDP 走 QUIC 数据报（默认）；quic = 走 QUIC 流。留空表示不指定"
            >
              <Select
                allowClear
                options={[
                  { value: '', label: '不指定（默认 native）' },
                  { value: 'native', label: 'native' },
                  { value: 'quic', label: 'quic' },
                ]}
              />
            </Form.Item>
          </>
        )}
        {type === 'snell' && (
          <>
            <Form.Item name="snell_version" label="Snell 版本">
              <Select options={[{ value: 5, label: 'v5' }, { value: 6, label: 'v6' }]} />
            </Form.Item>
            {snellVersion === 6 ? (
              <Form.Item name="snell_mode" label="模式" extra="留空表示不指定（sing-box 默认）">
                <Select
                  allowClear
                  options={[
                    { value: '', label: '不指定' },
                    { value: 'unshaped', label: 'unshaped' },
                    { value: 'unsafe-raw', label: 'unsafe-raw' },
                  ]}
                />
              </Form.Item>
            ) : (
              <Form.Item name="snell_obfs_mode" label="混淆">
                <Select options={[{ value: 'none', label: '无' }, { value: 'http', label: 'http' }]} />
              </Form.Item>
            )}
            <Form.Item label="PSK">
              <Space.Compact style={{ width: '100%' }}>
                <Form.Item name="snell_psk" noStyle>
                  <Input placeholder="留空自动生成，或点右侧生成" />
                </Form.Item>
                <Button onClick={() => form.setFieldValue('snell_psk', randomHex(16))}>生成</Button>
              </Space.Compact>
            </Form.Item>
          </>
        )}
        {showTransport && (
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
                {transportType === 'ws' && (
                  <>
                    <Form.Item
                      name="max_early_data"
                      label="WS 0-RTT (max_early_data)"
                      extra="留空或 0 表示关闭 early data；开启后与 CDN 首包加速配合可降低延迟"
                    >
                      <InputNumber min={0} precision={0} style={{ width: '100%' }} placeholder="例如 1024" />
                    </Form.Item>
                    <Form.Item name="early_data_header" label="Early Data 头名">
                      <Input placeholder="Sec-WebSocket-Protocol" />
                    </Form.Item>
                  </>
                )}
              </>
            )}
          </>
        )}

        {type === 'trojan' && (
          <>
            <Divider orientation="left">Trojan 回落（可选）</Divider>
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 12 }}
              message="不需要回落时请留空"
              description="仅转发无法通过 Trojan 验证的 TLS 流量。"
            />
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
              <Input placeholder="127.0.0.1" />
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
              <InputNumber min={1} max={65535} precision={0} style={{ width: '100%' }} />
            </Form.Item>
          </>
        )}

        {showTLS && (
          <>
            <Divider orientation="left">TLS</Divider>
            <Form.Item
              name="tls_mode"
              label="TLS 模式"
              rules={[{ required: true }]}
            >
              <Select options={tlsModeOptions()} />
            </Form.Item>
            <Form.Item
              name="tls_alpn"
              label="ALPN"
              extra="多个值用英文逗号分隔，例如 h3 或 h2,http/1.1"
            >
              <Input placeholder={type === 'tuic' ? 'h3' : 'h2, http/1.1'} />
            </Form.Item>
            {type !== 'hysteria2' && type !== 'tuic' && (
              <Form.Item name="tls_fingerprint" label="uTLS 指纹">
                <Select
                  options={['chrome', 'firefox', 'safari', 'ios', 'random', 'randomized'].map((v) => ({ value: v, label: v }))}
                />
              </Form.Item>
            )}
            {tlsMode === 'reality' && (
              <>
                <Form.Item name="reality_handshake_server" label="目标握手域名" rules={[{ required: true }]}>
                  <Input placeholder="www.microsoft.com" />
                </Form.Item>
                <Form.Item name="reality_handshake_port" label="握手端口">
                  <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                </Form.Item>
              </>
            )}
            {tlsMode === 'self' && (
              <>
                <Form.Item
                  name="tls_server_name"
                  label="SNI / 证书域名"
                  extra="支持自定义域名；客户端将自动跳过校验"
                >
                  <Input placeholder="www.bing.com" />
                </Form.Item>
              </>
            )}
            {tlsMode === 'tls' && (
              <>
                <Form.Item name="tls_server_name" label="SNI / server_name">
                  <Input placeholder="example.com" />
                </Form.Item>
                <Form.Item name="tls_cert_path" label="证书路径">
                  <Input placeholder="/etc/ssl/example.com.crt" />
                </Form.Item>
                <Form.Item name="tls_key_path" label="私钥路径">
                  <Input placeholder="/etc/ssl/example.com.key" />
                </Form.Item>
                <Form.Item
                  name="tls_insecure"
                  label="客户端跳过证书验证"
                  valuePropName="checked"
                >
                  <Switch />
                </Form.Item>
              </>
            )}
            {tlsMode === 'acme' && (
              <>
                <Form.Item name="acme_domain" label="ACME 域名" rules={[{ required: true }]}>
                  <Input placeholder="example.com" />
                </Form.Item>
                <Form.Item name="acme_email" label="ACME 邮箱">
                  <Input placeholder="you@example.com" />
                </Form.Item>
              </>
            )}
          </>
        )}

        <Divider />
        <Form.Item name="enabled" label="启用" valuePropName="checked">
          <Switch />
        </Form.Item>
      </Form>
    </Modal>
  )
}
