import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { listServers, serverLogs, streamLogs } from '../../../api'
import { useSSE } from '../../../useSSE'
import type { SSEMessage } from '../../../useSSE'
import Logs, { splitAddress } from './Logs'

vi.mock('../../../api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../../api')>(),
  listServers: vi.fn(), serverLogs: vi.fn(), streamLogs: vi.fn(),
}))
vi.mock('../../../useSSE', () => ({ useSSE: vi.fn(() => ({ connected: true })) }))

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => setTimeout(callback, 0))
  HTMLElement.prototype.scrollTo = vi.fn()
  vi.mocked(listServers).mockResolvedValue([{ id: 1, name: 'One', online: true } as Awaited<ReturnType<typeof listServers>>[number]])
  vi.mocked(serverLogs).mockResolvedValue('historical one\nhistorical two')
  vi.mocked(streamLogs).mockResolvedValue({ ok: true, output: '' })
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
