import { useEffect, useRef, useState } from 'react'
import { Button, Drawer, Grid, Layout, Menu } from 'antd'
import {
  AreaChartOutlined,
  CloudServerOutlined,
  DashboardOutlined,
  FileTextOutlined,
  LinkOutlined,
  LogoutOutlined,
  MenuOutlined,
  SettingOutlined,
  TeamOutlined,
} from '@ant-design/icons'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../store'
import ThemeSelector from './ThemeSelector'

const { Header, Sider, Content } = Layout

function Brand() {
  const nav = useNavigate()
  const { user } = useAuth()
  const targetPath = user?.role === 'admin' ? '/admin/overview' : '/dashboard'
  return (
    <button
      type="button"
      onClick={() => nav(targetPath)}
      className="console-brand"
      title="返回概览界面"
      aria-label="返回概览界面"
    >
      SingBox<span> Panel</span>
    </button>
  )
}

function UserMenu({ compact, showTheme = false }: { compact?: boolean; showTheme?: boolean }) {
  const { user, logout } = useAuth()
  const nav = useNavigate()
  const username = user?.email || '用户'
  const roleLabel = user?.role === 'admin' ? '管理员' : '用户'
  return (
    <div className={`console-account${compact ? ' is-compact' : ''}`} role="group" aria-label={`当前账号：${username}，${roleLabel}`}>
      <div className="console-account-identity" title={`${username} · ${roleLabel}`}>
        <div className="console-account-details">
          <span className="console-account-name">{username}</span>
          {!compact && <span className="console-account-role">{roleLabel}</span>}
        </div>
      </div>
      <div className="console-account-actions">
        {showTheme && <ThemeSelector scope={user?.role === 'admin' ? 'admin' : 'user'} variant="account" compact={compact} />}
        {showTheme && <span className="console-account-divider" aria-hidden="true" />}
        <Button
          type="text"
          className="console-logout"
          icon={<LogoutOutlined />}
          title="退出登录"
          aria-label="退出登录"
          onClick={() => {
            logout()
            nav('/login')
          }}
        >
          {compact ? '' : '退出'}
        </Button>
      </div>
    </div>
  )
}

function RouteContent() {
  const { pathname } = useLocation()
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const media = window.matchMedia('(prefers-reduced-motion: reduce)')
    if (media.matches || !ref.current?.animate) return
    const animation = ref.current.animate(
      [{ opacity: 0, transform: 'translateY(8px)' }, { opacity: 1, transform: 'translateY(0)' }],
      { duration: 240, easing: 'cubic-bezier(0.2, 0.7, 0.2, 1)' },
    )
    const stop = () => { if (media.matches) animation.cancel() }
    media.addEventListener('change', stop)
    return () => {
      animation.cancel()
      media.removeEventListener('change', stop)
    }
  }, [pathname])

  return <div className="console-route" ref={ref}><Outlet /></div>
}

const ADMIN_ITEMS = [
  { key: '/admin/overview', icon: <DashboardOutlined />, label: '概览' },
  { key: '/admin/servers', icon: <CloudServerOutlined />, label: '主机' },
  { key: '/admin/traffic', icon: <AreaChartOutlined />, label: '流量' },
  { key: '/admin/users', icon: <TeamOutlined />, label: '用户' },
  { key: '/admin/logs', icon: <FileTextOutlined />, label: '日志' },
  { key: '/admin/settings', icon: <SettingOutlined />, label: '设置' },
  { key: '/dashboard', icon: <LinkOutlined />, label: '订阅' },
]

export default function AppLayout() {
  const { user } = useAuth()
  const nav = useNavigate()
  const loc = useLocation()
  const screens = Grid.useBreakpoint()
  const isMobile = !screens.md
  const [drawerOpen, setDrawerOpen] = useState(false)

  // ---- regular user: single-page layout, no sidebar ----
  if (user?.role !== 'admin') {
    return (
      <Layout className="console-shell console-user-shell">
        <Header className="console-header console-user-header">
          <Brand />
          <UserMenu compact={isMobile} showTheme />
        </Header>
        <Content className="console-content">
          <div className="console-user-content">
            <RouteContent />
          </div>
        </Content>
      </Layout>
    )
  }

  // ---- admin ----
  const selected = ADMIN_ITEMS.find((i) => loc.pathname.startsWith(i.key))?.key ?? loc.pathname
  const pageTitle = ADMIN_ITEMS.find((i) => i.key === selected)?.label || '控制台'
  const menu = (
    <Menu
      theme="light"
      className="console-nav"
      mode="inline"
      selectedKeys={[selected]}
      items={ADMIN_ITEMS}
      onClick={(e) => {
        nav(e.key)
        setDrawerOpen(false)
      }}
      style={{ borderInlineEnd: 0 }}
    />
  )

  if (isMobile) {
    return (
      <Layout className="console-shell console-mobile-shell">
        <Header className="console-header console-mobile-header">
          <Button type="text" icon={<MenuOutlined />} onClick={() => setDrawerOpen(true)} title="打开导航菜单" aria-label="打开导航菜单" />
          <div className="console-mobile-brand">
            <Brand />
          </div>
          <UserMenu compact showTheme />
        </Header>
        <Drawer
          placement="left"
          rootClassName="console-nav-drawer"
          open={drawerOpen}
          onClose={() => setDrawerOpen(false)}
          width={248}
          title={<Brand />}
          styles={{ body: { padding: '12px 10px', display: 'flex', flexDirection: 'column' } }}
        >
          {menu}
        </Drawer>
        <Content className="console-content">
          {selected !== '/dashboard' && <h1 className="console-mobile-title">{pageTitle}</h1>}
          <RouteContent />
        </Content>
      </Layout>
    )
  }

  return (
    <Layout className="console-shell">
      <Sider
        theme="light"
        width={232}
        className="console-sidebar"
      >
        <div className="console-sidebar-brand">
          <Brand />
        </div>
        {menu}
      </Sider>
      <Layout className="console-main">
        <Header className="console-header">
          <div className="console-location"><span>管理控制台</span><span aria-hidden="true">/</span><h1>{pageTitle}</h1></div>
          <UserMenu showTheme />
        </Header>
        <Content className="console-content">
          <RouteContent />
        </Content>
      </Layout>
    </Layout>
  )
}
