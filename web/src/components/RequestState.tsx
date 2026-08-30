import type { ReactNode } from 'react'
import { Alert, Button, Empty, Spin } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'

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
        <Spin />
        <span>加载中…</span>
      </div>
    )
  }

  if (error && !hasData) {
    return (
      <div className="request-state">
        <Alert
          type="error"
          showIcon
          message="数据加载失败"
          description={error}
          action={onRetry ? <Button icon={<ReloadOutlined />} onClick={onRetry} aria-label="重试">重试</Button> : undefined}
        />
      </div>
    )
  }

  return (
    <>
      {error ? (
        <Alert
          className="request-state-stale"
          type="warning"
          showIcon
          message="刷新失败，当前显示的是上次成功获取的数据"
          description={error}
          action={onRetry ? <Button size="small" icon={<ReloadOutlined />} onClick={onRetry} aria-label="重试">重试</Button> : undefined}
        />
      ) : loading ? (
        <div className="request-state-refreshing" role="status" aria-live="polite">
          <Spin size="small" /> 正在刷新…
        </div>
      ) : null}
      {empty ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={emptyDescription} /> : children}
    </>
  )
}
