import { useEffect, useState } from 'react'
import { getSubscriptionSync } from '../../lib/api'
import type { SubscriptionSyncResult } from '../../lib/api'
import { Alert, Button } from '../../components/kit'

const BASE_POLL_MS = 5000
const MAX_POLL_MS = 30000
const MAX_CONSECUTIVE_FAILURES = 6

export function SubscriptionSyncStatus({ initial }: { initial?: SubscriptionSyncResult }) {
  const [status, setStatus] = useState(initial)
  const [unavailable, setUnavailable] = useState(false)
  const [exhausted, setExhausted] = useState(false)
  const [retrySeq, setRetrySeq] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    let failures = 0
    let pausedForHidden = false
    let finished = false

    const clearTimer = () => {
      if (timer !== undefined) {
        clearTimeout(timer)
        timer = undefined
      }
    }

    const schedule = (delayMs: number) => {
      if (controller.signal.aborted || finished) return
      clearTimer()
      if (typeof document !== 'undefined' && document.hidden) {
        pausedForHidden = true
        return
      }
      timer = setTimeout(() => void poll(), delayMs)
    }

    const poll = async () => {
      if (controller.signal.aborted || finished) return
      clearTimer()
      try {
        const next = await getSubscriptionSync(controller.signal)
        if (controller.signal.aborted) return
        failures = 0
        setStatus(next)
        setUnavailable(false)
        setExhausted(false)
        if (next.apply_state === 'applied') {
          finished = true
          return
        }
        schedule(BASE_POLL_MS)
      } catch {
        if (controller.signal.aborted) return
        failures += 1
        setUnavailable(true)
        if (failures >= MAX_CONSECUTIVE_FAILURES) {
          setExhausted(true)
          return
        }
        const delay = Math.min(BASE_POLL_MS * (2 ** (failures - 1)), MAX_POLL_MS)
        schedule(delay)
      }
    }

    const onVisibility = () => {
      if (typeof document !== 'undefined' && !document.hidden && pausedForHidden && !finished) {
        pausedForHidden = false
        void poll()
      }
    }

    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', onVisibility)
    }
    void poll()
    return () => {
      finished = true
      controller.abort()
      clearTimer()
      if (typeof document !== 'undefined') {
        document.removeEventListener('visibilitychange', onVisibility)
      }
    }
  }, [retrySeq])

  if (!unavailable && (!status || status.apply_state === 'applied')) return null
  const failed = status?.apply_state === 'failed'
  const text = unavailable
    ? exhausted
      ? '多次检查节点同步状态均失败，已暂停自动重试；请稍后手动刷新确认。'
      : '暂时无法确认节点同步状态，将自动重试；请勿将重置链接视为节点已同步。'
    : failed
      ? `节点凭证同步失败（${status.failed_servers} 个失败，${status.pending_servers} 个待同步），后台将自动重试；旧凭证仍可能有效。`
      : `节点凭证同步中（${status?.pending_servers} 个节点），旧凭证在同步前仍可能有效。`
  return (
    <Alert
      className="mt-3"
      type={failed || unavailable ? 'warning' : 'info'}
      showIcon
      message={text}
      action={exhausted ? (
        <Button size="small" onClick={() => { setExhausted(false); setRetrySeq((n) => n + 1) }}>
          手动刷新
        </Button>
      ) : undefined}
    />
  )
}
