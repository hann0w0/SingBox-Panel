import { useEffect, useRef, useState, type DragEvent, type PointerEvent as ReactPointerEvent } from 'react'
import { Button, Dropdown, Form, Grid, Input, Modal, Segmented, Select, Typography, message } from '../../../ui'
import { ChevronDown, Pencil, GripVertical, Ellipsis, Plus, Search } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { createServer, deleteServer, errMsg, getOverview, getServersMeta, installSingbox, isCanceledRequest, updateAllAgents, updateServer, updateServerOrder } from '../../../api'
import type { Server } from '../../../types'
import { formatBytes } from '../../../util'
import { RequestState } from '../../../components/RequestState'
import { PageHeader, Pills, Stats, Status } from '../../../ui/Page'
import '../../../styles/servers.css'
import { MODAL_MD, MODAL_SM } from '../../../ui/modal'

const rate = (n: number) => `${formatBytes(n)}/s`

export default function Servers() {
  const nav = useNavigate()
  const screens = Grid.useBreakpoint()
  const isMobile = !screens.md
  const [servers, setServers] = useState<Server[]>([])
  const [latestAgentVer, setLatestAgentVer] = useState<string>('unknown')
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [updatingAll, setUpdatingAll] = useState(false)
  const [updatingSingbox, setUpdatingSingbox] = useState(false)
  const [singboxModalOpen, setSingboxModalOpen] = useState(false)
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<Server | null>(null)
  const [sorting, setSorting] = useState(false)
  const [draggingID, setDraggingID] = useState<number | null>(null)
  const [dropTarget, setDropTarget] = useState<{ id: number; after: boolean } | null>(null)
  const [sortAnnouncement, setSortAnnouncement] = useState('')
  const [usersTotal, setUsersTotal] = useState<number | null>(null)
  const [filter, setFilter] = useState('all')
  const [query, setQuery] = useState('')
  const pointerDragRef = useRef<{ id: number; pointerId: number } | null>(null)
  const pointerDropRef = useRef<{ id: number; after: boolean } | null>(null)
  const loadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })
  const [form] = Form.useForm()
  const submitLock = useRef(false)
  const [singboxForm] = Form.useForm()
  const singboxMethod = Form.useWatch('method', singboxForm) ?? 'script'

  const handleUpdateAllAgents = () => {
    Modal.confirm({
      title: '批量同步 Agent',
      content: '所有在线服务器将同步到当前 Agent 版本。',
      okText: '确认同步',
      onOk: async () => {
        setUpdatingAll(true)
        try {
          const res = await updateAllAgents()
          if (res.failed > 0) {
            const failed = res.results.filter((result) => !result.success)
            Modal.warning({
              title: res.message,
              width: MODAL_MD,
              content: (
                <div style={{ maxHeight: 320, overflowY: 'auto' }}>
                  {failed.map((result) => (
                    <Typography.Paragraph key={result.server_id} style={{ marginBottom: 8 }}>
                      <Typography.Text strong>{result.server_name}</Typography.Text>
                      <br />
                      <Typography.Text type="danger">{result.error || '同步失败'}</Typography.Text>
                    </Typography.Paragraph>
                  ))}
                </div>
              ),
            })
          } else {
            message.success(res.message || 'Agent 同步完成')
          }
          load()
        } catch (e) {
          message.error(errMsg(e))
        } finally {
          setUpdatingAll(false)
        }
      },
    })
  }

  const handleUpdateSingbox = () => {
    setSingboxModalOpen(true)
  }

  const doUpdateSingbox = async () => {
    const values = await singboxForm.validateFields()
    const onlineServers = servers.filter((s) => s.online)
    setSingboxModalOpen(false)
    if (onlineServers.length === 0) {
      message.warning('没有在线节点可更新')
      return
    }
    // Fire-and-forget: the per-node install blocks for minutes on the backend,
    // so kick them all off concurrently and let them finish in the background
    // instead of holding the UI. Report the aggregate result when they settle.
    message.success(`已在后台向 ${onlineServers.length} 台在线节点下发 sing-box 更新，完成后会自动刷新`)
    setUpdatingSingbox(true)
    const results = await Promise.allSettled(
      onlineServers.map((s) => installSingbox(s.id, values)),
    )
    const failed = results.filter((r) => r.status === 'rejected').length
    setUpdatingSingbox(false)
    if (failed === 0) {
      message.success('sing-box 更新完成')
    } else {
      message.warning(`sing-box 更新完成：${results.length - failed} 台成功，${failed} 台失败`)
    }
    load()
  }

  const hasDataRef = useRef(false)
  const load = (quiet = false) => {
    loadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++loadRef.current.generation
    loadRef.current.controller = controller
    if (!quiet || !hasDataRef.current) setLoading(true)
    setLoadError(null)
    getServersMeta(controller.signal)
      .then((res) => {
        if (generation !== loadRef.current.generation) return
        hasDataRef.current = true
        setServers(res.servers)
        if (res.latest_agent_version) {
          setLatestAgentVer(res.latest_agent_version)
        }
      })
      .catch((e) => {
        if (generation === loadRef.current.generation && !controller.signal.aborted && !isCanceledRequest(e)) setLoadError(errMsg(e))
      })
      .finally(() => {
        if (generation === loadRef.current.generation) {
          loadRef.current.controller = null
          setLoading(false)
        }
      })
    getOverview(controller.signal)
      .then((o) => {
        if (generation === loadRef.current.generation) setUsersTotal(o.users_total)
      })
      .catch(() => {
        // Only feeds the summary strip; the list does not depend on it.
      })
  }
  useEffect(() => {
    load()
    // Online state, rates and versions change on their own; keep them fresh
    // without the user pressing refresh. Skip while a drag is in progress.
    const timer = window.setInterval(() => {
      if (pointerDragRef.current === null && !sorting) load(true)
    }, 10000)
    return () => {
      window.clearInterval(timer)
      loadRef.current.generation++
      loadRef.current.controller?.abort()
    }
  }, [sorting])

  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    setOpen(true)
  }

  const openEdit = (s: Server) => {
    setEditing(s)
    // Reset first so fields the previous target had do not leak in.
    form.resetFields()
    form.setFieldsValue({ name: s.name, address: s.address, remark: s.remark, traffic_reset_day: s.traffic_reset_day ?? 1 })
    setOpen(true)
  }

  const onSubmit = async () => {
    if (submitLock.current) return
    submitLock.current = true
    setSaving(true)
    try {
      const v = await form.validateFields()
      if (editing) {
        await updateServer(editing.id, v)
        message.success('已保存')
        setOpen(false)
        load()
        return
      }
      const { install_command } = await createServer(v)
      setOpen(false)
      form.resetFields()
      load()
      Modal.success({
        title: '在服务器上以 root 执行',
        width: MODAL_MD,
        content: (
          <div>
            <Typography.Paragraph copyable={{ text: install_command }} code style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
              {install_command}
            </Typography.Paragraph>
          </div>
        ),
      })
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      submitLock.current = false
      setSaving(false)
    }
  }

  const onDelete = (s: Server) => {
    Modal.confirm({
      title: '删除服务器',
      okText: '删除',
      okType: 'danger',
      content: `确认从面板移除「${s.name}」？服务器上的程序和配置不受影响。`,
      onOk: async () => {
        try {
          await deleteServer(s.id)
          message.success('面板节点已删除')
          load()
        } catch (e) {
          message.error(errMsg(e))
          throw e
        }
      },
    })
  }

  const startDrag = (e: DragEvent<HTMLSpanElement>, s: Server) => {
    setDraggingID(s.id)
    setDropTarget(null)
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', String(s.id))
    const row = e.currentTarget.closest<HTMLElement>('.srv-row')
    if (row) e.dataTransfer.setDragImage(row, 24, 24)
  }

  const dropServer = async (targetID: number, after: boolean, sourceIDOverride?: number) => {
    const sourceID = sourceIDOverride ?? draggingID
    setDraggingID(null)
    setDropTarget(null)
    if (sourceID === null || sourceID === targetID) return

    const next = [...servers]
    const sourceIndex = next.findIndex((s) => s.id === sourceID)
    if (sourceIndex < 0) return
    const [moved] = next.splice(sourceIndex, 1)
    let targetIndex = next.findIndex((s) => s.id === targetID)
    if (targetIndex < 0) return
    if (after) targetIndex += 1
    next.splice(targetIndex, 0, moved)

    setSorting(true)
    try {
      await updateServerOrder(next.map((s) => s.id))
      setServers(next)
      const newPosition = next.findIndex((server) => server.id === moved.id) + 1
      setSortAnnouncement(`${moved.name} 已移动到第 ${newPosition} 位`)
      message.success('节点顺序已更新')
    } catch (e) {
      message.error(errMsg(e))
      load()
    } finally {
      setSorting(false)
    }
  }

  const moveServerByKeyboard = (server: Server, direction: -1 | 1) => {
    const index = servers.findIndex((item) => item.id === server.id)
    const target = servers[index + direction]
    if (index < 0 || !target || sorting) return
    void dropServer(target.id, direction > 0, server.id)
  }

  // Rows are stacked vertically: the upper/lower half of a row decides the
  // insertion side; above or below the list snaps to the first/last row.
  const pointerDropTargetAt = (sourceID: number, _clientX: number, clientY: number) => {
    const rows = Array.from(document.querySelectorAll<HTMLElement>('.srv-list .srv-row[data-server-id]'))
      .map((row) => ({ rect: row.getBoundingClientRect(), id: Number(row.dataset.serverId) }))
      .filter((entry) => Number.isFinite(entry.id) && entry.id !== sourceID)
    if (rows.length === 0) return null
    for (const { rect, id } of rows) {
      if (clientY >= rect.top && clientY <= rect.bottom) return { id, after: clientY >= rect.top + rect.height / 2 }
    }
    if (clientY < rows[0].rect.top) return { id: rows[0].id, after: false }
    return { id: rows[rows.length - 1].id, after: true }
  }

  const handlePointerDragStart = (event: ReactPointerEvent<HTMLSpanElement>, server: Server) => {
    if (sorting || (event.pointerType !== 'touch' && event.pointerType !== 'pen')) return
    event.preventDefault()
    event.stopPropagation()
    event.currentTarget.setPointerCapture(event.pointerId)
    pointerDragRef.current = { id: server.id, pointerId: event.pointerId }
    pointerDropRef.current = null
    setDraggingID(server.id)
    setDropTarget(null)
  }

  const handlePointerDragMove = (event: ReactPointerEvent<HTMLSpanElement>) => {
    const drag = pointerDragRef.current
    if (!drag || event.pointerId !== drag.pointerId) return
    event.preventDefault()
    const edgeSize = Math.min(72, window.innerHeight / 5)
    if (event.clientY < edgeSize) window.scrollBy({ top: -12, behavior: 'auto' })
    else if (event.clientY > window.innerHeight - edgeSize) window.scrollBy({ top: 12, behavior: 'auto' })
    const target = pointerDropTargetAt(drag.id, event.clientX, event.clientY)
    pointerDropRef.current = target
    setDropTarget(target)
  }

  const finishPointerDrag = (event: ReactPointerEvent<HTMLSpanElement>) => {
    const drag = pointerDragRef.current
    if (!drag || event.pointerId !== drag.pointerId) return
    event.preventDefault()
    const target = pointerDropTargetAt(drag.id, event.clientX, event.clientY) ?? pointerDropRef.current
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId)
    }
    pointerDragRef.current = null
    pointerDropRef.current = null
    if (target) void dropServer(target.id, target.after, drag.id)
    else {
      setDraggingID(null)
      setDropTarget(null)
    }
  }

  const cancelPointerDrag = (event: ReactPointerEvent<HTMLSpanElement>) => {
    const drag = pointerDragRef.current
    if (!drag || event.pointerId !== drag.pointerId) return
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId)
    }
    pointerDragRef.current = null
    pointerDropRef.current = null
    setDraggingID(null)
    setDropTarget(null)
  }

  const online = servers.filter((s) => s.online)
  const totalDown = online.reduce((sum, s) => sum + (s.traffic_available ? s.traffic_download_rate ?? 0 : 0), 0)
  const totalUp = online.reduce((sum, s) => sum + (s.traffic_available ? s.traffic_upload_rate ?? 0 : 0), 0)
  const q = query.trim().toLowerCase()
  const shown = servers.filter((s) => (filter === 'all' || (filter === 'online') === s.online)
    && (!q || `${s.name} ${s.address} ${s.public_ip || ''} ${s.remark || ''}`.toLowerCase().includes(q)))
  const open_ = (s: Server) => nav(`/admin/servers/${s.id}`)
  // Reordering a filtered view would move servers relative to hidden rows.
  const canSort = filter === 'all' && !q

  return (
    <div className="srv-page">
      <PageHeader
        title="服务器"
        actions={<>
          <Dropdown placement="bottomRight" menu={{ items: [
            { key: 'singbox', label: '更新 sing-box', disabled: updatingSingbox, onClick: handleUpdateSingbox },
            { key: 'agent', label: '同步 Agent', disabled: updatingAll, onClick: handleUpdateAllAgents },
          ] }}>
            <Button loading={updatingSingbox || updatingAll}>批量维护 <ChevronDown style={{ fontSize: 10 }} /></Button>
          </Dropdown>
          <Button type="primary" icon={<Plus />} onClick={openCreate}>新增服务器</Button>
        </>}
      />
      <Stats items={[
        { label: '在线', value: <>{online.length}<span className="srv-stat-total"> / {servers.length}</span></> },
        { label: '下行速率', value: rate(totalDown) },
        { label: '上行速率', value: rate(totalUp) },
        { label: '用户', value: usersTotal ?? '—' },
      ]} />
      <Pills
        label="筛选"
        active={filter}
        onChange={setFilter}
        items={[
          { key: 'all', label: '全部', count: servers.length },
          { key: 'online', label: '在线', count: online.length },
          { key: 'offline', label: '离线', count: servers.length - online.length },
        ]}
        actions={<Input allowClear className="srv-search" prefix={<Search />} placeholder="搜索" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="搜索服务器" />}
      />
      <RequestState loading={loading} error={loadError} hasData={servers.length > 0} empty={!loading && !loadError && servers.length === 0} emptyDescription="暂无服务器" onRetry={() => load()}>
      <div className="srv-list" role="list">
        {shown.length === 0 && <div className="ui-empty">没有匹配的服务器</div>}
        {shown.map((s) => {
          const memPct = s.mem_total ? Math.round((s.mem_used / s.mem_total) * 100) : null
          const position = servers.findIndex((item) => item.id === s.id)
          const dropClass = dropTarget?.id === s.id ? (dropTarget.after ? ' is-drop-after' : ' is-drop-before') : ''
          return (
            <div
              key={s.id}
              role="listitem"
              data-server-id={s.id}
              className={`srv-row${s.online ? '' : ' is-offline'}${draggingID === s.id ? ' is-dragging' : ''}${dropClass}`}
              onClick={(e) => {
                if ((e.target as Element).closest('button, a, input, [role="button"], .ui-dropdown') || window.getSelection()?.toString()) return
                open_(s)
              }}
              onDragOver={(e) => {
                if (draggingID === null || draggingID === s.id) return
                e.preventDefault()
                e.dataTransfer.dropEffect = 'move'
                const rect = e.currentTarget.getBoundingClientRect()
                setDropTarget({ id: s.id, after: e.clientY >= rect.top + rect.height / 2 })
              }}
              onDrop={(e) => {
                if (draggingID === null) return
                e.preventDefault()
                e.stopPropagation()
                const rect = e.currentTarget.getBoundingClientRect()
                void dropServer(s.id, e.clientY >= rect.top + rect.height / 2)
              }}
            >
              <span
                className={`srv-drag${canSort ? '' : ' is-hidden'}`}
                draggable={!isMobile && !sorting && canSort}
                aria-hidden={!canSort || undefined}
                tabIndex={canSort ? 0 : -1}
                role="button"
                aria-label={`拖动 ${s.name} 排序`}
                aria-describedby="server-sort-help"
                onKeyDown={(e) => {
                  if (!canSort) return
                  if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
                    e.preventDefault()
                    moveServerByKeyboard(s, e.key === 'ArrowUp' ? -1 : 1)
                  }
                }}
                onPointerDown={(e) => { if (canSort) handlePointerDragStart(e, s) }}
                onPointerMove={handlePointerDragMove}
                onPointerUp={finishPointerDrag}
                onPointerCancel={cancelPointerDrag}
                onDragStart={(e) => startDrag(e, s)}
                onDragEnd={() => {
                  setDraggingID(null)
                  setDropTarget(null)
                  pointerDragRef.current = null
                  pointerDropRef.current = null
                }}
              >
                <GripVertical />
              </span>
              <div className="srv-name">
                <a href={`/admin/servers/${s.id}`} onClick={(e) => { e.preventDefault(); open_(s) }}>{s.name}</a>
                <span className="mono">{s.address || s.public_ip || '未设置地址'}</span>
                {s.remark?.trim() && <span className="srv-remark" title={s.remark}>{s.remark}</span>}
              </div>
              <div className="srv-status"><Status tone={s.online ? 'ok' : 'off'}>{s.online ? '在线' : '离线'}</Status></div>
              <div className="srv-version">
                <span>{s.singbox_installed ? `sing-box ${s.singbox_version || ''}` : '未安装 sing-box'}{s.singbox_has_update && <i className="srv-up" title={`可升级至 ${s.singbox_latest_version || ''}`}>↑</i>}</span>
                <small>Agent {s.agent_version || '—'}{s.online && s.agent_has_update && <i className="srv-up" title={`同步至 ${s.agent_latest_version || latestAgentVer}`}>↑</i>}</small>
              </div>
              <div className="srv-load">
                <span className="srv-label">负载 / 内存</span>
                <span>{s.online ? s.load1.toFixed(2) : '—'}<span className="faint"> · </span><span className={memPct !== null && s.online && memPct >= 85 ? 'srv-hot' : undefined}>{s.online && memPct !== null ? `${memPct}%` : '—'}</span></span>
              </div>
              <div className="srv-rate mono">
                <span>↓ {s.online && s.traffic_available ? rate(s.traffic_download_rate ?? 0) : '—'}</span>
                <span className="faint">↑ {s.online && s.traffic_available ? rate(s.traffic_upload_rate ?? 0) : '—'}</span>
              </div>
              <div className="ui-row-actions">
                <Button size="small" type="text" icon={<Pencil />} aria-label={`编辑 ${s.name}`} onClick={() => openEdit(s)} />
                <Dropdown placement="bottomRight" menu={{ items: [
                  { key: 'up', label: '上移', disabled: !canSort || position <= 0 || sorting, onClick: () => moveServerByKeyboard(s, -1) },
                  { key: 'down', label: '下移', disabled: !canSort || position >= servers.length - 1 || sorting, onClick: () => moveServerByKeyboard(s, 1) },
                  { type: 'divider' },
                  { key: 'delete', label: '删除', danger: true, onClick: () => onDelete(s) },
                ] }}>
                  <Button size="small" type="text" icon={<Ellipsis />} aria-label={`${s.name} 更多操作`} />
                </Dropdown>
              </div>
            </div>
          )
        })}
      </div>
      </RequestState>
      <span id="server-sort-help" className="sr-only">使用方向键调整服务器顺序</span>
      <div className="sr-only" aria-live="polite" aria-atomic="true">{sortAnnouncement}</div>

      <Modal
        title={editing ? `编辑 ${editing.name}` : '新增服务器'}
        width={MODAL_SM}
        open={open}
        onOk={onSubmit}
        onCancel={() => setOpen(false)}
        confirmLoading={saving}
      >
        <Form form={form} layout="vertical" initialValues={{ traffic_reset_day: 1 }}>
          <Form.Item name="name" label="名称" rules={[{ required: true }]}>
            <Input placeholder="hk-1" />
          </Form.Item>
          <Form.Item name="address" label="连接地址">
            <Input placeholder="hk.example.com" />
          </Form.Item>
          <Form.Item name="remark" label="备注">
            <Input />
          </Form.Item>
          <Form.Item name="traffic_reset_day" label="流量重置日" extra="每月当天 UTC 00:00 重新计量；短月按月末，重置前流量不计入页面，历史记录保留。" rules={[{ required: true }]}>
            <Select options={Array.from({ length: 31 }, (_, index) => ({ value: index + 1, label: `每月 ${index + 1} 日` }))} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title="批量更新 sing-box"
        width={MODAL_SM}
        open={singboxModalOpen}
        onOk={doUpdateSingbox}
        onCancel={() => setSingboxModalOpen(false)}
        okText="开始更新"
      >
        <Form form={singboxForm} layout="vertical" initialValues={{ channel: 'stable', method: 'script' }}>
          <Form.Item name="channel" label="版本渠道">
            <Segmented options={[{ value: 'stable', label: '稳定版' }, { value: 'beta', label: '测试版' }]} />
          </Form.Item>
          <Form.Item name="method" label="安装方式">
            <Select
              onChange={(value) => {
                if (value !== 'script') singboxForm.setFieldsValue({ version: undefined })
              }}
              options={[
                { value: 'script', label: '官方安装脚本' },
                { value: 'apt', label: '官方 APT 源' },
                { value: 'dnf', label: '官方 DNF 源' },
              ]}
            />
          </Form.Item>
          <Form.Item name="version" label="版本" extra={singboxMethod !== 'script' ? '指定版本请使用官方脚本' : undefined}>
            <Input disabled={singboxMethod !== 'script'} placeholder={singboxMethod === 'script' ? '最新' : '使用软件源最新版本'} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}
