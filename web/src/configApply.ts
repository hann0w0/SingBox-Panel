import { message } from './antdHelper'
import { errMsg, type ConfigApplyResult } from './api'

export function isConfigApplyResult(value: unknown): value is ConfigApplyResult {
  if (!value || typeof value !== 'object' || !('apply_state' in value)) return false
  const state = (value as { apply_state?: unknown }).apply_state
  return state === 'applied' || state === 'pending' || state === 'failed'
}

// Render a config-apply outcome. When `key` is given the message replaces an
// existing toast in place (spinner → result), otherwise it shows a fresh one.
export function showConfigApplyResult(result: ConfigApplyResult, key?: string) {
  switch (result.apply_state) {
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

// Run a config-mutating action in the background behind a persistent "配置下发中"
// spinner toast that resolves in place to the apply result (or an error). Callers
// close their modal first, then await/detach this so the task runs in the
// background and the UI stays responsive. Errors are surfaced via the toast, not
// rethrown, so the returned promise always resolves.
export async function applyWithToast(
  key: string,
  action: () => Promise<unknown>,
  loading = '配置下发中',
): Promise<void> {
  message.loading({ content: loading, key, duration: 0 })
  try {
    const result = await action()
    if (isConfigApplyResult(result)) showConfigApplyResult(result, key)
    else message.success({ content: '操作完成', key })
  } catch (e) {
    message.error({ content: errMsg(e), key })
  }
}
