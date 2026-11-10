import { act, cleanup, fireEvent, render as renderComponent, screen, waitFor } from '@testing-library/react'
import type { ReactElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getNodeFormats, type NodeFormats } from '../../../api'
import { NodeFormatsExportModal } from './NodeFormatsModal'
import { copyToClipboard } from '../../../util'
import { TooltipProvider } from '../../../components/ui/tooltip'

vi.mock('../../../api', () => ({ getNodeFormats: vi.fn(), errMsg: (error: Error) => error.message }))
vi.mock('../../../util', () => ({ copyToClipboard: vi.fn().mockResolvedValue(undefined) }))

const initial: NodeFormats = { items: [], uri: '', clash: '', surge: '', users: [{ id: 1, email: 'alice' }, { id: 2, email: 'bob' }], multi_user_count: 2, user_id: 0 }
const render = (element: ReactElement) => renderComponent(element, { wrapper: TooltipProvider })
const result = (id: number, tag: string): NodeFormats => ({ ...initial, user_id: id, items: [
  { tag, name: tag, type: 'vless', server: 'fra.example.com', port: 443, params: { UUID: `${tag}-uuid` }, uri: `vless://${tag}-uuid@fra.example.com:443`, clash: `${tag}-clash`, surge: '' },
] })
const choose = (name: string) => {
  fireEvent.click(screen.getByRole('combobox', { name: '多用户凭证' }))
  fireEvent.click(screen.getByRole('option', { name }))
}
const scrollDescriptor = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollIntoView')
beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, value: vi.fn() })
  vi.mocked(getNodeFormats).mockResolvedValue(initial)
  vi.mocked(copyToClipboard).mockResolvedValue(undefined)
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  if (scrollDescriptor) Object.defineProperty(Element.prototype, 'scrollIntoView', scrollDescriptor)
  else Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
})

describe('node export account selection', () => {
  it('exports and copies nodes with the selected account', async () => {
    render(<NodeFormatsExportModal serverId={7} open onCancel={vi.fn()} />)
    await screen.findByText('选择已授权用户后可导出多用户节点')
    vi.mocked(getNodeFormats).mockResolvedValue(result(1, 'alice-node'))
    choose('alice')
    fireEvent.click(await screen.findByRole('button', { name: '复制 alice-node URI' }))
    expect(getNodeFormats).toHaveBeenLastCalledWith(7, expect.any(AbortSignal), 1)
    expect(copyToClipboard).toHaveBeenCalledWith('vless://alice-node-uuid@fra.example.com:443')
  })

  it('discards stale credentials when switching accounts or closing the dialog', async () => {
    const { rerender } = render(<NodeFormatsExportModal serverId={7} open onCancel={vi.fn()} />)
    await screen.findByText('选择已授权用户后可导出多用户节点')
    let finishAlice!: (data: NodeFormats) => void
    vi.mocked(getNodeFormats).mockImplementation((_id, _signal, userId) => userId === 1
      ? new Promise((resolve) => { finishAlice = resolve }) : Promise.resolve(result(2, 'bob-node')))
    choose('alice')
    const calls = vi.mocked(getNodeFormats).mock.calls
    const aliceSignal = calls[calls.length - 1][1]!
    choose('bob')
    await screen.findByRole('button', { name: '复制 bob-node URI' })
    expect(aliceSignal.aborted).toBe(true)
    await act(async () => finishAlice(result(1, 'alice-node')))
    expect(screen.queryByRole('button', { name: '复制 alice-node URI' })).toBeNull()
    rerender(<NodeFormatsExportModal serverId={7} open={false} onCancel={vi.fn()} />)
    vi.mocked(getNodeFormats).mockResolvedValue(initial)
    rerender(<NodeFormatsExportModal serverId={9} open onCancel={vi.fn()} />)
    await screen.findByText('选择已授权用户后可导出多用户节点')
    expect(getNodeFormats).toHaveBeenLastCalledWith(9, expect.any(AbortSignal), undefined)
    expect(screen.queryByRole('button', { name: '复制 bob-node URI' })).toBeNull()
  })

  it('removes the old result on failure and supports retry', async () => {
    render(<NodeFormatsExportModal serverId={7} open onCancel={vi.fn()} />)
    await screen.findByText('选择已授权用户后可导出多用户节点')
    vi.mocked(getNodeFormats).mockResolvedValue(result(1, 'alice-node'))
    choose('alice')
    await screen.findByRole('button', { name: '复制 alice-node URI' })
    vi.mocked(getNodeFormats).mockRejectedValue(new Error('用户授权已撤销'))
    choose('bob')
    await screen.findByText('用户授权已撤销')
    expect(screen.queryByRole('button', { name: '复制 alice-node URI' })).toBeNull()
    vi.mocked(getNodeFormats).mockResolvedValue(result(2, 'bob-node'))
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await screen.findByRole('button', { name: '复制 bob-node URI' })
  })

  it('explains how to enable export when no eligible account exists', async () => {
    vi.mocked(getNodeFormats).mockResolvedValue({ ...initial, users: [] })
    render(<NodeFormatsExportModal serverId={7} open onCancel={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('暂无可用的授权用户，请先在用户页分配此服务器的多用户入站。')).toBeTruthy())
  })

  it('renders connection details and URI/Clash/Surge copy buttons for a single inbound', async () => {
    vi.mocked(getNodeFormats).mockResolvedValue({
      ...result(0, 'vless-7ec402'),
      multi_user_count: 0,
    })
    render(<NodeFormatsExportModal serverId={7} open inboundTag="vless-7ec402" multiUser={false} onCancel={vi.fn()} />)
    await screen.findByText('vless-7ec402-uuid')
    fireEvent.click(screen.getByRole('button', { name: '复制 vless-7ec402 URI' }))
    expect(copyToClipboard).toHaveBeenCalledWith('vless://vless-7ec402-uuid@fra.example.com:443')
    fireEvent.click(screen.getByRole('button', { name: '复制 vless-7ec402 Clash' }))
    expect(copyToClipboard).toHaveBeenCalledWith('vless-7ec402-clash')
  })
})
