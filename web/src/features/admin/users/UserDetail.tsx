import { useCallback, useEffect, useState } from 'react'
import { Link, useBeforeUnload, useBlocker, useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, History, Pencil, Trash2 } from 'lucide-react'
import { deleteUser, errMsg, getUser, resetSub } from '../../../api'
import { RequestState } from '../../../components/RequestState'
import { useAuth } from '../../../store'
import type { User } from '../../../types'
import { Button, Modal, message } from '../../../ui'
import { PageHeader, Section } from '../../../ui/Page'
import { MODAL_SM } from '../../../ui/modal'
import { UserAccessEditor } from '../access/Access'
import { SubscriptionLinks } from '../../user/SubscriptionLinks'
import { SubscriptionHistory, UserAccountDialog } from './UserDialogs'
import '../../../styles/users.css'

function Detail({ id }: { id: number }) {
  const navigate = useNavigate()
  const currentUser = useAuth((state) => state.user)
  const [data, setData] = useState<{ user: User; subscription_url: string } | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [retry, setRetry] = useState(0)
  const [editing, setEditing] = useState(false)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [draft, setDraft] = useState({ dirty: false, saving: false })
  const blocker = useBlocker(draft.dirty || draft.saving)
  useBeforeUnload(useCallback((event) => {
    if (!draft.dirty && !draft.saving) return
    event.preventDefault()
    event.returnValue = ''
  }, [draft.dirty, draft.saving]))

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError(null)
    getUser(id, controller.signal)
      .then((next) => { if (!controller.signal.aborted) setData(next) })
      .catch((cause) => { if (!controller.signal.aborted) setError(errMsg(cause)) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [id, retry])

  const remove = () => {
    if (!data || draft.dirty || draft.saving) return
    Modal.confirm({
      title: '删除用户',
      content: `确认删除「${data.user.email}」？订阅和节点访问权限将失效，删除后无法恢复。`,
      okText: '删除', okType: 'danger',
      onOk: async () => {
        await deleteUser(id)
        message.success('已删除用户')
        navigate('/admin/users', { replace: true })
      },
    })
  }
  const resetOwnSubscription = () => Modal.confirm({
    title: '重置订阅链接？', content: '旧链接将立即失效，并更换你在多用户节点上的连接凭证。旧节点配置在同步后失效；离线节点将在重连后同步，请更新客户端订阅。', okText: '重置', okType: 'danger',
    onOk: async () => {
      const result = await resetSub()
      setData((current) => current ? { ...current, subscription_url: result.subscription_url } : current)
      message.success('订阅链接已重置，节点凭证正在同步，请更新客户端订阅')
    },
  })
  const savedAccount = (user: User) => {
    setData((current) => current ? { ...current, user: { ...current.user, ...user } } : current)
    // Editing another user must never replace the administrator's session.
    if (currentUser?.id === user.id) useAuth.getState().setUser(user)
  }

  return <div className="um-page sub-page um-user-detail">
    <Link to="/admin/users" className="um-back"><ArrowLeft /> 返回用户</Link>
    <RequestState loading={loading} error={error} hasData={!!data} onRetry={() => setRetry((value) => value + 1)}>
      {data && <>
        <PageHeader title={data.user.email} meta={<>
          <span className="ui-tag">{data.user.role === 'admin' ? '管理员' : '用户'}</span>
          {currentUser?.id === id && <span>当前账号</span>}
          {!data.user.enabled ? <span>已停用</span> : data.user.expire_at && new Date(data.user.expire_at).getTime() <= Date.now() ? <span>已到期</span> : null}
        </>} actions={<>
          <Button icon={<History />} onClick={() => setHistoryOpen(true)}>使用记录</Button>
          <Button icon={<Pencil />} disabled={draft.saving} onClick={() => setEditing(true)}>编辑用户</Button>
          {data.user.role !== 'admin' && <Button type="text" icon={<Trash2 />} disabled={draft.dirty || draft.saving} title={draft.dirty ? '请先保存或撤销节点更改' : '删除用户'} aria-label="删除用户" onClick={remove} />}
        </>} />
        <Section title="订阅链接" actions={currentUser?.id === id ? <Button size="small" type="text" onClick={resetOwnSubscription}>重置链接</Button> : undefined}>
          <SubscriptionLinks url={data.subscription_url} pending={draft.dirty} />
        </Section>
        <Section title="节点分配"><UserAccessEditor userId={id} onStateChange={setDraft} /></Section>
        {editing && <UserAccountDialog user={data.user} onClose={() => setEditing(false)} onSaved={savedAccount} />}
        <SubscriptionHistory user={data.user} open={historyOpen} onClose={() => setHistoryOpen(false)} />
      </>}
    </RequestState>
    <Modal open={blocker.state === 'blocked'} title={draft.saving ? '正在保存更改' : '放弃未保存的更改？'} width={MODAL_SM}
      onCancel={() => blocker.state === 'blocked' && blocker.reset()} footer={<>
        <Button onClick={() => blocker.state === 'blocked' && blocker.reset()}>继续编辑</Button>
        <Button danger disabled={draft.saving} onClick={() => blocker.state === 'blocked' && blocker.proceed()}>放弃并离开</Button>
      </>}>
      {draft.saving ? '请等待保存完成后再离开，避免丢失操作结果。' : '离开会丢弃本次节点分配、排序和名称更改。'}
    </Modal>
  </div>
}

export default function UserDetail() {
  const { id } = useParams()
  const userId = Number(id)
  if (!/^\d+$/.test(id ?? '') || !Number.isSafeInteger(userId) || userId <= 0) {
    return <div className="um-page"><Link to="/admin/users" className="um-back"><ArrowLeft /> 返回用户</Link><p role="alert">用户不存在</p></div>
  }
  return <Detail key={userId} id={userId} />
}
