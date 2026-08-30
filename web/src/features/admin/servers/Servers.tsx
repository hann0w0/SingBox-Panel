import { useEffect, useRef, useState, type DragEvent, type PointerEvent as ReactPointerEvent } from 'react'
import { Alert, Button, Card, Form, Grid, Input, Modal, Radio, Select, Space, Table, Tag, Typography, message } from 'antd'
import { ArrowUpOutlined, CloudDownloadOutlined, DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { createServer, deleteServer, errMsg, getServersMeta, installSingbox, updateAllAgents, updateServer, updateServerOrder } from '../../../api'
import type { Server } from '../../../types'
import { RequestState } from '../../../components/RequestState'

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

  const load = () => {
    loadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++loadRef.current.generation
    loadRef.current.controller = controller
    setLoading(true)
    setLoadError(null)
    getServersMeta(controller.signal)
      .then((res) => {
        if (generation !== loadRef.current.generation) return
        setServers(res.servers)
        if (res.latest_agent_version) {
          setLatestAgentVer(res.latest_agent_version)
        }
      })
      .catch((e) => {
        if (generation === loadRef.current.generation && !controller.signal.aborted) setLoadError(errMsg(e))
      })
      .finally(() => {
        if (generation === loadRef.current.generation) {
          loadRef.current.controller = null
          setLoading(false)
        }
      })
  }
  useEffect(() => {
    load()
    return () => {
      loadRef.current.generation++
      loadRef.current.controller?.abort()
    }
  }, [])

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
        title: '服务器已创建 · 在 VPS 上执行以下命令接入',
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
    const row = e.currentTarget.closest('tr')
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

  // Calculate a stable insertion slot from the rows that remain after the
  // dragged server is removed. The list is not reordered while the finger is
  // moving, so the same touch position cannot make the target oscillate.
  const pointerDropTargetAt = (sourceID: number, clientY: number) => {
    const rows = Array.from(document.querySelectorAll<HTMLElement>('.servers-card tbody tr[data-row-key]'))
      .map((row) => ({ row, id: Number(row.dataset.rowKey) }))
      .filter((entry) => Number.isFinite(entry.id) && entry.id !== sourceID)
    if (rows.length === 0) return null

    let insertionIndex = rows.length
    for (let index = 0; index < rows.length; index += 1) {
      const rect = rows[index].row.getBoundingClientRect()
      if (clientY <= rect.top + rect.height / 2) {
        insertionIndex = index
        break
      }
    }
    if (insertionIndex === rows.length) {
      return { id: rows[rows.length - 1].id, after: true }
    }
    return { id: rows[insertionIndex].id, after: false }
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
    const target = pointerDropTargetAt(drag.id, event.clientY)
    pointerDropRef.current = target
    setDropTarget(target)
  }

  const finishPointerDrag = (event: ReactPointerEvent<HTMLSpanElement>) => {
    const drag = pointerDragRef.current
    if (!drag || event.pointerId !== drag.pointerId) return
    event.preventDefault()
    const target = pointerDropTargetAt(drag.id, event.clientY) ?? pointerDropRef.current
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
      title="服务器"
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
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate} title="新增服务器" aria-label="新增服务器">
            <span className="server-action-label">新增服务器</span>
          </Button>
        </Space>
      }
    >
      <RequestState loading={loading} error={loadError} hasData={servers.length > 0} empty={!loading && !loadError && servers.length === 0} emptyDescription="暂无服务器" onRetry={load}>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={servers}
        pagination={false}
        scroll={{ x: 'max-content' }}
        // Tapping the row opens the node — far easier than side-scrolling to a
        // link on a phone.
        onRow={(s) => ({
          onClick: () => nav(`/admin/servers/${s.id}`),
          onKeyDown: (event) => {
            if (event.target !== event.currentTarget) return
            if (event.key === 'Enter' || event.key === ' ') {
              event.preventDefault()
              nav(`/admin/servers/${s.id}`)
            }
          },
          onDragOver: (e) => {
            if (draggingID === null || draggingID === s.id) return
            e.preventDefault()
            e.dataTransfer.dropEffect = 'move'
            const rect = e.currentTarget.getBoundingClientRect()
            setDropTarget({ id: s.id, after: e.clientY >= rect.top + rect.height / 2 })
          },
          onDrop: (e) => {
            if (draggingID === null) return
            e.preventDefault()
            e.stopPropagation()
            const rect = e.currentTarget.getBoundingClientRect()
            void dropServer(s.id, e.clientY >= rect.top + rect.height / 2)
          },
          className: [
            draggingID === s.id ? 'server-row-dragging' : '',
            dropTarget?.id === s.id ? (dropTarget.after ? 'server-row-drop-after' : 'server-row-drop-before') : '',
          ].filter(Boolean).join(' '),
          role: 'link',
          tabIndex: 0,
          style: { cursor: 'pointer' },
        })}
        columns={[
          {
            title: '',
            width: 36,
            align: 'center',
            className: 'server-drag-column',
            render: (_, s: Server) => (
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
                  if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
                    e.preventDefault()
                    moveServerByKeyboard(s, e.key === 'ArrowUp' ? -1 : 1)
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
                <span className="server-drag-bars" aria-hidden="true">
                  <i />
                  <i />
                  <i />
                </span>
              </span>
            ),
          },
          { title: '名称', dataIndex: 'name' },
          {
            title: '连接地址',
            responsive: ['md'],
            render: (_, s: Server) =>
              s.address || s.public_ip || '—',
          },
          {
            title: '状态',
            dataIndex: 'online',
            render: (v: boolean) => (v ? <Tag color="green">在线</Tag> : <Tag>离线</Tag>),
          },
          {
            title: 'sing-box',
            render: (_, s: Server) => {
              if (!s.singbox_installed) return <Tag color="orange">未安装</Tag>
              const versionStr = s.singbox_version || '已安装'
              if (s.singbox_has_update) {
                return (
                  <Tag
                    color="orange"
                    icon={<ArrowUpOutlined />}
                    title={`发现新版本 (${s.singbox_latest_version || '可升级'})，点击可进入详情升级`}
                  >
                    {versionStr} (可升级)
                  </Tag>
                )
              }
              return <Tag color="blue">{versionStr}</Tag>
            },
          },
          {
            title: 'Agent',
            render: (_, s: Server) => {
              if (!s.agent_version) return <Tag>—</Tag>
              const needsSync = s.online && s.agent_has_update === true
              return needsSync ? (
                <Tag color="orange" title={`同步至 ${s.agent_latest_version || latestAgentVer}`}>
                  {s.agent_version} (待同步)
                </Tag>
              ) : (
                <Tag color="purple">{s.agent_version}</Tag>
              )
            },
          },
          {
            title: '',
            width: 88,
            render: (_, s: Server) => (
              <Space size={0}>
                <Button
                  size="small"
                  type="text"
                  icon={<EditOutlined />}
                  aria-label="编辑"
                  title="编辑"
                  onClick={(e) => {
                    e.stopPropagation() // row click opens the node
                    openEdit(s)
                  }}
                />
                <Button
                  size="small"
                  type="text"
                  danger
                  icon={<DeleteOutlined />}
                  aria-label="删除"
                  title="删除"
                  onClick={(e) => {
                    e.stopPropagation()
                    onDelete(s)
                  }}
                />
              </Space>
            ),
          },
        ]}
      />
      </RequestState>
      <span id="server-sort-help" className="sr-only">使用上方向键或下方向键调整服务器顺序</span>
      <div className="sr-only" aria-live="polite" aria-atomic="true">{sortAnnouncement}</div>

      <Modal
        title={editing ? `编辑 ${editing.name}` : '新增服务器'}
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
        <div style={{ fontSize: 13, color: 'rgba(0,0,0,0.45)', marginBottom: 12 }}>
          向所有<b>在线</b>节点下发官方 sing-box 安装/升级指令。点击开始后窗口即关闭，更新在后台进行，完成后节点列表会自动刷新。
        </div>
        <Form form={singboxForm} layout="vertical" initialValues={{ channel: 'beta', method: 'script' }}>
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
            <Input placeholder="如 1.14.0-beta.17，可留空" />
          </Form.Item>
        </Form>
      </Modal>

    </Card>
  )
}
