import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  AutoComplete,
  Button,
  Form,
  Input,
  InputNumber,
  Modal,
  Popover,
  Select,
  Switch,
  Table,
  Typography,
  message,
} from '../../../components/kit'
import type { ColumnsType } from '../../../components/kit'
import { Pills, Status } from '../../../components/kit/Page'
import { Field, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/components/ui/field'
import {
  ChevronDown,
  ChevronsUpDown,
  ChevronUp,
  Eye,
  Pencil,
  Plus,
  RotateCw,
  Search,
  Trash2,
  Undo2,
  X,
} from 'lucide-react'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  batchSetCustomNodeGroup,
  createCustomNode,
  createCustomNodeSubscription,
  deleteCustomNode,
  deleteCustomNodeSubscription,
  errMsg,
  listCustomNodeSubscriptions,
  listCustomNodes,
  syncCustomNodeSubscription,
  updateCustomNode,
  updateCustomNodeSubscription,
} from '../../../lib/api'
import type { CustomNode, CustomNodeSubscription, NameRewriteRule } from '../../../lib/api'
import { RegionFlag, regionCodeFromFlag, removeRegionFlag } from '../../../components/RegionFlag'
import { RequestState } from '../../../components/RequestState'
import { buildCustomNodeParams } from '../../../lib/customNodeForm'
import { EXTERNAL_NODE_PROTOCOL_TYPES, toProtocolOptions } from '../../../lib/protocols'
import { SHADOWSOCKS_METHODS } from '../../../lib/util'
import { MODAL_MD } from '../../../components/kit/modal'
import { FormGroup } from '../servers/InboundForm'
import '../../../styles/forms.css'
import '../../../styles/inbound-editor.css'
import {
  HAS_AUTH,
  HAS_BANDWIDTH,
  HAS_PASSWORD,
  HAS_TLS,
  HAS_TRANSPORT,
  HAS_UUID,
  MAX_VISIBLE_SUBSCRIPTIONS,
  SUBSCRIPTION_TABLE_SCROLL_HEIGHT,
  TLS_REALITY,
  nodeTypeLabel,
  protocolOf,
} from './nodeLabels'

function hasPreviewValue(value: unknown): boolean {
  if (typeof value === 'boolean') return value
  if (typeof value === 'number') return value !== 0
  if (typeof value === 'string') return value.trim() !== '' && !['false', '0', 'off', 'none'].includes(value.trim().toLowerCase())
  return value != null
}

const detailParamLabels: Record<string, string> = {
  uuid: 'UUID', password: '密码', username: '用户名', psk: 'PSK', method: '加密方式',
  flow: 'Flow', tls: '安全', sni: 'SNI', pbk: 'PublicKey', sid: 'ShortID',
  fingerprint: '指纹', insecure: '跳过证书验证', transport: '传输', network: '网络',
  path: 'Path', host: 'Host', alpn: 'ALPN', congestion_control: '拥塞控制',
  udp_relay_mode: 'UDP Relay', udp_over_stream: 'UDP over Stream', ss_plugin: 'Plugin',
  obfs: '混淆', obfs_password: '混淆密码', up_mbps: '上行 Mbps', down_mbps: '下行 Mbps',
  version: '版本', obfs_mode: '混淆模式', mode: '模式', security: '加密', alter_id: 'AlterId',
  http_path: 'HTTP Path', http_headers: 'HTTP Headers',
}

function nodeDisplayName(node: CustomNode) {
  const code = node.detail?.region || regionCodeFromFlag(node.name)
  const name = removeRegionFlag(node.name) || '未命名'
  return (
    <span className="custom-node-name">
      {code ? <RegionFlag code={code} fallback={false} /> : null}
      <span>{name}</span>
    </span>
  )
}

function detailValue(value: unknown): string {
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  if (value == null) return ''
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

export function CustomNodesPanel({ nodes, loading, error, onNodesChange }: {
  nodes: CustomNode[]
  loading: boolean
  error?: string | null
  onNodesChange: () => void
}) {
  const [nodeOpen, setNodeOpen] = useState(false)
  const [nodeSaving, setNodeSaving] = useState(false)
  const [editingNode, setEditingNode] = useState<CustomNode | null>(null)
  const [nodeMode, setNodeMode] = useState<'manual' | 'uri'>('manual')
  const [nodeForm] = Form.useForm()
  const nodeProtocol = Form.useWatch('protocol', nodeForm)
  const nodeTransport = Form.useWatch('transport', nodeForm)
  const nodeObfsType = Form.useWatch('obfs', nodeForm)
  const nodeSnellObfsMode = Form.useWatch('snell_obfs_mode', nodeForm)
  const nodeTLSMode = Form.useWatch('tls_mode', nodeForm)
  const nodeSnellVersion = Form.useWatch('snell_version', nodeForm)
  const nodeLinkSource = Form.useWatch('link', nodeForm)
  const [selectedGroup, setSelectedGroup] = useState<string>()
  const [nodeSearch, setNodeSearch] = useState('')
  const [section, setSection] = useState<'subscriptions' | 'nodes'>('subscriptions')
  const [detailNode, setDetailNode] = useState<CustomNode | null>(null)
  const [groupMoving, setGroupMoving] = useState(false)
  const [subscriptions, setSubscriptions] = useState<CustomNodeSubscription[]>([])
  const [subscriptionsLoading, setSubscriptionsLoading] = useState(false)
  const [subscriptionsError, setSubscriptionsError] = useState<string | null>(null)
  const [subscriptionOpen, setSubscriptionOpen] = useState(false)
  const [editingSubscription, setEditingSubscription] = useState<CustomNodeSubscription | null>(null)
  const [subscriptionForm] = Form.useForm()
  const [subscriptionSaving, setSubscriptionSaving] = useState(false)
  const nodeSubmitLock = useRef(false)
  const subscriptionSubmitLock = useRef(false)
  const subscriptionLoadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })
  const [syncingSubscriptionID, setSyncingSubscriptionID] = useState<number | null>(null)
  const subscriptionRules = Form.useWatch('name_rewrite_rules', subscriptionForm) as NameRewriteRule[] | undefined
  // Inline group quick-edit on the group column (Popover).
  const [quickGroup, setQuickGroup] = useState<CustomNode | null>(null)
  const [quickGroupValue, setQuickGroupValue] = useState('')
  const normalizedNodeLink = typeof nodeLinkSource === 'string' ? nodeLinkSource.trim() : ''

  const loadSubscriptions = useCallback(() => {
    subscriptionLoadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++subscriptionLoadRef.current.generation
    subscriptionLoadRef.current.controller = controller
    setSubscriptionsLoading(true)
    setSubscriptionsError(null)
    listCustomNodeSubscriptions(controller.signal)
      .then((items) => {
        if (generation === subscriptionLoadRef.current.generation) setSubscriptions(items)
      })
      .catch((e) => {
        if (generation === subscriptionLoadRef.current.generation && !controller.signal.aborted) setSubscriptionsError(errMsg(e))
      })
      .finally(() => {
        if (generation === subscriptionLoadRef.current.generation) {
          subscriptionLoadRef.current.controller = null
          setSubscriptionsLoading(false)
        }
      })
  }, [])

  useEffect(() => {
    loadSubscriptions()
    return () => {
      subscriptionLoadRef.current.generation += 1
      subscriptionLoadRef.current.controller?.abort()
    }
  }, [loadSubscriptions])

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
      .map((g) => ({ value: g, label: `${g} · ${groupCounts.get(g)}` })),
    [groupCounts],
  )

  const openNodeCreate = () => {
    setEditingNode(null)
    setNodeMode('manual')
    nodeForm.resetFields()
    nodeForm.setFieldsValue({ link: '', tls_mode: 'tls', transport: 'tcp', snell_version: 5, snell_obfs_mode: 'none', snell_mode: '', method: '2022-blake3-aes-128-gcm', group: '' })
    setNodeOpen(true)
  }

  const openSubscriptionCreate = () => {
    setEditingSubscription(null)
    subscriptionForm.resetFields()
    subscriptionForm.setFieldsValue({ auto_update: true, group: '', name_rewrite_rules: [] })
    setSubscriptionOpen(true)
  }

  const openSubscriptionEdit = (subscription: CustomNodeSubscription) => {
    setEditingSubscription(subscription)
    subscriptionForm.resetFields()
    subscriptionForm.setFieldsValue({
      name: subscription.name,
      url: subscription.url,
      group: subscription.group || '',
      auto_update: subscription.auto_update,
      name_rewrite_rules: (subscription.name_rewrite_rules || []).map((rule) => {
        const action = rule.action === 'replace_text' ? 'rename' : (rule.action || 'rename')
        return {
          action,
          pattern: action === 'rename' && rule.match_mode === 'regexp' && !rule.pattern.startsWith('re:') ? `re:${rule.pattern}` : rule.pattern,
          replacement: rule.replacement || '',
          match_mode: rule.match_mode,
        }
      }),
    })
    setSubscriptionOpen(true)
  }

  const saveSubscription = async () => {
    if (subscriptionSubmitLock.current) return
    subscriptionSubmitLock.current = true
    setSubscriptionSaving(true)
    try {
      const values = await subscriptionForm.validateFields()
      const body = {
        name: values.name.trim(),
        url: values.url.trim(),
        group: typeof values.group === 'string' ? values.group.trim() : '',
        auto_update: values.auto_update !== false,
        update_interval_minutes: editingSubscription?.update_interval_minutes ?? 60,
        base_sort_order: editingSubscription?.base_sort_order ?? 0,
        name_rewrite_rules: (values.name_rewrite_rules || []).map((rule: NameRewriteRule) => {
          const action = rule.action === 'replace_text' ? 'rename' : (rule.action || 'rename')
          const pattern = String(rule.pattern ?? '')
          return {
            action,
            pattern,
            replacement: action === 'rename' ? String(rule.replacement ?? '') : '',
            match_mode: action === 'rename' ? (pattern.startsWith('re:') ? 'regexp' : 'text') : undefined,
          }
        }),
      }
      if (editingSubscription) {
        await updateCustomNodeSubscription(editingSubscription.id, body)
        message.success('订阅已保存')
      } else {
        const result = await createCustomNodeSubscription(body)
        if (result.sync_error) {
          message.warning(`订阅已保存，首次同步失败：${result.sync_error}`)
        } else if (result.sync.skipped) {
          message.warning(`订阅已保存，部分同步失败：${result.subscription.last_error}`)
        } else {
          message.success(`订阅已保存并同步 ${result.sync.total} 个节点${result.sync.filtered ? `，过滤 ${result.sync.filtered} 个` : ''}`)
        }
      }
      setSubscriptionOpen(false)
      loadSubscriptions()
      onNodesChange()
    } catch (e) {
      if (e && typeof e === 'object' && 'errorFields' in e) return
      message.error(errMsg(e))
    } finally {
      subscriptionSubmitLock.current = false
      setSubscriptionSaving(false)
    }
  }

  const syncSubscription = async (subscription: CustomNodeSubscription) => {
    setSyncingSubscriptionID(subscription.id)
    try {
      const result = await syncCustomNodeSubscription(subscription.id)
      const summary = `当前 ${result.sync.total} 个节点，新增 ${result.sync.created}，更新 ${result.sync.updated}，删除 ${result.sync.deleted}${result.sync.filtered ? `，过滤 ${result.sync.filtered}` : ''}`
      if (result.sync.skipped) message.warning(`部分同步完成：${summary}；${result.subscription.last_error}`)
      else message.success(`同步完成：${summary}`)
      loadSubscriptions()
      onNodesChange()
    } catch (e) {
      message.error(errMsg(e))
      loadSubscriptions()
    } finally {
      setSyncingSubscriptionID(null)
    }
  }

  const removeSubscription = (subscription: CustomNodeSubscription) => {
    Modal.confirm({
      title: '删除订阅',
      content: `确认删除「${subscription.name}」？将同时删除其下 ${subscription.node_count} 个节点。`,
      okType: 'danger',
      okText: '删除',
      onOk: async () => {
        await deleteCustomNodeSubscription(subscription.id)
        message.success('订阅及其节点已删除')
        loadSubscriptions()
        onNodesChange()
      },
    })
  }

  const openNodeEdit = (n: CustomNode) => {
    setEditingNode(n)
    setNodeMode(n.link?.trim() ? 'uri' : 'manual')
    const p = n.params ?? {}
    nodeForm.resetFields()
    nodeForm.setFieldsValue({
      name: n.name,
      group: n.group || '',
      link: n.link,
      protocol: n.protocol,
      address: n.address,
      port: n.port,
      uuid: p.uuid,
      password: p.password,
      method: p.method,
      flow: p.flow,
      tls_mode: p.tls ?? (n.protocol === 'http' ? 'none' : 'tls'),
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
      // Match the backend's normalization of historical TUIC relay modes.
      udp_relay_mode: p.udp_relay_mode && p.udp_relay_mode !== 'quic' ? 'native' : p.udp_relay_mode,
      udp_over_stream: !!p.udp_over_stream,
      ss_plugin: p.ss_plugin,
      obfs: p.obfs,
      obfs_password: p.obfs_password,
      gecko_min_packet_size: p.gecko_min_packet_size,
      gecko_max_packet_size: p.gecko_max_packet_size,
      up_mbps: p.up_mbps,
      down_mbps: p.down_mbps,
      psk: p.psk,
      snell_version: p.version ?? 5,
      snell_obfs_mode: p.obfs_mode ?? 'none',
      snell_obfs_host: p.obfs_host,
      snell_mode: p.mode === 'default' ? '' : (p.mode ?? ''),
      username: p.username,
      all_users: n.all_users,
    })
    setNodeOpen(true)
  }

  const submitNode = async () => {
    if (nodeSubmitLock.current) return
    nodeSubmitLock.current = true
    setNodeSaving(true)
    try {
      const v = await nodeForm.validateFields()
      const body: Record<string, unknown> = {
      name: editingNode?.name,
      group: typeof v.group === 'string' ? v.group.trim() : '',
      // 归属在「节点分配」里统一管理：新增默认不分配给任何用户，
      // 管理员在节点分配弹窗里勾选后才对指定用户可见；编辑保持原受众。
      all_users: editingNode ? editingNode.all_users : false,
      user_ids: editingNode && !editingNode.all_users ? editingNode.user_ids ?? [] : [],
      excluded_user_ids: editingNode && editingNode.all_users ? editingNode.excluded_user_ids ?? [] : [],
      sort_order: editingNode?.sort_order ?? 0,
    }
      if (nodeMode === 'uri') {
        const sameLink = editingNode?.link === normalizedNodeLink
        Object.assign(body, {
        link: normalizedNodeLink,
        protocol: sameLink ? editingNode.protocol : '',
        address: sameLink ? editingNode.address : '',
        port: sameLink ? editingNode.port : undefined,
        params: sameLink ? editingNode.params : null,
        })
      } else {
        Object.assign(body, {
        link: '',
        protocol: v.protocol,
        address: typeof v.address === 'string' ? v.address.trim() : '',
        port: v.port,
        params: buildCustomNodeParams(editingNode, v),
        })
      }
      if (editingNode) await updateCustomNode(editingNode.id, body)
      else await createCustomNode(body)
      message.success('已保存')
      setNodeOpen(false)
      onNodesChange()
    } catch (e) {
      if (e && typeof e === 'object' && 'errorFields' in e) return
      message.error(errMsg(e))
    } finally {
      nodeSubmitLock.current = false
      setNodeSaving(false)
    }
  }

  const removeNode = (n: CustomNode) => {
    const label = n.name || n.link.slice(0, 24) || (n.address ? `${n.address}:${n.port}` : '未命名')
    Modal.confirm({
      title: '删除节点',
      content: `确认删除「${label}」？`,
      okText: '删除',
      okType: 'danger',
      onOk: async () => {
        await deleteCustomNode(n.id)
        onNodesChange()
      },
    })
  }

  // Move a manually managed node into a group. An empty group moves it back
  // to 未分组.
  const moveToGroup = async (ids: number[], group: string) => {
    if (ids.length === 0) return false
    const clean = group.trim()
    try {
      await batchSetCustomNodeGroup(ids, clean)
      message.success(clean ? `已移动到「${clean}」` : '已移出分组（未分组）')
      onNodesChange()
      return true
    } catch (e) {
      message.error(errMsg(e))
      return false
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
      if (await moveToGroup([quickGroup.id], quickGroupValue)) setQuickGroup(null)
    } finally {
      setGroupMoving(false)
    }
  }

  // Keep group filtering searchable above the table, especially with many groups.
  const groupFilterOptions = useMemo(() => {
    const opts = groupOptions.map((g) => ({ label: g.label, value: g.value }))
    const ungrouped = nodes.filter((n) => !(n.group || '').trim()).length
    if (ungrouped > 0) opts.push({ label: `未分组 · ${ungrouped}`, value: '__none__' })
    return opts
  }, [groupOptions, nodes])

  const filteredNodes = useMemo(() => {
    const query = nodeSearch.trim().toLocaleLowerCase()
    return nodes.filter((node) => {
      const group = (node.group || '').trim()
      const groupMatches = !selectedGroup || (group ? group === selectedGroup : selectedGroup === '__none__')
      if (!groupMatches || !query) return groupMatches
      return [node.name, removeRegionFlag(node.name), node.address, node.detail?.address, node.protocol]
        .some((value) => typeof value === 'string' && value.toLocaleLowerCase().includes(query))
    })
  }, [nodeSearch, nodes, selectedGroup])

  const detailParams = useMemo(() => {
    if (!detailNode) return []
    const source = detailNode.detail?.params ?? detailNode.params ?? {}
    return Object.entries(source)
      .filter(([key, value]) => !['服务器', '端口', '协议'].includes(key) && hasPreviewValue(value))
      .map(([key, value]) => [detailParamLabels[key] ?? key, detailValue(value)] as const)
  }, [detailNode])

  const detailSubscription = detailNode?.subscription_id
    ? subscriptions.find((subscription) => subscription.id === detailNode.subscription_id)
    : undefined

  const nodeAddress = (node: CustomNode) => {
    const address = node.detail?.address || node.address
    const port = node.detail?.port || node.port
    return address ? `${address}${port ? `:${port}` : ''}` : ''
  }

  const nodeColumns: ColumnsType<CustomNode> = [
    {
      title: '名称',
      dataIndex: 'name',
      ellipsis: true,
      render: (_: string, node: CustomNode) => (
        <span className="um-node-title">
          {nodeDisplayName(node)}
        </span>
      ),
    },
    {
      title: '分组',
      dataIndex: 'group',
      width: 160,
      render: (v: string, n: CustomNode) => n.subscription_id ? (
        <span className="um-group-label" title="由订阅管理">{v?.trim() || '未分组'}</span>
      ) : (
        <span className="custom-node-row-action">
          <Popover
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
              <div className="um-quick-group">
                <AutoComplete
                  autoFocus
                  allowClear
                  openOnFocus
                  value={quickGroupValue}
                  onChange={setQuickGroupValue}
                  options={groupOptions}
                  filterOption={(input, option) => (option?.value ?? '').toLowerCase().includes(input.toLowerCase())}
                  placeholder="分组名"
                  style={{ width: '100%' }}
                />
                <div className="um-quick-group-actions">
                  <Button size="small" onClick={closeQuickGroup}>取消</Button>
                  <Button size="small" type="primary" loading={groupMoving} onClick={() => void saveQuickGroup()}>保存</Button>
                </div>
              </div>
            )}
          >
            <button type="button" className={`um-group-button${v && v.trim() ? '' : ' is-empty'}`} aria-label={`修改分组：${v?.trim() || '未分组'}`}>
              <span>{v && v.trim() ? v.trim() : '未分组'}</span>
              <ChevronDown />
            </button>
          </Popover>
        </span>
      ),
    },
    {
      title: '类型',
      width: 110,
      render: (_: unknown, n: CustomNode) => {
        const key = n.link && n.link.trim() ? protocolOf(n.link) : n.protocol
        return <span className="ui-tag">{nodeTypeLabel[key] || key || '-'}</span>
      },
    },
    {
      title: '地址',
      width: 220,
      ellipsis: true,
      render: (_: unknown, n: CustomNode) => {
        const address = nodeAddress(n)
        return address ? <span className="mono muted" title={address}>{address}</span> : <span className="faint">—</span>
      },
    },
    {
      title: <span className="sr-only">操作</span>,
      width: 112,
      align: 'right',
      render: (_: unknown, n: CustomNode) => (
        <span className="ui-row-actions custom-node-row-action">
          <Button size="small" type="text" icon={<Eye />} title="详情" aria-label={`查看 ${n.name || '节点'}`} onClick={() => setDetailNode(n)} />
          <Button size="small" type="text" icon={<Pencil />} title="编辑" aria-label={`编辑 ${n.name || '节点'}`} onClick={() => openNodeEdit(n)} />
          <Button size="small" type="text" icon={<Trash2 />} title="删除" aria-label={`删除 ${n.name || '节点'}`} onClick={() => removeNode(n)} />
        </span>
      ),
    },
  ]

  const syncLabel = (subscription: CustomNodeSubscription) => subscription.last_sync_at
    ? new Date(subscription.last_sync_at).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
    : '尚未同步'

  const subscriptionActions = (subscription: CustomNodeSubscription) => (
    <span className="ui-row-actions">
      <Button
        size="small"
        type="text"
        icon={<RotateCw />}
        title="立即同步"
        aria-label={`同步 ${subscription.name}`}
        loading={syncingSubscriptionID === subscription.id}
        onClick={() => void syncSubscription(subscription)}
      />
      <Button size="small" type="text" icon={<Pencil />} title="编辑" aria-label={`编辑 ${subscription.name}`} onClick={() => openSubscriptionEdit(subscription)} />
      <Button size="small" type="text" icon={<Trash2 />} title="删除" aria-label={`删除 ${subscription.name}`} onClick={() => removeSubscription(subscription)} />
    </span>
  )

  const subscriptionStatus = (subscription: CustomNodeSubscription) => {
    if (subscription.last_sync_status === 'partial') {
      return <span title={subscription.last_error}><Status tone="warn">部分成功</Status></span>
    }
    if (subscription.last_sync_status === 'failed' || (!subscription.last_sync_status && subscription.last_error)) {
      return <span title={subscription.last_error}><Status tone="error">同步失败</Status></span>
    }
    return subscription.last_success_at || subscription.last_sync_at
      ? <Status tone="ok">正常</Status>
      : <Status tone="off">待同步</Status>
  }

  const nodeFilters = (
    <>
      {selectedGroup || nodeSearch.trim() ? <span className="um-filter-count mono">{filteredNodes.length} / {nodes.length}</span> : null}
      <Select
        allowClear
        showSearch
        value={selectedGroup}
        onChange={setSelectedGroup}
        options={groupFilterOptions}
        placeholder="全部分组"
        aria-label="按分组筛选"
        optionFilterProp="label"
        className="um-filter-group"
      />
      <Input
        allowClear
        prefix={<Search />}
        value={nodeSearch}
        onChange={(event) => setNodeSearch(event.target.value)}
        placeholder="搜索节点"
        aria-label="搜索节点"
        className="um-filter-search"
      />
    </>
  )

  const addButton = section === 'subscriptions'
    ? <Button type="primary" icon={<Plus />} onClick={openSubscriptionCreate}>新增订阅</Button>
    : <Button type="primary" icon={<Plus />} onClick={openNodeCreate}>新增节点</Button>

  return (
    <>
      <Pills
        label="节点视图"
        active={section}
        onChange={(key) => setSection(key as 'subscriptions' | 'nodes')}
        items={[
          { key: 'subscriptions', label: '订阅源', count: subscriptions.length },
          { key: 'nodes', label: '节点', count: nodes.length },
        ]}
        actions={section === 'nodes' && nodes.length > 0 ? <>{nodeFilters}{addButton}</> : addButton}
      />
      {section === 'subscriptions' ? (
        <RequestState loading={subscriptionsLoading} error={subscriptionsError} hasData={subscriptions.length > 0} empty={!subscriptionsLoading && !subscriptionsError && subscriptions.length === 0} emptyDescription="暂无订阅源" onRetry={loadSubscriptions}>
          <Table<CustomNodeSubscription>
            rowKey="id"
            className="um-table"
            dataSource={subscriptions}
            virtual={subscriptions.length > MAX_VISIBLE_SUBSCRIPTIONS}
            scroll={{
              x: 720,
              y: subscriptions.length > MAX_VISIBLE_SUBSCRIPTIONS ? SUBSCRIPTION_TABLE_SCROLL_HEIGHT : undefined,
            }}
            columns={[
              {
                title: '名称',
                dataIndex: 'name',
                ellipsis: true,
                render: (value: string, subscription) => <span className="um-strong" title={subscription.url}>{value}</span>,
              },
              {
                title: '分组',
                dataIndex: 'group',
                width: 140,
                ellipsis: true,
                render: (value: string) => value?.trim() ? <span className="muted">{value}</span> : <span className="faint">—</span>,
              },
              {
                title: '节点',
                dataIndex: 'node_count',
                width: 80,
                render: (value: number) => <span className="mono">{value}</span>,
              },
              {
                title: '状态',
                width: 110,
                render: (_: unknown, subscription) => subscriptionStatus(subscription),
              },
              {
                title: '最近同步',
                width: 130,
                render: (_: unknown, subscription) => (
                  <span className="mono muted" title={subscription.last_sync_at ? new Date(subscription.last_sync_at).toLocaleString('zh-CN') : undefined}>
                    {syncLabel(subscription)}
                  </span>
                ),
              },
              {
                title: <span className="sr-only">操作</span>,
                width: 112,
                align: 'right',
                fixed: 'right' as const,
                render: (_: unknown, subscription) => subscriptionActions(subscription),
              },
            ]}
            locale={{ emptyText: '暂无订阅源' }}
          />
        </RequestState>
      ) : (
        <RequestState loading={loading} error={error} hasData={nodes.length > 0} empty={!loading && !error && nodes.length === 0} emptyDescription="暂无节点" onRetry={onNodesChange}>
          <Table<CustomNode>
            rowKey="id"
            className="um-table"
            dataSource={filteredNodes}
            virtual={filteredNodes.length > 20}
            tableLayout="fixed"
            scroll={{ x: 900, y: filteredNodes.length > 20 ? 560 : undefined }}
            columns={nodeColumns}
            onRow={(node) => ({
              className: 'um-clickable-row',
              onClick: (event) => {
                const target = event.target as HTMLElement
                if (target.closest('.custom-node-row-action, button, input')) return
                setDetailNode(node)
              },
            })}
            locale={{ emptyText: nodeSearch.trim() || selectedGroup ? '没有匹配的节点' : '暂无节点' }}
          />
        </RequestState>
      )}

      <Modal
        title={editingSubscription ? '编辑订阅' : '新增订阅'}
        open={subscriptionOpen}
        onOk={() => void saveSubscription()}
        onCancel={() => setSubscriptionOpen(false)}
        okText={editingSubscription ? '保存' : '保存并同步'}
        confirmLoading={subscriptionSaving}
        className="um-modal um-source-modal"
        width={MODAL_MD}
      >
        <Form form={subscriptionForm} fieldLayout onFinish={saveSubscription}>
          <FieldGroup>
          <FieldGroup>
          <FieldGroup className="um-source-grid">
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: '请输入订阅名称' }]}>
            <Input />
          </Form.Item>
          <Form.Item name="group" label="节点分组">
            <AutoComplete
              options={groupOptions}
              allowClear
              filterOption={(input, option) => (option?.value ?? '').toLowerCase().includes(input.toLowerCase())}
            />
          </Form.Item>
          </FieldGroup>
          <Form.Item name="url" label="订阅链接" rules={[{ required: true, whitespace: true, message: '请输入 HTTP(S) 订阅链接' }]}>
            <Input.TextArea rows={2} spellCheck={false} />
          </Form.Item>
          </FieldGroup>
          <FieldSet className="um-source-section">
            <FieldLegend>处理规则</FieldLegend>
            <Form.List name="name_rewrite_rules">
              {(fields, { add, remove, move }) => (
                <FieldGroup className="gap-3">
                  {fields.map((field, index) => {
                    const action = subscriptionRules?.[field.name]?.action || 'rename'
                    return <FieldSet className="um-source-rule" key={field.key}>
                      <div className="um-source-rule-head">
                        <span>规则 {index + 1}</span>
                        <div className="ui-row-actions">
                          <Button type="text" size="small" icon={<ChevronUp />} title="上移" aria-label="上移规则" disabled={index === 0} onClick={() => move(index, index - 1)} />
                          <Button type="text" size="small" icon={<ChevronDown />} title="下移" aria-label="下移规则" disabled={index === fields.length - 1} onClick={() => move(index, index + 1)} />
                          <Button type="text" size="small" icon={<Trash2 />} title="删除" aria-label="删除规则" onClick={() => remove(field.name)} />
                        </div>
                      </div>
                      <FieldGroup className="um-source-rule-fields">
                      <Form.Item {...field} name={[field.name, 'action']} label="操作">
                        <Select
                          options={[
                            { value: 'rename', label: '节点重命名' },
                            { value: 'exclude_protocol', label: '去除协议' },
                            { value: 'include_node', label: '保留节点' },
                            { value: 'exclude_node', label: '去除节点' },
                          ]}
                        />
                      </Form.Item>
                      <Form.Item
                        {...field}
                        name={[field.name, 'pattern']}
                        rules={[{ required: true, whitespace: true, message: action === 'exclude_protocol' ? '请输入协议' : '请输入匹配内容' }]}
                        label="匹配"
                      >
                        <Input
                          aria-label="匹配"
                          maxLength={256}
                          spellCheck={false}
                        />
                      </Form.Item>
                      {action === 'rename' ? (
                        <Form.Item {...field} name={[field.name, 'replacement']} label="替换为">
                          <Input aria-label="替换为" maxLength={512} spellCheck={false} />
                        </Form.Item>
                      ) : null}
                      </FieldGroup>
                    </FieldSet>
                  })}
                  <Button icon={<Plus />} onClick={() => add({ action: 'rename', pattern: '', replacement: '', match_mode: 'text' })} disabled={fields.length >= 50} className="self-start">
                    {fields.length >= 50 ? '已达 50 条上限' : '添加规则'}
                  </Button>
                </FieldGroup>
              )}
            </Form.List>
          </FieldSet>
          <Field className="um-source-section" orientation="horizontal">
            <Form.Item name="auto_update" valuePropName="checked" noStyle><Switch /></Form.Item>
            <FieldLabel htmlFor={`${subscriptionForm._id}-auto_update`}>自动更新</FieldLabel>
          </Field>
          </FieldGroup>
        </Form>
      </Modal>

      <Modal
        title={detailNode ? nodeDisplayName(detailNode) : '节点详情'}
        className="um-modal"
        open={!!detailNode}
        onCancel={() => setDetailNode(null)}
        footer={<Button onClick={() => setDetailNode(null)}>关闭</Button>}
        width={MODAL_MD}
      >
        {detailNode ? (
          <dl className="ui-kv ui-card um-detail">
            <div><dt>来源</dt><dd>{detailNode.subscription_id ? `订阅 · ${detailSubscription?.name || `#${detailNode.subscription_id}`}` : '手动添加'}</dd></div>
            <div><dt>分组</dt><dd>{detailNode.group?.trim() || '未分组'}</dd></div>
            <div><dt>协议</dt><dd>{detailNode.detail?.protocol || detailNode.protocol || protocolOf(detailNode.link || '')}</dd></div>
            <div><dt>服务器</dt><dd className="mono">{nodeAddress(detailNode) || '—'}</dd></div>
            <div>
              <dt>URI</dt>
              <dd>
                {detailNode.detail?.uri ? (
                  <Typography.Text className="um-detail-value mono" copyable={{ text: detailNode.detail.uri }}>{detailNode.detail.uri}</Typography.Text>
                ) : <span className="faint">—</span>}
              </dd>
            </div>
            {detailParams.map(([label, value]) => (
              <div key={label}>
                <dt>{label}</dt>
                <dd><Typography.Text className="um-detail-value mono" copyable={{ text: value }}>{value}</Typography.Text></dd>
              </div>
            ))}
          </dl>
        ) : null}
      </Modal>

      {/* 自定义节点 modal */}
      <Modal
        title={editingNode ? '编辑节点' : '新增节点'}
        open={nodeOpen}
        onOk={submitNode}
        onCancel={() => setNodeOpen(false)}
        confirmLoading={nodeSaving}
        okText="保存"
        cancelText="取消"
        className="um-modal inbound-compact"
        width={680}
      >
        <Form form={nodeForm} layout="vertical" className="fm-form inbound-compact-form" onFinish={submitNode}>
          <Tabs value={nodeMode} onValueChange={(value) => setNodeMode(value as 'manual' | 'uri')}>
            <TabsList aria-label="节点添加方式">
              <TabsTrigger value="manual">手动配置</TabsTrigger>
              <TabsTrigger value="uri">URI 导入</TabsTrigger>
            </TabsList>
            <TabsContent value="uri" className="mt-5">
              <div className="fm-grid">
                <Form.Item name="link" label="URI 链接" className="fm-full" rules={[{ required: true, whitespace: true, message: '请粘贴节点 URI 链接' }]}>
                  <Input.TextArea
                    rows={3}
                    spellCheck={false}
                  />
                </Form.Item>
                <Form.Item name="group" label="分组" className="fm-full">
                  <AutoComplete options={groupOptions} allowClear filterOption={(input, option) => (option?.value ?? '').toLowerCase().includes(input.toLowerCase())} />
                </Form.Item>
              </div>
            </TabsContent>
            <TabsContent value="manual" className="mt-5">
              <FormGroup title="基本">
                <Form.Item name="protocol" label="协议" rules={[{ required: true, message: '请选择协议' }]}>
                  <Select
                    placeholder="选择协议"
                    onChange={(protocol) => {
                      if (protocol === 'http') {
                        nodeForm.setFieldValue('tls_mode', 'none')
                      } else if (TLS_REALITY.includes(protocol)) {
                        if (nodeProtocol === 'http' && !['vless', 'vmess'].includes(protocol)) nodeForm.setFieldValue('tls_mode', 'tls')
                      } else if (HAS_TLS.includes(protocol)) {
                        nodeForm.setFieldValue('tls_mode', 'tls')
                      }
                    }}
                    options={toProtocolOptions(EXTERNAL_NODE_PROTOCOL_TYPES)}
                  />
                </Form.Item>
                <Form.Item name="group" label="分组">
                  <AutoComplete options={groupOptions} allowClear filterOption={(input, option) => (option?.value ?? '').toLowerCase().includes(input.toLowerCase())} />
                </Form.Item>
                <Form.Item name="address" label="地址" rules={[{ required: true, message: '请填写地址' }]}>
                  <Input aria-label="地址" />
                </Form.Item>
                <Form.Item name="port" label="端口" rules={[{ required: true, message: '请填写端口' }]}>
                  <InputNumber precision={0} aria-label="端口" min={1} max={65535} style={{ width: '100%' }} />
                </Form.Item>

                {HAS_UUID.includes(nodeProtocol) && (
                  <Form.Item name="uuid" label="UUID" rules={[{ required: true, message: '请填写 UUID' }]}>
                    <Input />
                  </Form.Item>
                )}
                {nodeProtocol === 'shadowsocks' && (
                  <Form.Item name="method" label="加密方式">
                    <Select
                      options={SHADOWSOCKS_METHODS.map((m) => ({ value: m, label: m }))}
                    />
                  </Form.Item>
                )}
                {HAS_PASSWORD.includes(nodeProtocol) && (
                  <Form.Item name="password" label={nodeProtocol === 'shadowsocks' ? '密码 / PSK' : '密码'} rules={[{ required: true, message: '请填写密码' }]}>
                    <Input.Password />
                  </Form.Item>
                )}
                {HAS_AUTH.includes(nodeProtocol) && (
                  <>
                    <Form.Item name="username" label="用户名">
                      <Input autoComplete="off" />
                    </Form.Item>
                    <Form.Item name="password" label="密码">
                      <Input.Password autoComplete="new-password" />
                    </Form.Item>
                  </>
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
                        options={[
                          { value: 'native', label: 'native' },
                          { value: 'quic', label: 'quic' },
                        ]}
                      />
                    </Form.Item>
                  </>
                )}
                {nodeProtocol === 'shadowsocks' && (
                  <Form.Item name="ss_plugin" label="SIP002 Plugin" className="fm-full">
                    <Input />
                  </Form.Item>
                )}
                {nodeProtocol === 'anytls' && (
                  <div className="fm-switches">
                    <Form.Item name="udp_over_stream" label="UDP over Stream" valuePropName="checked" className="fm-switch">
                      <Switch />
                    </Form.Item>
                  </div>
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
                            const bytes = value ? new TextEncoder().encode(String(value)).length : 0
                            if (getFieldValue('snell_version') === 6 && bytes > 0 && (bytes < 12 || bytes > 255)) {
                              return Promise.reject(new Error('Snell v6 的 PSK 长度必须在 12-255 字节之间'))
                            }
                            return Promise.resolve()
                          },
                        }),
                      ]}
                    >
                      <Input />
                    </Form.Item>
                    <Form.Item name="snell_version" label="版本">
                      <Select options={[{ value: 5, label: 'v5' }, { value: 6, label: 'v6' }]} />
                    </Form.Item>
                    {nodeSnellVersion === 6 ? (
                      <Form.Item name="snell_mode" label="模式">
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
                      <>
                        <Form.Item name="snell_obfs_mode" label="混淆">
                          <Select options={[{ value: 'none', label: '无' }, { value: 'http', label: 'http' }, { value: 'tls', label: 'tls' }]} />
                        </Form.Item>
                        {(nodeSnellObfsMode === 'http' || nodeSnellObfsMode === 'tls') && (
                          <Form.Item name="snell_obfs_host" label="混淆 Host">
                            <Input />
                          </Form.Item>
                        )}
                      </>
                    )}
                  </>
                )}
              </FormGroup>

              {HAS_TLS.includes(nodeProtocol) && (
                <FormGroup title="TLS">
                  <Form.Item name="tls_mode" label="TLS 模式">
                    <Select
                      options={
                        TLS_REALITY.includes(nodeProtocol)
                          ? [
                              { value: 'none', label: '无' },
                              { value: 'tls', label: 'TLS' },
                              { value: 'reality', label: 'REALITY' },
                            ]
                          : nodeProtocol === 'http'
                            ? [
                                { value: 'none', label: '无' },
                                { value: 'tls', label: 'TLS' },
                              ]
                            : [
                                { value: 'tls', label: 'TLS' },
                              ]
                      }
                    />
                  </Form.Item>
                  <Form.Item name="sni" label="SNI">
                    <Input />
                  </Form.Item>
                  {(nodeProtocol === 'vless' || nodeProtocol === 'vmess' || nodeProtocol === 'trojan' || nodeProtocol === 'http') && (
                    <>
                      <Form.Item name="alpn" label="ALPN">
                        <Input />
                      </Form.Item>
                      <Form.Item name="fingerprint" label="uTLS 指纹">
                        <Select
                          options={['chrome', 'firefox', 'safari', 'ios', 'random', 'randomized'].map((v) => ({ value: v, label: v }))}
                        />
                      </Form.Item>
                    </>
                  )}
                  {nodeTLSMode === 'reality' && (
                    <>
                      <Form.Item name="pbk" label="REALITY 公钥" rules={[{ required: true, message: '请填写公钥' }]}>
                        <Input />
                      </Form.Item>
                      <Form.Item name="sid" label="REALITY Short ID">
                        <Input />
                      </Form.Item>
                    </>
                  )}
                  {nodeTLSMode === 'tls' && (
                    <div className="fm-switches">
                      <Form.Item name="insecure" label="跳过证书校验" valuePropName="checked" className="fm-switch">
                        <Switch />
                      </Form.Item>
                    </div>
                  )}
                </FormGroup>
              )}

              {HAS_TRANSPORT.includes(nodeProtocol) && (
                <FormGroup title="传输">
                  <Form.Item name="transport" label="传输协议">
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
                      <Form.Item name="path" label="传输路径" style={{ gridColumnStart: 1 }}>
                        <Input />
                      </Form.Item>
                      <Form.Item name="host" label="Host 头">
                        <Input />
                      </Form.Item>
                    </>
                  )}
                </FormGroup>
              )}

              {HAS_BANDWIDTH.includes(nodeProtocol) && (
                <FormGroup title="混淆与带宽">
                  <Form.Item name="obfs" label="混淆类型">
                    <Select
                      options={[
                        { value: '', label: '无' },
                        { value: 'salamander', label: 'salamander' },
                        ...(nodeProtocol === 'hysteria2' ? [{ value: 'gecko', label: 'gecko' }] : []),
                      ]}
                    />
                  </Form.Item>
                  <Form.Item name="obfs_password" label="混淆密码">
                    <Input.Password />
                  </Form.Item>
                  {nodeProtocol === 'hysteria2' && nodeObfsType === 'gecko' && (
                    <>
                      <Form.Item name="gecko_min_packet_size" label="最小包大小"><InputNumber min={512} precision={0} style={{ width: '100%' }} /></Form.Item>
                      <Form.Item name="gecko_max_packet_size" label="最大包大小"><InputNumber min={512} precision={0} style={{ width: '100%' }} /></Form.Item>
                    </>
                  )}
                  <Form.Item name="up_mbps" label="上行 Mbps">
                    <InputNumber precision={0} min={1} style={{ width: '100%' }} />
                  </Form.Item>
                  <Form.Item name="down_mbps" label="下行 Mbps">
                    <InputNumber precision={0} min={1} style={{ width: '100%' }} />
                  </Form.Item>
                </FormGroup>
              )}
            </TabsContent>
          </Tabs>
        </Form>
      </Modal>
    </>
  )
}
