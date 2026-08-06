import { memo, useCallback, useEffect, useMemo, useState } from 'react'
import {
  Button,
  Card,
  Checkbox,
  Empty,
  Form,
  Grid,
  Input,
  InputNumber,
  Modal,
  Radio,
  Select,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  message,
} from 'antd'
import {
  DownOutlined,
  EditOutlined,
  PlusOutlined,
  RightOutlined,
  SaveOutlined,
  UndoOutlined,
} from '@ant-design/icons'
import {
  createCustomNode,
  deleteCustomNode,
  errMsg,
  getUserAccess,
  listServers,
  updateCustomNode,
  updateUserAccess,
} from '../../api'
import type { CustomNode } from '../../api'
import type { Inbound, Server } from '../../types'

// ======================= 节点分配 =======================

// protocolOf extracts the share-link scheme (used to label custom nodes).
function protocolOf(link: string): string {
  const m = /^([a-z0-9+.-]+):\/\//i.exec(link.trim())
  return m ? m[1].toLowerCase() : '-'
}

// nodeTypeLabel maps a protocol (or share-link scheme) to a colored tag.
const nodeTypeLabel: Record<string, { label: string; color: string }> = {
  vless: { label: 'VLESS', color: 'blue' },
  vmess: { label: 'VMess', color: 'purple' },
  ss: { label: 'SS', color: 'green' },
  shadowsocks: { label: 'SS', color: 'green' },
  trojan: { label: 'Trojan', color: 'geekblue' },
  hysteria2: { label: 'Hysteria2', color: 'cyan' },
  hysteria: { label: 'Hysteria', color: 'cyan' },
  tuic: { label: 'TUIC', color: 'orange' },
  anytls: { label: 'AnyTLS', color: 'magenta' },
  socks: { label: 'SOCKS', color: 'default' },
  socks5: { label: 'SOCKS5', color: 'default' },
  snell: { label: 'Snell', color: 'gold' },
  mixed: { label: 'Mixed', color: 'volcano' },
}

type AccessKind = 'managed' | 'custom'

interface AccessNodeItem {
  id: number
  kind: AccessKind
  name: string
  detail: string
  protocol: string
  enabled: boolean
}

interface AccessGroup {
  key: string
  label: string
  meta: string
  items: AccessNodeItem[]
}

// AccessItem renders one node as a pill/chip inside the assign dialog.
// Memoized so ticking one chip only re-renders that chip, not every node.
// onToggle takes (item, checked) so the parent can pass a stable useCallback
// reference — an inline (checked) => toggleItem(item, checked) arrow would get
// a fresh identity every render and defeat the memo (all chips would re-render).
const AccessItem = memo(function AccessItem({ item, checked, disabled, onToggle }: {
  item: AccessNodeItem
  checked: boolean
  disabled: boolean
  onToggle: (item: AccessNodeItem, checked: boolean) => void
}) {
  return (
    <label className={`access-chip${checked ? ' checked' : ''}${disabled ? ' disabled' : ''}`} title={item.detail}>
      <Checkbox checked={checked} disabled={disabled} onChange={(event) => onToggle(item, event.target.checked)} />
      <span className="access-chip-name">{item.name}</span>
      <Tag color={nodeTypeLabel[item.protocol]?.color} className="access-chip-tag">
        {nodeTypeLabel[item.protocol]?.label || item.protocol || '-'}
      </Tag>
      {!item.enabled ? <Tag className="access-chip-tag">停用</Tag> : null}
    </label>
  )
})

// AssignModal is a controlled dialog that edits one user's node assignment.
// It is opened from a row in the users table (open + userId), so the admin no
// longer has to first pick a user from a separate panel.
export function AssignModal({ userId, userEmail, nodes, open, onClose, onSaved }: {
  userId?: number
  userEmail?: string
  nodes: CustomNode[]
  open: boolean
  onClose: () => void
  onSaved: () => void
}) {
  const [servers, setServers] = useState<Server[]>([])
  const [loadedInboundIDs, setLoadedInboundIDs] = useState<number[]>([])
  const [loadedCustomNodeIDs, setLoadedCustomNodeIDs] = useState<number[]>([])
  const [inboundIDs, setInboundIDs] = useState<number[]>([])
  const [customNodeIDs, setCustomNodeIDs] = useState<number[]>([])
  const [accessLoading, setAccessLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(() => new Set())

  useEffect(() => {
    if (!open) return
    listServers()
      .then(setServers)
      .catch((e) => message.error(errMsg(e)))
  }, [open])

  useEffect(() => {
    if (!open || !userId) {
      setLoadedInboundIDs([])
      setLoadedCustomNodeIDs([])
      setInboundIDs([])
      setCustomNodeIDs([])
      return
    }
    let active = true
    setAccessLoading(true)
    getUserAccess(userId)
      .then((access) => {
        if (!active) return
        setLoadedInboundIDs(access.inbound_ids)
        setLoadedCustomNodeIDs(access.custom_node_ids)
        setInboundIDs(access.inbound_ids)
        setCustomNodeIDs(access.custom_node_ids)
      })
      .catch((e) => {
        if (!active) return
        setLoadedInboundIDs([])
        setLoadedCustomNodeIDs([])
        setInboundIDs([])
        setCustomNodeIDs([])
        message.error(errMsg(e))
      })
      .finally(() => {
        if (active) setAccessLoading(false)
      })
    return () => {
      active = false
    }
  }, [open, userId])

  const groups = useMemo<AccessGroup[]>(() => {
    const managed = servers.map((server) => ({
      key: `server:${server.id}`,
      label: server.name,
      meta: [server.region, server.address || server.public_ip].filter(Boolean).join(' · '),
      items: (server.inbounds ?? []).map((inbound: Inbound) => ({
        id: inbound.id,
        kind: 'managed' as const,
        name: inbound.tag || nodeTypeLabel[inbound.type]?.label || inbound.type,
        detail: `${server.address || server.public_ip || '-'}:${inbound.listen_port}`,
        protocol: inbound.type,
        enabled: inbound.enabled,
      })),
    })).filter((group) => group.items.length > 0)
    if (nodes.length === 0) return managed
    return [...managed, {
      key: 'custom',
      label: '其他节点',
      meta: '订阅节点',
      items: nodes.map((node) => ({
        id: node.id,
        kind: 'custom' as const,
        name: node.name || '未命名节点',
        detail: node.address ? `${node.address}:${node.port}` : '分享链接',
        protocol: node.link?.trim() ? protocolOf(node.link) : node.protocol,
        enabled: node.enabled,
      })),
    }]
  }, [nodes, servers])

  // Expand every group when the dialog opens so all nodes are visible at once
  // (rows are memoized, so ticking a chip does not re-render the whole list).
  useEffect(() => {
    if (open) setExpandedGroups(new Set(groups.map((g) => g.key)))
  }, [open, groups])

  const selectedInboundSet = useMemo(() => new Set(inboundIDs), [inboundIDs])
  const selectedCustomSet = useMemo(() => new Set(customNodeIDs), [customNodeIDs])
  const isSelected = (item: AccessNodeItem) => (
    item.kind === 'managed' ? selectedInboundSet.has(item.id) : selectedCustomSet.has(item.id)
  )

  const totalNodeCount = groups.reduce((sum, group) => sum + group.items.length, 0)
  const selectedCount = inboundIDs.length + customNodeIDs.length
  const isDirty = useMemo(() => {
    const equal = (a: number[], b: number[]) => {
      if (a.length !== b.length) return false
      const selected = new Set(a)
      return b.every((id) => selected.has(id))
    }
    return !equal(loadedInboundIDs, inboundIDs) || !equal(loadedCustomNodeIDs, customNodeIDs)
  }, [customNodeIDs, inboundIDs, loadedCustomNodeIDs, loadedInboundIDs])

  const setIDs = useCallback((kind: AccessKind, ids: number[], checked: boolean) => {
    const update = (current: number[]) => {
      const next = new Set(current)
      for (const id of ids) {
        if (checked) next.add(id)
        else next.delete(id)
      }
      return [...next]
    }
    if (kind === 'managed') setInboundIDs(update)
    else setCustomNodeIDs(update)
  }, [])

  const toggleItem = useCallback((item: AccessNodeItem, checked: boolean) => {
    setIDs(item.kind, [item.id], checked)
  }, [setIDs])

  const toggleGroupExpanded = (key: string) => {
    setExpandedGroups((current) => {
      const next = new Set(current)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const discardChanges = () => {
    setInboundIDs(loadedInboundIDs)
    setCustomNodeIDs(loadedCustomNodeIDs)
  }

  const handleClose = () => {
    if (!isDirty) {
      onClose()
      return
    }
    Modal.confirm({
      title: '放弃未保存的更改？',
      content: '关闭会丢弃当前节点选择。',
      okText: '放弃并关闭',
      cancelText: '继续编辑',
      okType: 'danger',
      onOk: onClose,
    })
  }

  const save = async () => {
    if (!userId || saving || !isDirty) return
    setSaving(true)
    try {
      const access = await updateUserAccess(userId, {
        inbound_ids: inboundIDs,
        custom_node_ids: customNodeIDs,
      })
      setLoadedInboundIDs(access.inbound_ids)
      setLoadedCustomNodeIDs(access.custom_node_ids)
      setInboundIDs(access.inbound_ids)
      setCustomNodeIDs(access.custom_node_ids)
      message.success(`已保存 ${userEmail ?? ''} 的节点分配`)
      onSaved()
      onClose()
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      title={userEmail ? `节点分配 - ${userEmail}` : '节点分配'}
      open={open}
      onCancel={handleClose}
      footer={null}
      width={820}
      style={{ maxWidth: 'calc(100vw - 16px)' }}
      destroyOnClose
    >
      <div className="access-target-bar" style={{ marginBottom: 12 }}>
        <Tag color="blue">已选 {selectedCount} / {totalNodeCount}</Tag>
      </div>
      <Spin spinning={accessLoading}>
        <div className="access-modal-body">
          {groups.length === 0 ? (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无可用节点，请先在「主机」页创建入站或在「其他节点」添加" />
          ) : groups.map((group) => {
            const groupSelectedCount = group.items.filter(isSelected).length
            const expanded = expandedGroups.has(group.key)
            const kinds = group.items.reduce<Record<AccessKind, number[]>>((result, item) => {
              result[item.kind].push(item.id)
              return result
            }, { managed: [], custom: [] })
            return (
              <section className="access-modal-group" key={group.key}>
                <div className="access-modal-group-head">
                  <button
                    type="button"
                    className="access-group-toggle"
                    onClick={() => toggleGroupExpanded(group.key)}
                    title={expanded ? '收起' : '展开'}
                  >
                    {expanded ? <DownOutlined /> : <RightOutlined />}
                  </button>
                  <Checkbox
                    checked={groupSelectedCount === group.items.length && group.items.length > 0}
                    indeterminate={groupSelectedCount > 0 && groupSelectedCount < group.items.length}
                    disabled={saving}
                    onChange={(event) => {
                      setIDs('managed', kinds.managed, event.target.checked)
                      setIDs('custom', kinds.custom, event.target.checked)
                    }}
                  />
                  <strong>{group.label}</strong>
                  {group.meta ? <small>{group.meta}</small> : null}
                  <span className="access-group-count">{groupSelectedCount} / {group.items.length}</span>
                </div>
                {expanded ? (
                  <div className="access-modal-items">
                    {group.items.map((item) => (
                      <AccessItem
                        key={`${item.kind}:${item.id}`}
                        item={item}
                        checked={isSelected(item)}
                        disabled={saving}
                        onToggle={toggleItem}
                      />
                    ))}
                  </div>
                ) : null}
              </section>
            )
          })}
        </div>
      </Spin>
      <div className="access-modal-footer">
        <span className={isDirty ? 'dirty' : ''}>{isDirty ? '有未保存更改' : '分配已同步'}</span>
        <Space>
          <Button icon={<UndoOutlined />} disabled={!isDirty || saving} onClick={discardChanges}>撤销</Button>
          <Button onClick={handleClose}>取消</Button>
          <Button type="primary" icon={<SaveOutlined />} loading={saving} disabled={!isDirty || accessLoading} onClick={() => void save()}>保存</Button>
        </Space>
      </div>
    </Modal>
  )
}


// ======================= 自定义节点 =======================

const HAS_UUID = ['vless', 'vmess', 'tuic']
const HAS_PASSWORD = ['trojan', 'anytls', 'tuic', 'hysteria2', 'hysteria', 'shadowsocks']
const HAS_TLS = ['vless', 'vmess', 'trojan', 'anytls', 'tuic', 'hysteria2', 'hysteria']
const TLS_REALITY = ['vless', 'vmess', 'trojan']
const HAS_TRANSPORT = ['vless', 'vmess', 'trojan']
const HAS_BANDWIDTH = ['hysteria2', 'hysteria']

export function CustomNodesPanel({ nodes, onNodesChange }: { nodes: CustomNode[]; onNodesChange: () => void }) {
  const screens = Grid.useBreakpoint()
  const isMobile = !screens.md
  const [nodeOpen, setNodeOpen] = useState(false)
  const [editingNode, setEditingNode] = useState<CustomNode | null>(null)
  const [nodeForm] = Form.useForm()
  const nodeMode = Form.useWatch('node_mode', nodeForm) ?? 'link'
  const nodeProtocol = Form.useWatch('protocol', nodeForm)
  const nodeTransport = Form.useWatch('transport', nodeForm)
  const nodeTLSMode = Form.useWatch('tls_mode', nodeForm)
  const nodeSnellVersion = Form.useWatch('snell_version', nodeForm)

  const openNodeCreate = () => {
    setEditingNode(null)
    nodeForm.resetFields()
    nodeForm.setFieldsValue({ enabled: true, node_mode: 'link', sort_order: 0, tls_mode: 'tls', transport: 'tcp', snell_version: 5, snell_obfs_mode: 'none', snell_mode: '', method: '2022-blake3-aes-128-gcm' })
    setNodeOpen(true)
  }

  const openNodeEdit = (n: CustomNode) => {
    setEditingNode(n)
    const p = n.params ?? {}
    nodeForm.resetFields()
    nodeForm.setFieldsValue({
      name: n.name,
      node_mode: n.link && n.link.trim() ? 'link' : 'manual',
      link: n.link,
      protocol: n.protocol,
      address: n.address,
      port: n.port,
      uuid: p.uuid,
      password: p.password,
      method: p.method,
      flow: p.flow,
      tls_mode: p.tls ?? 'tls',
      sni: p.sni,
      pbk: p.pbk,
      sid: p.sid,
      fingerprint: p.fingerprint,
      alpn: p.alpn,
      insecure: !!p.insecure,
      transport: p.transport ?? 'tcp',
      path: p.path,
      host: p.host,
      congestion_control: p.congestion_control,
      udp_relay_mode: p.udp_relay_mode,
      udp_over_stream: !!p.udp_over_stream,
      ss_plugin: p.ss_plugin,
      obfs: p.obfs,
      obfs_password: p.obfs_password,
      up_mbps: p.up_mbps,
      down_mbps: p.down_mbps,
      psk: p.psk,
      snell_version: p.version ?? 5,
      snell_obfs_mode: p.obfs_mode ?? 'none',
      snell_mode: p.mode === 'default' ? '' : (p.mode ?? ''),
      username: p.username,
      all_users: n.all_users,
      enabled: n.enabled,
      sort_order: n.sort_order,
    })
    setNodeOpen(true)
  }

  const submitNode = async () => {
    const v = await nodeForm.validateFields()
    const body: Record<string, unknown> = {
      name: v.name,
      // 归属在「节点分配」里统一管理：新增默认不分配给任何用户，
      // 管理员在节点分配弹窗里勾选后才对指定用户可见；编辑保持原受众。
      all_users: editingNode ? editingNode.all_users : false,
      user_ids: [],
      excluded_user_ids: editingNode && editingNode.all_users ? editingNode.excluded_user_ids ?? [] : [],
      enabled: v.enabled,
      sort_order: v.sort_order ?? 0,
    }
    if (v.node_mode === 'link' && v.link && v.link.trim()) {
      Object.assign(body, {
        link: v.link.trim(),
        protocol: '',
        address: '',
        port: undefined,
        params: null,
      })
    } else {
      Object.assign(body, {
        link: '',
        protocol: v.protocol,
        address: typeof v.address === 'string' ? v.address.trim() : '',
        port: v.port,
        params: {
          uuid: v.uuid,
          password: v.password,
          method: v.method,
          flow: v.flow,
          tls: v.tls_mode,
          sni: v.sni,
          pbk: v.pbk,
          sid: v.sid,
          fingerprint: v.fingerprint,
          insecure: v.insecure,
          transport: v.transport,
          path: v.path,
          host: v.host,
          alpn: v.alpn,
          congestion_control: v.congestion_control,
          udp_relay_mode: v.udp_relay_mode,
          udp_over_stream: !!v.udp_over_stream,
          ss_plugin: v.ss_plugin,
          obfs: v.obfs,
          obfs_password: v.obfs_password,
          up_mbps: v.up_mbps,
          down_mbps: v.down_mbps,
          psk: v.psk,
          version: v.snell_version,
          obfs_mode: v.snell_obfs_mode,
          mode: v.snell_mode,
          username: v.username,
        },
      })
    }
    try {
      if (editingNode) await updateCustomNode(editingNode.id, body)
      else await createCustomNode(body)
      message.success('已保存')
      setNodeOpen(false)
      onNodesChange()
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  const removeNode = (n: CustomNode) => {
    Modal.confirm({
      title: `删除自定义节点 ${n.name || n.link.slice(0, 24)}?`,
      okType: 'danger',
      onOk: async () => {
        await deleteCustomNode(n.id)
        onNodesChange()
      },
    })
  }

  const nodeColumns = [
    {
      title: '名称',
      dataIndex: 'name',
      render: (v: string) => v || <span style={{ color: '#999' }}>（未命名）</span>,
    },
    {
      title: '类型',
      width: 100,
      render: (_: unknown, n: CustomNode) => {
        const key = n.link && n.link.trim() ? protocolOf(n.link) : n.protocol
        const t = nodeTypeLabel[key]
        return t ? <Tag color={t.color}>{t.label}</Tag> : <Tag>{key || '-'}</Tag>
      },
    },
    {
      title: '操作',
      width: isMobile ? 96 : 130,
      render: (_: unknown, n: CustomNode) => (
        <Space size={isMobile ? 2 : 8}>
          <Button size="small" type="link" style={{ padding: '0 4px' }} onClick={() => openNodeEdit(n)}>编辑</Button>
          <Button size="small" type="link" danger style={{ padding: '0 4px' }} onClick={() => removeNode(n)}>删除</Button>
        </Space>
      ),
    },
  ]

  return (
    <>
      <Card
        title="其他节点"
        size="small"
        extra={<Button type="primary" size="small" icon={<PlusOutlined />} onClick={openNodeCreate}>新增节点</Button>}
        style={{ flex: 1, display: 'flex', flexDirection: 'column', width: '100%' }}
        styles={{ body: { flex: 1, overflow: 'auto' } }}
      >
        <Table
          rowKey="id"
          size="small"
          className="compact-rows"
          dataSource={nodes}
          pagination={false}
          scroll={{ x: isMobile ? undefined : 560, y: 340 }}
          columns={nodeColumns}
          locale={{ emptyText: '暂无其他节点。可粘贴朋友分享的链接（vless://、ss://、trojan:// 等），合并进订阅输出。' }}
        />
      </Card>

      {/* 自定义节点 modal */}
      <Modal
        title={editingNode ? '编辑自定义节点' : '新增自定义节点'}
        open={nodeOpen}
        onOk={submitNode}
        onCancel={() => setNodeOpen(false)}
        destroyOnClose
        width={560}
        style={{ maxWidth: 'calc(100vw - 16px)', top: 24 }}
        styles={{ body: { maxHeight: 'calc(100vh - 200px)', overflowY: 'auto', paddingInline: 4 } }}
      >
        <Form form={nodeForm} layout="vertical">
          <Form.Item name="node_mode" label="定义方式" extra="有分享链接的直接粘贴；Snell 等没有标准链接的协议请选“手动填写”。">
            <Radio.Group
              options={[
                { value: 'link', label: '分享链接' },
                { value: 'manual', label: '手动填写' },
              ]}
            />
          </Form.Item>
          {nodeMode === 'link' && (
            <Form.Item name="link" label="分享链接" extra="支持 vless:// vmess:// ss:// trojan:// hysteria2:// hysteria:// tuic:// anytls:// socks5://，保存时自动解析。">
              <Input.TextArea rows={2} placeholder="vless://..." autoSize={{ minRows: 2, maxRows: 4 }} />
            </Form.Item>
          )}
          {nodeMode === 'manual' && (
            <>
              <Form.Item name="protocol" label="协议" rules={[{ required: true, message: '请选择协议' }]}>
                <Select
                  placeholder="选择协议"
                  options={[
                    { value: 'vless', label: 'VLESS' },
                    { value: 'vmess', label: 'VMess' },
                    { value: 'trojan', label: 'Trojan' },
                    { value: 'anytls', label: 'AnyTLS' },
                    { value: 'shadowsocks', label: 'Shadowsocks' },
                    { value: 'tuic', label: 'TUIC' },
                    { value: 'hysteria2', label: 'Hysteria2' },
                    { value: 'hysteria', label: 'Hysteria' },
                    { value: 'snell', label: 'Snell' },
                    { value: 'mixed', label: 'Mixed (HTTP+SOCKS5)' },
                  ]}
                />
              </Form.Item>
              <Space.Compact style={{ width: '100%', marginBottom: 16 }}>
                <Form.Item name="address" noStyle rules={[{ required: true, message: '请填写地址' }]}>
                  <Input placeholder="地址（域名或 IP）" style={{ width: '70%' }} />
                </Form.Item>
                <Form.Item name="port" noStyle rules={[{ required: true, message: '请填写端口' }]}>
                  <InputNumber min={1} max={65535} placeholder="端口" style={{ width: '30%' }} />
                </Form.Item>
              </Space.Compact>

              {HAS_UUID.includes(nodeProtocol) && (
                <Form.Item name="uuid" label="UUID" rules={[{ required: true, message: '请填写 UUID' }]}>
                  <Input placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" />
                </Form.Item>
              )}
              {HAS_PASSWORD.includes(nodeProtocol) && (
                <Form.Item name="password" label={nodeProtocol === 'shadowsocks' ? '密码 / PSK' : '密码'} rules={[{ required: true, message: '请填写密码' }]}>
                  <Input.Password placeholder={nodeProtocol === 'shadowsocks' ? 'SS 密码（2022 算法为 Base64 密钥）' : '密码'} />
                </Form.Item>
              )}
              {nodeProtocol === 'shadowsocks' && (
                <Form.Item name="method" label="加密方式">
                  <Select
                    options={[
                      '2022-blake3-aes-128-gcm',
                      '2022-blake3-aes-256-gcm',
                      '2022-blake3-chacha20-poly1305',
                      'aes-256-gcm',
                      'chacha20-ietf-poly1305',
                    ].map((m) => ({ value: m, label: m }))}
                  />
                </Form.Item>
              )}
              {nodeProtocol === 'vless' && (
                <Form.Item name="flow" label="Flow">
                  <Select
                    options={[
                      { value: '', label: '无' },
                      { value: 'xtls-rprx-vision', label: 'xtls-rprx-vision' },
                    ]}
                  />
                </Form.Item>
              )}
              {nodeProtocol === 'tuic' && (
                <>
                  <Form.Item name="congestion_control" label="拥塞控制">
                    <Select
                      options={[
                        { value: 'cubic', label: 'cubic' },
                        { value: 'new_reno', label: 'new_reno' },
                        { value: 'bbr', label: 'bbr' },
                      ]}
                    />
                  </Form.Item>
                  <Form.Item name="udp_relay_mode" label="UDP 中继模式">
                    <Select
                      placeholder="native"
                      options={[
                        { value: 'native', label: 'native' },
                        { value: 'quic', label: 'quic' },
                        { value: 'stable', label: 'stable' },
                      ]}
                    />
                  </Form.Item>
                </>
              )}
              {nodeProtocol === 'anytls' && (
                <Form.Item name="udp_over_stream" label="UDP over Stream" valuePropName="checked">
                  <Switch />
                </Form.Item>
              )}
              {nodeProtocol === 'shadowsocks' && (
                <Form.Item name="ss_plugin" label="SIP002 Plugin" extra="如 obfs-local;obfs=http;obfs-host=example.com">
                  <Input placeholder="obfs-local;obfs=http" />
                </Form.Item>
              )}

              {HAS_TLS.includes(nodeProtocol) && (
                <>
                  <Form.Item name="tls_mode" label="TLS 模式">
                    <Select
                      options={
                        TLS_REALITY.includes(nodeProtocol)
                          ? [
                              { value: 'none', label: '无' },
                              { value: 'tls', label: 'TLS' },
                              { value: 'reality', label: 'REALITY' },
                            ]
                          : [
                              { value: 'tls', label: 'TLS' },
                            ]
                      }
                    />
                  </Form.Item>
                  <Form.Item name="sni" label="SNI">
                    <Input placeholder="TLS/REALITY 服务器名" />
                  </Form.Item>
                  {(nodeProtocol === 'vless' || nodeProtocol === 'vmess' || nodeProtocol === 'trojan') && (
                    <>
                      <Form.Item name="alpn" label="ALPN" extra="多个用英文逗号分隔，如 h2,http/1.1">
                        <Input placeholder="h2, http/1.1" />
                      </Form.Item>
                      <Form.Item name="fingerprint" label="uTLS 指纹">
                        <Select
                          options={['chrome', 'firefox', 'safari', 'ios', 'random', 'randomized'].map((v) => ({ value: v, label: v }))}
                        />
                      </Form.Item>
                    </>
                  )}
                  {nodeTLSMode === 'tls' && (
                    <Form.Item name="insecure" label="跳过证书校验" valuePropName="checked">
                      <Switch />
                    </Form.Item>
                  )}
                  {nodeTLSMode === 'reality' && (
                    <Form.Item name="pbk" label="REALITY 公钥 (pbk)" rules={[{ required: true, message: '请填写公钥' }]}>
                      <Input placeholder="REALITY public key" />
                    </Form.Item>
                  )}
                  {nodeTLSMode === 'reality' && (
                    <Form.Item name="sid" label="REALITY short id">
                      <Input placeholder="留空默认 0000000000000000" />
                    </Form.Item>
                  )}
                </>
              )}

              {HAS_TRANSPORT.includes(nodeProtocol) && (
                <>
                  <Form.Item name="transport" label="传输">
                    <Select
                      options={[
                        { value: 'tcp', label: 'TCP' },
                        { value: 'ws', label: 'WebSocket' },
                        { value: 'httpupgrade', label: 'HTTPUpgrade' },
                      ]}
                    />
                  </Form.Item>
                  {(nodeTransport === 'ws' || nodeTransport === 'httpupgrade') && (
                    <>
                      <Form.Item name="path" label="传输路径">
                        <Input placeholder="/ws" />
                      </Form.Item>
                      <Form.Item name="host" label="Host 头">
                        <Input placeholder="example.com" />
                      </Form.Item>
                    </>
                  )}
                </>
              )}

              {HAS_BANDWIDTH.includes(nodeProtocol) && (
                <>
                  <Form.Item name="obfs" label="混淆类型">
                    <Select
                      options={[
                        { value: '', label: '无' },
                        { value: 'salamander', label: 'salamander' },
                      ]}
                    />
                  </Form.Item>
                  <Form.Item name="obfs_password" label="混淆密码">
                    <Input.Password placeholder="salamander 混淆密码" />
                  </Form.Item>
                  <Space size={16}>
                    <Form.Item name="up_mbps" label="上行 Mbps">
                      <InputNumber min={1} style={{ width: 110 }} placeholder="0" />
                    </Form.Item>
                    <Form.Item name="down_mbps" label="下行 Mbps">
                      <InputNumber min={1} style={{ width: 110 }} placeholder="0" />
                    </Form.Item>
                  </Space>
                </>
              )}

              {nodeProtocol === 'snell' && (
                <>
                  <Form.Item
                    name="psk"
                    label="PSK"
                    dependencies={['snell_version']}
                    rules={[
                      { required: true, message: '请填写 PSK' },
                      ({ getFieldValue }) => ({
                        validator(_, value) {
                          if (getFieldValue('snell_version') === 6 && value && (value.length < 12 || value.length > 255)) {
                            return Promise.reject(new Error('Snell v6 的 PSK 长度必须在 12-255 字节之间'))
                          }
                          return Promise.resolve()
                        },
                      }),
                    ]}
                  >
                    <Input placeholder="Snell 预共享密钥（v6 需 12-255 字节）" />
                  </Form.Item>
                  <Space size={16}>
                    <Form.Item name="snell_version" label="版本">
                      <Select style={{ width: 110 }} options={[{ value: 5, label: 'v5' }, { value: 6, label: 'v6' }]} />
                    </Form.Item>
                    {nodeSnellVersion === 6 ? (
                      <Form.Item name="snell_mode" label="模式（v6）">
                        <Select
                          style={{ width: 140 }}
                          allowClear
                          options={[
                            { value: '', label: '不指定' },
                            { value: 'unshaped', label: 'unshaped' },
                            { value: 'unsafe-raw', label: 'unsafe-raw' },
                          ]}
                        />
                      </Form.Item>
                    ) : (
                      <Form.Item name="snell_obfs_mode" label="混淆（v5）">
                        <Select style={{ width: 120 }} options={[{ value: 'none', label: '无' }, { value: 'http', label: 'http' }]} />
                      </Form.Item>
                    )}
                  </Space>
                </>
              )}
              {nodeProtocol === 'mixed' && (
                <>
                  <Form.Item name="username" label="用户名（可选）">
                    <Input placeholder="用户名" />
                  </Form.Item>
                  <Form.Item name="password" label="密码（可选）">
                    <Input.Password placeholder="密码" />
                  </Form.Item>
                </>
              )}
            </>
          )}
          <Form.Item name="name" label="显示名称（留空自动生成）">
            <Input placeholder="例如：朋友的 HK 节点" />
          </Form.Item>
          <Space size={24}>
            <Form.Item name="enabled" label="启用" valuePropName="checked">
              <Switch />
            </Form.Item>
            <Form.Item name="sort_order" label="排序">
              <InputNumber min={0} />
            </Form.Item>
          </Space>
        </Form>
      </Modal>
    </>
  )
}
