import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Dashboard from './Dashboard'
import { getMe, getUserNodes, resetSub } from '../../api'
import type { UserNode } from '../../api'
import type { User } from '../../types'
import { useAuth } from '../../store'
import { copyToClipboard } from '../../util'
import { TooltipProvider } from '../../components/ui/tooltip'
import { DialogHost, Modal } from '../../ui/dialogs'

vi.mock('../../api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../api')>(),
  getMe: vi.fn(), getUserNodes: vi.fn(), resetSub: vi.fn(),
}))
vi.mock('../../util', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../util')>(), copyToClipboard: vi.fn(),
}))

const user = { id: 2, email: 'alice', role: 'user', enabled: true, server_ids: [1], inbound_ids: [1] } as User
const node: UserNode = {
  name: 'Tokyo', type: 'vless', server: 'tokyo.example', port: 443, region: 'JP',
  link: 'vless://old-uuid@tokyo.example:443', params: { UUID: 'old-uuid' },
}
const nextNode: UserNode = { ...node, link: 'vless://new-uuid@tokyo.example:443', params: { UUID: 'new-uuid' } }
const nextUrl = 'https://panel.example/sub/reset'
const mount = () => render(<TooltipProvider><Dashboard /><DialogHost /></TooltipProvider>)

beforeEach(() => {
  vi.clearAllMocks()
  useAuth.setState({ token: 'test-token', user })
  vi.mocked(getMe).mockResolvedValue({ user, subscription_url: 'https://panel.example/sub/test' })
  vi.mocked(getUserNodes).mockResolvedValue([node])
  vi.mocked(resetSub).mockResolvedValue({ subscription_url: nextUrl })
  vi.mocked(copyToClipboard).mockResolvedValue(undefined)
})
afterEach(() => {
  cleanup()
  Modal.destroyAll()
  useAuth.setState({ token: null, user: null })
  vi.unstubAllGlobals()
})

describe('user subscription dashboard', () => {
  it.each([false, true])('places reset below the subscription links without a page title or password action (mobile: %s)', async (mobile) => {
    vi.stubGlobal('innerWidth', mobile ? 390 : 1280)
    mount()
    const heading = await screen.findByRole('heading', { name: '订阅链接', level: 2 })
    const section = heading.closest('section')!
    expect(screen.queryByRole('heading', { level: 1 })).toBeNull()
    expect(document.querySelector('.ui-page-meta')).toBeNull()
    const reset = within(section).getByRole('button', { name: '重置链接' })
    expect(reset.closest('.sub-actions')?.previousElementSibling?.classList.contains('sub-clients')).toBe(true)
    expect(screen.queryByText(user.email)).toBeNull()
    expect(screen.queryByRole('button', { name: '修改密码' })).toBeNull()
    expect(document.querySelector('input[type="password"]')).toBeNull()
    await screen.findByText('Tokyo')
    for (const [label, target] of [['Surge', 'surge'], ['ClashMeta', 'clash'], ['Shadowrocket', 'shadowrocket']]) {
      fireEvent.click(screen.getByRole('button', { name: `复制 ${label} 订阅链接` }))
      await waitFor(() => expect(copyToClipboard).toHaveBeenLastCalledWith(`https://panel.example/sub/test?target=${target}`))
    }
  })

  it('warns about credential rotation and does nothing when reset is cancelled', async () => {
    mount()
    fireEvent.click(await screen.findByRole('button', { name: '重置链接' }))
    const dialog = screen.getByRole('alertdialog', { name: '重置订阅链接？' })
    expect(dialog.textContent).toContain('多用户节点上的连接凭证')
    expect(dialog.textContent).toContain('离线节点将在重连后同步')
    fireEvent.click(within(dialog).getByRole('button', { name: '取消' }))
    expect(resetSub).not.toHaveBeenCalled()
    expect(getUserNodes).toHaveBeenCalledTimes(1)
  })

  it('refreshes subscription URLs and node details after reset without showing stale credentials', async () => {
    mount()
    await screen.findByText('Tokyo')
    let resolveNodes!: (nodes: UserNode[]) => void
    vi.mocked(getUserNodes).mockReturnValueOnce(new Promise((resolve) => { resolveNodes = resolve }))
    vi.mocked(getMe).mockResolvedValue({ user, subscription_url: nextUrl })
    fireEvent.click(screen.getByRole('button', { name: '重置链接' }))
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '重置' }))
    await waitFor(() => expect(getUserNodes).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(screen.queryByText('Tokyo')).toBeNull()
    expect(screen.getAllByRole('status', { name: '加载中' }).length).toBeGreaterThan(0)
    await act(async () => resolveNodes([nextNode]))
    fireEvent.click(await screen.findByText('Tokyo'))
    const detail = screen.getByRole('dialog', { name: 'Tokyo' })
    expect(within(detail).getByText('new-uuid')).toBeTruthy()
    expect(within(detail).queryByText('old-uuid')).toBeNull()
    expect(within(detail).getByText(nextNode.link)).toBeTruthy()
    fireEvent.click(within(detail.querySelector('footer')!).getByRole('button', { name: '关闭' }))
    for (const [label, target] of [['Surge', 'surge'], ['ClashMeta', 'clash'], ['Shadowrocket', 'shadowrocket']]) {
      fireEvent.click(screen.getByRole('button', { name: `复制 ${label} 订阅链接` }))
      await waitFor(() => expect(copyToClipboard).toHaveBeenLastCalledWith(`${nextUrl}?target=${target}`))
    }
    expect(resetSub).toHaveBeenCalledTimes(1)
    expect(useAuth.getState().token).toBe('test-token')
  })

  it('keeps the original links and nodes when the reset request fails', async () => {
    vi.mocked(resetSub).mockRejectedValueOnce(new Error('重置失败'))
    mount()
    await screen.findByText('Tokyo')
    fireEvent.click(screen.getByRole('button', { name: '重置链接' }))
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '重置' }))
    await screen.findByRole('alert')
    expect(screen.getByRole('alertdialog').textContent).toContain('重置失败')
    expect(getUserNodes).toHaveBeenCalledTimes(1)
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '取消' }))
    expect(screen.getByText('Tokyo')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '复制 Surge 订阅链接' }))
    await waitFor(() => expect(copyToClipboard).toHaveBeenCalledWith('https://panel.example/sub/test?target=surge'))
  })

  it('keeps the new URL and offers a retry if refreshed node details fail to load', async () => {
    mount()
    await screen.findByText('Tokyo')
    vi.mocked(getMe).mockResolvedValue({ user, subscription_url: nextUrl })
    vi.mocked(getUserNodes).mockRejectedValueOnce(new Error('暂时无法刷新节点'))
    fireEvent.click(screen.getByRole('button', { name: '重置链接' }))
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '重置' }))
    await screen.findByText('暂时无法刷新节点')
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(screen.queryByText('Tokyo')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '复制 Surge 订阅链接' }))
    await waitFor(() => expect(copyToClipboard).toHaveBeenCalledWith(`${nextUrl}?target=surge`))
    vi.mocked(getUserNodes).mockResolvedValue([nextNode])
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await screen.findByText('Tokyo')
    expect(getUserNodes).toHaveBeenCalledTimes(3)
  })
})
