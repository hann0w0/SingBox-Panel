import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider, useLocation, useNavigate } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../../../App'
import { getMe, getUser, getUserAccess, getUserNodes, listUsers, listServers, listCustomNodes, resetSub, updateUserAccess, updateUser } from '../../../api'
import type { CustomNode, UserAccess } from '../../../api'
import { TooltipProvider } from '../../../components/ui/tooltip'
import { ThemePreferenceProvider } from '../../../themePreference'
import { useAuth } from '../../../store'
import type { Server, User } from '../../../types'
import { copyToClipboard } from '../../../util'

const NativeRequest = globalThis.Request
vi.mock('../../../api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../../api')>(),
  listUsers: vi.fn(), listServers: vi.fn(), listCustomNodes: vi.fn(), getMe: vi.fn(), getUserNodes: vi.fn(),
  getUser: vi.fn(), getUserAccess: vi.fn(), resetSub: vi.fn(), updateUserAccess: vi.fn(), updateUser: vi.fn(),
}))
vi.mock('../../../util', async (importOriginal) => ({ ...await importOriginal<typeof import('../../../util')>(), copyToClipboard: vi.fn() }))
const admin = { id: 1, email: 'admin', role: 'admin', enabled: true, server_ids: [], inbound_ids: [] } as unknown as User
const alice = { ...admin, id: 2, email: 'alice', role: 'user' as const }
const server = { id: 5, name: 'Tokyo', address: 'tokyo.example', inbounds: [
  { id: 10, tag: 'vless-main', type: 'vless', enabled: true, listen_port: 443 },
  { id: 11, tag: 'trojan-backup', type: 'trojan', enabled: true, listen_port: 8443 },
] } as Server
const external = { id: 30, name: 'Singapore', group: 'External', protocol: 'trojan', enabled: true, all_users: true, address: 'sg.example', port: 443 } as CustomNode
const access: UserAccess = {
  user_id: 2, server_ids: [5], server_wide: false, inbound_ids: [10], custom_node_ids: [30],
  node_order: [{ node_type: 'managed', node_id: 10 }, { node_type: 'custom', node_id: 30 }],
}
let routers: ReturnType<typeof createMemoryRouter>[] = []

function NavigationProbe() {
  const location = useLocation()
  const navigate = useNavigate()
  return <><output data-testid="location">{location.pathname}{location.search}</output><button onClick={() => navigate(-1)}>后退</button></>
}
const mount = (path: string) => {
  const router = createMemoryRouter([{ path: '*', element: <ThemePreferenceProvider><TooltipProvider><App /><NavigationProbe /></TooltipProvider></ThemePreferenceProvider> }], { initialEntries: [path] })
  routers.push(router)
  render(<RouterProvider router={router} />)
  return router
}
const selectTab = (name: RegExp) => fireEvent.click(screen.getByRole('tab', { name }))
const orderedNames = () => screen.getAllByRole('textbox', { name: /自定义/ }).map((input) => input.getAttribute('placeholder'))

beforeEach(() => {
  vi.clearAllMocks()
  // jsdom's AbortSignal is not accepted by Node's native Request. There are
  // no route loaders here; retain the router signal without passing it to Node.
  vi.stubGlobal('Request', class extends NativeRequest {
    constructor(input: RequestInfo | URL, init?: RequestInit) {
      super(input, { ...init, signal: undefined })
      if (init?.signal) Object.defineProperty(this, 'signal', { value: init.signal })
    }
  })
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.spyOn(window, 'scrollBy').mockImplementation(() => {})
  useAuth.setState({ token: 'test-token', user: admin })
  vi.mocked(listUsers).mockResolvedValue([admin, alice])
  vi.mocked(listServers).mockResolvedValue([server])
  vi.mocked(listCustomNodes).mockResolvedValue([external])
  vi.mocked(getMe).mockResolvedValue({ user: admin, subscription_url: 'https://panel.example/sub/test' })
  vi.mocked(getUserNodes).mockResolvedValue([])
  vi.mocked(resetSub).mockResolvedValue({ subscription_url: 'https://panel.example/sub/reset' })
  vi.mocked(getUser).mockImplementation(async (id) => ({ user: id === 1 ? admin : alice, subscription_url: `https://panel.example/sub/user-${id}` }))
  vi.mocked(getUserAccess).mockResolvedValue(structuredClone(access))
  vi.mocked(updateUserAccess).mockImplementation(async (id, body) => ({ ...body, user_id: id, server_wide: false }))
  vi.mocked(updateUser).mockImplementation(async (id, body) => ({ ...alice, email: body.email ?? alice.email, id }))
  vi.mocked(copyToClipboard).mockResolvedValue(undefined)
})
afterEach(() => { cleanup(); routers.forEach((router) => router.dispose()); routers = []; useAuth.setState({ token: null, user: null }); vi.unstubAllGlobals() })

describe('user detail navigation', () => {
  it('opens a user by clicking the row and returns to the list with browser Back', async () => {
    mount('/admin/users')
    await screen.findByRole('button', { name: '新增用户' })
    expect(getMe).not.toHaveBeenCalled()
    expect(getUser).not.toHaveBeenCalled()
    expect(screen.queryByRole('tablist')).toBeNull()
    const nav = screen.getByRole('navigation', { name: '主导航' })
    expect(within(nav).queryByRole('link', { name: '订阅' })).toBeNull()
    const link = await screen.findByRole('link', { name: '管理 alice' })
    expect(link.getAttribute('href')).toBe('/admin/users/2')
    fireEvent.click(link.closest('tr')!)
    await screen.findByRole('button', { name: '复制 Surge 订阅链接' })
    expect(screen.getByTestId('location').textContent).toBe('/admin/users/2')
    expect(screen.getAllByRole('heading', { level: 1 }).map((heading) => heading.textContent)).toEqual(['alice'])
    expect(within(nav).getByRole('link', { name: '用户' }).getAttribute('aria-current')).toBe('page')
    fireEvent.click(screen.getByRole('button', { name: '后退' }))
    await screen.findByRole('button', { name: '新增用户' })
    expect(screen.getByTestId('location').textContent).toBe('/admin/users')
  })

  it.each(['/dashboard', '/admin/users?tab=subscription'])('redirects the old %s entry to the admin detail', async (path) => {
    mount(path)
    await screen.findByRole('button', { name: '复制 Surge 订阅链接' })
    expect(screen.getByTestId('location').textContent).toBe('/admin/users/1')
    expect(listUsers).not.toHaveBeenCalled()
    expect(screen.queryByRole('tab', { name: '我的订阅' })).toBeNull()
    expect(screen.getByRole('button', { name: '重置链接' })).toBeTruthy()
  })

  it('keeps a way back and a retry when the selected user cannot be loaded', async () => {
    vi.mocked(getUser).mockRejectedValue(new Error('用户不存在'))
    mount('/admin/users/999')
    await screen.findByText('用户不存在')
    expect(screen.queryByRole('button', { name: '复制 Surge 订阅链接' })).toBeNull()
    expect(getUserAccess).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('link', { name: '返回用户' }))
    await screen.findByRole('button', { name: '新增用户' })
  })

  it.each(['/dashboard', '/admin/users/1'])('keeps ordinary users on their own dashboard from %s', async (path) => {
    const user = { ...admin, role: 'user' as const }
    useAuth.setState({ user })
    vi.mocked(getMe).mockResolvedValue({ user, subscription_url: 'https://panel.example/sub/test' })
    mount(path)
    await screen.findByRole('heading', { name: '订阅链接' })
    expect(screen.queryByRole('tab', { name: '用户管理' })).toBeNull()
    expect(screen.queryByRole('navigation', { name: '主导航' })).toBeNull()
    expect(screen.getByTestId('location').textContent).toBe('/dashboard')
    expect(listUsers).not.toHaveBeenCalled()
    expect(getUser).not.toHaveBeenCalled()
    expect(getUserAccess).not.toHaveBeenCalled()
  })
})

describe('editing a selected user subscription', () => {
  it('warns about credential rotation when resetting the admin own subscription and copies the new URL', async () => {
    mount('/admin/users/1')
    fireEvent.click(await screen.findByRole('button', { name: '重置链接' }))
    const dialog = screen.getByRole('alertdialog', { name: '重置订阅链接？' })
    expect(dialog.textContent).toContain('多用户节点上的连接凭证')
    expect(dialog.textContent).toContain('离线节点将在重连后同步')
    fireEvent.click(within(dialog).getByRole('button', { name: '重置' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    fireEvent.click(screen.getByRole('button', { name: '复制 Surge 订阅链接' }))
    await waitFor(() => expect(copyToClipboard).toHaveBeenCalledWith('https://panel.example/sub/reset?target=surge'))
    expect(resetSub).toHaveBeenCalledTimes(1)
    expect(useAuth.getState().user).toEqual(admin)
  })

  it('copies the selected user URL in each format without switching the admin session', async () => {
    mount('/admin/users/2')
    await screen.findByRole('heading', { name: 'alice' })
    for (const [label, target] of [['Surge', 'surge'], ['ClashMeta', 'clash'], ['Shadowrocket', 'shadowrocket']]) {
      const button = screen.getByRole('button', { name: `复制 ${label} 订阅链接` })
      const logo = button.querySelector('img')!
      expect(logo.getAttribute('width')).toBe('28')
      expect(logo.getAttribute('height')).toBe('28')
      expect(logo.draggable).toBe(false)
      fireEvent.click(button)
      await waitFor(() => expect(copyToClipboard).toHaveBeenLastCalledWith(`https://panel.example/sub/user-2?target=${target}`))
    }
    expect(getMe).not.toHaveBeenCalled()
    expect(useAuth.getState().user).toEqual(admin)
    expect(screen.queryByRole('button', { name: '重置链接' })).toBeNull()
  })

  it('saves names and keyboard ordering for the target user, then reloads them', async () => {
    const router = mount('/admin/users/2')
    fireEvent.change(await screen.findByRole('textbox', { name: '自定义 Tokyo 的节点名称' }), { target: { value: 'My Tokyo' } })
    fireEvent.keyDown(screen.getByRole('button', { name: '调整第 1 个节点顺序' }), { key: 'ArrowDown' })
    expect(orderedNames()).toEqual(['Singapore', 'Tokyo'])
    fireEvent.click(screen.getByRole('button', { name: '保存更改' }))
    await waitFor(() => expect(updateUserAccess).toHaveBeenCalledWith(2, {
      server_ids: [], inbound_ids: [10], custom_node_ids: [30],
      node_order: [{ node_type: 'custom', node_id: 30 }, { node_type: 'managed', node_id: 10, custom_name: 'My Tokyo' }],
    }))
    await waitFor(() => expect((screen.getByRole('button', { name: '保存更改' }) as HTMLButtonElement).disabled).toBe(true))
    vi.mocked(getUserAccess).mockResolvedValue(await vi.mocked(updateUserAccess).mock.results[0].value)
    fireEvent.click(screen.getByRole('link', { name: '返回用户' }))
    await screen.findByRole('button', { name: '新增用户' })
    await act(() => router.navigate('/admin/users/2'))
    const input = await screen.findByRole('textbox', { name: '自定义 Tokyo 的节点名称' })
    expect((input as HTMLInputElement).value).toBe('My Tokyo')
    expect(orderedNames()).toEqual(['Singapore', 'Tokyo'])
  })

  it('keeps rename beside remove without move buttons, and retains keyboard sorting with safe boundaries', async () => {
    mount('/admin/users/2')
    const handle = await screen.findByRole('button', { name: '调整第 1 个节点顺序' })
    expect(screen.queryByRole('button', { name: /^(上移|下移)/ })).toBeNull()
    const actions = handle.closest('.um-access-order-item')!.querySelector('.um-access-order-actions')!
    expect(within(actions as HTMLElement).getAllByRole('button').map((button) => button.getAttribute('aria-label'))).toEqual(['重命名 Tokyo', '移除 Tokyo'])
    fireEvent.keyDown(handle, { key: 'ArrowUp' })
    expect(orderedNames()).toEqual(['Tokyo', 'Singapore'])
    fireEvent.keyDown(handle, { key: 'ArrowDown' })
    expect(orderedNames()).toEqual(['Singapore', 'Tokyo'])
    fireEvent.keyDown(handle, { key: 'ArrowDown' })
    expect(orderedNames()).toEqual(['Singapore', 'Tokyo'])
    fireEvent.keyDown(handle, { key: 'ArrowUp' })
    expect(orderedNames()).toEqual(['Tokyo', 'Singapore'])
    expect(screen.getByRole('textbox', { name: '自定义 Tokyo 的节点名称' }).closest('[draggable="true"]')).toBeNull()
  })

  it('opens one inline name editor, keeps the draft on completion, and cancels only the current edit', async () => {
    mount('/admin/users/2')
    const rename = await screen.findByRole('button', { name: '重命名 Tokyo' })
    const row = rename.closest('.um-access-order-item')!
    const input = screen.getByRole('textbox', { name: '自定义 Tokyo 的节点名称' }) as HTMLInputElement
    expect(row.classList.contains('is-renaming')).toBe(false)
    expect(document.getElementById(rename.getAttribute('aria-controls')!)?.contains(input)).toBe(true)
    fireEvent.click(rename)
    expect(row.classList.contains('is-renaming')).toBe(true)
    expect(document.activeElement).toBe(input)
    fireEvent.change(input, { target: { value: 'My Tokyo' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(row.classList.contains('is-renaming')).toBe(false)
    expect(document.activeElement).toBe(rename)
    expect(row.querySelector('strong')?.textContent).toBe('My Tokyo')
    expect(updateUserAccess).not.toHaveBeenCalled()
    fireEvent.click(rename)
    fireEvent.change(input, { target: { value: 'Cancel this edit' } })
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(input.value).toBe('My Tokyo')
    expect(row.classList.contains('is-renaming')).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: '保存更改' }))
    await waitFor(() => expect(updateUserAccess).toHaveBeenCalledWith(2, expect.objectContaining({
      node_order: [{ node_type: 'managed', node_id: 10, custom_name: 'My Tokyo' }, { node_type: 'custom', node_id: 30 }],
    })))
  })

  it('keeps a renamed draft through reordering and switching editors, and closes editors on undo', async () => {
    mount('/admin/users/2')
    fireEvent.click(await screen.findByRole('button', { name: '重命名 Tokyo' }))
    fireEvent.change(screen.getByRole('textbox', { name: '自定义 Tokyo 的节点名称' }), { target: { value: 'Tokyo draft' } })
    fireEvent.keyDown(screen.getByRole('button', { name: '调整第 1 个节点顺序' }), { key: 'ArrowDown' })
    expect(orderedNames()).toEqual(['Singapore', 'Tokyo'])
    expect(screen.getByRole('button', { name: '重命名 Tokyo' }).closest('.is-renaming')).not.toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '重命名 Singapore' }))
    expect(document.querySelectorAll('.um-access-order-item.is-renaming').length).toBe(1)
    expect(screen.getByRole('button', { name: '重命名 Tokyo' }).closest('.is-renaming')).toBeNull()
    selectTab(/面板节点/)
    selectTab(/已分配/)
    expect((screen.getByRole('textbox', { name: '自定义 Tokyo 的节点名称' }) as HTMLInputElement).value).toBe('Tokyo draft')
    fireEvent.click(screen.getByRole('button', { name: '撤销' }))
    expect(document.querySelector('.um-access-order-item.is-renaming')).toBeNull()
    expect(orderedNames()).toEqual(['Tokyo', 'Singapore'])
    expect((screen.getByRole('textbox', { name: '自定义 Tokyo 的节点名称' }) as HTMLInputElement).value).toBe('')
  })

  it('does not close a name editor while confirming Chinese IME input, and supports clearing the alias', async () => {
    mount('/admin/users/2')
    fireEvent.click(await screen.findByRole('button', { name: '重命名 Tokyo' }))
    const input = screen.getByRole('textbox', { name: '自定义 Tokyo 的节点名称' }) as HTMLInputElement
    const row = input.closest('.um-access-order-item')!
    fireEvent.change(input, { target: { value: '东京' } })
    fireEvent.keyDown(input, { key: 'Enter', isComposing: true })
    expect(row.classList.contains('is-renaming')).toBe(true)
    fireEvent.keyDown(input, { key: 'Enter', keyCode: 229 })
    expect(row.classList.contains('is-renaming')).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: '完成重命名 Tokyo' }))
    expect(row.classList.contains('is-renaming')).toBe(false)
    expect(row.querySelector('strong')?.textContent).toBe('东京')
    fireEvent.click(screen.getByRole('button', { name: '重命名 Tokyo' }))
    fireEvent.click(within(row as HTMLElement).getByRole('button', { name: '清空' }))
    fireEvent.click(screen.getByRole('button', { name: '完成重命名 Tokyo' }))
    expect(input.value).toBe('')
    expect(row.querySelector('strong')?.textContent).toBe('Tokyo')
    expect((screen.getByRole('button', { name: '保存更改' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('assigns and removes managed and external nodes in one draft, and can undo', async () => {
    mount('/admin/users/2')
    await screen.findByRole('textbox', { name: '自定义 Tokyo 的节点名称' })
    selectTab(/面板节点/)
    fireEvent.click(screen.getByRole('checkbox', { name: '选择 Tokyo 全部节点' }))
    selectTab(/外部节点/)
    fireEvent.click(screen.getByRole('checkbox', { name: '选择 External 全部节点' }))
    selectTab(/已分配/)
    expect(screen.queryByRole('textbox', { name: /Singapore/ })).toBeNull()
    expect(orderedNames()).toEqual(['Tokyo - vless-main', 'Tokyo - Trojan'])
    fireEvent.click(screen.getByRole('button', { name: '撤销' }))
    expect(orderedNames()).toEqual(['Tokyo', 'Singapore'])
    fireEvent.click(screen.getByRole('button', { name: '移除 Singapore' }))
    fireEvent.click(screen.getByRole('button', { name: '保存更改' }))
    await waitFor(() => expect(updateUserAccess).toHaveBeenCalledWith(2, { server_ids: [], inbound_ids: [10], custom_node_ids: [], node_order: [{ node_type: 'managed', node_id: 10 }] }))
  })

  it('preserves whole-server grants when only the personal display name changes', async () => {
    vi.mocked(listServers).mockResolvedValue([{ ...server, inbounds: server.inbounds!.slice(0, 1) }])
    vi.mocked(getUserAccess).mockResolvedValue({ ...structuredClone(access), server_wide: true })
    mount('/admin/users/2')
    fireEvent.change(await screen.findByRole('textbox', { name: '自定义 Tokyo 的节点名称' }), { target: { value: 'Renamed' } })
    fireEvent.click(screen.getByRole('button', { name: '保存更改' }))
    await waitFor(() => expect(updateUserAccess).toHaveBeenCalledWith(2, expect.objectContaining({ server_ids: [5], inbound_ids: [] })))
  })

  it('lets mobile users tap the node name rather than only the checkbox', async () => {
    mount('/admin/users/2')
    await screen.findByRole('textbox', { name: '自定义 Tokyo 的节点名称' })
    selectTab(/外部节点/)
    fireEvent.click(screen.getByRole('button', { name: '展开 External' }))
    const checkbox = screen.getByRole('checkbox', { name: '分配 Singapore' })
    expect(checkbox.getAttribute('aria-checked')).toBe('true')
    expect(checkbox.closest('label')!.parentElement?.closest('label')).toBeNull()
    fireEvent.click(screen.getByText('Singapore', { exact: true }))
    expect(checkbox.getAttribute('aria-checked')).toBe('false')
    fireEvent.click(screen.getByText('Singapore', { exact: true }))
    expect(checkbox.getAttribute('aria-checked')).toBe('true')
  })

  it.each(['mouse', 'touch', 'pen'])('supports %s dragging and cancels interrupted gestures without reordering', async (pointerType) => {
    vi.stubGlobal('innerWidth', 390)
    vi.stubGlobal('PointerEvent', class extends MouseEvent {
      pointerId: number
      pointerType: string
      constructor(type: string, init: PointerEventInit = {}) {
        super(type, init)
        this.pointerId = init.pointerId ?? 1
        this.pointerType = init.pointerType ?? 'touch'
      }
    })
    mount('/admin/users/2')
    const handle = await screen.findByRole('button', { name: '调整第 1 个节点顺序' })
    expect(handle.draggable).toBe(false)
    for (const name of ['setPointerCapture', 'releasePointerCapture', 'hasPointerCapture']) Object.defineProperty(handle, name, { value: vi.fn(() => true) })
    const targetRow = screen.getByRole('button', { name: '调整第 2 个节点顺序' }).closest('[data-node-key]')!
    vi.spyOn(targetRow, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 200, 390, 90))
    const pointer = { pointerId: 1, pointerType, clientY: 310 }
    const start = { ...pointer, clientY: 150 }
    // A small wobble during a tap must not change the order.
    fireEvent.pointerDown(handle, start)
    fireEvent.pointerMove(handle, { ...start, clientY: 153 })
    fireEvent.pointerUp(handle, start)
    expect(orderedNames()).toEqual(['Tokyo', 'Singapore'])
    fireEvent.pointerDown(handle, start)
    fireEvent.pointerMove(handle, pointer)
    expect(orderedNames()).toEqual(['Tokyo', 'Singapore'])
    fireEvent.pointerCancel(handle, pointer)
    expect(orderedNames()).toEqual(['Tokyo', 'Singapore'])
    fireEvent.pointerDown(handle, start)
    fireEvent.pointerMove(handle, pointer)
    fireEvent.lostPointerCapture(handle, pointer)
    expect(orderedNames()).toEqual(['Tokyo', 'Singapore'])
    fireEvent.pointerDown(handle, start)
    if (pointerType === 'touch') {
      vi.spyOn(document.querySelector('.app-header')!, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 390, 96))
      const listBounds = vi.spyOn(document.querySelector('.um-access-order-list')!, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 300, 390, 400))
      vi.spyOn(document.querySelector('.um-access-page-footer')!, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 600, 390, 53))
      vi.mocked(window.scrollBy).mockClear()
      fireEvent.pointerMove(handle, { ...pointer, clientY: 320 })
      expect(window.scrollBy).not.toHaveBeenCalled()
      listBounds.mockReturnValue(new DOMRect(0, -100, 390, 800))
      fireEvent.pointerMove(handle, { ...pointer, clientY: 120 })
      expect(window.scrollBy).toHaveBeenLastCalledWith(0, -12)
      fireEvent.pointerMove(handle, { ...pointer, clientY: 590 })
      expect(window.scrollBy).toHaveBeenLastCalledWith(0, 12)
    }
    fireEvent.pointerMove(handle, pointer)
    fireEvent.pointerUp(handle, pointer)
    expect(orderedNames()).toEqual(['Singapore', 'Tokyo'])
    fireEvent.click(screen.getByRole('button', { name: '保存更改' }))
    await waitFor(() => expect(updateUserAccess).toHaveBeenCalledWith(2, expect.objectContaining({ node_order: [{ node_type: 'custom', node_id: 30 }, { node_type: 'managed', node_id: 10 }] })))
  })

  it('guards both link navigation and browser Back while the draft is unsaved', async () => {
    mount('/admin/users')
    fireEvent.click(await screen.findByRole('link', { name: '管理 alice' }))
    fireEvent.change(await screen.findByRole('textbox', { name: '自定义 Tokyo 的节点名称' }), { target: { value: 'Draft name' } })
    const unload = new Event('beforeunload', { cancelable: true })
    fireEvent(window, unload)
    expect(unload.defaultPrevented).toBe(true)
    fireEvent.click(screen.getByRole('link', { name: '返回用户' }))
    await screen.findByRole('dialog', { name: '放弃未保存的更改？' })
    fireEvent.click(screen.getByRole('button', { name: '继续编辑' }))
    expect((screen.getByRole('textbox', { name: '自定义 Tokyo 的节点名称' }) as HTMLInputElement).value).toBe('Draft name')
    fireEvent.click(screen.getByRole('button', { name: '后退' }))
    await screen.findByRole('dialog', { name: '放弃未保存的更改？' })
    fireEvent.click(screen.getByRole('button', { name: '放弃并离开' }))
    await screen.findByRole('button', { name: '新增用户' })
    expect(updateUserAccess).not.toHaveBeenCalled()
  })

  it('preserves a failed save for retry and rejects duplicate submissions', async () => {
    let reject!: (reason: Error) => void
    vi.mocked(updateUserAccess).mockImplementationOnce(() => new Promise((_, fail) => { reject = fail }))
    mount('/admin/users/2')
    fireEvent.change(await screen.findByRole('textbox', { name: '自定义 Tokyo 的节点名称' }), { target: { value: 'Retry name' } })
    const button = screen.getByRole('button', { name: '保存更改' })
    fireEvent.click(button)
    fireEvent.click(button)
    expect(updateUserAccess).toHaveBeenCalledTimes(1)
    await act(() => reject(new Error('保存失败')))
    expect((screen.getByRole('textbox', { name: '自定义 Tokyo 的节点名称' }) as HTMLInputElement).value).toBe('Retry name')
    fireEvent.click(screen.getByRole('button', { name: '保存更改' }))
    await waitFor(() => expect(updateUserAccess).toHaveBeenCalledTimes(2))
  })

  it('never offers an editable partial catalog after an external-node load failure', async () => {
    vi.mocked(listCustomNodes).mockRejectedValueOnce(new Error('外部节点加载失败'))
    mount('/admin/users/2')
    await screen.findByText('外部节点加载失败')
    expect(screen.queryByRole('button', { name: '保存更改' })).toBeNull()
    expect(screen.queryByRole('tablist')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await screen.findByRole('textbox', { name: '自定义 Singapore 的节点名称' })
    expect(updateUserAccess).not.toHaveBeenCalled()
  })

  it('edits the selected account without replacing the administrator or clearing access', async () => {
    mount('/admin/users/2')
    fireEvent.click(await screen.findByRole('button', { name: '编辑用户' }))
    fireEvent.change(screen.getByRole('textbox', { name: /用户名/ }), { target: { value: 'alice-renamed' } })
    fireEvent.click(screen.getByRole('button', { name: '确定' }))
    await screen.findByRole('heading', { name: 'alice-renamed', level: 1 })
    expect(updateUser).toHaveBeenCalledWith(2, { email: 'alice-renamed', password: undefined })
    expect(useAuth.getState().user).toEqual(admin)
    expect(updateUserAccess).not.toHaveBeenCalled()
  })
})
