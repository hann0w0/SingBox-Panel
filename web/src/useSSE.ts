import { useEffect, useRef, useState, useCallback } from 'react'
import { useAuth } from './store'

export interface SSEMessage {
  kind: string
  ts: number
  data: unknown
}

// Reconnect backoff: start at 1s and double up to 30s, with a little jitter so
// several tabs don't all reconnect in lockstep.
const RECONNECT_BASE_MS = 1000
const RECONNECT_MAX_MS = 30_000

// Kept behind an object so the redirect can be verified without asking jsdom
// to perform a real navigation in unit tests.
export const sseAuth = {
  redirectToLogin() {
    if (!location.pathname.startsWith('/login')) location.href = '/login'
  },
}

// 统一使用 Zustand store 清理认证状态，与 api.ts 保持一致
function handleUnauthorized() {
  useAuth.getState().logout()
  sseAuth.redirectToLogin()
}

/**
 * useSSE connects to a Server-Sent Events endpoint using fetch + ReadableStream.
 * EventSource is not used because it cannot send custom Authorization headers,
 * which the panel's auth middleware requires.
 *
 * The connection automatically reconnects with exponential backoff after any
 * drop (network blip, panel restart, heartbeat timeout) so realtime pages do
 * not silently go stale until a manual refresh. Reconnecting is paused while
 * the document is hidden and resumes on visibility.
 *
 * @param url       Relative path to the SSE endpoint.
 * @param enabled   When false, no connection is made (and any existing one is closed).
 * @param onMessage Callback invoked for each parsed SSE message.
 */
export function useSSE(
  url: string,
  enabled: boolean,
  onMessage: (msg: SSEMessage) => void,
): { connected: boolean } {
  const [connected, setConnected] = useState(false)
  const cbRef = useRef(onMessage)
  cbRef.current = onMessage

  useEffect(() => {
    if (!enabled) {
      setConnected(false)
      return
    }

    let controller: AbortController | null = null
    let reader: ReadableStreamDefaultReader<Uint8Array> | null = null
    let retryTimer: ReturnType<typeof setTimeout> | null = null
    let attempts = 0
    let stopped = false
    let generation = 0
    let needsReconnect = false

    const clearRetry = () => {
      if (retryTimer) {
        clearTimeout(retryTimer)
        retryTimer = null
      }
    }

    const closeActiveConnection = () => {
      generation += 1
      const activeController = controller
      const activeReader = reader
      controller = null
      reader = null
      activeController?.abort()
      if (activeReader) void activeReader.cancel().catch(() => {})
    }

    const scheduleReconnect = () => {
      if (stopped || controller || retryTimer) return
      setConnected(false)
      // Pause reconnecting while the tab is hidden; the visibility listener
      // below kicks off a reconnect when it becomes visible again.
      if (document.hidden) {
        needsReconnect = true
        return
      }
      needsReconnect = false
      const delay = Math.min(RECONNECT_BASE_MS * 2 ** attempts, RECONNECT_MAX_MS)
      attempts += 1
      // jitter: ±40% so parallel tabs don't reconnect in lockstep
      const jitter = 0.6 + Math.random() * 0.8
      retryTimer = setTimeout(() => {
        retryTimer = null
        void connect()
      }, delay * jitter)
    }

    const connect = async () => {
      if (stopped || document.hidden || controller) return
      clearRetry()
      needsReconnect = false
      const connectionGeneration = ++generation
      const connectionController = new AbortController()
      controller = connectionController
      let shouldReconnect = true
      let buffer = ''
      try {
        // 从 Zustand 内存读取认证 Token
        const token = useAuth.getState().token
        const resp = await fetch(url, {
          headers: token ? { Authorization: `Bearer ${token}` } : {},
          signal: connectionController.signal,
        })
        if (stopped || connectionGeneration !== generation) return
        if (resp.status === 401) {
          shouldReconnect = false
          stopped = true
          handleUnauthorized()
          return
        }
        if (!resp.ok || !resp.body) {
          return
        }
        // A successful (re)connect resets the backoff ladder.
        attempts = 0
        setConnected(true)
        const connectionReader = resp.body.getReader()
        reader = connectionReader
        const decoder = new TextDecoder()

        for (;;) {
          const { done, value } = await connectionReader.read()
          if (done) break
          if (stopped || connectionGeneration !== generation) return
          buffer += decoder.decode(value, { stream: true })
          // SSE frames are separated by \n\n
          const frames = buffer.split('\n\n')
          buffer = frames.pop() ?? ''
          for (const frame of frames) {
            for (const line of frame.split('\n')) {
              if (line.startsWith('data: ')) {
                const raw = line.slice(6)
                try {
                  const msg = JSON.parse(raw) as SSEMessage
                  cbRef.current(msg)
                } catch {
                  // ignore malformed JSON
                }
              }
              // Lines starting with ':' are heartbeat comments — ignore.
            }
          }
        }
      } catch (err) {
        // AbortError is expected on cleanup; ignore it.
        if (err instanceof DOMException && err.name === 'AbortError') shouldReconnect = false
      } finally {
        if (connectionGeneration === generation) {
          controller = null
          reader = null
          setConnected(false)
          if (shouldReconnect) scheduleReconnect()
        }
      }
    }

    const onVisibility = () => {
      if (!document.hidden && (needsReconnect || (!controller && !retryTimer))) {
        clearRetry()
        void connect()
      }
    }

    connect()
    document.addEventListener('visibilitychange', onVisibility)

    return () => {
      stopped = true
      clearRetry()
      document.removeEventListener('visibilitychange', onVisibility)
      closeActiveConnection()
    }
  }, [url, enabled])

  return { connected }
}
