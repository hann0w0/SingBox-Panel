import { StrictMode } from 'react'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Dashboard from './Dashboard'
import { createUserForward, deleteUserForward, getMe, getMyTraffic, getSubscriptionSync, getUserForwards, getUserInbounds, getUserNodes, getUserOutbounds, importUserOutbound, resetSub } from '../../lib/api'
import type { UserForward, UserInboundOption, UserNode, UserPrivateOutbound } from '../../lib/api'
import type { RouteRule, User, UserTrafficSeries } from '../../lib/types'
import { useAuth } from '../../lib/store'
import { copyToClipboard } from '../../lib/util'
import { TooltipProvider } from '../../components/ui/tooltip'
import { DialogHost, Modal } from '../../components/kit/dialogs'
import { message } from '../../components/kit/feedback'

vi.mock('../../lib/api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../lib/api')>(),
  getMe: vi.fn(), getUserNodes: vi.fn(), getUserOutbounds: vi.fn(), getUserInbounds: vi.fn(), getUserForwards: vi.fn(),
  getMyTraffic: vi.fn(), resetSub: vi.fn(), getSubscriptionSync: vi.fn(),
  createUserForward: vi.fn(), deleteUserForward: vi.fn(), importUserOutbound: vi.fn(),
}))
vi.mock('../../lib/util', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../lib/util')>(), copyToClipboard: vi.fn(),
}))

const user = { id: 2, email: 'alice', role: 'user', enabled: true, forwarding_enabled: true, server_ids: [1], inbound_ids: [1] } as User
const node: UserNode = {
  name: 'Tokyo', type: 'vless', server: 'tokyo.example', port: 443, region: 'JP',
  link: 'vless://old-uuid@tokyo.example:443', params: { UUID: 'old-uuid' },
}
const nextNode: UserNode = { ...node, link: 'vless://new-uuid@tokyo.example:443', params: { UUID: 'new-uuid' } }
const nextUrl = 'https://panel.example/sub/reset'
const outbound: UserPrivateOutbound = { id: 7, tag: 'HK 家宽', type: 'vless', remark: '' }
const inbound: UserInboundOption = { id: 8, server_id: 1, server_name: 'Tokyo', tag: 'vless-in', type: 'vless', port: 443, multi_user: true }
const forward: UserForward = { id: 9, server_id: 1, server_name: 'Tokyo', inbound_tag: inbound.tag, outbound_id: 7, outbound_tag: outbound.tag, node_name: '我的转发', remark: '', enabled: true }
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}
const mount = () => render(<TooltipProvider><Dashboard /><DialogHost /></TooltipProvider>)
const openForward = async () => {
  fireEvent.click(await screen.findByRole('tab', { name: /^转发/ }))
  fireEvent.click(screen.getByRole('button', { name: '添加转发' }))
}
const selectForward = async () => {
  fireEvent.click(screen.getByRole('combobox', { name: /入口节点/ }))
  fireEvent.click(await screen.findByRole('option', { name: 'Tokyo · vless-in' }))
  await waitFor(() => expect(screen.getByRole('combobox', { name: /入口节点/ }).textContent).toContain('Tokyo · vless-in'))
  fireEvent.click(screen.getByRole('combobox', { name: /^出站/ }))
  fireEvent.click(await screen.findByRole('option', { name: outbound.tag }))
  await waitFor(() => expect(screen.getByRole('combobox', { name: /^出站/ }).textContent).toContain(outbound.tag))
}
const reset = async () => {
  fireEvent.click(screen.getByRole('button', { name: '重置链接' }))
  fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '重置' }))
  await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
}

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  Element.prototype.scrollIntoView = vi.fn()
  useAuth.setState({ token: 'test-token', user })
  vi.mocked(getMe).mockResolvedValue({ user, subscription_url: 'https://panel.example/sub/test' })
  vi.mocked(getUserNodes).mockResolvedValue([node])
  vi.mocked(getUserOutbounds).mockResolvedValue([])
  vi.mocked(getUserInbounds).mockResolvedValue([])
  vi.mocked(getUserForwards).mockResolvedValue([])
  vi.mocked(getMyTraffic).mockResolvedValue({
    range: '24h', step_seconds: 3600, timezone: 'Asia/Tokyo', upload: 1024, download: 2048, points: [], inbounds: [],
  })
  vi.mocked(resetSub).mockResolvedValue({ subscription_url: nextUrl, sync: { apply_state: 'pending', pending_servers: 1, failed_servers: 0 } })
  vi.mocked(getSubscriptionSync).mockResolvedValue({ apply_state: 'applied', pending_servers: 0, failed_servers: 0 })
  vi.mocked(copyToClipboard).mockResolvedValue(undefined)
})
afterEach(() => {
  cleanup()
  Modal.destroyAll()
  useAuth.setState({ token: null, user: null })
  vi.unstubAllGlobals()
})

describe('user subscription dashboard', () => {
  it('keeps the console tabs visible but disables forwarding views when the account lacks permission', async () => {
    vi.mocked(getMe).mockResolvedValue({ user: { ...user, forwarding_enabled: false }, subscription_url: 'https://panel.example/sub/test' })
    mount()
    await screen.findByText('Tokyo')
    const tabs = screen.getByRole('tablist', { name: '控制台视图' })
    expect(within(tabs).getByRole('tab', { name: /^节点/ }).getAttribute('aria-selected')).toBe('true')
    expect((within(tabs).getByRole('tab', { name: /^出站/ }) as HTMLButtonElement).disabled).toBe(true)
    expect((within(tabs).getByRole('tab', { name: /^转发/ }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText('Tokyo')).toBeTruthy()
    expect(getUserOutbounds).not.toHaveBeenCalled()
    expect(getUserInbounds).not.toHaveBeenCalled()
    expect(getUserForwards).not.toHaveBeenCalled()
  })

  it('shows the signed-in user usage and only their private outbounds', async () => {
    vi.mocked(getUserOutbounds).mockResolvedValue([
      { id: 1, tag: 'HK 家宽', type: 'vless', remark: '' },
    ])
    mount()
    expect(await screen.findByRole('heading', { name: '用量', level: 2 })).toBeTruthy()
    fireEvent.click(await screen.findByRole('tab', { name: /^出站/ }))
    expect(await screen.findByText('HK 家宽')).toBeTruthy()
    expect(screen.getByText('2.00 KB')).toBeTruthy()
    expect(screen.getByText('1.00 KB')).toBeTruthy()
    fireEvent.click(screen.getByRole('radio', { name: '7 天' }))
    await waitFor(() => expect(getMyTraffic).toHaveBeenLastCalledWith('7d', expect.anything()))
    for (const request of [getMe, getUserNodes, getUserOutbounds, getUserInbounds, getUserForwards]) expect(request).toHaveBeenCalledTimes(1)
  })

  it.each([false, true])('places reset in the subscription header without a page title or password action (mobile: %s)', async (mobile) => {
    vi.stubGlobal('innerWidth', mobile ? 390 : 1280)
    mount()
    const heading = await screen.findByRole('heading', { name: '订阅链接', level: 2 })
    const section = heading.closest('section')!
    expect(screen.queryByRole('heading', { level: 1 })).toBeNull()
    expect(document.querySelector('.ui-page-meta')).toBeNull()
    const reset = within(section).getByRole('button', { name: '重置链接' })
    expect(reset.closest('.ui-section-head')).toBeTruthy()
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
    expect(within(detail).queryByText('new-uuid')).toBeNull()
    expect(within(detail).queryByText('old-uuid')).toBeNull()
    fireEvent.click(within(detail).getByRole('button', { name: '显示凭据' }))
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

  it.each(['resolve', 'reject'] as const)('ignores obsolete list responses and finalizers after effect replay (%s)', async (settle) => {
    const oldMe = deferred<Awaited<ReturnType<typeof getMe>>>()
    const oldNodes = deferred<UserNode[]>()
    const oldTraffic = deferred<UserTrafficSeries>()
    const currentOutbounds = deferred<UserPrivateOutbound[]>()
    vi.mocked(getMe).mockReturnValueOnce(oldMe.promise)
    vi.mocked(getUserNodes).mockReturnValueOnce(oldNodes.promise)
    vi.mocked(getUserOutbounds).mockReturnValueOnce(currentOutbounds.promise)
    vi.mocked(getMyTraffic).mockReturnValueOnce(oldTraffic.promise)
    render(<StrictMode><TooltipProvider><Dashboard /><DialogHost /></TooltipProvider></StrictMode>)
    await screen.findByText('Tokyo')
    expect(vi.mocked(getMe).mock.calls[0][0]?.aborted).toBe(true)
    expect(vi.mocked(getUserNodes).mock.calls[0][0]?.aborted).toBe(true)
    await act(async () => {
      if (settle === 'resolve') {
        oldMe.resolve({ user: { ...user, forwarding_enabled: false }, subscription_url: 'https://stale.example' })
        oldNodes.resolve([{ ...node, name: 'Stale node' }])
        oldTraffic.resolve({ range: '24h', upload: 0, download: 999999, points: [], inbounds: [], step_seconds: 3600, timezone: 'UTC' })
      } else {
        for (const request of [oldMe, oldNodes, oldTraffic]) request.reject(new Error('Obsolete error'))
      }
    })
    expect(screen.getByText('Tokyo')).toBeTruthy()
    expect(screen.getByText('2.00 KB')).toBeTruthy()
    fireEvent.click(screen.getByRole('tab', { name: /^出站/ }))
    expect(screen.getAllByRole('status', { name: '加载中' }).length).toBeGreaterThan(0)
    expect(screen.queryByText(/Stale|Obsolete/)).toBeNull()
    await act(async () => currentOutbounds.resolve([outbound]))
    expect(screen.getByText(outbound.tag)).toBeTruthy()
    await openForward()
    fireEvent.click(screen.getByRole('combobox', { name: /入口节点/ }))
    expect(screen.queryByRole('option', { name: /Stale/ })).toBeNull()
    expect(screen.queryByText('Stale forward')).toBeNull()
  })

  it('retains traffic during failures and ignores a superseded range response', async () => {
    mount()
    await screen.findByText('2.00 KB')
    const week = deferred<UserTrafficSeries>()
    const month = deferred<UserTrafficSeries>()
    vi.mocked(getMyTraffic).mockReturnValueOnce(week.promise).mockReturnValueOnce(month.promise)
    fireEvent.click(screen.getByRole('radio', { name: '7 天' }))
    fireEvent.click(screen.getByRole('radio', { name: '30 天' }))
    expect(vi.mocked(getMyTraffic).mock.calls[1][1]?.aborted).toBe(true)
    await act(async () => week.resolve({ range: '7d', upload: 0, download: 999999, points: [], inbounds: [], step_seconds: 3600, timezone: 'UTC' }))
    expect(screen.getByRole('status', { name: '正在刷新' })).toBeTruthy()
    expect(screen.getByText('2.00 KB')).toBeTruthy()
    await act(async () => month.reject(new Error('用量加载失败')))
    expect(screen.getByText('用量加载失败')).toBeTruthy()
    expect(screen.getByText('2.00 KB')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await waitFor(() => expect(getMyTraffic).toHaveBeenLastCalledWith('30d', expect.anything()))
    await waitFor(() => expect(screen.queryByText('用量加载失败')).toBeNull())
    expect(getMe).toHaveBeenCalledTimes(1)
  })

  it('keeps saved list data on refresh errors and retries each failed list independently', async () => {
    vi.mocked(getUserOutbounds).mockResolvedValue([outbound])
    vi.mocked(getUserForwards).mockResolvedValue([forward])
    vi.mocked(getUserInbounds).mockResolvedValue([inbound])
    mount()
    await screen.findByText('Tokyo')
    vi.mocked(getUserOutbounds).mockRejectedValueOnce(new Error('出站加载失败'))
    vi.mocked(getUserForwards).mockRejectedValueOnce(new Error('转发加载失败'))
    vi.mocked(getUserInbounds).mockRejectedValueOnce(new Error('入口加载失败'))
    await reset()
    fireEvent.click(screen.getByRole('tab', { name: /^出站/ }))
    expect(await screen.findByText('出站加载失败')).toBeTruthy()
    expect(screen.getByText(outbound.tag)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await waitFor(() => expect(screen.queryByText('出站加载失败')).toBeNull())
    expect(getUserOutbounds).toHaveBeenCalledTimes(3)
    expect(getMe).toHaveBeenCalledTimes(2)
    await openForward()
    expect(screen.getByText('我的转发')).toBeTruthy()
    const dialog = screen.getByRole('dialog', { name: '添加转发' })
    expect(within(dialog).getByText('入口加载失败')).toBeTruthy()
    fireEvent.click(within(dialog).getByRole('button', { name: '重试' }))
    await waitFor(() => expect(screen.queryByText('入口加载失败')).toBeNull())
    expect(getUserInbounds).toHaveBeenCalledTimes(3)
    fireEvent.click(within(dialog).getByRole('button', { name: '取消' }))
    expect(screen.getByText('转发加载失败')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await waitFor(() => expect(screen.queryByText('转发加载失败')).toBeNull())
    expect(getUserForwards).toHaveBeenCalledTimes(3)
  })

  it.each(['uri', 'subscription'] as const)('validates %s imports and retains a failed draft with duplicate submission protection', async (mode) => {
    const pending = deferred<Awaited<ReturnType<typeof importUserOutbound>>>()
    vi.mocked(importUserOutbound).mockReturnValueOnce(pending.promise).mockResolvedValueOnce({ outbounds: [] })
    const errorToast = vi.spyOn(message, 'error')
    mount()
    fireEvent.click(await screen.findByRole('tab', { name: /^出站/ }))
    fireEvent.click(screen.getByRole('button', { name: '添加出站' }))
    const dialog = screen.getByRole('dialog', { name: '添加出站' })
    if (mode === 'subscription') fireEvent.click(within(dialog).getByRole('radio', { name: '订阅导入' }))
    fireEvent.click(within(dialog).getByRole('button', { name: '导入' }))
    await screen.findByText(mode === 'uri' ? '请输入节点 URI' : '请输入订阅地址')
    expect(importUserOutbound).not.toHaveBeenCalled()
    expect(errorToast).not.toHaveBeenCalled()
    const value = mode === 'uri' ? 'vless://uuid@hk.example:443#HK' : 'https://example.com/sub'
    fireEvent.change(within(dialog).getByRole('textbox'), { target: { value: ` ${value} ` } })
    const submit = within(dialog).getByRole('button', { name: '导入' })
    fireEvent.click(submit)
    fireEvent.submit(dialog.querySelector('form')!)
    await waitFor(() => expect(importUserOutbound).toHaveBeenCalledTimes(1))
    expect(importUserOutbound).toHaveBeenCalledWith(mode === 'uri' ? { uri: value } : { url: value })
    await act(async () => pending.reject(new Error('导入失败')))
    expect(errorToast).toHaveBeenCalledWith('导入失败')
    expect((within(dialog).getByRole('textbox') as HTMLTextAreaElement).value).toBe(` ${value} `)
    fireEvent.click(submit)
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(importUserOutbound).toHaveBeenCalledTimes(2)
  })

  it.each(['pending', 'failed'] as const)('reports a saved forward with %s apply state and prevents duplicate requests', async (apply_state) => {
    vi.mocked(getUserInbounds).mockResolvedValue([inbound])
    vi.mocked(getUserOutbounds).mockResolvedValue([outbound])
    const pending = deferred<Awaited<ReturnType<typeof createUserForward>>>()
    vi.mocked(createUserForward).mockReturnValueOnce(pending.promise)
    const info = vi.spyOn(message, 'info')
    const warning = vi.spyOn(message, 'warning')
    const error = vi.spyOn(message, 'error')
    mount()
    await openForward()
    const dialog = screen.getByRole('dialog', { name: '添加转发' })
    fireEvent.click(within(dialog).getByRole('button', { name: '添加' }))
    await screen.findByText('请选择入口节点')
    await screen.findByText('请选择出站')
    expect(createUserForward).not.toHaveBeenCalled()
    expect(error).not.toHaveBeenCalled()
    await selectForward()
    fireEvent.click(within(dialog).getByRole('button', { name: '添加' }))
    fireEvent.submit(dialog.querySelector('form')!)
    await waitFor(() => expect(createUserForward).toHaveBeenCalledTimes(1))
    expect(createUserForward).toHaveBeenCalledWith({ inbound_server_id: 1, inbound_tag: 'vless-in', outbound_id: 7 })
    await act(async () => pending.resolve({ forward: { id: 9 } as RouteRule, apply_state, apply_error: 'agent rejected config' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    if (apply_state === 'failed') expect(warning).toHaveBeenCalledWith(expect.objectContaining({ content: '已保存，但节点下发失败：agent rejected config' }))
    else expect(info).toHaveBeenCalledWith(expect.objectContaining({ content: '已保存；节点当前离线，重连后会自动下发' }))
    expect(getUserForwards).toHaveBeenCalledTimes(2)
  })

  it('keeps a failed forward draft available for retry', async () => {
    vi.mocked(getUserInbounds).mockResolvedValue([inbound])
    vi.mocked(getUserOutbounds).mockResolvedValue([outbound])
    vi.mocked(createUserForward).mockRejectedValueOnce(new Error('保存转发失败')).mockResolvedValueOnce({ forward: { id: 9 } as RouteRule, apply_state: 'applied' })
    const error = vi.spyOn(message, 'error')
    mount()
    await openForward()
    await selectForward()
    fireEvent.click(screen.getByRole('button', { name: '添加' }))
    await waitFor(() => expect(error).toHaveBeenCalledWith('保存转发失败'))
    expect(screen.getByRole('combobox', { name: /入口节点/ }).textContent).toContain('Tokyo · vless-in')
    expect(screen.getByRole('combobox', { name: /^出站/ }).textContent).toContain(outbound.tag)
    fireEvent.click(screen.getByRole('button', { name: '添加' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(createUserForward).toHaveBeenCalledTimes(2)
  })

  it.each(['pending', 'failed'] as const)('reports %s configuration apply after forward deletion', async (apply_state) => {
    vi.mocked(getUserForwards).mockResolvedValue([forward])
    vi.mocked(deleteUserForward).mockResolvedValue({ apply_state, apply_error: 'apply deletion failed' })
    const info = vi.spyOn(message, 'info')
    const warning = vi.spyOn(message, 'warning')
    mount()
    fireEvent.click(await screen.findByRole('tab', { name: /^转发/ }))
    fireEvent.click(await screen.findByRole('button', { name: '删除转发 vless-in' }))
    const dialog = screen.getByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: '删除' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(deleteUserForward).toHaveBeenCalledWith(9)
    if (apply_state === 'failed') expect(warning).toHaveBeenCalledWith(expect.objectContaining({ content: '已保存，但节点下发失败：apply deletion failed' }))
    else expect(info).toHaveBeenCalled()
    expect(getUserForwards).toHaveBeenCalledTimes(2)
  })

  it('does not refresh or notify after an import completes on an unmounted dashboard', async () => {
    const pending = deferred<Awaited<ReturnType<typeof importUserOutbound>>>()
    vi.mocked(importUserOutbound).mockReturnValueOnce(pending.promise)
    const success = vi.spyOn(message, 'success')
    const view = mount()
    fireEvent.click(await screen.findByRole('tab', { name: /^出站/ }))
    fireEvent.click(screen.getByRole('button', { name: '添加出站' }))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'vless://uuid@hk.example:443' } })
    fireEvent.click(screen.getByRole('button', { name: '导入' }))
    await waitFor(() => expect(importUserOutbound).toHaveBeenCalledTimes(1))
    view.unmount()
    await act(async () => pending.resolve({ outbounds: [] }))
    expect(success).not.toHaveBeenCalled()
    expect(getUserOutbounds).toHaveBeenCalledTimes(1)
  })
})
