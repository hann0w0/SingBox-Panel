import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useSSE, type SSEMessage } from '../../../hooks/useSSE'
import { applyServerConfig, getServer, listOutbounds, listRules, listServerAuthUsers } from '../../../lib/api'
import type { ServerAuthUser } from '../../../lib/api'
import type { Server } from '../../../lib/types'
import ServerDetail from './ServerDetail'
import { TooltipProvider } from '../../../components/ui/tooltip'

vi.mock('../../../hooks/useSSE', () => ({ useSSE: vi.fn(() => ({ connected: true })) }))
vi.mock('../../../lib/api', async (original) => ({ ...await original<typeof import('../../../lib/api')>(), applyServerConfig: vi.fn(), getServer: vi.fn(), listOutbounds: vi.fn(), listRules: vi.fn(), listServerAuthUsers: vi.fn() }))
vi.mock('./InboundForm', () => ({ default: () => null }))
vi.mock('./RoutingForms', () => ({
  OutboundForm: () => null,
  RuleForm: ({ open, users, onSaved }: { open: boolean; users: ServerAuthUser[]; onSaved: () => void }) => open ? <div role="group" aria-label="可选路由用户">{users.map((user) => <span key={user.id}>{user.name}</span>)}<button onClick={onSaved}>保存规则并刷新</button></div> : null,
}))
vi.mock('./ServerDialogs', () => ({ ConfigEditorModal: () => null, ImportPreviewModal: () => null, InstallSingboxModal: () => null }))
vi.mock('./NodeFormatsModal', () => ({ NodeFormatsExportModal: () => null }))
vi.mock('./ServerOverviewCard', () => ({ ServerOverviewCard: () => null }))
vi.mock('./ServerTraffic', () => ({ default: () => null }))
vi.mock('../logs/Logs', () => ({ default: () => null }))

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: Error) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}
function SwitchServer() {
  const navigate = useNavigate()
  return <button onClick={() => navigate('/admin/servers/2/rules')}>切换服务器</button>
}
const mount = () => render(<MemoryRouter initialEntries={['/admin/servers/1/rules']}><TooltipProvider><SwitchServer /><Routes><Route path="/admin/servers/:id/:section" element={<ServerDetail />} /></Routes></TooltipProvider></MemoryRouter>)
const alice: ServerAuthUser = { id: 1, name: 'alice', auth_user: 'auth-alice' }
const bob: ServerAuthUser = { id: 2, name: 'bob', auth_user: 'auth-bob' }

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: query === '(prefers-reduced-motion: reduce)',
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
  vi.mocked(getServer).mockImplementation(async (sid) => ({ server: { id: sid, name: `Server ${sid}`, address: 'example.com', inbounds: [], config_mode: 'managed' } as unknown as Server, install_command: '', public_url: '' }))
  vi.mocked(listOutbounds).mockResolvedValue([])
  vi.mocked(listRules).mockResolvedValue([])
  vi.mocked(listServerAuthUsers).mockResolvedValue([bob])
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

it('keeps form controls editable for an imported raw configuration without a lock banner', async () => {
  vi.mocked(getServer).mockResolvedValue({ server: { id: 1, name: 'Imported', address: 'example.com', inbounds: [], config_mode: 'raw' } as unknown as Server, install_command: '', public_url: '' })
  vi.mocked(listRules).mockResolvedValue([{ id: 5, server_id: 1, outbound: 'direct', match: { domain: ['example.com'] }, enabled: true, sort: 0, remark: '' }])
  mount()
  const add = await screen.findByRole('button', { name: '新增规则' })
  for (const button of [add, screen.getByRole('button', { name: '编辑 规则 5' }), screen.getByRole('button', { name: '删除 规则 5' }), screen.getByRole('button', { name: '调整第 1 条规则顺序' })]) {
    expect(button.hasAttribute('disabled')).toBe(false)
  }
  expect(screen.queryByText('原始配置模式，面板编辑已锁定')).toBeNull()
  expect(screen.queryByRole('button', { name: '切换到面板管理' })).toBeNull()
  fireEvent.click(add)
  expect(screen.getByRole('group', { name: '可选路由用户' })).toBeTruthy()
})

it.each(['resolve', 'reject'] as const)('aborts a pending user request after the main load settles and ignores it on server switch (%s)', async (settle) => {
  const first = deferred<ServerAuthUser[]>()
  const second = deferred<ServerAuthUser[]>()
  vi.mocked(listServerAuthUsers).mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
  mount()
  await waitFor(() => expect(listServerAuthUsers).toHaveBeenCalledTimes(1))
  fireEvent.click(screen.getByRole('button', { name: '切换服务器' }))
  await waitFor(() => expect(listServerAuthUsers).toHaveBeenCalledTimes(2))
  expect(vi.mocked(listServerAuthUsers).mock.calls[0][1]?.aborted).toBe(true)
  fireEvent.click(screen.getByRole('button', { name: '新增规则' }))
  const users = screen.getByRole('group', { name: '可选路由用户' })
  await act(async () => {
    if (settle === 'resolve') first.resolve([alice])
    else first.reject(new Error('stale request'))
  })
  expect(within(users).queryByText('alice')).toBeNull()
  await act(async () => second.resolve([bob]))
  expect(within(users).getByText('bob')).toBeTruthy()
})

it('clears previously loaded routing users while the next server loads', async () => {
  const second = deferred<ServerAuthUser[]>()
  vi.mocked(listServerAuthUsers).mockResolvedValueOnce([alice]).mockReturnValueOnce(second.promise)
  mount()
  fireEvent.click(await screen.findByRole('button', { name: '新增规则' }))
  await screen.findByText('alice')
  fireEvent.click(screen.getByRole('button', { name: '切换服务器' }))
  await waitFor(() => expect(listServerAuthUsers).toHaveBeenCalledTimes(2))
  fireEvent.click(screen.getByRole('button', { name: '新增规则' }))
  expect(screen.queryByText('alice')).toBeNull()
  await act(async () => second.resolve([bob]))
  await screen.findByText('bob')
})

it('aborts an older route-user request on same-server reload and tracks the replacement until unmount', async () => {
  const first = deferred<ServerAuthUser[]>()
  const second = deferred<ServerAuthUser[]>()
  vi.mocked(listServerAuthUsers).mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
  const view = mount()
  await waitFor(() => expect(listServerAuthUsers).toHaveBeenCalledTimes(1))
  fireEvent.click(screen.getByRole('button', { name: '新增规则' }))
  fireEvent.click(screen.getByRole('button', { name: '保存规则并刷新' }))
  await waitFor(() => expect(listServerAuthUsers).toHaveBeenCalledTimes(2))
  expect(vi.mocked(listServerAuthUsers).mock.calls[0][1]?.aborted).toBe(true)
  await act(async () => first.resolve([alice]))
  expect(screen.queryByText('alice')).toBeNull()
  view.unmount()
  expect(vi.mocked(listServerAuthUsers).mock.calls[1][1]?.aborted).toBe(true)
  await act(async () => second.resolve([bob]))
})

it('updates the detail summary bar live when server_status SSE events arrive', async () => {
  let onMessage: ((msg: SSEMessage) => void) | undefined
  vi.mocked(useSSE).mockImplementation((_url, _enabled, cb) => {
    onMessage = cb
    return { connected: true }
  })
  vi.mocked(getServer).mockResolvedValue({
    server: {
      id: 1, name: 'Server 1', address: 'example.com', inbounds: [], config_mode: 'managed',
      online: true, load1: 0.1, mem_used: 100 * 1024 * 1024, mem_total: 1000 * 1024 * 1024,
      traffic_available: true, traffic_download_rate: 1024, traffic_upload_rate: 512,
    } as unknown as Server,
    install_command: '', public_url: '',
  })
  mount()
  expect(await screen.findByText('1.00 KB/s')).toBeTruthy()
  expect(screen.getByText('0.10')).toBeTruthy()
  expect(useSSE).toHaveBeenCalledWith('/api/admin/servers/1/traffic/live', true, expect.any(Function))

  act(() => {
    onMessage?.({
      kind: 'server_status',
      ts: Date.now(),
      data: {
        server_id: 1,
        online: true,
        load1: 0.45,
        mem_used: 300 * 1024 * 1024,
        mem_total: 1000 * 1024 * 1024,
        traffic_available: true,
        traffic_download_rate: 16 * 1024,
        traffic_upload_rate: 8 * 1024,
      },
    })
  })
  expect(screen.getByText('0.45')).toBeTruthy()
  expect(screen.getByText('30%')).toBeTruthy()
  await waitFor(() => {
    expect(screen.getByText('16.00 KB/s')).toBeTruthy()
    expect(screen.getByText('8.00 KB/s')).toBeTruthy()
  })
})

it('shows the apply-config button only when config_dirty is true and applies once on click', async () => {
  vi.mocked(applyServerConfig).mockResolvedValue({ ok: true, apply_state: 'applied', config_dirty: false })
  vi.mocked(getServer)
    .mockResolvedValueOnce({
      server: { id: 1, name: 'Server 1', address: 'example.com', inbounds: [], config_mode: 'managed', config_dirty: false } as unknown as Server,
      install_command: '', public_url: '',
    })
  const first = mount()
  await screen.findByRole('button', { name: '新增规则' })
  expect(screen.queryByRole('button', { name: '应用配置' })).toBeNull()
  first.unmount()

  vi.mocked(getServer)
    .mockResolvedValueOnce({
      server: { id: 1, name: 'Server 1', address: 'example.com', inbounds: [], config_mode: 'managed', config_dirty: true } as unknown as Server,
      install_command: '', public_url: '',
    })
    .mockResolvedValueOnce({
      server: { id: 1, name: 'Server 1', address: 'example.com', inbounds: [], config_mode: 'managed', config_dirty: false } as unknown as Server,
      install_command: '', public_url: '',
    })
  mount()
  const applyButton = await screen.findByRole('button', { name: '应用配置' })
  expect(applyButton.classList.contains('sd-apply-btn')).toBe(true)
  fireEvent.click(applyButton)
  await waitFor(() => expect(applyServerConfig).toHaveBeenCalledWith(1))
  await waitFor(() => expect(screen.queryByRole('button', { name: '应用配置' })).toBeNull())
})
