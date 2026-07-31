import { useState } from 'react'
import { Button, Card, Form, Input, Typography, message } from 'antd'
import { LockOutlined, UserOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { errMsg, login } from '../api'
import { useAuth } from '../store'

export default function Login() {
  const setAuth = useAuth((s) => s.setAuth)
  const nav = useNavigate()
  const [loading, setLoading] = useState(false)

  const doLogin = async (v: { username: string; password: string }) => {
    setLoading(true)
    try {
      const { token, user } = await login(v.username, v.password)
      setAuth(token, user)
      nav(user.role === 'admin' ? '/admin/overview' : '/dashboard')
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div
      style={{
        // dvh follows the mobile browser chrome; vh is the desktop fallback.
        minHeight: '100dvh',
        width: '100%',
        boxSizing: 'border-box',
        padding: 16,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        background:
          'radial-gradient(1100px 560px at 15% 15%, rgba(58,91,255,0.14), transparent 60%),' +
          'radial-gradient(900px 520px at 88% 85%, rgba(132,72,255,0.12), transparent 60%),' +
          'linear-gradient(160deg, #f3f6ff 0%, #eef1f8 100%)',
      }}
    >
      <Card
        // Fluid up to 384px: a fixed width overflows a 375px phone.
        style={{ width: '100%', maxWidth: 384, boxShadow: '0 12px 40px rgba(30,41,89,0.12)', border: '1px solid #eef0f6' }}
        styles={{ body: { padding: 'clamp(20px, 6vw, 32px)' } }}
      >
        <div style={{ textAlign: 'center', marginBottom: 28 }}>
          <Typography.Title level={2} style={{ margin: 0, letterSpacing: 0.5 }}>
            SingBox <span style={{ color: '#3a5bff' }}>Panel</span>
          </Typography.Title>
        </div>
        <Form layout="vertical" onFinish={doLogin} requiredMark={false}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input prefix={<UserOutlined />} placeholder="用户名" size="large" autoComplete="username" />
          </Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password prefix={<LockOutlined />} size="large" autoComplete="current-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block size="large" loading={loading} style={{ marginTop: 8 }}>
            登录
          </Button>
        </Form>
      </Card>
    </div>
  )
}
