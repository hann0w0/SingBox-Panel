import { useEffect, useState, type DragEvent } from 'react'
import { Alert, Button, Card, Form, Input, Modal, Space, Table, Tag, Typography, message } from 'antd'
import { ArrowUpOutlined, DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { createServer, deleteServer, errMsg, getServersMeta, updateAllAgents, updateServer, updateServerOrder } from '../../api'
import type { Server } from '../../types'

export default function Servers() {
  const nav = useNavigate()
  const [servers, setServers] = useState<Server[]>([])
  const [latestAgentVer, setLatestAgentVer] = useState<string>('v1.0.0')
  const [loading, setLoading] = useState(false)
  const [updatingAll, setUpdatingAll] = useState(false)
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<Server | null>(null)
  const [sorting, setSorting] = useState(false)
  const [draggingID, setDraggingID] = useState<number | null>(null)
  const [dropTarget, setDropTarget] = useState<{ id: number; after: boolean } | null>(null)
  const [form] = Form.useForm()

  const handleUpdateAllAgents = () => {
    Modal.confirm({
      title: '批量更新 Agent',
      content: '确定要向所有在线服务器发派 Agent 升级指令吗？',
      okText: '确认更新',
      onOk: async () => {
        setUpdatingAll(true)
        try {
          const res = await updateAllAgents()
          message.success(res.message || '升级指令已下发')
          load()
        } catch (e) {
          message.error(errMsg(e))
        } finally {
          setUpdatingAll(false)
        }
      },
    })
  }

  const load = () => {
    setLoading(true)
    getServersMeta()
      .then((res) => {
        setServers(res.servers)
        if (res.latest_agent_version) {
          setLatestAgentVer(res.latest_agent_version)
        }
      })
      .catch((e) => message.error(errMsg(e)))
      .finally(() => setLoading(false))
  }
  useEffect(() => {
    load()
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
    form.setFieldsValue({ name: s.name, address: s.address, region: s.region, remark: s.remark })
    setOpen(true)
  }

  const onSubmit = async () => {
    const v = await form.validateFields()
    if (editing) {
      try {
        await updateServer(editing.id, v)
        message.success('已保存')
        setOpen(false)
        load()
      } catch (e) {
        message.error(errMsg(e))
      }
      return
    }
    try {
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

  const dropServer = async (targetID: number, after: boolean) => {
    const sourceID = draggingID
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
      message.success('节点顺序已更新')
    } catch (e) {
      message.error(errMsg(e))
      load()
    } finally {
      setSorting(false)
    }
  }

  const outdatedServers = servers.filter(
    (s) => s.online && s.agent_version && s.agent_version !== latestAgentVer
  )

  return (
    <Card
      title="服务器"
      extra={
        <Space>
          <Button icon={<ReloadOutlined />} onClick={handleUpdateAllAgents} loading={updatingAll}>
            更新全部 Agent
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            新增服务器
          </Button>
        </Space>
      }
    >
      <Table
        rowKey="id"
        loading={loading}
        dataSource={servers}
        pagination={false}
        // Tapping the row opens the node — far easier than side-scrolling to a
        // link on a phone.
        onRow={(s) => ({
          onClick: () => nav(`/admin/servers/${s.id}`),
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
                draggable={!sorting}
                role="button"
                aria-label={`拖动 ${s.name} 排序`}
                title="按住拖动排序"
                onClick={(e) => e.stopPropagation()}
                onDragStart={(e) => startDrag(e, s)}
                onDragEnd={() => {
                  setDraggingID(null)
                  setDropTarget(null)
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
          { title: '地区', dataIndex: 'region', responsive: ['sm'], render: (v) => v || '—' },
          {
            title: '连接地址',
            responsive: ['md'],
            render: (_, s: Server) =>
              s.address || (s.public_ip ? `${s.public_ip}（自动）` : '—'),
          },
          {
            title: '状态',
            dataIndex: 'online',
            render: (v: boolean) => (v ? <Tag color="green">在线</Tag> : <Tag>离线</Tag>),
          },
          {
            title: 'sing-box',
            responsive: ['sm'],
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
            responsive: ['sm'],
            render: (_, s: Server) => {
              if (!s.agent_version) return <Tag>—</Tag>
              const isOutdated = s.online && s.agent_version !== latestAgentVer
              return isOutdated ? (
                <Tag color="orange" title={`可升级至 ${latestAgentVer}`}>
                  {s.agent_version} (可升级)
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

      <Modal
        title={editing ? `编辑 ${editing.name}` : '新增服务器'}
        open={open}
        onOk={onSubmit}
        onCancel={() => setOpen(false)}
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
          <Form.Item name="region" label="地区">
            <Input placeholder="HK" />
          </Form.Item>
          <Form.Item name="remark" label="备注">
            <Input />
          </Form.Item>
        </Form>
      </Modal>

    </Card>
  )
}
