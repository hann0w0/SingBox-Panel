import { memo, useCallback, useEffect, useMemo, useState } from 'react'
import type { Key as ReactKey } from 'react'
import {
  Alert,
  AutoComplete,
  Button,
  Card,
  Checkbox,
  Empty,
  Form,
  Grid,
  Input,
  InputNumber,
  Modal,
  Popover,
  Radio,
  Select,
  Space,
  Spin,
  Switch,
  Table,
  Tabs,
  Tag,
  message,
} from 'antd'
import {
  CheckOutlined,
  CloudDownloadOutlined,
  DeleteOutlined,
  DownOutlined,
  EditOutlined,
  FileSearchOutlined,
  FolderAddOutlined,
  PlusOutlined,
  RightOutlined,
  SaveOutlined,
  UndoOutlined,
  WarningOutlined,
} from '@ant-design/icons'
import {
  batchDeleteCustomNodes,
  batchSetCustomNodeGroup,
  createCustomNode,
  deleteCustomNode,
  errMsg,
  getUserAccess,
  importCustomNodes,
  listServers,
  previewCustomNodeImport,
  updateCustomNode,
  updateUserAccess,
} from '../../api'
import type { CustomNode, CustomNodeImportPreview, CustomNodeImportPreviewNode } from '../../api'
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
  const [activeTab, setActiveTab] = useState<'managed' | 'custom'>('managed')
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

  const managedGroups = useMemo<AccessGroup[]>(() =>
    servers.map((server) => ({
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
    })).filter((group) => group.items.length > 0), [servers])

  // Custom nodes are grouped by their admin-defined group (CustomNode.group).
  // Nodes without a group land in a trailing "未分组" group so nothing is
  // hidden from the assign dialog.
  const customGroups = useMemo<AccessGroup[]>(() => {
    if (nodes.length === 0) return []
    const byGroup = new Map<string, AccessNodeItem[]>()
    for (const node of nodes) {
      const group = (node.group || '').trim()
      const key = group || '__none__'
      if (!byGroup.has(key)) byGroup.set(key, [])
      byGroup.get(key)!.push({
        id: node.id,
        kind: 'custom' as const,
        name: node.name || '未命名节点',
        detail: node.address ? `${node.address}:${node.port}` : '分享链接',
        protocol: node.link?.trim() ? protocolOf(node.link) : node.protocol,
        enabled: node.enabled,
      })
    }
    const groups: AccessGroup[] = []
    for (const [key, items] of byGroup) {
      groups.push({
        key: `custom:${key}`,
        label: key === '__none__' ? '未分组' : key,
        meta: key === '__none__' ? '未设置分组' : '',
        items,
      })
    }
    // Named groups first (sorted), unnamed group last.
    return groups.sort((a, b) => {
      if (a.label === '未分组') return 1
      if (b.label === '未分组') return -1
      return a.label.localeCompare(b.label, 'zh')
    })
  }, [nodes])

  // Expand every group when the dialog opens so all nodes are visible at once
  // (rows are memoized, so ticking a chip does not re-render the whole list).
  const allGroupKeys = useMemo(
    () => [...managedGroups, ...customGroups].map((g) => g.key),
    [customGroups, managedGroups],
  )
  useEffect(() => {
    if (open) setExpandedGroups(new Set(allGroupKeys))
  }, [open, allGroupKeys])

  const selectedInboundSet = useMemo(() => new Set(inboundIDs), [inboundIDs])
  const selectedCustomSet = useMemo(() => new Set(customNodeIDs), [customNodeIDs])
  const isSelected = (item: AccessNodeItem) => (
    item.kind === 'managed' ? selectedInboundSet.has(item.id) : selectedCustomSet.has(item.id)
  )

  const totalNodeCount = managedGroups.reduce((sum, group) => sum + group.items.length, 0)
    + customGroups.reduce((sum, group) => sum + group.items.length, 0)
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

  const allCollapsed = allGroupKeys.every((key) => !expandedGroups.has(key))
  const toggleAllGroups = () => {
    setExpandedGroups(allCollapsed ? new Set(allGroupKeys) : new Set())
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

  // Renders one list of groups (servers on the managed tab, custom-node groups
  // on the other tab) as expandable, select-all-able sections.
  const renderGroupSections = (groupList: AccessGroup[]) => groupList.map((group) => {
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
          <strong>{group.label === '未分组' ? <span className="access-group-unnamed">{group.label}</span> : group.label}</strong>
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
  })

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
      <div className="access-target-bar" style={{ marginBottom: 12, display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <Tag color="blue">已选 {selectedCount} / {totalNodeCount}</Tag>
        {allGroupKeys.length > 1 ? (
          <Button size="small" type="text" icon={allCollapsed ? <DownOutlined /> : <RightOutlined />} onClick={toggleAllGroups}>
            {allCollapsed ? '全部展开' : '全部收起'}
          </Button>
        ) : null}
      </div>
      <Spin spinning={accessLoading}>
        <Tabs
          activeKey={activeTab}
          onChange={(key) => setActiveTab(key as 'managed' | 'custom')}
          items={[
            {
              key: 'managed',
              label: `面板节点（${managedGroups.reduce((sum, g) => sum + g.items.length, 0)}）`,
              children: managedGroups.length === 0 ? (
                <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无面板节点，请先在「主机」页创建入站" />
              ) : (
                <div className="access-modal-body">{renderGroupSections(managedGroups)}</div>
              ),
            },
            {
              key: 'custom',
              label: `其他节点（${customGroups.reduce((sum, g) => sum + g.items.length, 0)}）`,
              children: customGroups.length === 0 ? (
                <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无其他节点，请先在「其他节点」添加" />
              ) : (
                <div className="access-modal-body">{renderGroupSections(customGroups)}</div>
              ),
            },
          ]}
        />
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

const importSourceLabels: Record<string, string> = {
  url: '订阅 URL',
  subscription_url: '订阅 URL',
  clash: 'Clash / Mihomo',
  'clash-yaml': 'Clash / Mihomo YAML',
  yaml: 'YAML',
  surge: 'Surge',
  links: '分享链接',
  link: '分享链接',
  base64: 'Base64 列表',
}

function hasPreviewValue(value: unknown): boolean {
  if (typeof value === 'boolean') return value
  if (typeof value === 'number') return value !== 0
  if (typeof value === 'string') return value.trim() !== '' && !['false', '0', 'off', 'none'].includes(value.trim().toLowerCase())
  return value != null
}

function previewParamSummary(node: CustomNodeImportPreviewNode): string[] {
  const params = node.params ?? {}
  const labels: Record<string, string> = {
    uuid: 'UUID',
    password: '密码',
    psk: 'PSK',
    method: '加密',
    tls: 'TLS',
    sni: 'SNI',
    insecure: '跳过证书',
    skip_cert_verify: '跳过证书',
    udp: 'UDP',
    udp_over_stream: 'UDP over Stream',
    transport: '传输',
    flow: 'Flow',
    fingerprint: '指纹',
    pbk: 'REALITY',
    plugin: 'Plugin',
  }
  const result: string[] = []
  for (const [key, value] of Object.entries(params)) {
    if (!hasPreviewValue(value)) continue
    const label = labels[key] ?? key
    if (!result.includes(label)) result.push(label)
  }
  return result.slice(0, 5)
}

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
  const nodeGroup = Form.useWatch('group', nodeForm)
  const nodeSnellVersion = Form.useWatch('snell_version', nodeForm)
  const nodeLinkSource = Form.useWatch('link', nodeForm)
  const [importPreview, setImportPreview] = useState<CustomNodeImportPreview | null>(null)
  const [previewSource, setPreviewSource] = useState('')
  const [previewLoading, setPreviewLoading] = useState(false)
  const [importLoading, setImportLoading] = useState(false)
  const [importError, setImportError] = useState('')
  const [selectedNodeIDs, setSelectedNodeIDs] = useState<number[]>([])
  const [groupMoveOpen, setGroupMoveOpen] = useState(false)
  const [groupMoveValue, setGroupMoveValue] = useState('')
  const [groupMoving, setGroupMoving] = useState(false)
  const [batchDeleting, setBatchDeleting] = useState(false)
  // Inline group quick-edit on the group column (Popover).
  const [quickGroup, setQuickGroup] = useState<CustomNode | null>(null)
  const [quickGroupValue, setQuickGroupValue] = useState('')
  const normalizedLinkSource = typeof nodeLinkSource === 'string' ? nodeLinkSource.trim() : ''
  const previewReady = !!importPreview && previewSource === normalizedLinkSource

  const resetImportState = () => {
    setImportPreview(null)
    setPreviewSource('')
    setPreviewLoading(false)
    setImportLoading(false)
    setImportError('')
  }

  // Never leave a stale preview visible after the source text changes.
  useEffect(() => {
    if (previewSource && previewSource !== normalizedLinkSource) {
      setImportPreview(null)
      setPreviewSource('')
      setImportError('')
    }
  }, [normalizedLinkSource, previewSource])

  // Distinct groups already in use, offered as selectable options in the node
  // form. Each option shows how many nodes it currently holds; the field also
  // accepts typing a brand-new group name.
  const groupCounts = useMemo(() => {
    const counts = new Map<string, number>()
    for (const n of nodes) {
      const g = (n.group || '').trim()
      if (g) counts.set(g, (counts.get(g) ?? 0) + 1)
    }
    return counts
  }, [nodes])

  const groupOptions = useMemo(
    () => Array.from(groupCounts.keys())
      .sort((a, b) => a.localeCompare(b, 'zh'))
      .map((g) => ({ value: g, label: `${g}（${groupCounts.get(g)} 个节点）` })),
    [groupCounts],
  )

  const openNodeCreate = () => {
    setEditingNode(null)
    resetImportState()
    nodeForm.resetFields()
    nodeForm.setFieldsValue({ enabled: true, node_mode: 'link', sort_order: 0, tls_mode: 'tls', transport: 'tcp', snell_version: 5, snell_obfs_mode: 'none', snell_mode: '', method: '2022-blake3-aes-128-gcm', group: '' })
    setNodeOpen(true)
  }

  const openNodeEdit = (n: CustomNode) => {
    setEditingNode(n)
    resetImportState()
    const p = n.params ?? {}
    nodeForm.resetFields()
    nodeForm.setFieldsValue({
      name: n.name,
      group: n.group || '',
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

  const previewNodeImport = async () => {
    const source = normalizedLinkSource
    if (!source) {
      nodeForm.setFields([{ name: 'link', errors: ['请粘贴订阅 URL、配置文本或分享链接'] }])
      return
    }
    nodeForm.setFields([{ name: 'link', errors: [] }])
    setPreviewLoading(true)
    setImportError('')
    try {
      const result = await previewCustomNodeImport(source)
      setImportPreview(result)
      setPreviewSource(source)
      if (result.nodes.length === 0) {
        message.warning(result.skipped[0]?.error || '没有解析到可添加的节点')
      }
    } catch (e) {
      setImportPreview(null)
      setPreviewSource('')
      const error = errMsg(e)
      setImportError(error)
      message.error(error)
    } finally {
      setPreviewLoading(false)
    }
  }

  const submitNode = async () => {
    if (!editingNode && nodeMode === 'link') {
      if (!normalizedLinkSource) {
        nodeForm.setFields([{ name: 'link', errors: ['请粘贴订阅 URL、配置文本或分享链接'] }])
        return
      }
      // The first confirmation is intentionally non-mutating: it only parses
      // and displays exactly what will be added. A second confirmation imports.
      if (!previewReady) {
        await previewNodeImport()
        return
      }
      if (!importPreview || importPreview.nodes.length === 0) return

      const v = nodeForm.getFieldsValue(['enabled', 'sort_order', 'group'])
      const group = typeof v.group === 'string' ? v.group.trim() : ''
      setImportLoading(true)
      setImportError('')
      try {
        const result = await importCustomNodes({
          source: normalizedLinkSource,
          group,
          enabled: v.enabled !== false,
          sort_order: v.sort_order ?? 0,
        })
        const createdCount = result.nodes.length || result.count || 0
        if (createdCount === 0) {
          const error = result.skipped[0]?.error || '没有添加任何节点'
          setImportError(error)
          message.error(error)
          return
        }
        message.success(`已添加 ${createdCount} 个其他节点`)
        if (result.skipped.length > 0) message.warning(`${result.skipped.length} 项未导入，请检查提示`)
        setNodeOpen(false)
        resetImportState()
        onNodesChange()
      } catch (e) {
        const error = errMsg(e)
        setImportError(error)
        message.error(error)
      } finally {
        setImportLoading(false)
      }
      return
    }

    const v = await nodeForm.validateFields()
    const body: Record<string, unknown> = {
      name: v.name,
      group: typeof v.group === 'string' ? v.group.trim() : '',
      // 归属在「节点分配」里统一管理：新增默认不分配给任何用户，
      // 管理员在节点分配弹窗里勾选后才对指定用户可见；编辑保持原受众。
      all_users: editingNode ? editingNode.all_users : false,
      user_ids: editingNode && !editingNode.all_users ? editingNode.user_ids ?? [] : [],
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

  // ---- batch operations on the node table ----

  const selectedNodes = useMemo(
    () => nodes.filter((n) => selectedNodeIDs.includes(n.id)),
    [nodes, selectedNodeIDs],
  )

  const batchDeleteNodes = () => {
    const count = selectedNodes.length
    if (count === 0) return
    Modal.confirm({
      title: `删除选中的 ${count} 个节点？`,
      content: '此操作不可撤销，节点将从订阅输出中移除。',
      okType: 'danger',
      okText: '删除',
      cancelText: '取消',
      onOk: async () => {
        setBatchDeleting(true)
        try {
          const result = await batchDeleteCustomNodes(selectedNodeIDs)
          message.success(`已删除 ${result.deleted ?? count} 个节点`)
          setSelectedNodeIDs([])
          onNodesChange()
        } catch (e) {
          message.error(errMsg(e))
        } finally {
          setBatchDeleting(false)
        }
      },
    })
  }

  // Move the current selection (or a single node, when invoked from the group
  // column) into a group. An empty group moves the node(s) back to 未分组.
  const moveToGroup = async (ids: number[], group: string) => {
    if (ids.length === 0) return
    const clean = group.trim()
    try {
      await batchSetCustomNodeGroup(ids, clean)
      message.success(clean ? `已移动到「${clean}」` : '已移出分组（未分组）')
      onNodesChange()
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  const confirmMoveSelected = async () => {
    if (selectedNodeIDs.length === 0) return
    setGroupMoving(true)
    try {
      await moveToGroup(selectedNodeIDs, groupMoveValue)
      setGroupMoveOpen(false)
      setGroupMoveValue('')
      setSelectedNodeIDs([])
    } finally {
      setGroupMoving(false)
    }
  }

  // ---- inline group quick-edit (click the group tag on a row) ----

  const openQuickGroup = (n: CustomNode) => {
    setQuickGroup(n)
    setQuickGroupValue((n.group || '').trim())
  }

  const closeQuickGroup = () => {
    if (groupMoving) return
    setQuickGroup(null)
  }

  const saveQuickGroup = async () => {
    if (!quickGroup) return
    setGroupMoving(true)
    try {
      await moveToGroup([quickGroup.id], quickGroupValue)
      setQuickGroup(null)
    } finally {
      setGroupMoving(false)
    }
  }

  // Filter options for the group column: every distinct group plus a sentinel
  // for nodes without any group.
  const groupFilterOptions = useMemo(() => {
    const opts = groupOptions.map((g) => ({ text: g.value, value: g.value }))
    if (nodes.some((n) => !(n.group || '').trim())) opts.push({ text: '未分组', value: '__none__' })
    return opts
  }, [groupOptions, nodes])

  const nodeColumns = [
    {
      title: '名称',
      dataIndex: 'name',
      render: (v: string) => v || <span style={{ color: '#999' }}>（未命名）</span>,
    },
    {
      title: '分组',
      dataIndex: 'group',
      width: 150,
      filters: groupFilterOptions,
      onFilter: (value: ReactKey | boolean, n: CustomNode) => {
        const g = (n.group || '').trim()
        return value === '__none__' ? !g : g === value
      },
      render: (v: string, n: CustomNode) => (
      <Popover
      trigger="click"
      open={quickGroup?.id === n.id}
      onOpenChange={(open) => {
        if (open) {
          openQuickGroup(n)
        } else if (!groupMoving) {
          // Only close when this popover is the one currently open.
          // Without the guard, clicking another row's tag first fires the
          // "click outside" close for the previous popover, which clears
          // quickGroup and instantly closes the newly opened one too.
          setQuickGroup((current) => (current?.id === n.id ? null : current))
        }
      }}
      content={(
        <div style={{ width: 240 }}>
          <AutoComplete
            autoFocus
            allowClear
            value={quickGroupValue}
            onChange={setQuickGroupValue}
            options={groupOptions}
            filterOption={(input, option) => (option?.value ?? '').toLowerCase().includes(input.toLowerCase())}
            placeholder="选择分组或输入新分组"
            style={{ width: '100%' }}
            // Render the dropdown inside the popover, otherwise clicking an
            // option is seen as a "click outside" and closes the popover
            // before the selection registers.
            getPopupContainer={(trigger) => trigger.parentElement ?? document.body}
          />
          <div style={{ marginTop: 8, display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
            <Button size="small" onClick={closeQuickGroup}>取消</Button>
            <Button size="small" type="primary" loading={groupMoving} onClick={() => void saveQuickGroup()}>保存</Button>
          </div>
        </div>
      )}
      >
          {v && v.trim() ? (
            <Tag color="geekblue" style={{ cursor: 'pointer' }} title="点击修改分组">{v.trim()}</Tag>
          ) : (
            <Tag style={{ cursor: 'pointer', color: '#999' }} title="点击设置分组">未分组 ▾</Tag>
          )}
        </Popover>
      ),
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
        {selectedNodeIDs.length > 0 ? (
          <div className="custom-node-batch-bar">
            <span>已选 <strong>{selectedNodeIDs.length}</strong> 个节点</span>
            <Space size={8}>
              <Button
                size="small"
                icon={<FolderAddOutlined />}
                loading={groupMoving}
                onClick={() => {
                  setGroupMoveValue('')
                  setGroupMoveOpen(true)
                }}
              >
                移动到分组
              </Button>
              <Button
                size="small"
                danger
                icon={<DeleteOutlined />}
                loading={batchDeleting}
                onClick={batchDeleteNodes}
              >
                删除
              </Button>
              <Button size="small" type="text" onClick={() => setSelectedNodeIDs([])}>取消选择</Button>
            </Space>
          </div>
        ) : null}
        <Table
          rowKey="id"
          size="small"
          className="compact-rows"
          dataSource={nodes}
          pagination={false}
          scroll={{ x: isMobile ? undefined : 700, y: 340 }}
          columns={nodeColumns}
          rowSelection={{
            selectedRowKeys: selectedNodeIDs,
            onChange: (keys) => setSelectedNodeIDs(keys as number[]),
          }}
          locale={{ emptyText: '暂无其他节点。可粘贴朋友分享的链接（vless://、ss://、trojan:// 等），合并进订阅输出。' }}
        />
      </Card>

      {/* 批量移动到分组 dialog */}
      <Modal
        title={`移动到分组（${selectedNodeIDs.length} 个节点）`}
        open={groupMoveOpen}
        onOk={() => void confirmMoveSelected()}
        onCancel={() => {
          setGroupMoveOpen(false)
          setGroupMoveValue('')
        }}
        okText="移动"
        confirmLoading={groupMoving}
        destroyOnClose
      >
        <div style={{ padding: '8px 0' }}>
          <AutoComplete
            allowClear
            value={groupMoveValue}
            onChange={setGroupMoveValue}
            options={groupOptions}
            filterOption={(input, option) => (option?.value ?? '').toLowerCase().includes(input.toLowerCase())}
            placeholder="选择已有分组，或输入新分组名（留空 = 移到未分组）"
            style={{ width: '100%' }}
          />
          <div style={{ color: '#999', fontSize: 12, marginTop: 8 }}>
            留空提交将把所选节点移到「未分组」；输入新名称会创建新分组。
          </div>
        </div>
      </Modal>

      {/* 自定义节点 modal */}
      <Modal
        title={editingNode ? '编辑其他节点' : '新增其他节点'}
        open={nodeOpen}
        onOk={submitNode}
        onCancel={() => {
          setNodeOpen(false)
          resetImportState()
        }}
        okText={
          !editingNode && nodeMode === 'link'
            ? (previewReady ? `确认添加 ${importPreview?.nodes.length ?? 0} 个节点` : '解析并预览')
            : '保存'
        }
        cancelText="取消"
        confirmLoading={previewLoading || importLoading}
        okButtonProps={{
          disabled: !editingNode && nodeMode === 'link' && previewReady && (importPreview?.nodes.length ?? 0) === 0,
        }}
        destroyOnClose
        width={!editingNode && nodeMode === 'link' ? 680 : 560}
        style={{ maxWidth: 'calc(100vw - 16px)', top: 24 }}
        styles={{ body: { maxHeight: 'calc(100vh - 200px)', overflowY: 'auto', paddingInline: 4 } }}
      >
        <Form form={nodeForm} layout="vertical">
          <Form.Item name="node_mode" label="添加方式" extra="Snell 等没有通用链接格式的协议可手动填写。">
            <Radio.Group
              options={[
                { value: 'link', label: editingNode ? '分享链接' : '订阅 / 分享链接' },
                { value: 'manual', label: '手动填写' },
              ]}
            />
          </Form.Item>
          <Form.Item name="group" label="分组" extra="可选：给节点分组（如机场名），便于列表筛选与批量管理。">
            <AutoComplete
              placeholder="选择已有分组，或输入新分组名"
              options={groupOptions}
              allowClear
              filterOption={(input, option) => (option?.value ?? '').toLowerCase().includes(input.toLowerCase())}
            />
          </Form.Item>
          {nodeMode === 'link' && (
            <>
              <Form.Item
                name="link"
                label={editingNode ? '分享链接' : '订阅链接或配置内容'}
                rules={[{ required: true, whitespace: true, message: '请粘贴订阅 URL、配置文本或分享链接' }]}
              >
                <Input.TextArea
                  placeholder={editingNode ? 'vless://...' : 'https://example.com/sub\n\n也可直接粘贴 Clash YAML、Surge 配置或多行分享链接'}
                  autoSize={{ minRows: editingNode ? 2 : 4, maxRows: editingNode ? 4 : 9 }}
                  spellCheck={false}
                />
              </Form.Item>

              {!editingNode ? (
                <div className="custom-import-workspace">
                  <div className="custom-import-actions">
                    <span className="custom-import-source-note">
                      <CloudDownloadOutlined /> 自动识别 URL、Clash / Mihomo YAML、Surge 配置及多行分享链接
                    </span>
                    <Button
                      icon={<FileSearchOutlined />}
                      loading={previewLoading}
                      disabled={!normalizedLinkSource || importLoading}
                      onClick={() => void previewNodeImport()}
                    >
                      {previewReady ? '重新解析' : '解析并预览'}
                    </Button>
                  </div>

                  {importError ? (
                    <Alert className="custom-import-request-error" type="error" showIcon message={importError} />
                  ) : null}

                  {previewReady && importPreview ? (
                    <div className="custom-import-preview" aria-live="polite">
                      <div className="custom-import-summary">
                        <Space size={[6, 6]} wrap>
                          <Tag color="green" icon={<CheckOutlined />}>可添加 {importPreview.nodes.length}</Tag>
                          {importPreview.skipped.length > 0 ? (
                            <Tag color="orange" icon={<WarningOutlined />}>未解析 {importPreview.skipped.length}</Tag>
                          ) : null}
                          {importPreview.fetched ? <Tag color="blue">远程订阅</Tag> : null}
                          <Tag>
                            {importSourceLabels[(importPreview.source_type ?? '').toLowerCase()] || importPreview.source_type || '自动识别'}
                          </Tag>
                          <Tag color={nodeGroup && nodeGroup.trim() ? 'geekblue' : 'default'}>
                            {nodeGroup && nodeGroup.trim() ? `归入分组：${nodeGroup.trim()}` : '未设置分组'}
                          </Tag>
                        </Space>
                        <span>导入后默认不绑定用户，请在用户列表中分配。</span>
                      </div>

                      {importPreview.nodes.length > 0 ? (
                        <div className="custom-import-node-list">
                          {importPreview.nodes.map((node, index) => {
                            const protocol = (node.protocol || protocolOf(node.link || '')).toLowerCase()
                            const type = nodeTypeLabel[protocol]
                            const params = previewParamSummary(node)
                            const endpoint = node.address
                              ? `${node.address}${node.port ? `:${node.port}` : ''}`
                              : '地址未解析'
                            return (
                              <div className="custom-import-node" key={`${protocol}:${node.address}:${node.port}:${index}`}>
                                <div className="custom-import-node-main">
                                  <strong title={node.name || undefined}>{node.name || `未命名节点 ${index + 1}`}</strong>
                                  <Tag color={type?.color}>{type?.label || protocol || '-'}</Tag>
                                </div>
                                <code title={endpoint}>{endpoint}</code>
                                {params.length > 0 ? (
                                  <span className="custom-import-node-params" title={params.join('、')}>
                                    已识别：{params.join('、')}
                                  </span>
                                ) : null}
                              </div>
                            )
                          })}
                        </div>
                      ) : (
                        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="没有解析到可添加的节点" />
                      )}

                      {importPreview.skipped.length > 0 ? (
                        <Alert
                          type="warning"
                          showIcon
                          message={`${importPreview.skipped.length} 项无法解析`}
                          description={(
                            <div className="custom-import-errors">
                              {importPreview.skipped.slice(0, 10).map((item, index) => (
                                <div key={`${item.input}:${index}`}>
                                  <strong>{item.error}</strong>
                                  {item.input ? <code title={item.input}>{item.input}</code> : null}
                                </div>
                              ))}
                              {importPreview.skipped.length > 10 ? <span>另有 {importPreview.skipped.length - 10} 项未显示</span> : null}
                            </div>
                          )}
                        />
                      ) : null}
                    </div>
                  ) : null}
                </div>
              ) : null}
            </>
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
          {(editingNode || nodeMode === 'manual') && (
            <Form.Item name="name" label="显示名称（留空自动生成）">
              <Input placeholder="例如：朋友的 HK 节点" />
            </Form.Item>
          )}
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
