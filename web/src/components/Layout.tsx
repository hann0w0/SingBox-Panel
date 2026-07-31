import { useState } from 'react'
import { Button, Drawer, Grid, Layout, Menu } from 'antd'
import {
  CloudServerOutlined,
  DashboardOutlined,
  FileTextOutlined,
  LinkOutlined,
  LogoutOutlined,
  MenuOutlined,
  TeamOutlined,
} from '@ant-design/icons'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../store'

const { Header, Sider, Content } = Layout

function Brand() {
  const nav = useNavigate()
  const { user } = useAuth()
  const targetPath = user?.role === 'admin' ? '/admin/overview' : '/dashboard'
  return (
    <div
      onClick={() => nav(targetPath)}
      style={{
        fontWeight: 700,
        fontSize: 18,
        letterSpacing: 0.5,
        whiteSpace: 'nowrap',
        cursor: 'pointer',
        userSelect: 'none',
      }}
      title="返回概览界面"
    >
      SingBox<span style={{ color: '#3a5bff' }}> Panel</span>
    </div>
  )
}

function UserMenu({ compact }: { compact?: boolean }) {
  const { user, logout } = useAuth()
  const nav = useNavigate()
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, whiteSpace: 'nowrap' }}>
      {!compact && (
        <span style={{ fontWeight: 600, maxWidth: 160, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {user?.email}
        </span>
      )}
      <Button
        size="small"
        icon={<LogoutOutlined />}
        onClick={() => {
          logout()
          nav('/login')
        }}
      >
        {compact ? '' : '登出'}
      </Button>
    </div>
  )
}

const ADMIN_ITEMS = [
  { key: '/admin/overview', icon: <DashboardOutlined />, label: '概览' },
  { key: '/admin/servers', icon: <CloudServerOutlined />, label: '节点' },
  { key: '/admin/users', icon: <TeamOutlined />, label: '用户' },
  { key: '/admin/logs', icon: <FileTextOutlined />, label: '日志' },
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
      <Layout style={{ minHeight: '100vh' }}>
        <Header style={{ background: '#fff', display: 'flex', justifyContent: 'space-between', alignItems: 'center', paddingInline: isMobile ? 16 : 24 }}>
          <Brand />
          <UserMenu compact={isMobile} />
        </Header>
        <Content style={{ padding: isMobile ? 12 : 24 }}>
          <div style={{ maxWidth: 960, margin: '0 auto' }}>
            <Outlet />
          </div>
        </Content>
      </Layout>
    )
  }

  // ---- admin ----
  const selected = ADMIN_ITEMS.find((i) => loc.pathname.startsWith(i.key))?.key ?? loc.pathname
  const menu = (
    <Menu
      theme="light"
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
      <Layout style={{ minHeight: '100vh' }}>
        <Header style={{ background: '#fff', display: 'flex', alignItems: 'center', gap: 12, paddingInline: 16 }}>
          <Button type="text" icon={<MenuOutlined />} onClick={() => setDrawerOpen(true)} />
          <div style={{ flex: 1 }}>
            <Brand />
          </div>
          <UserMenu compact />
        </Header>
        <Drawer
          placement="left"
          open={drawerOpen}
          onClose={() => setDrawerOpen(false)}
          width={230}
          title={<Brand />}
          styles={{ body: { padding: 0 } }}
        >
          {menu}
        </Drawer>
        <Content style={{ padding: 12 }}>
          <Outlet />
        </Content>
      </Layout>
    )
  }

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider
        theme="light"
        width={220}
        style={{ position: 'sticky', top: 0, height: '100vh', overflow: 'auto', borderInlineEnd: '1px solid #eef0f4' }}
      >
        <div style={{ padding: '16px 20px' }}>
          <Brand />
        </div>
        {menu}
      </Sider>
      <Layout>
        <Header style={{ background: '#fff', display: 'flex', justifyContent: 'flex-end', alignItems: 'center', paddingInline: 24 }}>
          <UserMenu />
        </Header>
        <Content style={{ margin: 24 }}>
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  )
}
