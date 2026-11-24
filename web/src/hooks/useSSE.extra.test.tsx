import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useSSE } from './useSSE'
import { useAuth } from '../lib/store'

describe('useSSE stream framing', () => {
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
    useAuth.setState({ token: 'token', user: null })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('parses CRLF-delimited SSE frames', async () => {
    const received: unknown[] = []
    const encoder = new TextEncoder()
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode('data: {"kind":"tick","ts":1,"data":{}}\r\n\r\n'))
        controller.close()
      },
    })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ status: 200, ok: true, body } as Response))
    const hook = renderHook(() => useSSE('/api/events', true, (msg) => received.push(msg)))
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(received).toEqual([{ kind: 'tick', ts: 1, data: {} }])
    hook.unmount()
  })
})
