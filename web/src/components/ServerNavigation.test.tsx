import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { Link, MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppLayout from './Layout'
import { listServers } from '../api'
import { ThemePreferenceProvider } from '../themePreference'
import { TooltipProvider } from './ui/tooltip'
import { useAuth } from '../store'
import type { Server, User } from '../types'
import { parseServerId, serverIdFromLocation } from '../serverNavigation'

vi.mock('../api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../api')>(),
  listServers: vi.fn(),
}))

const servers = [{ id: 1, name: 'Alpha', online: true }, { id: 2, name: 'Beta', online: true }] as Server[]
function NavigationProbe() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname}{location.search}</output>
}
const mount = (path: string) => render(<MemoryRouter initialEntries={[path]}>
  <ThemePreferenceProvider><TooltipProvider>
    <Routes><Route element={<AppLayout />}>
      <Route path="/admin/servers" element={<Link to="/admin/servers/2/outbounds">管理第二台</Link>} />
      <Route path="/admin/servers/:id/:section?" element={<div>服务器配置</div>} />
    </Route></Routes>
    <NavigationProbe />
  </TooltipProvider></ThemePreferenceProvider>
</MemoryRouter>)

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  useAuth.setState({ token: 'test', user: { role: 'admin', email: 'admin' } as User })
  vi.mocked(listServers).mockResolvedValue(servers)
})
afterEach(() => { cleanup(); useAuth.setState({ token: null, user: null }); vi.unstubAllGlobals() })

describe('server-aware navigation', () => {
  it('keeps traffic and logs out of the top navigation and on the selected server', async () => {
    mount('/admin/servers')
    const nav = screen.getByRole('navigation', { name: '主导航' })
    expect(within(nav).queryByRole('link', { name: '流量' })).toBeNull()
    expect(within(nav).queryByRole('link', { name: '日志' })).toBeNull()
    fireEvent.click(screen.getByRole('link', { name: '管理第二台' }))
    expect(screen.getByTestId('location').textContent).toBe('/admin/servers/2/outbounds')
    expect(screen.getByRole('button', { name: '切换服务器' })).toBeTruthy()
  })

  it('scopes only positive integer server IDs from server pages', () => {
    for (const value of ['0', '-1', '1.5', 'Infinity', '9007199254740992', '1e2', '']) expect(parseServerId(value)).toBeNull()
    expect(serverIdFromLocation('/admin/servers/2/outbounds', '')).toBe(2)
    expect(serverIdFromLocation('/admin/servers/2/logs', '')).toBe(2)
    expect(serverIdFromLocation('/admin/users', '?server=2')).toBeNull()
    expect(serverIdFromLocation('/admin/servers/2bad', '')).toBeNull()
  })
})
