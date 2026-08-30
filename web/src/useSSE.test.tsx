import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { sseAuth, useSSE } from './useSSE'
import { useAuth } from './store'
import type { User } from './types'

describe('useSSE', () => {
  beforeEach(() => {
    const values = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, String(value)),
      removeItem: (key: string) => values.delete(key),
      clear: () => values.clear(),
      key: (index: number) => [...values.keys()][index] ?? null,
      get length() { return values.size },
    } satisfies Storage)
    vi.stubGlobal('fetch', vi.fn())
    useAuth.setState({ token: null, user: null })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('clears authentication and never retries a 401 response', async () => {
    vi.useFakeTimers()
    useAuth.getState().setAuth('expired', { id: 1, username: 'admin', is_admin: true } as unknown as User)
    const redirect = vi.spyOn(sseAuth, 'redirectToLogin').mockImplementation(() => {})
    const fetchMock = vi.mocked(fetch)
    fetchMock.mockResolvedValue({ status: 401, ok: false, body: null } as Response)

    const hook = renderHook(() => useSSE('/api/events', true, vi.fn()))
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(useAuth.getState().token).toBeNull()
    expect(useAuth.getState().user).toBeNull()
    expect(redirect).toHaveBeenCalledOnce()

    await act(async () => {
      await vi.runAllTimersAsync()
    })
    expect(fetchMock).toHaveBeenCalledOnce()
    hook.unmount()
    vi.useRealTimers()
  })

  it('does not open a second connection on a visibility event while connecting', async () => {
    const fetchMock = vi.mocked(fetch)
    fetchMock.mockImplementation((_input, init) => new Promise((_resolve, reject) => {
      init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    }))

    const hook = renderHook(() => useSSE('/api/events', true, vi.fn()))
    await act(async () => Promise.resolve())
    expect(fetchMock).toHaveBeenCalledOnce()

    act(() => document.dispatchEvent(new Event('visibilitychange')))
    expect(fetchMock).toHaveBeenCalledOnce()

    hook.unmount()
  })

  it('reconnects when a hidden-page connection closes before React state settles', async () => {
    const fetchMock = vi.mocked(fetch)
    let finishFirst: (() => void) | undefined
    fetchMock
      .mockResolvedValueOnce({
        status: 200,
        ok: true,
        body: new ReadableStream<Uint8Array>({
          start(controller) {
            finishFirst = () => controller.close()
          },
        }),
      } as Response)
      .mockImplementation((_input, init) => new Promise((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
      }))

    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    const hook = renderHook(() => useSSE('/api/events', true, vi.fn()))
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(fetchMock).toHaveBeenCalledOnce()

    hidden.mockReturnValue(true)
    await act(async () => {
      finishFirst?.()
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(fetchMock).toHaveBeenCalledOnce()

    hidden.mockReturnValue(false)
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
      await Promise.resolve()
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    hook.unmount()
  })
})
