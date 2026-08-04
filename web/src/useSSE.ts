import { useEffect, useRef, useState, useCallback } from 'react'

export interface SSEMessage {
  kind: string
  ts: number
  data: unknown
}

/**
 * useSSE connects to a Server-Sent Events endpoint using fetch + ReadableStream.
 * EventSource is not used because it cannot send custom Authorization headers,
 * which the panel's auth middleware requires.
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

    const connect = async () => {
      try {
        const resp = await fetch(url, {
          headers: token ? { Authorization: `Bearer ${token}` } : {},
          signal: controller.signal,
        })
        if (!resp.ok || !resp.body) {
          setConnected(false)
          return
        }
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
      } catch (err) {
        // AbortError is expected on cleanup; ignore it.
        if (err instanceof DOMException && err.name === 'AbortError') return
      } finally {
        setConnected(false)
      }
    }

    connect()

    return () => {
      controller.abort()
      if (reader) reader.cancel().catch(() => {})
    }
  }, [url, enabled])

  return { connected }
}
