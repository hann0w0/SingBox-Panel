import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getSubscriptionSync } from '../../api'
import { SubscriptionSyncStatus } from './SubscriptionSyncStatus'

vi.mock('../../api', () => ({ getSubscriptionSync: vi.fn() }))

beforeEach(() => { vi.useFakeTimers(); vi.clearAllMocks() })
afterEach(() => { cleanup(); vi.useRealTimers() })

describe('subscription credential synchronization', () => {
  it('shows pending and failed states, retries, and clears only after application', async () => {
    vi.mocked(getSubscriptionSync)
      .mockResolvedValueOnce({ apply_state: 'pending', pending_servers: 2, failed_servers: 0 })
      .mockResolvedValueOnce({ apply_state: 'failed', pending_servers: 2, failed_servers: 1 })
      .mockResolvedValueOnce({ apply_state: 'applied', pending_servers: 0, failed_servers: 0 })
    await act(async () => { render(<SubscriptionSyncStatus />) })
    expect(screen.getByRole('alert').textContent).toContain('同步中（2 个节点）')
    expect(screen.getByRole('alert').textContent).toContain('旧凭证')
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    expect(screen.getByRole('alert').textContent).toContain('1 个失败，2 个待同步')
    expect(screen.getByRole('alert').textContent).toContain('后台将自动重试')
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    expect(screen.queryByRole('alert')).toBeNull()
    await act(async () => { await vi.advanceTimersByTimeAsync(10000) })
    expect(getSubscriptionSync).toHaveBeenCalledTimes(3)
  })

  it('does not treat a failed status request as successful synchronization', async () => {
    vi.mocked(getSubscriptionSync)
      .mockRejectedValueOnce(new Error('temporary network error'))
      .mockResolvedValueOnce({ apply_state: 'pending', pending_servers: 1, failed_servers: 0 })
    await act(async () => { render(<SubscriptionSyncStatus />) })
    expect(screen.getByRole('alert').textContent).toContain('暂时无法确认')
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    expect(screen.getByRole('alert').textContent).toContain('同步中（1 个节点）')
  })

  it('aborts a stale request and stops polling when unmounted', async () => {
    let resolve!: (value: Awaited<ReturnType<typeof getSubscriptionSync>>) => void
    vi.mocked(getSubscriptionSync).mockReturnValueOnce(new Promise((done) => { resolve = done }))
    const { unmount } = render(<SubscriptionSyncStatus initial={{ apply_state: 'pending', pending_servers: 1, failed_servers: 0 }} />)
    const signal = vi.mocked(getSubscriptionSync).mock.calls[0][0]!
    expect(screen.getByRole('alert').textContent).toContain('同步中')
    unmount()
    expect(signal.aborted).toBe(true)
    await act(async () => {
      resolve({ apply_state: 'failed', pending_servers: 1, failed_servers: 1 })
      await vi.advanceTimersByTimeAsync(10000)
    })
    expect(getSubscriptionSync).toHaveBeenCalledTimes(1)
  })
})
