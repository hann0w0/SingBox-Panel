import { message } from '../components/kit/feedback'
import { errMsg, type ConfigApplyResult } from './api'

export function isConfigApplyResult(value: unknown): value is ConfigApplyResult {
  if (!value || typeof value !== 'object' || !('apply_state' in value)) return false
  const state = (value as { apply_state?: unknown }).apply_state
  return state === 'applied' || state === 'pending' || state === 'failed' || state === 'staged'
}

// Render a config-apply outcome. When `key` is given the message replaces an
// existing toast in place (spinner → result), otherwise it shows a fresh one.
export function showConfigApplyResult(result: ConfigApplyResult, key?: string) {
  switch (result.apply_state) {
    case 'staged':
      message.success({ content: '已暂存更改，点击「应用配置」后下发到节点', key })
      break
    case 'applied':
      message.success({ content: '已保存并应用到节点', key })
      break
    case 'pending':
      message.info({ content: '已保存；节点当前离线，重连后会自动下发', key })
      break
    case 'failed':
      message.warning({ content: `已保存，但节点下发失败：${result.apply_error || '未知错误'}`, key })
      break
  }
}

// Resolve the persistent toast and report whether the panel saved the change.
// A pending/failed Agent apply still means the database write succeeded.
// HTTP failures return false so editors can retain their draft for retry;
// detached callers still receive their error toast without an unhandled reject.
export async function applyWithToast(
  key: string,
  action: () => Promise<unknown>,
  loading = '配置下发中',
): Promise<boolean> {
  message.loading({ content: loading, key, duration: 0 })
  try {
    const result = await action()
    if (isConfigApplyResult(result)) showConfigApplyResult(result, key)
    else message.success({ content: '操作完成', key })
    return true
  } catch (e) {
    message.error({ content: errMsg(e), key })
    return false
  }
}
