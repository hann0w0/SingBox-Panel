import { message } from 'antd'
import type { ConfigApplyResult } from './api'

export function isConfigApplyResult(value: unknown): value is ConfigApplyResult {
  return !!value && typeof value === 'object' && 'apply_state' in value
}

export function showConfigApplyResult(result: ConfigApplyResult) {
  switch (result.apply_state) {
    case 'applied':
      message.success('已保存并应用到节点')
      break
    case 'pending':
      message.info('已保存；节点当前离线，重连后会自动下发')
      break
    case 'failed':
      message.warning(`已保存，但节点下发失败：${result.apply_error || '未知错误'}`)
      break
  }
}
