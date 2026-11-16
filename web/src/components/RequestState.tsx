import type { ReactNode } from 'react'
import { Alert, Button, Spin } from './kit'
import { RotateCw } from 'lucide-react'
import '../styles/settings-user.css'

function Message({ title, detail }: { title: string; detail?: ReactNode }) {
  return <span className="request-state-msg"><b>{title}</b>{detail ? <span>{detail}</span> : null}</span>
}

export function RequestState({
  loading,
  error,
  hasData,
  empty = false,
  emptyDescription = '暂无数据',
  onRetry,
  children,
}: {
  loading: boolean
  error?: string | null
  hasData: boolean
  empty?: boolean
  emptyDescription?: ReactNode
  onRetry?: () => void
  children: ReactNode
}) {
  if (loading && !hasData) {
    return (
      <div className="request-state request-state-loading" role="status" aria-label="加载中">
        <Spin size="small" />
      </div>
    )
  }

  const retry = onRetry
    ? <Button size="small" type="text" icon={<RotateCw />} onClick={onRetry} aria-label="重试">重试</Button>
    : undefined

  if (error && !hasData) {
    return (
      <div className="request-state">
        <Alert className="request-state-alert" type="error" showIcon message={<Message title="加载失败" detail={error} />} action={retry} />
      </div>
    )
  }

  return (
    <>
      {error ? (
        <Alert className="request-state-alert request-state-stale" type="warning" showIcon message={<Message title="刷新失败" detail={error} />} action={retry} />
      ) : loading ? (
        <div className="request-state-refreshing" role="status" aria-live="polite" aria-label="正在刷新">
          <i />
        </div>
      ) : null}
      {empty ? <div className="request-state-empty ui-empty">{emptyDescription}</div> : children}
    </>
  )
}
