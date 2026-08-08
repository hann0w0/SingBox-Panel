import { useEffect, useRef, useState, useCallback } from 'react'

export interface SSEMessage {
  kind: string
  ts: number
  data: unknown
}

// Reconnect backoff: start at 1s and double up to 30s, with a little jitter so
// several tabs don't all reconnect in lockstep.
const RECONNECT_BASE_MS = 1000
const RECONNECT_MAX_MS = 30_000

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

    const token = localStorage.getItem('singbox-panel_token')
    const controller = new AbortController()
    let reader: ReadableStreamDefaultReader<Uint8Array> | null = null
    let buffer = ''
    let retryTimer: ReturnType<typeof setTimeout> | null = null
    let attempts = 0
    let stopped = false

    const clearRetry = () => {
      if (retryTimer) {
        clearTimeout(retryTimer)
        retryTimer = null
      }
    }

    const scheduleReconnect = () => {
      if (stopped) return
      setConnected(false)
      // Pause reconnecting while the tab is hidden; the visibility listener
      // below kicks off a reconnect when it becomes visible again.
      if (document.hidden) return
      const delay = Math.min(RECONNECT_BASE_MS * 2 ** attempts, RECONNECT_MAX_MS)
      attempts += 1
      // jitter: ±40% so parallel tabs don't reconnect in lockstep
      const jitter = 0.6 + Math.random() * 0.8
      retryTimer = setTimeout(() => void connect(), delay * jitter)
    }

    const connect = async () => {
      if (stopped) return
      try {
        const resp = await fetch(url, {
          headers: token ? { Authorization: `Bearer ${token}` } : {},
          signal: controller.signal,
        })
        if (!resp.ok || !resp.body) {
          scheduleReconnect()
          return
        }
        // A successful (re)connect resets the backoff ladder.
        attempts = 0
        setConnected(true)
        reader = resp.body.getReader()
        const decoder = new TextDecoder()

        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
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
        // Stream ended cleanly (server closed) — treat as a drop and retry.
        scheduleReconnect()
      } catch (err) {
        // AbortError is expected on cleanup; ignore it.
        if (err instanceof DOMException && err.name === 'AbortError') return
        scheduleReconnect()
      }
    }

    const onVisibility = () => {
      if (!document.hidden && !connectedRef.current) {
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
      controller.abort()
      if (reader) reader.cancel().catch(() => {})
    }
  }, [url, enabled])

  // Keep the visibility handler honest about the current connection state.
  const connectedRef = useRef(connected)
  connectedRef.current = connected

  return { connected }
}
