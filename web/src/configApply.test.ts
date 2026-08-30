import { beforeEach, describe, expect, it, vi } from 'vitest'

const message = vi.hoisted(() => ({
  success: vi.fn(),
  info: vi.fn(),
  warning: vi.fn(),
  error: vi.fn(),
  loading: vi.fn(),
}))

vi.mock('./antdHelper', () => ({ message }))
vi.mock('antd', () => ({ message }))

import { isConfigApplyResult, showConfigApplyResult } from './configApply'

describe('configuration apply results', () => {
  beforeEach(() => vi.clearAllMocks())

  it('accepts only supported apply states', () => {
    expect(isConfigApplyResult({ apply_state: 'applied' })).toBe(true)
    expect(isConfigApplyResult({ apply_state: 'pending' })).toBe(true)
    expect(isConfigApplyResult({ apply_state: 'failed', apply_error: 'offline' })).toBe(true)
    expect(isConfigApplyResult({ apply_state: 'unknown' })).toBe(false)
    expect(isConfigApplyResult({})).toBe(false)
  })

  it('surfaces a failed node apply instead of reporting a generic success', () => {
    showConfigApplyResult({ apply_state: 'failed', apply_error: 'agent unavailable' }, 'ruleset')

    expect(message.warning).toHaveBeenCalledWith({
      content: '已保存，但节点下发失败：agent unavailable',
      key: 'ruleset',
    })
    expect(message.success).not.toHaveBeenCalled()
  })
})
