import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import {
  AutoComplete,
  Button,
  Checkbox,
  Form,
  Grid,
  Input,
  InputNumber,
  Modal,
  Popover,
  Select,
  Space,
  Switch,
  Table,
  Typography,
  message,
} from '../../../ui'
import type { ColumnsType } from '../../../ui'
import {
  Trash2,
  ChevronDown,
  Pencil,
  Eye,
  GripVertical,
  Plus,
  RotateCw,
  ChevronRight,
  Search,
  Undo2,
  X,
  ChevronUp,
} from 'lucide-react'
import {
  batchSetCustomNodeGroup,
  createCustomNodeSubscription,
  createCustomNode,
  deleteCustomNodeSubscription,
  deleteCustomNode,
  errMsg,
  getUserAccess,
  listCustomNodes,
  listCustomNodeSubscriptions,
  listServers,
  syncCustomNodeSubscription,
  updateCustomNodeSubscription,
  updateCustomNode,
  updateUserAccess,
} from '../../../api'
import type { CustomNode, CustomNodeSubscription, NameRewriteRule, UserNodeOrderItem } from '../../../api'
import type { Inbound, Server } from '../../../types'
import { RegionFlag, regionCodeFromFlag, removeRegionFlag } from '../../../components/RegionFlag'
import { VirtualList } from '../../../components/VirtualList'
import { SHADOWSOCKS_METHODS } from '../../../util'
import { RequestState } from '../../../components/RequestState'
import { buildCustomNodeParams } from '../../../customNodeForm'
import { Pills, Status } from '../../../ui/Page'
import '../../../styles/users.css'
import { MODAL_LG, MODAL_MD } from '../../../ui/modal'

// ======================= 节点分配 =======================

// protocolOf extracts the share-link scheme (used to label custom nodes).
function protocolOf(link: string): string {
  const m = /^([a-z0-9+.-]+):\/\//i.exec(link.trim())
  return m ? m[1].toLowerCase() : '-'
}

const nodeTypeLabel: Record<string, string> = {
  vless: 'VLESS',
  vmess: 'VMess',
  ss: 'SS',
  shadowsocks: 'SS',
  trojan: 'Trojan',
  hysteria2: 'Hysteria2',
  hysteria: 'Hysteria',
  tuic: 'TUIC',
  anytls: 'AnyTLS',
  socks: 'SOCKS',
  socks5: 'SOCKS5',
  snell: 'Snell',
  mixed: 'Mixed',
}

const MAX_VISIBLE_SUBSCRIPTIONS = 5
const MAX_VISIBLE_MOBILE_NODES = 5
const MANAGEMENT_TABLE_ROW_HEIGHT = 41
const SUBSCRIPTION_TABLE_SCROLL_HEIGHT = MAX_VISIBLE_SUBSCRIPTIONS * MANAGEMENT_TABLE_ROW_HEIGHT
const subscriptionKey = (subscription: CustomNodeSubscription) => subscription.id
const customNodeKey = (node: CustomNode) => node.id

type AccessKind = 'managed' | 'custom'

interface AccessNodeItem {
  id: number
  kind: AccessKind
  name: string
  serverId?: number
  serverName?: string
  detail: string
  protocol: string
  enabled: boolean
}

function formatProtocolDisplayName(typ: string): string {
  switch (typ) {
    case 'shadowsocks':
      return 'Shadowsocks'
    case 'socks':
      return 'SOCKS5'
    case 'vless':
      return 'VLESS'
    case 'vmess':
      return 'VMess'
    case 'trojan':
      return 'Trojan'
    case 'hysteria2':
      return 'Hysteria2'
    case 'hysteria':
      return 'Hysteria'
    case 'mixed':
      return 'Mixed'
    case 'tuic':
      return 'TUIC'
    case 'anytls':
      return 'AnyTLS'
    case 'snell':
      return 'Snell'
    default:
      return typ.toUpperCase()
  }
}

function formatManagedNodeDisplayName(serverName: string, tag: string, typ: string, countOnServer: number): string {
  const trimmedServer = serverName.trim()
  if (countOnServer <= 1 && trimmedServer) return trimmedServer
  const prefix = `${typ}-`
  if (tag.startsWith(prefix) && tag.length === prefix.length + 6) {
    return `${trimmedServer} - ${formatProtocolDisplayName(typ)}`
  }
  return `${trimmedServer} - ${tag}`
}

interface AccessGroup {
  key: string
  label: string
  meta: string
  items: AccessNodeItem[]
}

function userNodeOrderKey(item: UserNodeOrderItem) {
  return `${item.node_type}:${item.node_id}`
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
    <Checkbox className={`um-access-chip${checked ? ' checked' : ''}${disabled ? ' disabled' : ''}`} title={item.detail}
      aria-label={`分配 ${item.name}`} checked={checked} disabled={disabled} onChange={(event) => onToggle(item, event.target.checked)}>
      <span className="um-access-chip-name">{item.name}</span>
      <span className="um-access-chip-proto">{nodeTypeLabel[item.protocol] || item.protocol || '-'}</span>
      {!item.enabled ? <span className="um-access-chip-off">停用</span> : null}
    </Checkbox>
  )
})

// The same assignment draft owns selection, ordering and per-user names.
// Keep it mounted while switching views so none of those edits are lost.
export function UserAccessEditor({ userId, onStateChange }: {
  userId: number
  onStateChange: (state: { dirty: boolean; saving: boolean }) => void
}) {
  const [nodes, setNodes] = useState<CustomNode[]>([])
  const [servers, setServers] = useState<Server[]>([])
  const [loadedServerIDs, setLoadedServerIDs] = useState<number[]>([])
  const [loadedServerWide, setLoadedServerWide] = useState(false)
  const [loadedInboundIDs, setLoadedInboundIDs] = useState<number[]>([])
  const [loadedCustomNodeIDs, setLoadedCustomNodeIDs] = useState<number[]>([])
  const [loadedNodeOrder, setLoadedNodeOrder] = useState<UserNodeOrderItem[]>([])
  const [inboundIDs, setInboundIDs] = useState<number[]>([])
  const [customNodeIDs, setCustomNodeIDs] = useState<number[]>([])
  const [nodeOrder, setNodeOrder] = useState<UserNodeOrderItem[]>([])
  const [accessLoading, setAccessLoading] = useState(true)
  const [accessLoaded, setAccessLoaded] = useState(false)
  const [accessError, setAccessError] = useState<string | null>(null)
  const [retryKey, setRetryKey] = useState(0)
  const [saving, setSaving] = useState(false)
  const saveLockRef = useRef(false)
  const assignmentSessionRef = useRef(0)
  const [activeTab, setActiveTab] = useState<'nodes' | 'order'>('order')
  const [activeNodeTab, setActiveNodeTab] = useState<'managed' | 'custom'>('managed')
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(() => new Set())
  const [draggedNodeKey, setDraggedNodeKey] = useState<string | null>(null)
  const [dropIndex, setDropIndex] = useState<number | null>(null)
  const [sortAnnouncement, setSortAnnouncement] = useState('')
  const [renamingNode, setRenamingNode] = useState<{ key: string; originalName: string } | null>(null)
  const nodeOrderRef = useRef(nodeOrder)
  const customNameCacheRef = useRef(new Map<string, string>())
  const orderListRef = useRef<HTMLDivElement>(null)
  const orderItemRefs = useRef(new Map<string, HTMLDivElement>())
  const pointerDragRef = useRef<{ key: string; pointerId: number; sourceIndex: number; startY: number; clientY: number; moved: boolean; handle: HTMLElement } | null>(null)
  const dropIndexRef = useRef<number | null>(null)
  const dragFrameRef = useRef<number | null>(null)
  const footerRef = useRef<HTMLElement>(null)

  nodeOrderRef.current = nodeOrder

  useEffect(() => {
    if (!renamingNode) return
    const input = orderItemRefs.current.get(renamingNode.key)?.querySelector('input')
    input?.focus()
    input?.select()
  }, [renamingNode])

  useLayoutEffect(() => {
    assignmentSessionRef.current += 1
    saveLockRef.current = false
    setSaving(false)
    return () => {
      // A request can finish after the parent leaves or replaces this page.
      // It must not overwrite another user's draft or close their session.
      assignmentSessionRef.current += 1
      if (dragFrameRef.current !== null) cancelAnimationFrame(dragFrameRef.current)
      pointerDragRef.current = null
    }
  }, [userId])

  useEffect(() => {
    const controller = new AbortController()
    setAccessLoaded(false)
    setAccessLoading(true)
    setAccessError(null)
    // Never offer a writable draft with a partial catalog: a failed external
    // node request must not look like an empty selection that can be saved.
    Promise.all([getUserAccess(userId, controller.signal), listServers(controller.signal), listCustomNodes(controller.signal)])
      .then(([access, nextServers, nextNodes]) => {
        if (controller.signal.aborted) return
        setServers(nextServers)
        setNodes(nextNodes)
        const initialOrder = access.node_order ?? []
        customNameCacheRef.current.clear()
        for (const item of initialOrder) {
          if (item.custom_name) customNameCacheRef.current.set(userNodeOrderKey(item), item.custom_name)
        }
        setLoadedServerIDs(access.server_ids ?? [])
        setLoadedServerWide(!!access.server_wide)
        setLoadedInboundIDs(access.inbound_ids)
        setLoadedCustomNodeIDs(access.custom_node_ids)
        setLoadedNodeOrder(initialOrder)
        setInboundIDs(access.inbound_ids)
        setCustomNodeIDs(access.custom_node_ids)
        setNodeOrder(initialOrder)
        setAccessLoaded(true)
      })
      .catch((e) => {
        if (controller.signal.aborted) return
        setAccessError(errMsg(e))
      })
      .finally(() => {
        if (!controller.signal.aborted) setAccessLoading(false)
      })
    return () => controller.abort()
  }, [userId, retryKey])

  const managedGroups = useMemo<AccessGroup[]>(() =>
    servers.map((server) => ({
      key: `server:${server.id}`,
      label: server.name,
      meta: [server.region, server.address || server.public_ip].filter(Boolean).join(' · '),
      items: (server.inbounds ?? []).map((inbound: Inbound) => ({
        id: inbound.id,
        kind: 'managed' as const,
        name: inbound.tag || nodeTypeLabel[inbound.type] || inbound.type,
        serverId: server.id,
        serverName: server.name,
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

  const allGroupKeys = useMemo(
    () => (activeNodeTab === 'managed' ? managedGroups : customGroups).map((g) => g.key),
    [activeNodeTab, customGroups, managedGroups],
  )

  const allNodeGroups = useMemo(
    () => [...managedGroups, ...customGroups],
    [customGroups, managedGroups],
  )

  const selectedInboundSet = useMemo(() => new Set(inboundIDs), [inboundIDs])
  const selectedCustomSet = useMemo(() => new Set(customNodeIDs), [customNodeIDs])
  const isSelected = (item: AccessNodeItem) => (
    item.kind === 'managed' ? selectedInboundSet.has(item.id) : selectedCustomSet.has(item.id)
  )

  const isDirty = useMemo(() => {
    const equal = (a: number[], b: number[]) => {
      if (a.length !== b.length) return false
      const selected = new Set(a)
      return b.every((id) => selected.has(id))
    }
    const sameOrder = loadedNodeOrder.length === nodeOrder.length
      && loadedNodeOrder.every((item, index) =>
        userNodeOrderKey(item) === userNodeOrderKey(nodeOrder[index])
        && (item.custom_name ?? '').trim() === (nodeOrder[index].custom_name ?? '').trim(),
      )
    return !equal(loadedInboundIDs, inboundIDs) || !equal(loadedCustomNodeIDs, customNodeIDs) || !sameOrder
  }, [customNodeIDs, inboundIDs, loadedCustomNodeIDs, loadedInboundIDs, loadedNodeOrder, nodeOrder])

  useEffect(() => {
    onStateChange({ dirty: isDirty, saving })
  }, [isDirty, saving, onStateChange])

  const sameIDs = useCallback((a: number[], b: number[]) => {
    if (a.length !== b.length) return false
    const selected = new Set(a)
    return b.every((id) => selected.has(id))
  }, [])

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
    if (!checked) setRenamingNode((current) => current && ids.some((id) => current.key === `${kind}:${id}`) ? null : current)
    setNodeOrder((current) => {
      const changedKeys = new Set(ids.map((id) => `${kind}:${id}`))
      if (!checked) {
        for (const item of current) {
          const key = userNodeOrderKey(item)
          if (changedKeys.has(key) && item.custom_name) {
            customNameCacheRef.current.set(key, item.custom_name)
          }
        }
        return current.filter((item) => !changedKeys.has(userNodeOrderKey(item)))
      }
      const existing = new Set(current.map(userNodeOrderKey))
      const additions = ids
        .map((id) => {
          const key = `${kind}:${id}`
          const cachedName = customNameCacheRef.current.get(key)
          return (cachedName
            ? { node_type: kind, node_id: id, custom_name: cachedName }
            : { node_type: kind, node_id: id }) as UserNodeOrderItem
        })
        .filter((item) => !existing.has(userNodeOrderKey(item)))
      return [...current, ...additions]
    })
  }, [])

  const updateNodeCustomName = useCallback((key: string, customName: string) => {
    if (customName) customNameCacheRef.current.set(key, customName)
    else customNameCacheRef.current.delete(key)
    setNodeOrder((current) => current.map((item) => (
      userNodeOrderKey(item) === key
        ? { ...item, custom_name: customName || undefined }
        : item
    )))
  }, [])

  const finishNodeRename = (key: string, cancel = false) => {
    if (cancel && renamingNode?.key === key) updateNodeCustomName(key, renamingNode.originalName)
    setRenamingNode(null)
    orderItemRefs.current.get(key)?.querySelector<HTMLButtonElement>('.um-access-order-rename')?.focus()
  }

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

  const allCollapsed = allGroupKeys.length > 0 && allGroupKeys.every((key) => !expandedGroups.has(key))
  const toggleAllGroups = () => {
    setExpandedGroups(allCollapsed ? new Set(allGroupKeys) : new Set())
  }

  const discardChanges = () => {
    setRenamingNode(null)
    customNameCacheRef.current.clear()
    for (const item of loadedNodeOrder) {
      if (item.custom_name) customNameCacheRef.current.set(userNodeOrderKey(item), item.custom_name)
    }
    setInboundIDs(loadedInboundIDs)
    setCustomNodeIDs(loadedCustomNodeIDs)
    setNodeOrder(loadedNodeOrder)
  }

  const moveNodeOrder = useCallback((from: number, to: number) => {
    const current = nodeOrderRef.current
    if (from < 0 || to < 0 || from >= current.length || to >= current.length || from === to) return
    const next = [...current]
    const [moved] = next.splice(from, 1)
    next.splice(to, 0, moved)
    nodeOrderRef.current = next
    setNodeOrder(next)
    setSortAnnouncement(`${moved.node_type === 'managed' ? '面板节点' : '外部节点'} #${moved.node_id} 已移动到第 ${to + 1} 位`)
  }, [])

  const clearPointerDrag = useCallback(() => {
    const drag = pointerDragRef.current
    pointerDragRef.current = null
    dropIndexRef.current = null
    if (dragFrameRef.current !== null) cancelAnimationFrame(dragFrameRef.current)
    dragFrameRef.current = null
    if (drag?.handle.hasPointerCapture(drag.pointerId)) drag.handle.releasePointerCapture(drag.pointerId)
    setDraggedNodeKey(null)
    setDropIndex(null)
  }, [])

  const finishPointerDrag = useCallback((event: React.PointerEvent<HTMLElement>) => {
    const drag = pointerDragRef.current
    if (!drag || event.pointerId !== drag.pointerId) return
    const targetIndex = dropIndexRef.current
    clearPointerDrag()
    if (targetIndex !== null) moveNodeOrder(drag.sourceIndex, targetIndex)
  }, [clearPointerDrag, moveNodeOrder])

  const cancelPointerDrag = useCallback((event: React.PointerEvent<HTMLElement>) => {
    const drag = pointerDragRef.current
    if (!drag || event.pointerId !== drag.pointerId) return
    clearPointerDrag()
  }, [clearPointerDrag])

  const handlePointerDragStart = useCallback((event: React.PointerEvent<HTMLElement>, key: string, index: number) => {
    if (saving || pointerDragRef.current || event.button !== 0) return
    event.preventDefault()
    event.stopPropagation()
    event.currentTarget.focus({ preventScroll: true })
    event.currentTarget.setPointerCapture(event.pointerId)
    pointerDragRef.current = { key, pointerId: event.pointerId, sourceIndex: index, startY: event.clientY, clientY: event.clientY, moved: false, handle: event.currentTarget }
    dropIndexRef.current = null
    setDropIndex(null)
  }, [saving])

  const updatePointerDrop = useCallback(() => {
    const drag = pointerDragRef.current
    if (!drag?.moved) return
    const list = orderListRef.current
    if (list) {
      const listRect = list.getBoundingClientRect()
      const headerBottom = document.querySelector('.app-header')?.getBoundingClientRect().bottom ?? 0
      const scrollWithinList = list.scrollHeight > list.clientHeight
      // A page-scrolling list must use viewport edges, not its own first row:
      // otherwise starting a drag halfway down the page scrolls it away.
      const top = Math.max(0, headerBottom, scrollWithinList ? listRect.top : 0)
      const bottom = Math.min(window.innerHeight, scrollWithinList ? listRect.bottom : window.innerHeight, footerRef.current?.getBoundingClientRect().top ?? window.innerHeight)
      const edgeSize = Math.max(0, Math.min(56, (bottom - top) / 4))
      const offset = drag.clientY < top + edgeSize ? -12 : drag.clientY > bottom - edgeSize ? 12 : 0
      if (scrollWithinList) list.scrollTop += offset
      else if (offset) window.scrollBy(0, offset)
    }
    // Keep the rendered order fixed during a pointer drag. Calculate the
    // insertion index against the list with the dragged item removed and
    // commit the move only on pointerup; this prevents mobile oscillation.
    const currentOrder = nodeOrderRef.current
    const remaining = currentOrder.filter((item) => userNodeOrderKey(item) !== drag.key)
    let targetIndex = remaining.length
    for (let index = 0; index < remaining.length; index += 1) {
      const item = remaining[index]
      const element = orderItemRefs.current.get(userNodeOrderKey(item))
      if (!element) continue
      const rect = element.getBoundingClientRect()
      if (drag.clientY <= rect.top + rect.height / 2) {
        targetIndex = index
        break
      }
    }
    dropIndexRef.current = targetIndex
    setDropIndex(targetIndex)
  }, [])

  const handlePointerDragMove = useCallback((event: React.PointerEvent<HTMLElement>) => {
    const drag = pointerDragRef.current
    if (!drag || event.pointerId !== drag.pointerId) return
    event.preventDefault()
    drag.clientY = event.clientY
    if (!drag.moved && Math.abs(drag.clientY - drag.startY) < 5) return
    drag.moved = true
    setDraggedNodeKey(drag.key)
    updatePointerDrop()
    if (dragFrameRef.current === null) {
      const scrollWhileHeld = () => {
        updatePointerDrop()
        dragFrameRef.current = requestAnimationFrame(scrollWhileHeld)
      }
      dragFrameRef.current = requestAnimationFrame(scrollWhileHeld)
    }
  }, [updatePointerDrop])

  const save = async () => {
    if (!accessLoaded || accessLoading || saveLockRef.current || !isDirty) return
    clearPointerDrag()
    saveLockRef.current = true
    const session = assignmentSessionRef.current
    setSaving(true)
    try {
      // A historical ServerIDs=[A], InboundIDs=[] grant means every current
      // and future inbound on A. Keep that wildcard whenever the administrator
      // only edits custom nodes or ordering; materialize to explicit inbounds
      // only after the managed selection itself changes.
      const preserveServerWide = loadedServerWide && sameIDs(loadedInboundIDs, inboundIDs)
      const access = await updateUserAccess(userId, {
        server_ids: preserveServerWide ? loadedServerIDs : [],
        inbound_ids: preserveServerWide ? [] : inboundIDs,
        custom_node_ids: customNodeIDs,
        node_order: nodeOrder,
      })
      if (session !== assignmentSessionRef.current) return
      setLoadedServerIDs(access.server_ids ?? [])
      setLoadedServerWide(!!access.server_wide)
      setLoadedInboundIDs(access.inbound_ids)
      setLoadedCustomNodeIDs(access.custom_node_ids)
      setLoadedNodeOrder(access.node_order)
      setInboundIDs(access.inbound_ids)
      setCustomNodeIDs(access.custom_node_ids)
      setNodeOrder(access.node_order)
      setRenamingNode(null)
      message.success('已保存节点分配，更新客户端订阅后生效')
    } catch (e) {
      if (session === assignmentSessionRef.current) message.error(errMsg(e))
    } finally {
      if (session === assignmentSessionRef.current) {
        saveLockRef.current = false
        setSaving(false)
      }
    }
  }

  // Renders panel-server groups and external-node groups together as
  // expandable, select-all-able sections in the unified node tab.
  const renderGroupSections = (groupList: AccessGroup[]) => groupList.map((group) => {
    const groupSelectedCount = group.items.filter(isSelected).length
    const expanded = expandedGroups.has(group.key)
    const kinds = group.items.reduce<Record<AccessKind, number[]>>((result, item) => {
      result[item.kind].push(item.id)
      return result
    }, { managed: [], custom: [] })
    return (
      <section className="um-access-modal-group" key={group.key}>
        <div className="um-access-modal-group-head">
          <button
            type="button"
            className="um-access-group-toggle"
            onClick={() => toggleGroupExpanded(group.key)}
            title={expanded ? '收起' : '展开'}
            aria-label={`${expanded ? '收起' : '展开'} ${group.label}`}
            aria-expanded={expanded}
          >
            {expanded ? <ChevronDown /> : <ChevronRight />}
          </button>
          <Checkbox
            aria-label={`选择 ${group.label} 全部节点`}
            checked={groupSelectedCount === group.items.length && group.items.length > 0}
            indeterminate={groupSelectedCount > 0 && groupSelectedCount < group.items.length}
            disabled={saving}
            onChange={(event) => {
              setIDs('managed', kinds.managed, event.target.checked)
              setIDs('custom', kinds.custom, event.target.checked)
            }}
          />
          <strong>{group.label === '未分组' ? <span className="um-access-group-unnamed">{group.label}</span> : group.label}</strong>
          {group.meta ? <small>{group.meta}</small> : null}
          <span className="um-access-group-count">{groupSelectedCount} / {group.items.length}</span>
        </div>
        {expanded ? (
          <div className="um-access-modal-items">
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

  const nodeItemMap = useMemo(() => {
    const result = new Map<string, AccessNodeItem>()
    for (const group of [...managedGroups, ...customGroups]) {
      for (const item of group.items) result.set(`${item.kind}:${item.id}`, item)
    }
    return result
  }, [customGroups, managedGroups])

  const selectedManagedPerServer = useMemo(() => {
    const counts = new Map<number, number>()
    for (const group of managedGroups) {
      for (const item of group.items) {
        if (item.enabled && selectedInboundSet.has(item.id) && item.serverId != null) {
          counts.set(item.serverId, (counts.get(item.serverId) ?? 0) + 1)
        }
      }
    }
    return counts
  }, [managedGroups, selectedInboundSet])

  const renderNodeOrder = () => {
    if (nodeOrder.length === 0) {
      return <div className="ui-empty">未选择节点</div>
    }
    return (
      <div className="um-access-order-list" ref={orderListRef}>
        {(() => {
          const remainingOrder = draggedNodeKey
            ? nodeOrder.filter((candidate) => userNodeOrderKey(candidate) !== draggedNodeKey)
            : []
          const dropTargetKey = dropIndex === null
            ? null
            : remainingOrder[Math.min(dropIndex, Math.max(0, remainingOrder.length - 1))]
              ? userNodeOrderKey(remainingOrder[Math.min(dropIndex, Math.max(0, remainingOrder.length - 1))])
              : null
          const dropTargetAfter = dropIndex !== null && dropIndex >= remainingOrder.length
          return nodeOrder.map((orderItem, index) => {
            const key = userNodeOrderKey(orderItem)
            const item = nodeItemMap.get(key)
            const defaultName = item
              ? (item.kind === 'managed' && item.serverName
                ? formatManagedNodeDisplayName(
                  item.serverName,
                  item.name,
                  item.protocol,
                  (item.serverId != null ? selectedManagedPerServer.get(item.serverId) : undefined) ?? 1,
                )
                : item.name)
              : `节点 #${orderItem.node_id}`
            const subtitle = item
              ? (item.kind === 'managed' && item.serverName
                ? `${item.serverName} · ${item.name} · ${item.detail}`
                : item.detail)
              : (orderItem.node_type === 'managed' ? '面板节点' : '节点')
            const dropClass = dropTargetKey === key
              ? (dropTargetAfter ? ' sort-drop-after' : ' sort-drop-before')
              : ''
            return (
              <div
                className={`um-access-order-item${renamingNode?.key === key ? ' is-renaming' : ''}${draggedNodeKey === key ? ' dragging' : ''}${dropClass}`}
                key={key}
                data-node-key={key}
                ref={(element) => {
                  if (element) orderItemRefs.current.set(key, element)
                  else orderItemRefs.current.delete(key)
                }}
              >
                <button
                    type="button"
                    className="um-access-order-handle"
                    disabled={saving}
                    onDragStart={(event) => event.preventDefault()}
                    aria-label={`调整第 ${index + 1} 个节点顺序`}
                    aria-describedby="access-sort-help"
                    onKeyDown={(event) => {
                      if (event.key === 'Escape') clearPointerDrag()
                      if (pointerDragRef.current) return
                      if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
                        event.preventDefault()
                        moveNodeOrder(index, index + (event.key === 'ArrowUp' ? -1 : 1))
                      }
                    }}
                    onPointerDown={(event) => handlePointerDragStart(event, key, index)}
                    onPointerMove={handlePointerDragMove}
                    onPointerUp={finishPointerDrag}
                    onPointerCancel={cancelPointerDrag}
                    onLostPointerCapture={cancelPointerDrag}
                  >
                    <GripVertical />
                  </button>
                <span className="um-access-order-number">{index + 1}</span>
                <div className="um-access-order-content">
                  <strong title={(orderItem.custom_name ?? '').trim() || defaultName}>{(orderItem.custom_name ?? '').trim() || defaultName}{item && !item.enabled && <span className="ui-tag um-node-disabled">停用</span>}</strong>
                  <small title={subtitle}><span className="um-access-order-mobile-meta">{index + 1} · {orderItem.node_type === 'managed' ? '面板' : '外部'} · </span>{subtitle}</small>
                </div>
                <div className="um-access-order-name-editor" id={`user-node-name-${userId}-${key}`}>
                  <Input
                    size="small"
                    className="um-access-order-name-input"
                    placeholder={defaultName}
                    aria-label={`自定义 ${defaultName} 的节点名称`}
                    aria-description="留空使用默认名称，仅对当前用户生效"
                    value={orderItem.custom_name ?? ''}
                    maxLength={128}
                    allowClear
                    disabled={saving}
                    onMouseDown={(event) => event.stopPropagation()}
                    onPointerDown={(event) => event.stopPropagation()}
                    onKeyDown={(event) => {
                      if (renamingNode?.key !== key || event.nativeEvent.isComposing || event.keyCode === 229) return
                      if (event.key === 'Enter' || event.key === 'Escape') {
                        event.preventDefault()
                        finishNodeRename(key, event.key === 'Escape')
                      }
                    }}
                    onChange={(event) => updateNodeCustomName(key, event.target.value)}
                  />
                  <Button size="small" className="um-access-order-name-done" aria-label={`完成重命名 ${defaultName}`} disabled={saving} onClick={() => finishNodeRename(key)}>完成</Button>
                </div>
                <span className="um-access-order-kind">{orderItem.node_type === 'managed' ? '面板' : '外部'}</span>
                <span className={`um-access-order-protocol${item?.protocol ? ' ui-tag' : ''}`}>{item?.protocol ? nodeTypeLabel[item.protocol] || item.protocol : null}</span>
                <div className="um-access-order-actions">
                  <Button size="small" type="text" className="um-access-order-rename" icon={<Pencil />} aria-label={`重命名 ${defaultName}`} title="重命名" aria-controls={`user-node-name-${userId}-${key}`} disabled={saving}
                    onClick={() => renamingNode?.key === key ? finishNodeRename(key) : setRenamingNode({ key, originalName: orderItem.custom_name ?? '' })} />
                  <Button size="small" type="text" icon={<X />} aria-label={`移除 ${defaultName}`} title="取消分配" disabled={saving} className="um-access-order-remove" onClick={() => setIDs(orderItem.node_type, [orderItem.node_id], false)} />
                </div>
              </div>
            )
          })
        })()}
      </div>
    )
  }

  const managedCount = managedGroups.reduce((sum, group) => sum + group.items.length, 0)
  const customCount = customGroups.reduce((sum, group) => sum + group.items.length, 0)
  const view = activeTab === 'order' ? 'order' : activeNodeTab
  const activeGroups = activeNodeTab === 'managed' ? managedGroups : customGroups

  return (
    <div className="um-access-editor">
      <RequestState loading={accessLoading} error={accessError} hasData={accessLoaded} onRetry={() => setRetryKey((value) => value + 1)}>
        <Pills
          label="节点分配视图"
          active={view}
          onChange={(key) => {
            if (key === 'order') {
              setActiveTab('order')
            } else {
              setActiveTab('nodes')
              setActiveNodeTab(key as 'managed' | 'custom')
            }
          }}
          items={[
            { key: 'order', label: '已分配', count: nodeOrder.length },
            { key: 'managed', label: '面板节点', count: managedCount },
            { key: 'custom', label: '外部节点', count: customCount },
          ]}
          actions={view !== 'order' && allGroupKeys.length > 1 ? (
            <Button size="small" type="text" icon={allCollapsed ? <ChevronDown /> : <ChevronRight />} onClick={toggleAllGroups}>
              {allCollapsed ? '全部展开' : '全部收起'}
            </Button>
          ) : null}
        />
        <p className="um-access-note">{view === 'order' ? '拖拽排序，点铅笔改名；仅对当前用户生效。' : '勾选要分配的节点，也可整组选择。停用的节点不会出现在订阅中。'}</p>
        {loadedServerWide ? (
          <p className="um-access-note">
            整机授权：{loadedServerIDs.map((id) => servers.find((server) => server.id === id)?.name || `#${id}`).join('、')}
          </p>
        ) : null}
        {view === 'order' ? renderNodeOrder() : activeGroups.length === 0 ? (
          <div className="ui-empty">{allNodeGroups.length === 0 ? '暂无可分配节点' : activeNodeTab === 'managed' ? '暂无面板节点' : '暂无外部节点'}</div>
        ) : (
          <div className="um-access-modal-body">{renderGroupSections(activeGroups)}</div>
        )}
        <footer ref={footerRef} className="um-access-footer um-access-page-footer">
          <span className={isDirty ? 'is-dirty' : ''} role="status">{isDirty ? '有未保存更改' : '已同步'}</span>
          <div className="um-access-footer-actions">
            <Button icon={<Undo2 />} disabled={!isDirty || saving} onClick={discardChanges}>撤销</Button>
            <Button type="primary" loading={saving} disabled={!isDirty || accessLoading || !accessLoaded} onClick={() => void save()}>保存更改</Button>
          </div>
        </footer>
      </RequestState>
      <span id="access-sort-help" className="sr-only">使用上方向键或下方向键调整节点顺序</span>
      <div className="sr-only" aria-live="polite" aria-atomic="true">{sortAnnouncement}</div>
    </div>
  )
}


// ======================= 自定义节点 =======================

const HAS_UUID = ['vless', 'vmess', 'tuic']
const HAS_PASSWORD = ['trojan', 'anytls', 'tuic', 'hysteria2', 'hysteria', 'shadowsocks']
const HAS_TLS = ['vless', 'vmess', 'trojan', 'anytls', 'tuic', 'hysteria2', 'hysteria']
const TLS_REALITY = ['vless', 'vmess', 'trojan']
const HAS_TRANSPORT = ['vless', 'vmess', 'trojan']
const HAS_BANDWIDTH = ['hysteria2', 'hysteria']

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

const MobileNodeCard = memo(function MobileNodeCard({
  node,
  onDetail,
  onEdit,
  onRemove,
}: {
  node: CustomNode
  onDetail: (node: CustomNode) => void
  onEdit: (node: CustomNode) => void
  onRemove: (node: CustomNode) => void
}) {
  const protocol = node.link?.trim() ? protocolOf(node.link) : node.protocol
  const type = nodeTypeLabel[protocol]
  const group = node.group?.trim() || '未分组'
  const address = node.detail?.address || node.address
  const port = node.detail?.port || node.port

  return (
    <div
      className="um-mcard is-clickable"
      onClick={(event) => {
        const target = event.target as HTMLElement
        if (target.closest('button, input')) return
        onDetail(node)
      }}
    >
      <div className="um-mcard-top">
        <span className="um-mcard-name">{nodeDisplayName(node)}</span>
        <span className="ui-row-actions">
          <Button size="small" type="text" icon={<Pencil />} aria-label={`编辑 ${node.name || '节点'}`} onClick={() => onEdit(node)} />
          <Button size="small" type="text" icon={<Trash2 />} aria-label={`删除 ${node.name || '节点'}`} onClick={() => onRemove(node)} />
        </span>
      </div>
      <div className="um-mcard-meta">
        <span className="ui-tag">{type || protocol || '-'}</span>
        <span>{group}</span>
        {!node.enabled ? <span>停用</span> : null}
      </div>
      {address ? <div className="um-mcard-addr mono">{address}{port ? `:${port}` : ''}</div> : null}
    </div>
  )
})

export function CustomNodesPanel({ nodes, loading, error, onNodesChange }: {
  nodes: CustomNode[]
  loading: boolean
  error?: string | null
  onNodesChange: () => void
}) {
  const screens = Grid.useBreakpoint()
  const isMobile = !screens.md
  const [nodeOpen, setNodeOpen] = useState(false)
  const [nodeSaving, setNodeSaving] = useState(false)
  const [editingNode, setEditingNode] = useState<CustomNode | null>(null)
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
  const hasNodeURI = normalizedNodeLink.length > 0

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
    nodeForm.resetFields()
    nodeForm.setFieldsValue({ enabled: true, link: '', tls_mode: 'tls', transport: 'tcp', snell_version: 5, snell_obfs_mode: 'none', snell_mode: '', method: '2022-blake3-aes-128-gcm', group: '' })
    setNodeOpen(true)
  }

  const openSubscriptionCreate = () => {
    setEditingSubscription(null)
    subscriptionForm.resetFields()
    subscriptionForm.setFieldsValue({ enabled: true, auto_update: true, group: '', name_rewrite_rules: [] })
    setSubscriptionOpen(true)
  }

  const openSubscriptionEdit = (subscription: CustomNodeSubscription) => {
    setEditingSubscription(subscription)
    subscriptionForm.resetFields()
    subscriptionForm.setFieldsValue({
      name: subscription.name,
      url: subscription.url,
      group: subscription.group || '',
      enabled: subscription.enabled,
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
        enabled: values.enabled !== false,
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
        } else {
          message.success(`订阅已保存并同步 ${result.sync.total} 个节点${result.sync.filtered ? `，过滤 ${result.sync.filtered} 个` : ''}`)
        }
      }
      setSubscriptionOpen(false)
      loadSubscriptions()
      onNodesChange()
    } catch (e) {
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
      message.success(`同步完成：新增 ${result.sync.created}，更新 ${result.sync.updated}，删除 ${result.sync.deleted}${result.sync.filtered ? `，过滤 ${result.sync.filtered}` : ''}`)
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
      enabled: n.enabled,
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
      name: v.name,
      group: typeof v.group === 'string' ? v.group.trim() : '',
      // 归属在「节点分配」里统一管理：新增默认不分配给任何用户，
      // 管理员在节点分配弹窗里勾选后才对指定用户可见；编辑保持原受众。
      all_users: editingNode ? editingNode.all_users : false,
      user_ids: editingNode && !editingNode.all_users ? editingNode.user_ids ?? [] : [],
      excluded_user_ids: editingNode && editingNode.all_users ? editingNode.excluded_user_ids ?? [] : [],
      enabled: v.enabled !== false,
      sort_order: editingNode?.sort_order ?? 0,
    }
      if (normalizedNodeLink) {
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
      message.error(errMsg(e))
    } finally {
      nodeSubmitLock.current = false
      setNodeSaving(false)
    }
  }

  const removeNode = (n: CustomNode) => {
    Modal.confirm({
      title: '删除节点',
      content: `确认删除「${n.name || n.link.slice(0, 24)}」？`,
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
          {!node.enabled ? <span className="ui-tag">停用</span> : null}
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

  const subscriptionStatus = (subscription: CustomNodeSubscription) => subscription.last_error
    ? <span title={subscription.last_error}><Status tone="error">同步失败</Status></span>
    : !subscription.enabled
      ? <Status tone="off">已停用</Status>
      : subscription.last_success_at || subscription.last_sync_at
        ? <Status tone="ok">正常</Status>
        : <Status tone="off">待同步</Status>

  const mobileSubscriptionList = (
    <VirtualList
      className={`virtual-list um-vlist${subscriptions.length > MAX_VISIBLE_SUBSCRIPTIONS ? ' is-scrollable' : ''}`}
      items={subscriptions}
      getKey={subscriptionKey}
      estimatedItemHeight={104}
      gap={8}
      renderItem={(subscription) => (
        <div className="um-mcard" key={subscription.id}>
          <div className="um-mcard-top">
            <span className="um-mcard-name">{subscription.name}</span>
            {subscriptionActions(subscription)}
          </div>
          <div className="um-mcard-meta">
            {subscriptionStatus(subscription)}
            <span><span className="mono">{subscription.node_count}</span> 个节点</span>
            <span>{syncLabel(subscription)}</span>
          </div>
          {subscription.last_error ? <div className="um-mcard-error">{subscription.last_error}</div> : null}
        </div>
      )}
    />
  )

  const mobileNodeList = filteredNodes.length === 0 ? (
    <div className="ui-empty">{nodeSearch.trim() || selectedGroup ? '没有匹配的节点' : '暂无节点'}</div>
  ) : (
    <VirtualList
      className={`virtual-list um-vlist${filteredNodes.length > MAX_VISIBLE_MOBILE_NODES ? ' is-scrollable' : ''}`}
      items={filteredNodes}
      getKey={customNodeKey}
      estimatedItemHeight={96}
      gap={8}
      renderItem={(node) => (
        <MobileNodeCard
          key={node.id}
          node={node}
          onDetail={setDetailNode}
          onEdit={openNodeEdit}
          onRemove={removeNode}
        />
      )}
    />
  )

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
          {isMobile ? mobileSubscriptionList : <Table<CustomNodeSubscription>
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
          />}
        </RequestState>
      ) : (
        <RequestState loading={loading} error={error} hasData={nodes.length > 0} empty={!loading && !error && nodes.length === 0} emptyDescription="暂无节点" onRetry={onNodesChange}>
          {isMobile ? mobileNodeList : <Table<CustomNode>
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
          />}
        </RequestState>
      )}

      <Modal
        title={editingSubscription ? '编辑订阅' : '新增订阅'}
        open={subscriptionOpen}
        onOk={() => void saveSubscription()}
        onCancel={() => setSubscriptionOpen(false)}
        okText={editingSubscription ? '保存' : '保存并同步'}
        confirmLoading={subscriptionSaving}
        className="um-modal"
        width={MODAL_LG}
      >
        <Form form={subscriptionForm} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: '请输入订阅名称' }]}>
            <Input placeholder="机场 A" />
          </Form.Item>
          <Form.Item name="url" label="订阅链接" rules={[{ required: true, whitespace: true, message: '请输入 HTTP(S) 订阅链接' }]}>
            <Input.TextArea rows={3} placeholder="https://example.com/sub" spellCheck={false} />
          </Form.Item>
          <Form.Item name="group" label="节点分组">
            <AutoComplete
              placeholder="机场 A"
              options={groupOptions}
              allowClear
              filterOption={(input, option) => (option?.value ?? '').toLowerCase().includes(input.toLowerCase())}
            />
          </Form.Item>
          <div className="um-form-group">
            <h4>处理规则</h4>
            <Form.List name="name_rewrite_rules">
              {(fields, { add, remove, move }) => (
                <div>
                  {fields.length > 0 ? (
                    <div className="um-rule-head">
                      <span>#</span>
                      <span>操作</span>
                      <span>匹配</span>
                      <span>替换为</span>
                      <span><span className="sr-only">调整</span></span>
                    </div>
                  ) : null}
                  {fields.map((field, index) => {
                    const action = subscriptionRules?.[field.name]?.action || 'rename'
                    return <div className="um-rule-row" key={field.key}>
                      <span className="um-rule-order mono">{index + 1}</span>
                      <Form.Item {...field} name={[field.name, 'action']} className="um-rule-action">
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
                        className="um-rule-pattern"
                      >
                        <Input
                          aria-label="匹配"
                          placeholder={action === 'exclude_protocol' ? 'anytls,tuic' : action === 'include_node' ? '新加坡,HK' : action === 'exclude_node' ? '测试,US' : '香港 或 re:^HK'}
                          maxLength={256}
                          spellCheck={false}
                        />
                      </Form.Item>
                      {action === 'rename' ? (
                        <Form.Item {...field} name={[field.name, 'replacement']} className="um-rule-replacement">
                          <Input aria-label="替换为" placeholder="HK" maxLength={512} spellCheck={false} />
                        </Form.Item>
                      ) : (
                        <span className="um-rule-none faint">—</span>
                      )}
                      <div className="um-rule-actions ui-row-actions">
                        <Button type="text" size="small" icon={<ChevronUp />} title="上移" aria-label="上移规则" disabled={index === 0} onClick={() => move(index, index - 1)} />
                        <Button type="text" size="small" icon={<ChevronDown />} title="下移" aria-label="下移规则" disabled={index === fields.length - 1} onClick={() => move(index, index + 1)} />
                        <Button type="text" size="small" icon={<Trash2 />} title="删除" aria-label="删除规则" onClick={() => remove(field.name)} />
                      </div>
                    </div>
                  })}
                  <Button type="dashed" icon={<Plus />} onClick={() => add({ action: 'rename', pattern: '', replacement: '', match_mode: 'text' })} disabled={fields.length >= 50} block>
                    {fields.length >= 50 ? '已达 50 条上限' : '添加规则'}
                  </Button>
                </div>
              )}
            </Form.List>
          </div>
          <div className="um-form-group um-switch-row">
            <Form.Item name="auto_update" label="自动更新" valuePropName="checked">
              <Switch />
            </Form.Item>
            <Form.Item name="enabled" label="节点启用" valuePropName="checked">
              <Switch />
            </Form.Item>
          </div>
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
        className="um-modal"
        width={MODAL_MD}
        style={{ maxWidth: 'calc(100vw - 24px)' }}
      >
        <Form form={nodeForm} layout="vertical">
          <Form.Item name="group" label="分组">
            <AutoComplete
              placeholder="朋友分享"
              options={groupOptions}
              allowClear
              filterOption={(input, option) => (option?.value ?? '').toLowerCase().includes(input.toLowerCase())}
            />
          </Form.Item>
          <Form.Item name="link" label="URI 链接">
            <Input.TextArea
              placeholder="vless://..."
              autoSize={{ minRows: 2, maxRows: 4 }}
              spellCheck={false}
            />
          </Form.Item>
          {!hasNodeURI && (
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
              <Form.Item label="地址 / 端口" required>
                <Space.Compact style={{ width: '100%' }}>
                  <Form.Item name="address" noStyle rules={[{ required: true, message: '请填写地址' }]}>
                    <Input aria-label="地址" placeholder="example.com" style={{ width: '70%' }} />
                  </Form.Item>
                  <Form.Item name="port" noStyle rules={[{ required: true, message: '请填写端口' }]}>
                    <InputNumber precision={0} aria-label="端口" min={1} max={65535} placeholder="443" style={{ width: '30%' }} />
                  </Form.Item>
                </Space.Compact>
              </Form.Item>

              {HAS_UUID.includes(nodeProtocol) && (
                <Form.Item name="uuid" label="UUID" rules={[{ required: true, message: '请填写 UUID' }]}>
                  <Input placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" />
                </Form.Item>
              )}
              {HAS_PASSWORD.includes(nodeProtocol) && (
                <Form.Item name="password" label={nodeProtocol === 'shadowsocks' ? '密码 / PSK' : '密码'} rules={[{ required: true, message: '请填写密码' }]}>
                  <Input.Password />
                </Form.Item>
              )}
              {nodeProtocol === 'shadowsocks' && (
                <Form.Item name="method" label="加密方式">
                  <Select
                    options={SHADOWSOCKS_METHODS.map((m) => ({ value: m, label: m }))}
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
                <Form.Item name="ss_plugin" label="SIP002 Plugin">
                  <Input placeholder="obfs-local;obfs=http" />
                </Form.Item>
              )}

              {HAS_TLS.includes(nodeProtocol) && (
                <div className="um-form-group">
                  <h4>TLS</h4>
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
                    <Input placeholder="example.com" />
                  </Form.Item>
                  {(nodeProtocol === 'vless' || nodeProtocol === 'vmess' || nodeProtocol === 'trojan') && (
                    <>
                      <Form.Item name="alpn" label="ALPN">
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
                    <Form.Item name="pbk" label="REALITY 公钥" rules={[{ required: true, message: '请填写公钥' }]}>
                      <Input />
                    </Form.Item>
                  )}
                  {nodeTLSMode === 'reality' && (
                    <Form.Item name="sid" label="REALITY Short ID">
                      <Input placeholder="0000000000000000" />
                    </Form.Item>
                  )}
                </div>
              )}

              {HAS_TRANSPORT.includes(nodeProtocol) && (
                <div className="um-form-group">
                  <h4>传输</h4>
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
                      <Form.Item name="path" label="传输路径">
                        <Input placeholder="/ws" />
                      </Form.Item>
                      <Form.Item name="host" label="Host 头">
                        <Input placeholder="example.com" />
                      </Form.Item>
                    </>
                  )}
                </div>
              )}

              {HAS_BANDWIDTH.includes(nodeProtocol) && (
                <div className="um-form-group">
                  <h4>混淆与带宽</h4>
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
                    <div className="um-form-grid">
                      <Form.Item name="gecko_min_packet_size" label="最小包大小"><InputNumber min={512} precision={0} style={{ width: '100%' }} /></Form.Item>
                      <Form.Item name="gecko_max_packet_size" label="最大包大小"><InputNumber min={512} precision={0} style={{ width: '100%' }} /></Form.Item>
                    </div>
                  )}
                  <div className="um-form-grid">
                    <Form.Item name="up_mbps" label="上行 Mbps">
                      <InputNumber precision={0} min={1} style={{ width: '100%' }} placeholder="100" />
                    </Form.Item>
                    <Form.Item name="down_mbps" label="下行 Mbps">
                      <InputNumber precision={0} min={1} style={{ width: '100%' }} placeholder="100" />
                    </Form.Item>
                  </div>
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
                  <div className="um-form-grid">
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
                            <Input placeholder="bing.com" />
                          </Form.Item>
                        )}
                      </>
                    )}
                  </div>
                </>
              )}
              {nodeProtocol === 'mixed' && (
                <>
                  <Form.Item name="username" label="用户名">
                    <Input />
                  </Form.Item>
                  <Form.Item name="password" label="密码">
                    <Input.Password />
                  </Form.Item>
                </>
              )}
            </>
          )}
          {(!hasNodeURI || editingNode) && (
            <Form.Item name="name" label="显示名称">
              <Input placeholder="HK 01" />
            </Form.Item>
          )}
          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </>
  )
}
