import { useEffect, useRef, useState, type DragEvent, type PointerEvent as ReactPointerEvent } from 'react'
import { Alert, Button, Card, Form, Grid, Input, Modal, Radio, Select, Space, Tag, Typography, message } from 'antd'
import { ArrowUpOutlined, CloudDownloadOutlined, DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { createServer, deleteServer, errMsg, getOverview, getServersMeta, installSingbox, isCanceledRequest, updateAllAgents, updateServer, updateServerOrder } from '../../../api'
import type { Server } from '../../../types'
import { formatBytes } from '../../../util'
import { RequestState } from '../../../components/RequestState'

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
  const [usersExpiring, setUsersExpiring] = useState(0)
  const pointerDragRef = useRef<{ id: number; pointerId: number } | null>(null)
  const pointerDropRef = useRef<{ id: number; after: boolean } | null>(null)
  const loadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })
  const [form] = Form.useForm()
  const submitLock = useRef(false)
  const [singboxForm] = Form.useForm()

  const handleUpdateAllAgents = () => {
    Modal.confirm({
      title: '批量同步 Agent',
      content: '确定要将所有在线服务器同步到面板当前提供的 Agent 版本吗？',
      okText: '确认同步',
      onOk: async () => {
        setUpdatingAll(true)
        try {
          const res = await updateAllAgents()
          if (res.failed > 0) {
            const failed = res.results.filter((result) => !result.success)
            Modal.warning({
              title: res.message,
              width: 620,
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
        if (generation === loadRef.current.generation) setUsersExpiring(o.users_expiring)
      })
      .catch(() => {
        // Only feeds the expiry banner; the table does not depend on it.
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
    form.setFieldsValue({ name: s.name, address: s.address, remark: s.remark })
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
        title: '主机已创建 · 在 VPS 上执行以下命令接入',
        width: 680,
        content: (
          <div>
            <Typography.Paragraph copyable={{ text: install_command }} code style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
              {install_command}
            </Typography.Paragraph>
            <Typography.Text type="secondary">以 root 运行；Agent 会反连面板并可远程安装官方 sing-box。</Typography.Text>
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
      icon: null,
      title: <div style={{ textAlign: 'center' }}>删除面板节点 {s.name}?</div>,
      okText: '确认删除',
      okType: 'danger',
      content: (
        <Alert
          type="warning"
          showIcon
          message="仅从面板中移除该节点"
          description="不会向 VPS 下发任何操作，不会修改 sing-box 配置文件，也不会停止、重启或卸载 sing-box 服务；Agent 和自启服务同样保留。"
        />
      ),
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
    const card = e.currentTarget.closest<HTMLElement>('.server-card')
    if (card) e.dataTransfer.setDragImage(card, 24, 24)
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

  // Find the insertion slot among the cards that remain after the dragged
  // server is removed. Cards flow in reading order inside a grid, so a point
  // inside a card picks its left/right half; a point between cards snaps to
  // the nearest card on that row, else to the first card of the next row.
  const pointerDropTargetAt = (sourceID: number, clientX: number, clientY: number) => {
    const cards = Array.from(document.querySelectorAll<HTMLElement>('.servers-card .server-card[data-server-id]'))
      .map((card) => ({ rect: card.getBoundingClientRect(), id: Number(card.dataset.serverId) }))
      .filter((entry) => Number.isFinite(entry.id) && entry.id !== sourceID)
    if (cards.length === 0) return null
    for (const { rect, id } of cards) {
      if (clientX >= rect.left && clientX <= rect.right && clientY >= rect.top && clientY <= rect.bottom) {
        return { id, after: clientX >= rect.left + rect.width / 2 }
      }
    }
    const sameRow = cards.filter(({ rect }) => clientY >= rect.top && clientY <= rect.bottom)
    if (sameRow.length) {
      const leftOf = sameRow.filter(({ rect }) => rect.left > clientX)
      if (leftOf.length) return { id: leftOf[0].id, after: false }
      return { id: sameRow[sameRow.length - 1].id, after: true }
    }
    const below = cards.filter(({ rect }) => rect.top > clientY)
    if (below.length) return { id: below[0].id, after: false }
    return { id: cards[cards.length - 1].id, after: true }
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

  return (
    <Card
      className="servers-card"
      title="主机"
      extra={
        <Space wrap>
          <Button
            icon={<CloudDownloadOutlined />}
            onClick={handleUpdateSingbox}
            loading={updatingSingbox}
            title="更新 sing-box"
            aria-label="更新 sing-box"
          >
            <span className="server-action-label">更新 sing-box</span>
          </Button>
          <Button
            icon={<ReloadOutlined />}
            onClick={handleUpdateAllAgents}
            loading={updatingAll}
            title="更新 Agent"
            aria-label="更新 Agent"
          >
            <span className="server-action-label">更新 Agent</span>
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate} title="新增主机" aria-label="新增主机">
            <span className="server-action-label">新增主机</span>
          </Button>
        </Space>
      }
    >
      {(() => {
        const offline = servers.filter((s) => !s.online)
        const notes: string[] = []
        if (offline.length) notes.push(`${offline.length} 台离线：${offline.map((s) => s.name).join('、')}`)
        if (usersExpiring > 0) notes.push(`${usersExpiring} 个用户将在 7 天内到期`)
        return notes.length ? (
          <Alert type="warning" showIcon style={{ marginBottom: 14 }} message={notes.map((n) => <div key={n}>{n}</div>)} />
        ) : null
      })()}
      <RequestState loading={loading} error={loadError} hasData={servers.length > 0} empty={!loading && !loadError && servers.length === 0} emptyDescription="暂无主机" onRetry={() => load()}>
      <div className="server-card-grid">
        {servers.map((s) => {
          const memPct = s.mem_total ? Math.round((s.mem_used / s.mem_total) * 100) : null
          const dropClass = dropTarget?.id === s.id ? (dropTarget.after ? ' server-card-drop-after' : ' server-card-drop-before') : ''
          return (
            <div
              key={s.id}
              data-server-id={s.id}
              className={`server-card${s.online ? '' : ' is-offline'}${draggingID === s.id ? ' server-card-dragging' : ''}${dropClass}`}
              role="link"
              tabIndex={0}
              onClick={() => nav(`/admin/servers/${s.id}`)}
              onKeyDown={(event) => {
                if (event.target !== event.currentTarget) return
                if (event.key === 'Enter' || event.key === ' ') {
                  event.preventDefault()
                  nav(`/admin/servers/${s.id}`)
                }
              }}
              onDragOver={(e) => {
                if (draggingID === null || draggingID === s.id) return
                e.preventDefault()
                e.dataTransfer.dropEffect = 'move'
                const rect = e.currentTarget.getBoundingClientRect()
                setDropTarget({ id: s.id, after: e.clientX >= rect.left + rect.width / 2 })
              }}
              onDrop={(e) => {
                if (draggingID === null) return
                e.preventDefault()
                e.stopPropagation()
                const rect = e.currentTarget.getBoundingClientRect()
                void dropServer(s.id, e.clientX >= rect.left + rect.width / 2)
              }}
            >
              <div className="server-card-head">
                <span
                  className="server-drag-handle"
                  draggable={!isMobile && !sorting}
                  role="button"
                  tabIndex={0}
                  aria-label={`拖动 ${s.name} 排序`}
                  aria-describedby="server-sort-help"
                  title="按住拖动排序"
                  onClick={(e) => e.stopPropagation()}
                  onKeyDown={(e) => {
                    e.stopPropagation()
                    if (e.key === 'ArrowUp' || e.key === 'ArrowDown' || e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
                      e.preventDefault()
                      moveServerByKeyboard(s, e.key === 'ArrowUp' || e.key === 'ArrowLeft' ? -1 : 1)
                    }
                  }}
                  onPointerDown={(e) => handlePointerDragStart(e, s)}
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
                  <span className="server-drag-bars" aria-hidden="true"><i /><i /><i /></span>
                </span>
                <span className="server-card-name">{s.name}</span>
                {s.online ? <Tag color="green">在线</Tag> : <Tag>离线</Tag>}
                <Space size={0} className="server-card-actions" onClick={(e) => e.stopPropagation()}>
                  <Button size="small" type="text" icon={<EditOutlined />} aria-label="编辑" title="编辑" onClick={() => openEdit(s)} />
                  <Button size="small" type="text" danger icon={<DeleteOutlined />} aria-label="删除" title="删除" onClick={() => onDelete(s)} />
                </Space>
              </div>

              <div className="server-card-versions">
                {!s.singbox_installed ? (
                  <Tag color="orange">sing-box 未安装</Tag>
                ) : s.singbox_has_update ? (
                  <Tag color="orange" icon={<ArrowUpOutlined />} title={`发现新版本 ${s.singbox_latest_version || ''}，进入详情升级`}>sing-box {s.singbox_version || '已安装'} · 可升级</Tag>
                ) : (
                  <Tag color="blue">sing-box {s.singbox_version || '已安装'}</Tag>
                )}
                {s.agent_version ? (
                  s.online && s.agent_has_update ? (
                    <Tag color="orange" title={`同步至 ${s.agent_latest_version || latestAgentVer}`}>Agent {s.agent_version} · 待同步</Tag>
                  ) : (
                    <Tag color="purple">Agent {s.agent_version}</Tag>
                  )
                ) : null}
              </div>

              <div className="server-card-metrics">
                <div>
                  <span className="server-card-metric-label">负载</span>
                  <span className="server-card-metric-value">{s.online ? s.load1.toFixed(2) : '—'}</span>
                </div>
                <div>
                  <span className="server-card-metric-label">内存</span>
                  <span className="server-card-metric-value" style={{ color: memPct !== null && s.online ? (memPct >= 85 ? 'var(--console-error)' : memPct >= 60 ? 'var(--console-warning)' : undefined) : undefined }}>
                    {s.online && memPct !== null ? `${memPct}%` : '—'}
                  </span>
                </div>
                <div>
                  <span className="server-card-metric-label">下行</span>
                  <span className="server-card-metric-value">{s.online && s.traffic_available ? rate(s.traffic_download_rate ?? 0) : '—'}</span>
                </div>
                <div>
                  <span className="server-card-metric-label">上行</span>
                  <span className="server-card-metric-value">{s.online && s.traffic_available ? rate(s.traffic_upload_rate ?? 0) : '—'}</span>
                </div>
              </div>


              <div className="server-card-foot">
                <span className="server-card-muted" title="客户端连接地址">{s.address || s.public_ip || '未设置地址'}</span>
                <span className="server-card-muted">入站 {s.inbounds?.length ?? 0}</span>
              </div>
            </div>
          )
        })}
      </div>
      </RequestState>
      <span id="server-sort-help" className="sr-only">使用方向键调整服务器顺序</span>
      <div className="sr-only" aria-live="polite" aria-atomic="true">{sortAnnouncement}</div>

      <Modal
        title={editing ? `编辑 ${editing.name}` : '新增主机'}
        open={open}
        onOk={onSubmit}
        onCancel={() => setOpen(false)}
        confirmLoading={saving}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true }]}>
            <Input placeholder="hk-1" />
          </Form.Item>
          <Form.Item
            name="address"
            label="连接地址"
            extra="客户端订阅里使用的域名或 IP。留空则用 Agent 上报的公网 IP。"
          >
            <Input placeholder="hk.example.com" />
          </Form.Item>
          <Form.Item name="remark" label="备注">
            <Input />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title="更新 sing-box"
        open={singboxModalOpen}
        onOk={doUpdateSingbox}
        onCancel={() => setSingboxModalOpen(false)}
        okText="开始更新"
        destroyOnClose
      >
        <div style={{ fontSize: 13, color: 'var(--console-muted)', marginBottom: 12 }}>
          向所有<b>在线</b>节点下发官方 sing-box 安装/升级指令。点击开始后窗口即关闭，更新在后台进行，完成后节点列表会自动刷新。
        </div>
        <Form form={singboxForm} layout="vertical" initialValues={{ channel: 'stable', method: 'script' }}>
          <Form.Item name="channel" label="版本渠道">
            <Radio.Group>
              <Radio.Button value="stable">稳定版</Radio.Button>
              <Radio.Button value="beta">测试版 (beta)</Radio.Button>
            </Radio.Group>
          </Form.Item>
          <Form.Item name="method" label="安装方式">
            <Select
              options={[
                { value: 'script', label: '官方安装脚本' },
                { value: 'apt', label: '官方 APT 源' },
                { value: 'dnf', label: '官方 DNF 源' },
              ]}
            />
          </Form.Item>
          <Form.Item name="version" label="指定版本" extra="留空则安装所选渠道的最新版（仅脚本方式支持指定版本）">
            <Input placeholder="如 1.14.2，可留空" />
          </Form.Item>
        </Form>
      </Modal>

    </Card>
  )
}
