import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { getServer, listOutbounds, listRules, listServerAuthUsers } from '../../../api'
import type { ServerAuthUser } from '../../../api'
import type { Server } from '../../../types'
import ServerDetail from './ServerDetail'
import { TooltipProvider } from '../../../components/ui/tooltip'

vi.mock('../../../api', async (original) => ({ ...await original<typeof import('../../../api')>(), getServer: vi.fn(), listOutbounds: vi.fn(), listRules: vi.fn(), listServerAuthUsers: vi.fn() }))
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
  vi.mocked(getServer).mockImplementation(async (sid) => ({ server: { id: sid, name: `Server ${sid}`, address: 'example.com', inbounds: [], config_mode: 'managed' } as unknown as Server, install_command: '', public_url: '' }))
  vi.mocked(listOutbounds).mockResolvedValue([])
  vi.mocked(listRules).mockResolvedValue([])
  vi.mocked(listServerAuthUsers).mockResolvedValue([bob])
})
afterEach(cleanup)

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
