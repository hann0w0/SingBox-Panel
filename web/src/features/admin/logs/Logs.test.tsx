import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { listServers, serverLogs, setServerLogLevel, streamLogs } from '../../../lib/api'
import { copyToClipboard } from '../../../lib/util'
import { useSSE } from '../../../hooks/useSSE'
import type { SSEMessage } from '../../../hooks/useSSE'
import Logs, { splitAddress } from './Logs'

vi.mock('../../../lib/api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../../lib/api')>(),
  listServers: vi.fn(), serverLogs: vi.fn(), setServerLogLevel: vi.fn(), streamLogs: vi.fn(),
}))
vi.mock('../../../lib/util', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../../lib/util')>(),
  copyToClipboard: vi.fn(),
}))
vi.mock('../../../hooks/useSSE', () => ({ useSSE: vi.fn(() => ({ connected: true })) }))

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => setTimeout(callback, 0))
  HTMLElement.prototype.scrollTo = vi.fn()
  vi.mocked(listServers).mockResolvedValue([{ id: 1, name: 'One', online: true, log_level: 'info' } as Awaited<ReturnType<typeof listServers>>[number]])
  vi.mocked(serverLogs).mockResolvedValue('historical one\nhistorical two')
  vi.mocked(setServerLogLevel).mockResolvedValue({ ok: true, log_level: 'debug', apply_state: 'applied' })
  vi.mocked(streamLogs).mockResolvedValue({ ok: true, output: '' })
  vi.mocked(copyToClipboard).mockResolvedValue(undefined)
})
afterEach(() => { cleanup(); Reflect.deleteProperty(HTMLElement.prototype, 'scrollTo'); vi.unstubAllGlobals() })

it('splits a log message into its address column and remaining detail', () => {
  expect(splitAddress('connection to example.com:443')).toEqual({ address: 'example.com:443', detail: '' })
  expect(splitAddress('connection from 10.0.0.5:51234')).toEqual({ address: '10.0.0.5:51234', detail: '' })
  // The sentence's own full stop must not be read as part of the host.
  expect(splitAddress('exchanged A api.telegram.org. UDP 149.154.167.220:53 in 84ms')).toEqual({
    address: 'api.telegram.org',
    detail: 'exchanged A UDP 149.154.167.220:53 in 84ms',
  })
  expect(splitAddress('connection reset by peer')).toEqual({ address: '', detail: 'connection reset by peer' })
})

it('displays the initial snapshot once when the live follower replays it', async () => {
  render(<MemoryRouter initialEntries={['/admin/logs?server=1']}><Logs /></MemoryRouter>)
  await waitFor(() => expect(streamLogs).toHaveBeenCalledWith(1, expect.objectContaining({ enable: true, lines: 200 })))
  const callback = vi.mocked(useSSE).mock.lastCall?.[2]
  expect(callback).toBeDefined()
  const send = (line: string) => callback?.({ kind: 'log', ts: Date.now(), data: { level: 'info', msg: line } } as SSEMessage)
  await act(async () => { send('historical one'); send('historical two'); send('fresh line') })
  await waitFor(() => expect(screen.getByText(/3 \/ 3 行/)).toBeTruthy())
  expect(screen.queryByText(/4 \/ 4 行/)).toBeNull()
})

it('buffers events until the start request tells whether this tab joined an existing stream', async () => {
  let finish!: (value: { ok: boolean; output: string }) => void
  vi.mocked(streamLogs).mockImplementation((_id, body) => body.enable
    ? new Promise((resolve) => { finish = resolve })
    : Promise.resolve({ ok: true, output: '' }))
  render(<MemoryRouter initialEntries={['/admin/logs?server=1']}><Logs /></MemoryRouter>)
  await waitFor(() => expect(streamLogs).toHaveBeenCalledWith(1, expect.objectContaining({ enable: true })))
  const callback = vi.mocked(useSSE).mock.lastCall?.[2]
  await act(async () => callback?.({ kind: 'log', ts: Date.now(), data: { level: 'info', msg: 'fresh line' } }))
  expect(screen.getByText(/2 \/ 2 行/)).toBeTruthy()
  await act(async () => finish({ ok: true, output: 'joined existing log stream' }))
  await waitFor(() => expect(screen.getByText(/3 \/ 3 行/)).toBeTruthy())
})

it('removes local level/direction/noise filters and switches server sing-box log level directly', async () => {
  const onLogLevelChange = vi.fn()
  render(
    <MemoryRouter>
      <Logs serverId={1} serverOnline serverLogLevel="info" onLogLevelChange={onLogLevelChange} embedded />
    </MemoryRouter>,
  )
  await waitFor(() => expect(serverLogs).toHaveBeenCalledWith(1, 200, expect.any(AbortSignal)))

  expect(screen.queryByText('全部级别')).toBeNull()
  expect(screen.queryByText('全部方向')).toBeNull()
  expect(screen.queryByText('过滤噪音')).toBeNull()

  // Search filter still works
  fireEvent.change(screen.getByLabelText('搜索日志'), { target: { value: 'two' } })
  await waitFor(() => expect(screen.getByText(/1 \/ 2 行/)).toBeTruthy())

  // Open server log level dropdown and pick debug
  const trigger = screen.getByRole('button', { name: 'sing-box 日志级别' })
  expect(trigger.textContent).toContain('info')
  fireEvent.keyDown(trigger, { key: 'Enter' })
  fireEvent.click(await screen.findByRole('menuitem', { name: 'debug' }))

  await waitFor(() => expect(setServerLogLevel).toHaveBeenCalledWith(1, 'debug'))
  expect(onLogLevelChange).toHaveBeenCalledWith('debug')
})

it('renders error details in a horizontally scrollable cell and copies error text directly', async () => {
  const normalRaw = '13:12:55 INFO [123 0ms] outbound/shadowsocks[BigLobe]: outbound connection to 149.154.171.255:443'
  const errorRaw = '13:12:56 ERROR [124 3001ms] outbound/shadowsocks[BigLobe]: process connection from 10.100.129.127:51285: dial tcp 149.154.171.255:443: i/o timeout while waiting for TLS handshake'
  vi.mocked(serverLogs).mockResolvedValue(`${normalRaw}\n${errorRaw}`)

  const { container } = render(
    <MemoryRouter>
      <Logs serverId={1} serverOnline serverLogLevel="info" embedded />
    </MemoryRouter>,
  )

  const copyErrorButtons = await screen.findAllByRole('button', { name: '复制报错信息' })
  expect(copyErrorButtons).toHaveLength(1)
  expect(container.querySelector('.lg-error-scroll.is-error')).toBeTruthy()

  fireEvent.click(copyErrorButtons[0])
  await waitFor(() => {
    expect(copyToClipboard).toHaveBeenCalledWith(
      'process connection from 10.100.129.127:51285: dial tcp 149.154.171.255:443: i/o timeout while waiting for TLS handshake',
    )
  })
  expect(copyErrorButtons[0].textContent).toContain('已复制')

  // Full-line copy button still copies the complete raw log line.
  const copyLineButtons = screen.getAllByRole('button', { name: '复制该行日志' })
  fireEvent.click(copyLineButtons[0])
  await waitFor(() => expect(copyToClipboard).toHaveBeenCalledWith(errorRaw))
})
