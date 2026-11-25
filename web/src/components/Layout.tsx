import { useEffect, useState } from 'react'
import { Dropdown, Form, Input, Modal, message } from './kit'
import type { MenuProps } from './kit'
import { Check, Monitor, Download, ChevronDown, KeyRound, LogOut, Moon, Sun } from 'lucide-react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { changeMyPassword, errMsg, listServers, logoutSession } from '../lib/api'
import { MODAL_SM } from './kit/modal'
import { useAuth } from '../lib/store'
import { useInstallPrompt } from '../lib/pwa'
import { getConsolePalette, parseConsoleTheme } from '../lib/theme'
import { useThemePreference } from '../lib/themePreference'
import type { Server } from '../lib/types'
import logoLight from '../assets/logo.svg'
import logoDark from '../assets/logo-dark.svg'

function BrandMark({ className }: { className: string }) {
  return <span className={className} aria-hidden="true"><img className="brand-mark-light" src={logoLight} alt="" /><img className="brand-mark-dark" src={logoDark} alt="" /></span>
}

const ADMIN_NAV = [
  { to: '/admin/servers', label: '服务器' },
  { to: '/admin/rules', label: '规则' },
  { to: '/admin/users', label: '用户' },
  { to: '/admin/external-nodes', label: '节点' },
  { to: '/admin/settings', label: '设置' },
]

/** Breadcrumb server switcher shown while viewing one server. */
function ServerSwitcher({ id, onPick }: { id: number | null; onPick: (id: number) => void }) {
  const [servers, setServers] = useState<Server[]>([])
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)
  // The list only feeds names and online dots, so load it once per visit to the
  // detail pages and retry when the menu is opened after a failure.
  useEffect(() => {
    const controller = new AbortController()
    setFailed(false)
    listServers(controller.signal).then(setServers).catch(() => { if (!controller.signal.aborted) setFailed(true) })
    return () => controller.abort()
  }, [attempt])
  const current = servers.find((s) => s.id === id)
  const items: MenuProps['items'] = servers.map((s) => ({
    key: String(s.id),
    label: <span className="app-switch-item"><i className={`app-dot${s.online ? '' : ' is-off'}`} />{s.name}{s.id === id && <Check className="app-switch-check" />}</span>,
  }))
  return <>
    <span className="app-crumb-sep" aria-hidden="true">/</span>
    <Dropdown onOpenChange={(open) => { if (open) setAttempt((n) => n + 1) }} menu={{ items: items?.length ? items : [{ key: 'empty', disabled: true, label: failed ? '加载失败，请重试' : '加载中…' }], onClick: ({ key }) => { if (key !== 'empty') onPick(Number(key)) } }}>
      <button type="button" className="app-switch" aria-label="切换服务器">
        <i className={`app-dot${current && !current.online ? ' is-off' : ''}`} />
        <span className="app-switch-name">{current?.name ?? (failed ? '服务器' : '…')}</span>
        <ChevronDown className="app-switch-chev" />
      </button>
    </Dropdown>
  </>
}

function ThemeButton({ scope }: { scope: 'admin' | 'user' }) {
  const { themeId, resolvedThemeId, setTheme } = useThemePreference(scope)
  const icon = themeId === 'auto' ? <Monitor /> : resolvedThemeId === 'dark' ? <Moon /> : <Sun />
  return <Dropdown placement="bottomRight" menu={{
    selectable: true,
    selectedKeys: [themeId],
    items: [
      { key: 'auto', icon: <Monitor />, label: '跟随系统' },
      { key: 'blue', icon: <Sun />, label: '浅色' },
      { key: 'dark', icon: <Moon />, label: '深色' },
    ],
    onClick: ({ key }) => setTheme(parseConsoleTheme(key)),
  }}>
    <button type="button" className="app-icon-btn" aria-label={`主题：${themeId === 'auto' ? '跟随系统' : getConsolePalette(resolvedThemeId).name}`}>{icon}</button>
  </Dropdown>
}

/** Theme, account name and sign-out share one bordered toolbar. */
function AccountBar({ scope }: { scope: 'admin' | 'user' }) {
  const { user, logout } = useAuth()
  const navigate = useNavigate()
  const { canInstall, install } = useInstallPrompt()
  const [leaving, setLeaving] = useState(false)
  const [pwOpen, setPwOpen] = useState(false)
  const [pwSaving, setPwSaving] = useState(false)
  const [pwForm] = Form.useForm<{ old_password: string; new_password: string; confirm_password: string }>()
  const name = user?.email || '用户'
  const role = user?.role === 'admin' ? '管理员' : '用户'
  const signOut = () => {
    setLeaving(true)
    void logoutSession().catch(() => undefined).finally(() => { logout(); navigate('/login') })
  }
  const openPasswordModal = () => {
    pwForm.resetFields()
    setPwOpen(true)
  }
  const submitPassword = async () => {
    const values = await pwForm.validateFields()
    setPwSaving(true)
    try {
      await changeMyPassword(values.old_password, values.new_password)
      message.success('密码已更新，当前会话已自动续期')
      setPwOpen(false)
    } catch (cause) {
      message.error(errMsg(cause))
    } finally {
      setPwSaving(false)
    }
  }
  return <>
    <div className="app-account" role="group" aria-label={`当前账号：${name}，${role}`}>
      <span className="app-account-name" title={`${name} · ${role}`}>{name}</span>
      <span className="app-account-sep" aria-hidden="true" />
      <ThemeButton scope={scope} />
      <button type="button" className="app-icon-btn" aria-label="修改密码" title="修改密码" onClick={openPasswordModal}><KeyRound /></button>
      {canInstall && <button type="button" className="app-icon-btn" aria-label="安装到桌面" title="安装到桌面" onClick={() => void install().catch(() => undefined)}><Download /></button>}
      <span className="app-account-sep" aria-hidden="true" />
      <button type="button" className="app-icon-btn" aria-label="退出登录" title="退出登录" onClick={signOut} disabled={leaving}><LogOut /></button>
    </div>
    {pwOpen && (
      <Modal
        open
        title="修改密码"
        width={MODAL_SM}
        okText="保存密码"
        confirmLoading={pwSaving}
        onOk={submitPassword}
        onCancel={() => { if (!pwSaving) setPwOpen(false) }}
        maskClosable={!pwSaving}
        keyboard={!pwSaving}
      >
        <Form form={pwForm} layout="vertical" onFinish={submitPassword}>
          <Form.Item name="old_password" label="当前密码" rules={[{ required: true, message: '请输入当前密码' }]}>
            <Input.Password autoComplete="current-password" />
          </Form.Item>
          <Form.Item name="new_password" label="新密码" rules={[{ required: true, message: '请输入新密码' }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            name="confirm_password"
            label="确认新密码"
            dependencies={['new_password']}
            rules={[
              { required: true, message: '请再次输入新密码' },
              ({ getFieldValue }) => ({
                validator(_, value) {
                  if (!value || getFieldValue('new_password') === value) return Promise.resolve()
                  return Promise.reject(new Error('两次输入的新密码不一致'))
                },
              }),
            ]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        </Form>
      </Modal>
    )}
  </>
}

export default function AppLayout() {
  const { user } = useAuth()
  const { pathname } = useLocation()
  const admin = user?.role === 'admin'
  const navigate = useNavigate()
  const serverMatch = pathname.match(/^\/admin\/servers\/(\d+)(?:\/|$)/)
  const home = admin ? '/admin/servers' : '/dashboard'
  const pageKey = pathname.split('/').slice(0, 4).join('/')
  useEffect(() => { window.scrollTo(0, 0) }, [pageKey])

  return <div className="app-shell">
    <header className="app-header">
      <div className="app-bar">
        <Link to={home} className="app-brand"><BrandMark className="app-logo" /><span className="app-brand-name">SingBox Panel</span></Link>
        {serverMatch && <ServerSwitcher id={Number(serverMatch[1])} onPick={(next) => {
          const section = pathname.split('/')[4]
          navigate(`/admin/servers/${next}${section ? `/${section}` : ''}`)
        }} />}
        <div className="app-bar-right">
          <AccountBar scope={admin ? 'admin' : 'user'} />
        </div>
      </div>
      {admin && <nav className="app-nav" aria-label="主导航">
        {ADMIN_NAV.map((item) => <NavLink key={item.to} to={item.to} className={({ isActive }) => `app-nav-link${isActive ? ' is-active' : ''}`}>{item.label}</NavLink>)}
      </nav>}
    </header>
    <main className="app-main"><div className="app-route"><Outlet /></div></main>
  </div>
}
