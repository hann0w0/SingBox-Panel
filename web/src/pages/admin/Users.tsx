import { useEffect, useState } from 'react'
import { Button, Card, Checkbox, DatePicker, Form, Input, Modal, Space, Switch, Table, Tag, message } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { createUser, deleteUser, errMsg, listServers, listUsers, updateUser } from '../../api'
import type { Server, User } from '../../types'
import { formatDate } from '../../util'

export default function Users() {
  const [users, setUsers] = useState<User[]>([])
  const [servers, setServers] = useState<Server[]>([])
  const [loading, setLoading] = useState(false)
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<User | null>(null)
  const [form] = Form.useForm()

  const load = () => {
    setLoading(true)
    listUsers()
      .then(setUsers)
      .catch((e) => message.error(errMsg(e)))
      .finally(() => setLoading(false))
  }
  useEffect(() => {
    load()
    listServers().then(setServers).catch(() => {})
  }, [])

  const serverName = (id: number) => servers.find((s) => s.id === id)?.name ?? `#${id}`

  // Access tree: each server is a parent, its protocols are the children, so a
  // node can be granted whole or protocol-by-protocol (e.g. keep a home-
  // broadband inbound private).
  const sKey = (id: number) => `s:${id}`
  const iKey = (id: number) => `i:${id}`
  const accessTree = servers.map((s) => ({
    value: sKey(s.id),
    title: s.region ? `${s.name} · ${s.region}` : s.name,
    children: (s.inbounds ?? []).map((ib) => ({
      value: iKey(ib.id),
      title: ib.tag,
    })),
  }))
  const allInboundIds = servers.flatMap((s) => (s.inbounds ?? []).map((ib) => ib.id))

  // keysFor turns a stored grant (servers + optional inbound subset) into tree keys.
  const keysFor = (serverIDs: number[], inboundIDs: number[]) => {
    const keys: string[] = []
    for (const s of servers) {
      if (!serverIDs.includes(s.id)) continue
      const ibs = s.inbounds ?? []
      const sLimits = ibs.filter((ib) => inboundIDs.includes(ib.id))
      const isServerAllGranted = inboundIDs.length === 0 || sLimits.length === 0 || (ibs.length > 0 && sLimits.length === ibs.length)
      if (isServerAllGranted) {
        keys.push(sKey(s.id))
        keys.push(...ibs.map((ib) => iKey(ib.id)))
      } else {
        keys.push(...sLimits.map((ib) => iKey(ib.id)))
      }
    }
    return keys
  }

  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    // default: grant every current node and protocol; admin can uncheck
    form.setFieldsValue({
      enabled: true,
      access: keysFor(servers.map((s) => s.id), []),
    })
    setOpen(true)
  }

  const openEdit = (u: User) => {
    setEditing(u)
    // Reset first: the password box is not seeded here, so a password typed for
    // another user would survive in the store and silently overwrite this one's.
    form.resetFields()
    form.setFieldsValue({
      email: u.email,
      access: keysFor(u.server_ids ?? [], u.inbound_ids ?? []),
      expire: u.expire_at ? dayjs(u.expire_at) : null,
      enabled: u.enabled,
    })
    setOpen(true)
  }

  const submit = async () => {
    const v = await form.validateFields()
    const keys: string[] = v.access ?? []
    const rawInboundIDs = keys.filter((k) => k.startsWith('i:')).map((k) => Number(k.slice(2)))
    
    const serverIDs: number[] = []
    const restrictedInboundIDs: number[] = []

    for (const s of servers) {
      const sIbs = s.inbounds ?? []
      const selectedIbs = sIbs.filter((ib) => rawInboundIDs.includes(ib.id))
      
      const isServerNodeChecked = keys.includes(sKey(s.id))
      const isAllInboundsChecked = sIbs.length > 0 && selectedIbs.length === sIbs.length

      if (isServerNodeChecked || isAllInboundsChecked) {
        // 全选状态：授权整个服务器，不限制具体 inbound_ids，使未来新建协议自动同步
        serverIDs.push(s.id)
      } else if (selectedIbs.length > 0) {
        // 部分勾选状态：只授权勾选的具体协议 ID
        serverIDs.push(s.id)
        restrictedInboundIDs.push(...selectedIbs.map((ib) => ib.id))
      }
    }

    const body = {
      email: v.email,
      password: v.password || undefined,
      server_ids: serverIDs,
      inbound_ids: restrictedInboundIDs,
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
    <Card
      title="用户"
      extra={<Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新增用户</Button>}
    >
      <Table
        rowKey="id"
        loading={loading}
        dataSource={users}
        pagination={{ pageSize: 20 }}
        scroll={{ x: 820 }}
        columns={[
          { title: '用户名', dataIndex: 'email' },
          {
            title: '角色',
            dataIndex: 'role',
            render: (v: string) => (v === 'admin' ? <Tag color="red">管理员</Tag> : <Tag>用户</Tag>),
          },
          {
            title: '节点 / 协议',
            render: (_, u: User) => {
              const list = u.server_ids ?? []
              if (list.length === 0) return <Tag>未分配</Tag>
              const subset = u.inbound_ids ?? []
              return (
                <span>
                  {list.map(serverName).join('、')}
                  {subset.length > 0 ? <Tag color="orange" style={{ marginLeft: 6 }}>限 {subset.length} 个协议</Tag> : null}
                </span>
              )
            },
          },
          { title: '到期', dataIndex: 'expire_at', render: (v: string | null) => formatDate(v) },
          {
            title: '状态',
            dataIndex: 'enabled',
            render: (v: boolean) => (v ? <Tag color="green">启用</Tag> : <Tag color="red">停用</Tag>),
          },
          {
            title: '操作',
            render: (_, u: User) => (
              <Space>
                <Button size="small" type="link" onClick={() => openEdit(u)}>编辑</Button>
                <Button
                  size="small"
                  type="link"
                  danger
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
          <Form.Item
            name="access"
            label="可用节点与协议"
            extra="全选节点的协议将自动包含后续新建的协议，部分勾选则精细授权指定协议。"
          >
            <ServerAccessPicker servers={servers} />
          </Form.Item>
          <Form.Item name="expire" label="到期时间（留空永不过期）">
            <DatePicker showTime style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </Card>
  )
}

interface ServerAccessPickerProps {
  servers: Server[]
  value?: string[]
  onChange?: (value: string[]) => void
}

function ServerAccessPicker({ servers, value = [], onChange }: ServerAccessPickerProps) {
  const sKey = (id: number) => `s:${id}`
  const iKey = (id: number) => `i:${id}`

  const updateKeys = (newKeys: string[]) => {
    onChange?.(newKeys)
  }

  if (!servers.length) {
    return <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', padding: '12px 0' }}>暂无可用节点</div>
  }

  return (
    <div style={{ maxHeight: 340, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 12, paddingRight: 4 }}>
      {servers.map((s) => {
        const ibs = s.inbounds ?? []
        const serverKey = sKey(s.id)
        
        const serverChecked = value.includes(serverKey)
        const selectedIbKeys = ibs.filter((ib) => value.includes(iKey(ib.id))).map((ib) => iKey(ib.id))
        const isAllIbsChecked = ibs.length > 0 && selectedIbKeys.length === ibs.length
        
        const isChecked = serverChecked || isAllIbsChecked
        const isIndeterminate = !isChecked && selectedIbKeys.length > 0

        const handleServerToggle = (checked: boolean) => {
          let next = value.filter((k) => k !== serverKey && !ibs.some((ib) => iKey(ib.id) === k))
          if (checked) {
            next.push(serverKey)
            next.push(...ibs.map((ib) => iKey(ib.id)))
          }
          updateKeys(next)
        }

        const handleInboundToggle = (ibId: number) => {
          const targetIkey = iKey(ibId)
          const currentlyChecked = selectedIbKeys.includes(targetIkey)
          let currentIbKeys = selectedIbKeys
          if (!currentlyChecked) {
            currentIbKeys = [...currentIbKeys, targetIkey]
          } else {
            currentIbKeys = currentIbKeys.filter((k) => k !== targetIkey)
          }

          let next = value.filter((k) => k !== serverKey && !ibs.some((ib) => iKey(ib.id) === k))
          if (currentIbKeys.length === ibs.length && ibs.length > 0) {
            next.push(serverKey)
            next.push(...currentIbKeys)
          } else {
            next.push(...currentIbKeys)
          }
          updateKeys(next)
        }

        return (
          <div
            key={s.id}
            style={{
              background: '#ffffff',
              border: '1px solid #e5e7eb',
              borderRadius: 8,
              padding: '12px 14px',
              boxShadow: '0 1px 2px 0 rgba(0, 0, 0, 0.03)',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: '6px 8px', marginBottom: ibs.length ? 10 : 0 }}>
              <div
                style={{ cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 6, userSelect: 'none', minWidth: 0, flex: '1 1 auto' }}
                onClick={() => handleServerToggle(!isChecked)}
              >
                <Checkbox
                  checked={isChecked}
                  indeterminate={isIndeterminate}
                  onChange={(e) => handleServerToggle(e.target.checked)}
                />
                <span style={{ fontWeight: 600, fontSize: 13, color: '#111827', wordBreak: 'break-all' }}>
                  {s.name} {s.region ? `· ${s.region}` : ''}
                </span>
              </div>

              {isChecked ? (
                <Tag color="green" style={{ margin: 0, fontSize: 11, borderRadius: 10, padding: '0 8px', border: 'none', flexShrink: 0 }}>全节点授权（新协议自动同步）</Tag>
              ) : isIndeterminate ? (
                <Tag color="orange" style={{ margin: 0, fontSize: 11, borderRadius: 10, padding: '0 8px', border: 'none', flexShrink: 0 }}>部分授权 ({selectedIbKeys.length}/{ibs.length})</Tag>
              ) : (
                <Tag style={{ margin: 0, fontSize: 11, color: '#9ca3af', borderRadius: 10, padding: '0 8px', border: 'none', background: '#f3f4f6', flexShrink: 0 }}>未授权</Tag>
              )}
            </div>

            {ibs.length > 0 && (
              <div
                style={{
                  display: 'flex',
                  flexWrap: 'wrap',
                  gap: 8,
                  paddingLeft: 22,
                  paddingTop: 4,
                }}
              >
                {ibs.map((ib) => {
                  const ibChecked = value.includes(iKey(ib.id))
                  return (
                    <div
                      key={ib.id}
                      onClick={() => handleInboundToggle(ib.id)}
                      style={{
                        cursor: 'pointer',
                        padding: '4px 10px',
                        borderRadius: 6,
                        fontSize: 12,
                        lineHeight: '18px',
                        userSelect: 'none',
                        transition: 'all 0.15s ease',
                        display: 'inline-flex',
                        alignItems: 'center',
                        gap: 5,
                        background: ibChecked ? '#eff6ff' : '#f8fafc',
                        color: ibChecked ? '#2563eb' : '#475569',
                        border: ibChecked ? '1px solid #bfdbfe' : '1px solid #e2e8f0',
                        fontWeight: ibChecked ? 500 : 400,
                      }}
                      title={ib.tag}
                    >
                      <span style={{ fontSize: 11, lineHeight: 1 }}>{ibChecked ? '✓' : '+'}</span>
                      <span>{ib.tag}</span>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
