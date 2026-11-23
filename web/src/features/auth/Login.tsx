import { useState } from 'react'
import { Button, Form, Input, message } from '../../components/kit'
import { useLocation, useNavigate } from 'react-router-dom'
import { errMsg, login } from '../../lib/api'
import { useAuth } from '../../lib/store'
import './Login.css'
import logo from '../../assets/logo.svg'

function resolvePostLoginPath(rawFrom: unknown, role: string): string {
  const fallback = role === 'admin' ? '/admin/servers' : '/dashboard'
  if (typeof rawFrom !== 'string') return fallback
  const trimmed = rawFrom.trim()
  if (!trimmed.startsWith('/') || trimmed.startsWith('//') || trimmed.startsWith('/login')) return fallback
  if (role !== 'admin' && trimmed.startsWith('/admin')) return fallback
  return trimmed
}

export default function Login() {
  const setAuth = useAuth((s) => s.setAuth)
  const nav = useNavigate()
  const location = useLocation()
  const [loading, setLoading] = useState(false)

  const doLogin = async (v: { username: string; password: string }) => {
    setLoading(true)
    try {
      const { token, user } = await login(v.username, v.password)
      setAuth(token, user)
      const from = (location.state as { from?: unknown } | null)?.from
      nav(resolvePostLoginPath(from, user.role), { replace: true })
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setLoading(false)
    }
  }

  return (
    <main className="login-page" aria-labelledby="login-title">
      <div className="login-card">
        <header className="login-head">
          <img className="login-logo" src={logo} alt="" aria-hidden="true" />
          <h1 id="login-title">SingBox Panel</h1>
        </header>

        <Form name="login" layout="vertical" onFinish={doLogin} requiredMark={false}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, whitespace: true, message: '请输入用户名' }]}>
            <Input size="large" autoComplete="username" autoCapitalize="none" spellCheck={false} />
          </Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password
              size="large"
              autoComplete="current-password"
            />
          </Form.Item>
          <Button className="login-submit" type="primary" htmlType="submit" block size="large" loading={loading}>
            {loading ? '正在登录' : '登录'}
          </Button>
        </Form>
      </div>
    </main>
  )
}
