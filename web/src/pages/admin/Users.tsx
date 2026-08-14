import { useEffect, useState } from 'react'
import { Button, Card, DatePicker, Empty, Form, Grid, Input, Modal, Space, Switch, Table, Tag, message } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { createUser, deleteUser, errMsg, listCustomNodes, listUsers, updateUser } from '../../api'
import type { CustomNode } from '../../api'
import type { User } from '../../types'
import { AssignModal, CustomNodesPanel } from './Access'

const MAX_VISIBLE_USERS = 5
const MANAGEMENT_TABLE_SCROLL_HEIGHT = MAX_VISIBLE_USERS * 41

export default function Users() {
  const [users, setUsers] = useState<User[]>([])
  const [nodes, setNodes] = useState<CustomNode[]>([])
  const [loading, setLoading] = useState(false)
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<User | null>(null)
  const [assignUser, setAssignUser] = useState<User | null>(null)
  const [form] = Form.useForm()
  const screens = Grid.useBreakpoint()
  // md is antd's 768px breakpoint; below it we're on a phone-width layout.
  const isMobile = !screens.md

  const load = () => {
    setLoading(true)
    listUsers()
      .then(setUsers)
      .catch((e) => message.error(errMsg(e)))
      .finally(() => setLoading(false))
  }
  useEffect(() => {
    load()
  }, [])

  const loadNodes = () => {
    listCustomNodes()
      .then(setNodes)
      .catch((e) => message.error(errMsg(e)))
  }
  useEffect(loadNodes, [])

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
    const v = await form.validateFields()

    const body = {
      email: v.email,
      password: v.password || undefined,
      expire_at: v.expire ? Math.floor((v.expire as dayjs.Dayjs).valueOf() / 1000) : 0,
      enabled: v.enabled,
    }
    try {
      if (editing) await updateUser(editing.id, body)
      else await createUser(body)
      message.success('已保存')
      setOpen(false)
      load()
    } catch (e) {
      message.error(errMsg(e))
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

  const mobileUserList = users.length === 0 ? (
    <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无用户" />
  ) : (
    <div className={`mobile-admin-list mobile-user-list${users.length > MAX_VISIBLE_USERS ? ' is-scrollable' : ''}`}>
      {users.map((user) => (
        <div className="mobile-user-card" key={user.id}>
          <div className="mobile-card-heading">
            <span className="mobile-user-name">{user.email}</span>
            <span className="mobile-user-count">{user.node_count ?? 0} 个节点</span>
          </div>
          <div className="mobile-card-meta">
            <span>角色：{user.role === 'admin' ? '管理员' : '用户'}</span>
          </div>
          <div className="mobile-card-actions mobile-user-actions">
            <Button size="small" type="link" onClick={() => openEdit(user)}>编辑</Button>
            <Button size="small" type="link" onClick={() => setAssignUser(user)}>分配</Button>
            {user.role !== 'admin' ? (
              <Button size="small" type="link" danger onClick={() => removeUser(user)}>删除</Button>
            ) : null}
          </div>
        </div>
      ))}
    </div>
  )

  return (
    <>
      <div className="users-page">
        <div className="users-page-grid">
          <Card
            title="用户"
            size="small"
            className="users-top-card users-overview-card"
            extra={addUserButton}
          >
            {isMobile ? mobileUserList : <Table
              rowKey="id"
              size="small"
              className="compact-rows management-fixed-table users-table"
              loading={loading}
              dataSource={users}
              pagination={false}
              scroll={{
                x: 650,
                y: users.length > MAX_VISIBLE_USERS ? MANAGEMENT_TABLE_SCROLL_HEIGHT : undefined,
              }}
              columns={[
                {
                  title: '用户名',
                  dataIndex: 'email',
                  ellipsis: true,
                },
                {
                  title: '角色',
                  width: 90,
                  dataIndex: 'role',
                  render: (v: string) => (v === 'admin' ? <Tag color="red">管理员</Tag> : <Tag>用户</Tag>),
                },
                {
                  title: '节点',
                  width: 70,
                  dataIndex: 'node_count',
                  render: (value: number) => `${value ?? 0} 个`,
                },
                {
                  title: '操作',
                  width: 180,
                  fixed: 'right' as const,
                  render: (_, u: User) => (
                    <Space size={8} wrap>
                      <Button size="small" type="link" style={{ padding: '0 4px' }} onClick={() => openEdit(u)}>编辑</Button>
                      <Button size="small" type="link" style={{ padding: '0 4px' }} onClick={() => setAssignUser(u)}>分配节点</Button>
                      {u.role !== 'admin' ? (
                        <Button
                          size="small"
                          type="link"
                          danger
                          style={{ padding: '0 4px' }}
                          onClick={() => removeUser(u)}
                        >
                          删除
                        </Button>
                      ) : null}
                    </Space>
                  ),
                },
              ]}
              locale={{ emptyText: '暂无用户' }}
            />}
          </Card>
          <CustomNodesPanel nodes={nodes} onNodesChange={loadNodes} />
        </div>
      </div>

      <AssignModal
        userId={assignUser?.id}
        userEmail={assignUser?.email}
        nodes={nodes}
        open={!!assignUser}
        onClose={() => setAssignUser(null)}
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
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="email" label="用户名" rules={[{ required: true }]}>
            <Input placeholder="用于登录的用户名" />
          </Form.Item>
          <Form.Item
            name="password"
            label={editing ? '新密码（留空不修改）' : '密码'}
            rules={editing ? [] : [{ required: true, min: 6 }]}
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
