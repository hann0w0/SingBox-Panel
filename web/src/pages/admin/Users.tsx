import { useEffect, useState } from 'react'
import { Button, Card, Col, DatePicker, Form, Grid, Input, Modal, Row, Space, Switch, Table, Tag, message } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { createUser, deleteUser, errMsg, listCustomNodes, listUsers, updateUser } from '../../api'
import type { CustomNode } from '../../api'
import type { User } from '../../types'
import { AssignModal, CustomNodesPanel } from './Access'

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

  return (
    <>
      <div className="users-page">
        <Row className="users-management-section" gutter={[16, 16]}>
          <Col xs={24} xl={12} style={{ display: 'flex' }}>
          <Card
            title="用户"
            size="small"
            extra={<Button type="primary" size="small" icon={<PlusOutlined />} onClick={openCreate}>新增用户</Button>}
            style={{ flex: 1, display: 'flex', flexDirection: 'column', width: '100%' }}
            styles={{ body: { flex: 1, overflow: 'auto' } }}
          >
            <Table
              rowKey="id"
              size="small"
              className="compact-rows"
              loading={loading}
              dataSource={users}
              pagination={false}
              scroll={{ x: isMobile ? undefined : 560, y: 340 }}
              columns={[
                {
                  title: '用户名',
                  dataIndex: 'email',
                  ellipsis: true,
                  // On mobile the 角色 column is hidden, so fold the admin marker
                  // into the name cell to keep it visible.
                  render: (v: string, u: User) =>
                    isMobile && u.role === 'admin' ? (
                      <Space size={4}>
                        <span>{v}</span>
                        <Tag color="red" style={{ marginInlineEnd: 0 }}>管理员</Tag>
                      </Space>
                    ) : (
                      v
                    ),
                },
                {
                  title: '角色',
                  width: 90,
                  dataIndex: 'role',
                  hidden: isMobile,
                  render: (v: string) => (v === 'admin' ? <Tag color="red">管理员</Tag> : <Tag>用户</Tag>),
                },
                {
                  title: '状态',
                  width: 80,
                  dataIndex: 'enabled',
                  render: (v: boolean) => (v ? <Tag color="green">启用</Tag> : <Tag color="red">停用</Tag>),
                },
                {
                  title: '操作',
                  width: isMobile ? 150 : 180,
                  fixed: isMobile ? undefined : ('right' as const),
                  render: (_, u: User) => (
                    <Space size={isMobile ? 2 : 8} wrap>
                      <Button size="small" type="link" style={{ padding: '0 4px' }} onClick={() => openEdit(u)}>编辑</Button>
                      <Button size="small" type="link" style={{ padding: '0 4px' }} onClick={() => setAssignUser(u)}>分配节点</Button>
                      <Button
                        size="small"
                        type="link"
                        danger
                        style={{ padding: '0 4px' }}
                        disabled={u.role === 'admin'}
                        onClick={() =>
                          Modal.confirm({
                            title: `删除用户 ${u.email}?`,
                            okType: 'danger',
                            onOk: async () => {
                              await deleteUser(u.id)
                              load()
                            },
                          })
                        }
                      >
                        删除
                      </Button>
                    </Space>
                  ),
                },
              ]}
              locale={{ emptyText: '暂无用户' }}
            />
          </Card>
          </Col>
          <Col xs={24} xl={12} style={{ display: 'flex' }}>
            <CustomNodesPanel nodes={nodes} onNodesChange={loadNodes} />
          </Col>
        </Row>
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
