import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { RequestState } from './RequestState'

describe('RequestState', () => {
  it('shows a persistent retry action when initial loading fails', () => {
    const retry = vi.fn()
    render(
      <RequestState loading={false} error="network down" hasData={false} onRetry={retry}>
        <div>content</div>
      </RequestState>,
    )
    expect(screen.getByRole('alert').textContent).toContain('network down')
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    expect(retry).toHaveBeenCalledOnce()
  })

  it('retains last successful content and marks it stale after a refresh failure', () => {
    render(
      <RequestState loading={false} error="timeout" hasData>
        <div>last good data</div>
      </RequestState>,
    )
    expect(screen.getByText('last good data')).toBeTruthy()
    expect(screen.getByText('刷新失败，当前显示的是上次成功获取的数据')).toBeTruthy()
  })
})
