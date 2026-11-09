import { useEffect, useState } from 'react'
import { Dropdown } from '../ui'
import type { MenuProps } from '../ui'
import { Check, Monitor, Download, ChevronDown, LogOut, Moon, Sun } from 'lucide-react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { listServers, logoutSession } from '../api'
import { useAuth } from '../store'
import { useInstallPrompt } from '../pwa'
import { getConsolePalette, parseConsoleTheme } from '../theme'
import { useThemePreference } from '../themePreference'
import type { Server } from '../types'
import { serverIdFromLocation } from '../serverNavigation'
import logoLight from '../assets/logo.svg'
import logoDark from '../assets/logo-dark.svg'

function BrandMark({ className }: { className: string }) {
  return <span className={className} aria-hidden="true"><img className="brand-mark-light" src={logoLight} alt="" /><img className="brand-mark-dark" src={logoDark} alt="" /></span>
}

const ADMIN_NAV = [
  { to: '/admin/servers', label: '服务器' },
  { to: '/admin/traffic', label: '流量' },
  { to: '/admin/logs', label: '日志' },
  { to: '/admin/users', label: '用户' },
  { to: '/admin/external-nodes', label: '节点' },
  { to: '/admin/rulesets', label: '规则集' },
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
    <Dropdown onOpenChange={(open) => { if (open && failed) setAttempt((n) => n + 1) }} menu={{ items: items?.length ? items : [{ key: 'empty', disabled: true, label: failed ? '加载失败，请重试' : '加载中…' }], onClick: ({ key }) => { if (key !== 'empty') onPick(Number(key)) } }}>
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
  const name = user?.email || '用户'
  const role = user?.role === 'admin' ? '管理员' : '用户'
  const signOut = () => {
    setLeaving(true)
    void logoutSession().catch(() => undefined).finally(() => { logout(); navigate('/login') })
  }
  return <div className="app-account" role="group" aria-label={`当前账号：${name}，${role}`}>
    <span className="app-account-name" title={`${name} · ${role}`}>{name}</span>
    <span className="app-account-sep" aria-hidden="true" />
    <ThemeButton scope={scope} />
    {canInstall && <button type="button" className="app-icon-btn" aria-label="安装到桌面" title="安装到桌面" onClick={() => void install().catch(() => undefined)}><Download /></button>}
    <span className="app-account-sep" aria-hidden="true" />
    <button type="button" className="app-icon-btn" aria-label="退出登录" title="退出登录" onClick={signOut} disabled={leaving}><LogOut /></button>
  </div>
}

export default function AppLayout() {
  const { user } = useAuth()
  const { pathname, search } = useLocation()
  const admin = user?.role === 'admin'
  const navigate = useNavigate()
  const serverMatch = pathname.match(/^\/admin\/servers\/(\d+)(?:\/|$)/)
  // Traffic and logs pick their server through ?server=, shown in the same breadcrumb switcher.
  const pickerPage = admin && (pathname === '/admin/traffic' || pathname === '/admin/logs')
  const selectedServerId = admin ? serverIdFromLocation(pathname, search) : null
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
        {pickerPage && <ServerSwitcher id={selectedServerId} onPick={(next) => {
          const params = new URLSearchParams(search)
          params.set('server', String(next))
          navigate(`${pathname}?${params}`)
        }} />}
        <div className="app-bar-right">
          <AccountBar scope={admin ? 'admin' : 'user'} />
        </div>
      </div>
      {admin && <nav className="app-nav" aria-label="主导航">
        {ADMIN_NAV.map((item) => {
          const scoped = selectedServerId && (item.to === '/admin/traffic' || item.to === '/admin/logs')
          return <NavLink key={item.to} to={scoped ? `${item.to}?server=${selectedServerId}` : item.to} className={({ isActive }) => `app-nav-link${isActive ? ' is-active' : ''}`}>{item.label}</NavLink>
        })}
      </nav>}
    </header>
    <main className="app-main"><div className="app-route"><Outlet /></div></main>
  </div>
}
