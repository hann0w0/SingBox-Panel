import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SubscriptionFetchHistory, subscriptionLocationLabel } from './SubscriptionFetchHistory'
import { getUserSubFetchDetails, getUserSubFetches, getUserSubFetchLocation } from '../../../lib/api'
import type { SubscriptionHistorySummary } from '../../../lib/api'
import { TooltipProvider } from '../../../components/ui/tooltip'

vi.mock('../../../lib/api', async (original) => ({
  ...await original<typeof import('../../../lib/api')>(),
  getUserSubFetches: vi.fn(), getUserSubFetchDetails: vi.fn(), getUserSubFetchLocation: vi.fn(),
}))
const until = new Date().toISOString()
const summary: SubscriptionHistorySummary = { total: 6003, until, ips: [
  { ip: '1.1.1.1', client: 'Clash Verge', format: 'clash', fetches: 6000, failures: 1, client_count: 2, last_seen: until },
  { ip: '8.8.8.8', client: 'Surge', format: 'surge', fetches: 3, failures: 0, client_count: 1, last_seen: until },
] }
const row = { id: 1, user_id: 2, ip: '1.1.1.1', client: 'Clash Verge', user_agent: 'verge', status: 200, format: 'clash', created_at: until }
function mount() { return render(<TooltipProvider><SubscriptionFetchHistory userID={2} /></TooltipProvider>) }

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getUserSubFetches).mockResolvedValue(structuredClone(summary))
  vi.mocked(getUserSubFetchLocation).mockImplementation(async (_, ip) => ({ ip, status: 'unknown', organization: 'Cloudflare' }))
  vi.mocked(getUserSubFetchDetails).mockResolvedValue({ fetches: [row], next_cursor: 'next', until })
  Element.prototype.scrollIntoView = vi.fn()
})
afterEach(cleanup)

describe('subscription IP history', () => {
  it('shows full server counts without fetching details or restoring removed annotations', async () => {
    mount()
    await screen.findByText('6,003 次拉取 · 2 个 IP')
    expect(screen.getAllByRole('button', { name: /展开.*次拉取记录/ })).toHaveLength(2)
    expect(getUserSubFetchDetails).not.toHaveBeenCalled()
    expect(getUserSubFetches).toHaveBeenCalledWith(2, '30d', expect.any(AbortSignal))
    expect(screen.queryByText(/保留|位置来自|演示用量|仅供参考|GeoJS/)).toBeNull()
    expect(document.querySelector('select')).toBeNull()
  })
  it('loads per-IP details on demand and appends cursor pages without duplicate requests', async () => {
    mount()
    fireEvent.click(await screen.findByRole('button', { name: '展开 1.1.1.1 的 6000 次拉取记录' }))
    await screen.findByRole('button', { name: /显示更多/ })
    expect(getUserSubFetchDetails).toHaveBeenLastCalledWith(2, { ip: '1.1.1.1', range: '30d', until, limit: 6, before: undefined }, expect.any(AbortSignal))
    let resolve!: (value: { fetches: typeof row[]; next_cursor: string; until: string }) => void
    vi.mocked(getUserSubFetchDetails).mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const more = screen.getByRole('button', { name: /显示更多/ })
    fireEvent.click(more); fireEvent.click(more)
    expect(getUserSubFetchDetails).toHaveBeenCalledTimes(2)
    await act(async () => resolve({ fetches: [{ ...row, id: 2, status: 503 }], next_cursor: '', until }))
    expect(await screen.findByText('失败 · 503')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /显示更多/ })).toBeNull()
    expect(document.querySelectorAll('tbody tr')).toHaveLength(2)
  })
  it('retries failed detail pages without losing the existing rows', async () => {
    mount()
    fireEvent.click(await screen.findByRole('button', { name: '展开 1.1.1.1 的 6000 次拉取记录' }))
    const more = await screen.findByRole('button', { name: /显示更多/ })
    vi.mocked(getUserSubFetchDetails).mockRejectedValueOnce(new Error('offline'))
    fireEvent.click(more)
    await screen.findByText('offline')
    expect(document.querySelectorAll('tbody tr')).toHaveLength(1)
    vi.mocked(getUserSubFetchDetails).mockResolvedValueOnce({ fetches: [{ ...row, id: 2 }], next_cursor: '', until })
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await waitFor(() => expect(document.querySelectorAll('tbody tr')).toHaveLength(2))
    expect(getUserSubFetchDetails).toHaveBeenLastCalledWith(2, expect.objectContaining({ before: 'next' }), expect.any(AbortSignal))
  })
  it('changes period, cancels old details, and uses only the latest summary', async () => {
    mount()
    await screen.findByText('6,003 次拉取 · 2 个 IP')
    vi.mocked(getUserSubFetches).mockResolvedValueOnce({ ...summary, total: 3, ips: [summary.ips[1]] })
    fireEvent.click(screen.getByRole('radio', { name: '24 小时' }))
    await screen.findByText('3 次拉取 · 1 个 IP')
    expect(getUserSubFetches).toHaveBeenLastCalledWith(2, '24h', expect.any(AbortSignal))
    expect(screen.queryByText('1.1.1.1')).toBeNull()
  })
  it('displays location failure without hiding records and supports retry', async () => {
    vi.mocked(getUserSubFetchLocation).mockResolvedValue({ ip: '1.1.1.1', status: 'error' })
    mount()
    await screen.findAllByText('位置查询失败')
    fireEvent.click(screen.getByRole('button', { name: '展开 1.1.1.1 的 6000 次拉取记录' }))
    vi.mocked(getUserSubFetchLocation).mockResolvedValueOnce({ ip: '1.1.1.1', status: 'ok', country_code: 'CN', city: 'Shanghai' })
    fireEvent.click(screen.getByRole('button', { name: '重试查询' }))
    await screen.findByText('中国 · Shanghai')
    expect(screen.getByText('6,003 次拉取 · 2 个 IP')).toBeTruthy()
  })
  it('filters by IP or resolved location and clears empty results', async () => {
    vi.mocked(getUserSubFetchLocation).mockImplementation(async (_, ip) => ({ ip, status: 'ok', country_code: ip === '1.1.1.1' ? 'CN' : 'US' }))
    mount()
    await screen.findByText('中国')
    const input = screen.getByRole('textbox', { name: '筛选 IP、地区或客户端' })
    fireEvent.change(input, { target: { value: '中国' } })
    expect(screen.getAllByRole('button', { name: /展开.*次拉取记录/ })).toHaveLength(1)
    fireEvent.change(input, { target: { value: 'no-match' } })
    expect(screen.getByText('没有匹配的拉取记录')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '清除筛选' }))
    expect(screen.getAllByRole('button', { name: /展开.*次拉取记录/ })).toHaveLength(2)
  })
  it('uses the shared accessible sort menu', async () => {
    mount()
    const trigger = await screen.findByRole('button', { name: '排序：最近拉取' })
    fireEvent.keyDown(trigger, { key: 'Enter' })
    const menu = await screen.findByRole('menu')
    expect(within(menu).getByRole('menuitemradio', { name: '最近拉取' }).getAttribute('aria-checked')).toBe('true')
    fireEvent.click(within(menu).getByRole('menuitemradio', { name: '拉取次数' }))
    await screen.findByRole('button', { name: '排序：拉取次数' })
    expect(screen.queryByRole('menu')).toBeNull()
  })
  it('handles summary error, retry, and empty history', async () => {
    vi.mocked(getUserSubFetches).mockRejectedValueOnce(new Error('unavailable'))
    mount()
    await screen.findByText('unavailable')
    vi.mocked(getUserSubFetches).mockResolvedValueOnce({ ips: [], total: 0, until })
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await screen.findByText('暂无拉取记录')
    expect(getUserSubFetchLocation).not.toHaveBeenCalled()
  })
  it('distinguishes missing data from errors and removes duplicate region/city', () => {
    expect(subscriptionLocationLabel({ ip: '', status: 'ok', country_code: 'CN', region: 'Shanghai', city: 'Shanghai' })).toBe('中国 · Shanghai')
    expect(subscriptionLocationLabel({ ip: '', status: 'unknown' })).toBe('地区未知')
    expect(subscriptionLocationLabel({ ip: '', status: 'private' })).toBe('内网 / 保留地址')
  })
})
