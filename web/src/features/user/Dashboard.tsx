import { useEffect, useRef, useState } from 'react'
import { Button, Card, Form, Input, Modal, Space, Typography, message } from 'antd'
import { changePassword, errMsg, getMe, resetSub } from '../../api'
import type { User } from '../../types'
import { copyToClipboard } from '../../util'
import { useAuth } from '../../store'
import { RequestState } from '../../components/RequestState'

const LOGO_VERSION = '20260906-2'
type ClientKind = 'surge' | 'clash' | 'shadowrocket'

interface ClientSpec {
  kind: ClientKind
  label: string
  logo: string
  logoClass: string
  target: string
}

const CLIENTS: ClientSpec[] = [
  { kind: 'surge', label: 'Surge', logo: '/logos/surge.png', logoClass: 'client-logo-surge', target: 'surge' },
  { kind: 'clash', label: 'ClashMeta', logo: '/logos/clashmeta.png', logoClass: 'client-logo-clashmeta', target: 'clash' },
  { kind: 'shadowrocket', label: 'Shadowrocket', logo: '/logos/shadowrocket.png', logoClass: 'client-logo-shadowrocket', target: 'shadowrocket' },
]

function ClientLogo({ spec }: { spec: ClientSpec }) {
  return (
    <span className={`client-logo ${spec.logoClass}`} aria-hidden="true">
      <img src={`${spec.logo}?v=${LOGO_VERSION}`} alt="" />
    </span>
  )
}

export default function Dashboard() {
  const setAuth = useAuth((s) => s.setAuth)
  const setUser = useAuth((s) => s.setUser)
  const [user, setLocalUser] = useState<User | null>(null)
  const [subUrl, setSubUrl] = useState('')
  const [pwdOpen, setPwdOpen] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const loadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })
  const [pwdForm] = Form.useForm()

  const load = () => {
    loadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++loadRef.current.generation
    loadRef.current.controller = controller
    setLoading(true)
    setLoadError(null)
    getMe(controller.signal)
      .then((d) => {
        if (generation !== loadRef.current.generation) return
        setLocalUser(d.user)
        setSubUrl(d.subscription_url)
        setUser(d.user)
      })
      .catch((e) => {
        if (generation === loadRef.current.generation && !controller.signal.aborted) setLoadError(errMsg(e))
      })
      .finally(() => {
        if (generation === loadRef.current.generation && !controller.signal.aborted) setLoading(false)
      })
  }
  useEffect(() => {
    load()
    return () => {
      loadRef.current.generation++
      loadRef.current.controller?.abort()
    }
  }, [])

  if (!user) {
    return <RequestState loading={loading} error={loadError} hasData={false} onRetry={load}><span /></RequestState>
  }

  const link = (target: string) => (target ? `${subUrl}?target=${target}` : subUrl)
  const copySub = async (spec: ClientSpec) => {
    try {
      await copyToClipboard(link(spec.target))
      message.success(`已复制 ${spec.label} 订阅链接`)
    } catch {
      Modal.info({ title: `${spec.label} 订阅链接`, width: 640, content: <Typography.Paragraph copyable code style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{link(spec.target)}</Typography.Paragraph> })
    }
  }
  const doReset = () => Modal.confirm({
    title: '重置订阅链接?',
    content: '旧链接将立即失效，需要在客户端重新导入。',
    onOk: async () => {
      try {
        const result = await resetSub()
        setSubUrl(result.subscription_url)
        message.success('已重置')
      } catch (error) {
        message.error(errMsg(error))
        throw error
      }
    },
  })
  const doChangePwd = async () => {
    const v = await pwdForm.validateFields()
    try {
      const result = await changePassword(v.old_password, v.new_password)
      setAuth(result.token, user)
      message.success('密码已修改')
      setPwdOpen(false)
      pwdForm.resetFields()
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  return (
    <RequestState loading={loading} error={loadError} hasData onRetry={load}>
      <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Card title="订阅链接">
        <p className="subscription-hint">选择客户端，复制对应的订阅链接</p>
        <div className="client-subscriptions">
          {CLIENTS.map((spec) => (
            <Button key={spec.kind} className="client-subscription-button" icon={<ClientLogo spec={spec} />} onClick={() => void copySub(spec)}>
              {spec.label}
            </Button>
          ))}
        </div>
        <div className="subscription-actions">
            <Button onClick={doReset}>重置订阅链接</Button>
            <Button onClick={() => setPwdOpen(true)}>修改密码</Button>
        </div>
      </Card>

      <Modal title="修改密码" open={pwdOpen} onOk={doChangePwd} onCancel={() => setPwdOpen(false)} destroyOnClose>
        <Form form={pwdForm} layout="vertical">
          <Form.Item name="old_password" label="当前密码" rules={[{ required: true }]}><Input.Password /></Form.Item>
          <Form.Item name="new_password" label="新密码" rules={[{ required: true, message: '请输入新密码' }, { max: 72 }]}><Input.Password /></Form.Item>
        </Form>
      </Modal>
      </Space>
    </RequestState>
  )
}
