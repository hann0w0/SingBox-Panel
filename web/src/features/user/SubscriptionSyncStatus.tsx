import { useEffect, useState } from 'react'
import { getSubscriptionSync } from '../../api'
import type { SubscriptionSyncResult } from '../../api'
import { Alert } from '../../ui'

export function SubscriptionSyncStatus({ initial }: { initial?: SubscriptionSyncResult }) {
  const [status, setStatus] = useState(initial)
  const [unavailable, setUnavailable] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    const poll = async () => {
      let retry = true
      try {
        const next = await getSubscriptionSync(controller.signal)
        if (controller.signal.aborted) return
        setStatus(next)
        setUnavailable(false)
        retry = next.apply_state !== 'applied'
      } catch {
        if (!controller.signal.aborted) setUnavailable(true)
      } finally {
        if (!controller.signal.aborted && retry) timer = setTimeout(() => void poll(), 5000)
      }
    }
    void poll()
    return () => {
      controller.abort()
      clearTimeout(timer)
    }
  }, [])

  if (!unavailable && (!status || status.apply_state === 'applied')) return null
  const failed = status?.apply_state === 'failed'
  const text = unavailable
    ? '暂时无法确认节点同步状态，将自动重试；请勿将重置链接视为节点已同步。'
    : failed
      ? `节点凭证同步失败（${status.failed_servers} 个失败，${status.pending_servers} 个待同步），后台将自动重试；旧凭证仍可能有效。`
      : `节点凭证同步中（${status?.pending_servers} 个节点），旧凭证在同步前仍可能有效。`
  return <Alert className="mt-3" type={failed || unavailable ? 'warning' : 'info'} showIcon message={text} />
}
