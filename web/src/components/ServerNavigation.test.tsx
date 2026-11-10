import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Link, MemoryRouter, Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppLayout from './Layout'
import Logs from '../features/admin/logs/Logs'
import { listServers, serverLogs, streamLogs } from '../api'
import { ThemePreferenceProvider } from '../themePreference'
import { TooltipProvider } from './ui/tooltip'
import { useAuth } from '../store'
import type { Server, User } from '../types'
import { parseServerId, serverIdFromLocation } from '../serverNavigation'

vi.mock('../api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../api')>(),
  listServers: vi.fn(), serverLogs: vi.fn(), streamLogs: vi.fn(),
}))
vi.mock('../useSSE', () => ({ useSSE: () => ({ connected: false }) }))

const servers = [{ id: 1, name: 'Alpha', online: true }, { id: 2, name: 'Beta', online: true }] as Server[]

function ServerSectionRedirect({ section }: { section?: string }) {
  const { search } = useLocation()
  const serverId = parseServerId(new URLSearchParams(search).get('server'))
  if (!serverId) return <Navigate to="/admin/servers" replace />
  return <Navigate to={`/admin/servers/${serverId}${section ? `/${section}` : ''}`} replace />
}

function ServerDetailStub() {
  const { id, section } = useParams()
  const sid = Number(id)
  return (
    <div>
      <div data-testid="detail-server">{sid}</div>
      <div data-testid="detail-section">{section || 'inbounds'}</div>
      <Link to={`/admin/servers/${sid}/logs`}>查看日志</Link>
      {section === 'logs' && <Logs key={sid} serverId={sid} serverOnline />}
    </div>
  )
}

function NavigationProbe() {
  const location = useLocation()
  const navigate = useNavigate()
  return <>
    <output data-testid="location">{location.pathname}{location.search}</output>
    <button onClick={() => navigate('/admin/servers/2/logs')}>选择第二台日志</button>
    <button onClick={() => navigate(-1)}>后退</button>
  </>
}

const mount = (path: string) => render(<MemoryRouter initialEntries={[path]}>
  <ThemePreferenceProvider><TooltipProvider>
    <Routes><Route element={<AppLayout />}>
      <Route path="/admin/servers" element={<Link to="/admin/servers/2">管理第二台</Link>} />
      <Route path="/admin/servers/:id/:section?" element={<ServerDetailStub />} />
      <Route path="/admin/traffic" element={<ServerSectionRedirect />} />
      <Route path="/admin/logs" element={<ServerSectionRedirect section="logs" />} />
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
  vi.mocked(serverLogs).mockResolvedValue('')
  vi.mocked(streamLogs).mockResolvedValue({ ok: true, output: '' })
})
afterEach(() => { cleanup(); useAuth.setState({ token: null, user: null }); vi.unstubAllGlobals() })

describe('server-aware navigation', () => {
  it('omits standalone traffic and logs links from the main navigation and switches sections within server detail', async () => {
    mount('/admin/servers')
    expect(screen.queryByRole('link', { name: '流量' })).toBeNull()
    expect(screen.queryByRole('link', { name: '日志' })).toBeNull()

    fireEvent.click(screen.getByRole('link', { name: '管理第二台' }))
    expect((await screen.findByTestId('detail-server')).textContent).toBe('2')
    expect(screen.getByTestId('detail-section').textContent).toBe('inbounds')

    fireEvent.click(screen.getByRole('link', { name: '查看日志' }))
    await waitFor(() => expect(serverLogs).toHaveBeenCalledWith(2, 200, expect.any(AbortSignal)))
    expect(screen.getByTestId('location').textContent).toBe('/admin/servers/2/logs')

    fireEvent.click(screen.getByRole('button', { name: '后退' }))
    expect(screen.getByTestId('location').textContent).toBe('/admin/servers/2')
  })

  it('preserves the active detail section when choosing another server from the breadcrumb switcher', async () => {
    mount('/admin/servers/1/logs')
    await waitFor(() => expect(serverLogs).toHaveBeenCalledWith(1, 200, expect.any(AbortSignal)))
    fireEvent.keyDown(screen.getByRole('button', { name: '切换服务器' }), { key: 'Enter' })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Beta' }))
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/admin/servers/2/logs'))
    await waitFor(() => expect(serverLogs).toHaveBeenCalledWith(2, 200, expect.any(AbortSignal)))
  })

  it('redirects legacy traffic and logs routes to the corresponding server detail or server list', async () => {
    mount('/admin/traffic?server=2')
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/admin/servers/2'))

    cleanup()
    mount('/admin/logs?server=2')
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/admin/servers/2/logs'))

    cleanup()
    mount('/admin/traffic')
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/admin/servers'))
  })

  it('cancels the previous log request so its delayed response cannot replace the selected server', async () => {
    let finish!: (text: string) => void
    vi.mocked(serverLogs).mockImplementation((id) => id === 1 ? new Promise((resolve) => { finish = resolve }) : Promise.resolve(''))
    mount('/admin/servers/1/logs')
    await waitFor(() => expect(serverLogs).toHaveBeenCalledWith(1, 200, expect.any(AbortSignal)))
    const previousSignal = vi.mocked(serverLogs).mock.calls[0][2]!
    fireEvent.click(screen.getByRole('button', { name: '选择第二台日志' }))
    await waitFor(() => expect(serverLogs).toHaveBeenCalledWith(2, 200, expect.any(AbortSignal)))
    expect(previousSignal.aborted).toBe(true)
    await act(async () => finish('old server journal'))
    expect((screen.getByRole('button', { name: '下载日志' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.queryByText('old server journal')).toBeNull()
  })

  it('scopes only positive integer server IDs from server detail pages', () => {
    for (const value of ['0', '-1', '1.5', 'Infinity', '9007199254740992', '1e2', '']) expect(parseServerId(value)).toBeNull()
    expect(serverIdFromLocation('/admin/servers/2/rules')).toBe(2)
    expect(serverIdFromLocation('/admin/servers/2/logs')).toBe(2)
    expect(serverIdFromLocation('/admin/logs', '?server=2')).toBeNull()
    expect(serverIdFromLocation('/admin/users', '?server=2')).toBeNull()
    expect(serverIdFromLocation('/admin/servers/2bad')).toBeNull()
  })
})
