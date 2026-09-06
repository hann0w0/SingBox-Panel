import { useState } from 'react'
import { Button, ConfigProvider, Form, Input, message } from 'antd'
import { EyeInvisibleOutlined, EyeOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { errMsg, login } from '../../api'
import { useAuth } from '../../store'
import './Login.css'

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
    <ConfigProvider
      theme={{
        token: {
          colorPrimary: '#353432',
          colorText: '#302f2d',
          colorTextPlaceholder: '#96938e',
          colorBorder: '#e6e4e1',
          borderRadius: 8,
          controlHeightLG: 46,
          fontSize: 14,
        },
      }}
    >
      <main className="login-page">
        <aside className="login-brand" aria-hidden="true">
          <div className="login-wordmark">
            <span>SING</span>
            <span>BOX</span>
            <span>PANEL</span>
          </div>
          <div className="login-brand-footer">
            SING-BOX MANAGEMENT
          </div>
        </aside>

        <section className="login-content" aria-labelledby="login-title">
          <div className="login-stack">
            <div className="login-card">
              <header className="login-card-header">
                <h1 id="login-title">SingBox Panel</h1>
              </header>

              <Form name="login" layout="vertical" onFinish={doLogin} requiredMark={false}>
                <Form.Item name="username" label="用户名" rules={[{ required: true, whitespace: true, message: '请输入用户名' }]}>
                  <Input placeholder="请输入用户名" size="large" autoComplete="username" autoCapitalize="none" spellCheck={false} />
                </Form.Item>
                <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
                  <Input.Password
                    placeholder="请输入密码"
                    size="large"
                    autoComplete="current-password"
                    iconRender={(visible) => (
                      <button type="button" aria-label={visible ? '隐藏密码' : '显示密码'}>
                        {visible ? <EyeOutlined /> : <EyeInvisibleOutlined />}
                      </button>
                    )}
                  />
                </Form.Item>
                <Button className="login-submit" type="primary" htmlType="submit" block size="large" loading={loading}>
                  {loading ? '正在登录' : '登录'}
                </Button>
              </Form>
            </div>
          </div>
        </section>
      </main>
    </ConfigProvider>
  )
}
