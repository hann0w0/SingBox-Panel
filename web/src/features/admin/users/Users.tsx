import { useEffect, useRef, useState } from 'react'
import { Button, Card, DatePicker, Descriptions, Form, Input, Modal, Space, Switch, Table, Tag, Tooltip, Typography, message } from 'antd'
import { ApartmentOutlined, DeleteOutlined, EditOutlined, HistoryOutlined, PlusOutlined, WarningOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { createUser, deleteUser, errMsg, getUserSubFetches, listCustomNodes, listServers, listUsers, updateUser } from '../../../api'
import type { CustomNode, SubscriptionFetch, SubscriptionIPSummary } from '../../../api'
import type { Server, User } from '../../../types'
import { daysUntil, relativeTime } from '../../../util'
import { AssignModal } from '../access/Access'
import { RequestState } from '../../../components/RequestState'

// Several distinct addresses fetching one subscription within 24 hours is a
// common sign that a link is being shared.
const SHARED_IP_THRESHOLD = 4

function SubscriptionSummary({ user }: { user: User }) {
  const ips = user.sub_ips_24h ?? 0
  const fetches = user.sub_fetches_24h ?? 0
  const shared = ips >= SHARED_IP_THRESHOLD
  return (
    <Descriptions size="small" bordered column={{ xs: 1, sm: 2 }}>
      <Descriptions.Item label="最近订阅">
        {user.last_sub_at ? (
          <Tooltip title={new Date(user.last_sub_at).toLocaleString('zh-CN')}><span>{relativeTime(user.last_sub_at)}</span></Tooltip>
        ) : '从未'}
      </Descriptions.Item>
      <Descriptions.Item label="客户端">{user.last_sub_client || '—'}</Descriptions.Item>
      <Descriptions.Item label="来源 IP">{user.last_sub_ip || '—'}</Descriptions.Item>
      <Descriptions.Item label="24 小时内">
        {fetches === 0 ? '无拉取' : (
          <span style={{ color: shared ? 'var(--console-warning)' : undefined }}>
            {shared ? <WarningOutlined style={{ marginRight: 4 }} /> : null}
            {fetches} 次 · {ips} 个 IP{shared ? '（可能存在共享）' : ''}
          </span>
        )}
      </Descriptions.Item>
      <Descriptions.Item label="累计拉取">{user.sub_fetch_count ?? 0} 次</Descriptions.Item>
      <Descriptions.Item label="最近登录">
        {user.last_login_at ? `${relativeTime(user.last_login_at)}${user.last_login_ip ? ` · ${user.last_login_ip}` : ''}` : '从未'}
      </Descriptions.Item>
      {user.last_sub_ua ? (
        <Descriptions.Item label="User-Agent" span={2}>
          <Typography.Text style={{ wordBreak: 'break-all', fontSize: 12 }}>{user.last_sub_ua}</Typography.Text>
        </Descriptions.Item>
      ) : null}
    </Descriptions>
  )
}

function SubscriptionHistory({ user, open, onClose }: { user: User | null; open: boolean; onClose: () => void }) {
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [fetches, setFetches] = useState<SubscriptionFetch[]>([])
  const [ips, setIps] = useState<SubscriptionIPSummary[]>([])
  const [retention, setRetention] = useState(30)
  useEffect(() => {
    if (!open || !user) return
    const controller = new AbortController()
    setLoading(true)
    setError(null)
    getUserSubFetches(user.id, controller.signal)
      .then((d) => {
        setFetches(d.fetches)
        setIps(d.ips)
        setRetention(d.retention_days)
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(errMsg(e))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [open, user])
  return (
    <Modal title={user ? `${user.email} · 使用情况` : ''} open={open} onCancel={onClose} footer={null} width={760} destroyOnClose>
      <RequestState loading={loading} error={error} hasData={!loading} onRetry={() => undefined}>
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          {user && <SubscriptionSummary user={user} />}
          <Typography.Text type="secondary">明细保留 {retention} 天</Typography.Text>
          <Table<SubscriptionIPSummary>
            size="small"
            rowKey="ip"
            title={() => '来源 IP'}
            pagination={false}
            dataSource={ips}
            scroll={{ y: 200 }}
            locale={{ emptyText: '暂无记录' }}
            columns={[
              { title: 'IP', dataIndex: 'ip' },
              { title: '客户端', dataIndex: 'client', width: 130 },
              { title: '次数', dataIndex: 'fetches', width: 80 },
              { title: '最近', dataIndex: 'last_seen', width: 120, render: (v: string) => relativeTime(v) },
            ]}
          />
          <Table<SubscriptionFetch>
            size="small"
            rowKey="id"
            title={() => '最近拉取'}
            pagination={false}
            dataSource={fetches}
            scroll={{ y: 260 }}
            locale={{ emptyText: '暂无记录' }}
            columns={[
              { title: '时间', dataIndex: 'created_at', width: 170, render: (v: string) => new Date(v).toLocaleString('zh-CN') },
              { title: 'IP', dataIndex: 'ip', width: 150 },
              { title: '客户端', dataIndex: 'client', width: 110, render: (v: string, r) => <Tooltip title={r.user_agent}><span>{v}</span></Tooltip> },
              { title: '格式', dataIndex: 'format', width: 90 },
              { title: '结果', dataIndex: 'status', width: 70, render: (v: number) => (v === 200 ? <Tag color="green">成功</Tag> : <Tag color="red">{v}</Tag>) },
            ]}
          />
        </Space>
      </RequestState>
    </Modal>
  )
}

export default function Users() {
  const [users, setUsers] = useState<User[]>([])
  const [nodes, setNodes] = useState<CustomNode[]>([])
  const [servers, setServers] = useState<Server[]>([])
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [, setNodesLoading] = useState(false)
  const [, setNodesError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<User | null>(null)
  const [assignUser, setAssignUser] = useState<User | null>(null)
  const [assignOpen, setAssignOpen] = useState(false)
  const [assignOrigin, setAssignOrigin] = useState<{ x: number; y: number }>()
  const [historyUser, setHistoryUser] = useState<User | null>(null)
  const openAssign = (user: User, button: HTMLElement) => {
    const rect = button.getBoundingClientRect()
    setAssignOrigin({ x: rect.left + rect.width / 2 + window.scrollX, y: rect.top + rect.height / 2 + window.scrollY })
    setAssignUser(user)
    setAssignOpen(true)
  }
  const userLoadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })
  const nodeLoadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })
  const [form] = Form.useForm()
  const submitLock = useRef(false)
  const serverNames = (u: User) => u.server_ids.map((id) => servers.find((s) => s.id === id)?.name ?? `#${id}`)

  const load = () => {
    userLoadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++userLoadRef.current.generation
    userLoadRef.current.controller = controller
    setLoading(true)
    setLoadError(null)
    listUsers(controller.signal)
      .then((next) => {
        if (generation === userLoadRef.current.generation) setUsers(next)
      })
      .catch((e) => {
        if (generation === userLoadRef.current.generation && !controller.signal.aborted) setLoadError(errMsg(e))
      })
      .finally(() => {
        if (generation === userLoadRef.current.generation) {
          userLoadRef.current.controller = null
          setLoading(false)
        }
      })
  }
  useEffect(() => {
    load()
    return () => {
      userLoadRef.current.generation++
      userLoadRef.current.controller?.abort()
    }
  }, [])

  const loadNodes = () => {
    nodeLoadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++nodeLoadRef.current.generation
    nodeLoadRef.current.controller = controller
    setNodesLoading(true)
    setNodesError(null)
    listCustomNodes(controller.signal)
      .then((next) => {
        if (generation === nodeLoadRef.current.generation) setNodes(next)
      })
      .catch((e) => {
        if (generation === nodeLoadRef.current.generation && !controller.signal.aborted) setNodesError(errMsg(e))
      })
      .finally(() => {
        if (generation === nodeLoadRef.current.generation) {
          nodeLoadRef.current.controller = null
          setNodesLoading(false)
        }
      })
  }
  useEffect(() => {
    loadNodes()
    return () => {
      nodeLoadRef.current.generation++
      nodeLoadRef.current.controller?.abort()
    }
  }, [])
  useEffect(() => {
    const controller = new AbortController()
    listServers(controller.signal)
      .then((next) => {
        if (!controller.signal.aborted) setServers(next)
      })
      .catch(() => {
        // Names are decoration for the 服务器 column; counts still render.
      })
    return () => controller.abort()
  }, [])

  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    // default: enabled account, no expiry
    form.setFieldsValue({ enabled: true })
    setOpen(true)
  }

  const openEdit = (u: User) => {
    setEditing(u)
    // Reset first: the password box is not seeded here, so a password typed for
    // another user would survive in the store and silently overwrite this one's.
    form.resetFields()
    form.setFieldsValue({
      email: u.email,
      expire: u.expire_at ? dayjs(u.expire_at) : null,
      enabled: u.enabled,
    })
    setOpen(true)
  }

  const submit = async () => {
    if (submitLock.current) return
    submitLock.current = true
    setSaving(true)
    try {
      const v = await form.validateFields()
      const body = {
        email: v.email,
        password: v.password || undefined,
        expire_at: v.expire ? Math.floor((v.expire as dayjs.Dayjs).valueOf() / 1000) : 0,
        enabled: v.enabled,
      }
      if (editing) await updateUser(editing.id, body)
      else await createUser(body)
      message.success('已保存')
      setOpen(false)
      load()
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      submitLock.current = false
      setSaving(false)
    }
  }

  const addUserButton = <Button type="primary" size="small" icon={<PlusOutlined />} onClick={openCreate}>新增用户</Button>

  const removeUser = (user: User) => {
    Modal.confirm({
      title: `删除用户 ${user.email}?`,
      okType: 'danger',
      onOk: async () => {
        await deleteUser(user.id)
        load()
      },
    })
  }

  // A custom node is visible to a user when it targets everyone (minus
  // exclusions) or lists the user explicitly. Mirrors model.CustomNode.HasUser.
  const nodeVisible = (node: CustomNode, userID: number) =>
    node.enabled && (node.all_users ? !node.excluded_user_ids?.includes(userID) : node.user_ids?.includes(userID))
  const externalGroups = (u: User) => {
    const counts = new Map<string, number>()
    for (const node of nodes) {
      if (!nodeVisible(node, u.id)) continue
      const key = node.group || '未分组'
      counts.set(key, (counts.get(key) ?? 0) + 1)
    }
    return [...counts.entries()].sort((a, b) => b[1] - a[1])
  }
  const lastActive = (u: User) => {
    const login = u.last_login_at ? new Date(u.last_login_at).getTime() : 0
    const sub = u.last_sub_at ? new Date(u.last_sub_at).getTime() : 0
    if (!login && !sub) return null
    return sub >= login
      ? { at: u.last_sub_at!, what: `更新订阅${u.last_sub_client ? ` · ${u.last_sub_client}` : ''}`, ip: u.last_sub_ip }
      : { at: u.last_login_at!, what: '登录面板', ip: u.last_login_ip }
  }

  const userCard = (u: User) => {
    const names = serverNames(u)
    const groups = externalGroups(u)
    const active = lastActive(u)
    const expireDays = daysUntil(u.expire_at)
    return (
      <div className="user-card" key={u.id}>
        <div className="user-card-head">
          <div className="user-card-title">
            <span className="user-card-name">{u.email}</span>
            {u.role === 'admin' ? <Tag color="red">管理员</Tag> : null}
            {!u.enabled ? <Tag>已停用</Tag> : null}
            {expireDays !== null && expireDays < 0 ? <Tag color="red">已到期</Tag> : null}
          </div>
          <Space size={2} className="user-card-actions">
            <Button size="small" type="text" icon={<EditOutlined />} title="编辑" aria-label="编辑" onClick={() => openEdit(u)} />
            <Button size="small" type="text" icon={<ApartmentOutlined />} title="分配节点" aria-label="分配节点" onClick={(e) => openAssign(u, e.currentTarget)} />
            <Button size="small" type="text" icon={<HistoryOutlined />} title="使用记录" aria-label="使用记录" onClick={() => setHistoryUser(u)} />
            {u.role !== 'admin' ? (
              <Button size="small" type="text" danger icon={<DeleteOutlined />} title="删除" aria-label="删除" onClick={() => removeUser(u)} />
            ) : null}
          </Space>
        </div>

        <div className="user-card-row">
          <span className="user-card-label">主机</span>
          <div className="user-card-tags">
            {names.length ? names.map((name) => <Tag key={name}>{name}</Tag>) : <span className="user-card-muted">未分配</span>}
          </div>
        </div>

        <div className="user-card-row">
          <span className="user-card-label">节点</span>
          <div className="user-card-tags">
            {groups.length
              ? groups.map(([group, count]) => <Tag key={group} color="blue">{group} · {count}</Tag>)
              : <span className="user-card-muted">无</span>}
          </div>
        </div>

        <div className="user-card-foot">
          <span>
            {active
              ? <Tooltip title={`${new Date(active.at).toLocaleString('zh-CN')}${active.ip ? ` · ${active.ip}` : ''}`}><span>{relativeTime(active.at)}{active.what ? ` ${active.what}` : ''}</span></Tooltip>
              : <span className="user-card-muted">尚未使用</span>}
          </span>
          <span className="user-card-muted">
            {u.sub_fetch_count ? `拉取 ${u.sub_fetch_count} 次` : ''}
            {u.sub_fetch_count && u.expire_at ? ' · ' : ''}
            {u.expire_at ? `到期 ${new Date(u.expire_at).toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })}` : ''}
          </span>
        </div>
      </div>
    )
  }


  return (
    <>
      <Card
        title="用户"
        extra={addUserButton}
      >
        <RequestState loading={loading} error={loadError} hasData={users.length > 0} empty={!loading && !loadError && users.length === 0} emptyDescription="暂无用户" onRetry={load}>
          <div className="user-card-grid">
            {users.map(userCard)}
          </div>
        </RequestState>
      </Card>

      <SubscriptionHistory user={historyUser} open={!!historyUser} onClose={() => setHistoryUser(null)} />

      <AssignModal
        userId={assignUser?.id}
        userEmail={assignUser?.email}
        nodes={nodes}
        open={assignOpen}
        mousePosition={assignOrigin}
        onClose={() => setAssignOpen(false)}
        onSaved={() => {
          load()
          loadNodes()
        }}
      />

      <Modal
        title={editing ? '编辑用户' : '新增用户'}
        open={open}
        onOk={submit}
        onCancel={() => setOpen(false)}
        confirmLoading={saving}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="email" label="用户名" rules={[{ required: true }]}>
            <Input placeholder="用于登录的用户名" maxLength={191} />
          </Form.Item>
          <Form.Item
            name="password"
            label={editing ? '新密码（留空不修改）' : '密码'}
            rules={editing ? [{ max: 72 }] : [{ required: true, message: '请输入密码' }, { max: 72 }]}
          >
            <Input.Password />
          </Form.Item>
          <Form.Item name="expire" label="到期时间（留空永不过期）">
            <DatePicker showTime style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </>
  )
}
