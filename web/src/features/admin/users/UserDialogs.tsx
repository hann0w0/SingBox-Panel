import { useEffect, useRef, useState } from 'react'
import { Alert, Form, Input, Modal, Segmented, Switch, Table, message } from '../../../components/kit'
import { createUser, errMsg, getUserTraffic, updateUser } from '../../../lib/api'
import type { User, UserTrafficInbound, UserTrafficRange, UserTrafficSeries } from '../../../lib/types'
import { formatBytes } from '../../../lib/util'
import { ProtocolTag } from '../servers/ServerTraffic'
import { RequestState } from '../../../components/RequestState'
import { MODAL_SM } from '../../../components/kit/modal'
import { SubscriptionFetchHistory } from './SubscriptionFetchHistory'

export function UserAccountDialog({ user, onClose, onSaved }: {
  user?: User
  onClose: () => void
  onSaved: (user: User) => void
}) {
  const [form] = Form.useForm()
  const [saving, setSaving] = useState(false)
  const [pending, setPending] = useState<Parameters<typeof updateUser>[1] | null>(null)
  const lock = useRef(false)
  useEffect(() => {
    form.resetFields()
    setPending(null)
    if (user) form.setFieldsValue({ email: user.email, forwarding_enabled: !!user.forwarding_enabled })
    else form.setFieldsValue({ forwarding_enabled: false })
  }, [user?.id])
  const submit = async () => {
    if (lock.current) return
    lock.current = true
    setSaving(true)
    try {
      const values = pending || await form.validateFields()
      const body = { email: values.email, password: values.password || undefined, forwarding_enabled: !!values.forwarding_enabled }
      if (user?.forwarding_enabled && !body.forwarding_enabled && !pending) {
        setPending(body)
        return
      }
      const saved = user ? await updateUser(user.id, body) : await createUser(body)
      message.success('已保存用户')
      onSaved(saved)
      onClose()
    } finally {
      lock.current = false
      setSaving(false)
    }
  }
  return <Modal title={pending ? '关闭转发权限？' : user ? '编辑用户' : '新增用户'} width={MODAL_SM} open onCancel={() => pending ? setPending(null) : onClose()} onOk={submit} confirmLoading={saving} okText={pending ? '确认关闭并保存' : '确定'} cancelText={pending ? '继续编辑' : '取消'} okButtonProps={{ danger: !!pending }}>
    {pending && <Alert type="warning" showIcon message="将删除此用户的全部转发" description="保存后将删除此用户的所有转发配置、由转发生成的订阅节点及其排序记录，以及各服务器上关联的转发路由规则。重新开启权限不会恢复这些配置，需要重新创建。" />}
    <Form form={form} layout="vertical" hidden={!!pending} onFinish={submit}>
      <Form.Item name="email" label="用户名" rules={[{ required: true }]}>
        <Input maxLength={191} autoComplete="off" />
      </Form.Item>
      <Form.Item name="password" label={user ? '新密码' : '密码'} rules={user ? [{ min: 6, message: '密码至少 6 位' }, { max: 72 }] : [{ required: true, message: '请输入密码' }, { min: 6, message: '密码至少 6 位' }, { max: 72 }]}>
        <Input.Password placeholder={user ? '不修改' : undefined} autoComplete="new-password" />
      </Form.Item>
      <Form.Item name="forwarding_enabled" label="允许创建转发" valuePropName="checked">
        <Switch />
      </Form.Item>
    </Form>
  </Modal>
}

const USER_TRAFFIC_RANGES: { label: string; value: UserTrafficRange }[] = [
  { label: '24 小时', value: '24h' },
  { label: '7 天', value: '7d' },
  { label: '30 天', value: '30d' },
]

// UserTraffic shows traffic attributed to one user. Only inbounds in
// multi-user mode can tell users apart; single-user inbounds count toward
// their port totals on the traffic page instead.
function UserTraffic({ user }: { user: User }) {
  const [range, setRange] = useState<UserTrafficRange>('24h')
  const [data, setData] = useState<UserTrafficSeries | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [reloadKey, setReloadKey] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError(null)
    setData(null)
    getUserTraffic(user.id, range, controller.signal)
      .then((d) => {
        if (!controller.signal.aborted) setData(d)
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(errMsg(e))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [user.id, range, reloadKey])
  return (
    <section className="um-group">
      <header className="um-group-head">
        <h4>用量</h4>
        <Segmented<UserTrafficRange> options={USER_TRAFFIC_RANGES} value={range} onChange={setRange} />
      </header>
      <RequestState loading={loading && !data} error={error} hasData={!!data} onRetry={() => setReloadKey((k) => k + 1)}>
        {data ? (
          <>
            <div className="um-mini-stats is-3">
              <div><span>下载 ↓</span><b className="mono">{formatBytes(data.download)}</b></div>
              <div><span>上传 ↑</span><b className="mono">{formatBytes(data.upload)}</b></div>
              <div><span>合计</span><b className="mono">{formatBytes(data.upload + data.download)}</b></div>
            </div>
            <Table<UserTrafficInbound>
              size="small"
              rowKey={(r) => `${r.server_id}-${r.inbound_id}`}
              dataSource={data.inbounds}
              scroll={{ x: 640 }}
              locale={{ emptyText: '暂无记录' }}
              columns={[
                { title: '主机', dataIndex: 'server_name', width: 150, ellipsis: true, render: (v: string, r) => v || (r.deleted ? <span className="faint">已删除</span> : '—') },
                { title: '协议', dataIndex: 'type', width: 110, render: (v: string) => v ? <ProtocolTag type={v} /> : '—' },
                { title: '端口', dataIndex: 'port', width: 80, render: (v: number, r) => v ? <span className="mono" title={r.tag}>{v}</span> : '—' },
                { title: '下载', dataIndex: 'download', width: 100, align: 'right', sorter: (a, b) => a.download - b.download, render: (v: number) => <span className="mono">{formatBytes(v)}</span> },
                { title: '上传', dataIndex: 'upload', width: 100, align: 'right', sorter: (a, b) => a.upload - b.upload, render: (v: number) => <span className="mono muted">{formatBytes(v)}</span> },
                { title: '合计', key: 'total', width: 100, align: 'right', sorter: (a, b) => a.upload + a.download - b.upload - b.download, render: (_: unknown, r) => <span className="mono um-strong">{formatBytes(r.upload + r.download)}</span> },
              ]}
            />
          </>
        ) : null}
      </RequestState>
    </section>
  )
}

export function SubscriptionHistory({ user, open, onClose }: { user: User | null; open: boolean; onClose: () => void }) {
  return (
    <Modal title={user ? `${user.email} · 使用记录` : ''} open={open} onCancel={onClose} footer={null} width={960} className="um-modal um-history-modal">
      {open && user && <>
        <UserTraffic key={`traffic:${user.id}`} user={user} />
        <SubscriptionFetchHistory key={user.id} userID={user.id} />
      </>}
    </Modal>
  )
}
