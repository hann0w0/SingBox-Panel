import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import {
  AutoComplete,
  Button,
  Checkbox,
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
import {
  Trash2,
  ChevronDown,
  Pencil,
  Eye,
  ChevronsUpDown,
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
} from '../../../lib/api'
import type { CustomNode, CustomNodeSubscription, NameRewriteRule, UserNodeOrderItem } from '../../../lib/api'
import type { Inbound, Server } from '../../../lib/types'
import { RegionFlag, regionCodeFromFlag, removeRegionFlag } from '../../../components/RegionFlag'
import { nodeTypeLabel, protocolOf } from './nodeLabels'
import { formatHostPort, inboundConnectionHost, inboundConnectionPort } from '../../../lib/nodeAddress'
import { SHADOWSOCKS_METHODS } from '../../../lib/util'
import { RequestState } from '../../../components/RequestState'
import { buildCustomNodeParams } from '../../../lib/customNodeForm'
import { Pills, Status } from '../../../components/kit/Page'
import { Field, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/components/ui/field'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import '../../../styles/users.css'
import { MODAL_MD } from '../../../components/kit/modal'

// ======================= 节点分配 =======================


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
  return nodeTypeLabel[typ.trim().toLowerCase()] || typ.trim().toLowerCase()
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
        detail: formatHostPort(inboundConnectionHost(server, inbound.settings), inboundConnectionPort(inbound), '-'),
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
        detail: formatHostPort(node.address, node.port, '分享链接'),
        protocol: node.link?.trim() ? protocolOf(node.link) : node.protocol,
        enabled: true,
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
                    <ChevronsUpDown />
                  </button>
                <span className="um-access-order-number">{index + 1}</span>
                <div className="um-access-order-content">
                  <strong title={(orderItem.custom_name ?? '').trim() || defaultName}>{(orderItem.custom_name ?? '').trim() || defaultName}</strong>
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
