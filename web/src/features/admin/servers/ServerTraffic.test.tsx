import { act, cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getServerTraffic } from '../../../api'
import { useSSE, type SSEMessage } from '../../../useSSE'
import ServerTraffic from './ServerTraffic'

vi.mock('../../../api', () => ({
  getServerTraffic: vi.fn(),
  errMsg: (error: Error) => error.message,
  isCanceledRequest: () => false,
}))
vi.mock('../../../useSSE', () => ({ useSSE: vi.fn() }))

const SERVER_TS = Date.UTC(2026, 9, 25, 10, 0, 0)
const rates = { upload_rate: 512, download_rate: 1024 }
let monotonicMs = 0
let onTraffic: (message: SSEMessage) => void

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
  vi.setSystemTime(SERVER_TS)
  monotonicMs = 1000
  vi.spyOn(performance, 'now').mockImplementation(() => monotonicMs)
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  vi.mocked(getServerTraffic).mockImplementation(() => new Promise(() => {}))
  vi.mocked(useSSE).mockImplementation((_url, _enabled, onMessage) => {
    onTraffic = onMessage
    return { connected: true }
  })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

function emit(message: Partial<SSEMessage> = {}) {
  act(() => onTraffic({ kind: 'traffic', ts: SERVER_TS, age_ms: 0, data: rates, ...message }))
}

function advance(milliseconds: number) {
  act(() => {
    monotonicMs += milliseconds
    vi.advanceTimersByTime(milliseconds)
  })
}

function expectRates(active: boolean) {
  const card = screen.getByText('实时速率').parentElement!
  expect(within(card).getByText(active ? '↓ 1.00 KB/s' : '↓ 0 B/s')).toBeTruthy()
  expect(within(card).getByText(active ? '↑ 512 B/s' : '↑ 0 B/s')).toBeTruthy()
}

describe('live traffic freshness', () => {
  it.each([-120_000, 120_000])('expires samples after 15 seconds with browser clock offset %d ms', (offset) => {
    vi.setSystemTime(SERVER_TS + offset)
    render(<ServerTraffic serverId={1} />)
    emit()
    expectRates(true)

    advance(12_000)
    expectRates(true)
    advance(3000)
    expectRates(false)

    const chart = screen.getByRole('img', { name: '上传下载实时速度趋势图' })
    expect(within(chart).getByText('10:00:00')).toBeTruthy()
    expect(within(chart).getByText('10:00:15')).toBeTruthy()
  })

  it('ignores forward and backward changes to the browser wall clock', () => {
    render(<ServerTraffic serverId={1} />)
    emit()
    vi.setSystemTime(SERVER_TS + 12 * 60 * 60 * 1000)
    advance(6000)
    expectRates(true)

    vi.setSystemTime(SERVER_TS - 12 * 60 * 60 * 1000)
    advance(6000)
    expectRates(true)
    advance(3000)
    expectRates(false)
  })

  it('expires stale backlog immediately without refreshing its baseline with synthetic zero points', () => {
    vi.setSystemTime(SERVER_TS - 120_000)
    render(<ServerTraffic serverId={1} />)
    emit({ age_ms: 60_000 })
    expectRates(false)
    const chart = screen.getByRole('img', { name: '上传下载实时速度趋势图' })
    expect(within(chart).getByText('10:01:00')).toBeTruthy()
    const zeroLine = chart.querySelector('.tr-line-download')!.getAttribute('points')

    advance(3000)
    expectRates(false)
    expect(chart.querySelector('.tr-line-download')!.getAttribute('points')).toBe(zeroLine)

    emit({ ts: SERVER_TS + 63_000 })
    expectRates(true)
    advance(15_000)
    expectRates(false)
  })

  it.each([undefined, Number.NaN, -1])('starts freshness on receipt when server age is %s', (age) => {
    vi.setSystemTime(SERVER_TS + 120_000)
    render(<ServerTraffic serverId={1} />)
    emit({ age_ms: age })
    expectRates(true)
    advance(15_000)
    expectRates(false)
  })

  it('clears an unavailable sample immediately and accepts the next live sample', () => {
    render(<ServerTraffic serverId={1} />)
    emit()
    expectRates(true)
    emit({ data: null })
    expectRates(false)
    advance(3000)
    emit({ ts: SERVER_TS + 3000 })
    expectRates(true)
  })

  it('renders combined server load/memory and period traffic metrics and forwards traffic series to parent', async () => {
    const series = {
      available: true,
      range: '24h' as const,
      step_seconds: 3600,
      traffic_reset_day: 1,
      updated_at: '2026-10-25T10:00:00Z',
      upload: 1024,
      download: 2048,
      upload_rate: 0,
      download_rate: 0,
      tcp_connections: 0,
      udp_connections: 0,
      points: [],
      ports: [],
    }
    vi.mocked(getServerTraffic).mockResolvedValueOnce(series)
    const onDataChange = vi.fn()
    render(
      <ServerTraffic
        serverId={1}
        server={{ id: 1, name: 'Alpha', online: true, load1: 0.42, mem_used: 512 * 1024 * 1024, mem_total: 1024 * 1024 * 1024 } as any}
        onDataChange={onDataChange}
      />,
    )
    await act(async () => {})
    expect(onDataChange).toHaveBeenLastCalledWith(series)
    const periodCard = screen.getByText('区间流量').parentElement!
    expect(within(periodCard).getByText('3.00 KB')).toBeTruthy()
    expect(within(periodCard).getByText('↓ 2.00 KB · ↑ 1.00 KB')).toBeTruthy()
    expect(within(screen.getByText('负载').parentElement!).getByText('0.42')).toBeTruthy()
    const memCard = screen.getByText('内存').parentElement!
    expect(within(memCard).getByText('50%')).toBeTruthy()
    expect(within(memCard).getByText('512.00 MB / 1.00 GB')).toBeTruthy()
  })
})
