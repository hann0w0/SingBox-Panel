import type { MaintenanceInfo } from './api'

const versionOf = (version: string) => version.replace(/^v/, '')

export function updateFailureMessage(fresh: MaintenanceInfo, operationID?: string): string | null {
  if (!operationID || fresh.update_operation?.id !== operationID) return null
  if (fresh.update_operation.status === 'rolled_back') return '面板更新失败，已恢复更新前的版本'
  if (fresh.update_operation.status === 'failed') return '面板更新失败，请检查服务状态后重试'
  return null
}

export function updateRestartCompleted(before: MaintenanceInfo, fresh: MaintenanceInfo, target: string, operationID?: string): boolean {
  if (operationID && (fresh.update_operation?.id !== operationID || fresh.update_operation.status !== 'succeeded')) return false
  if (versionOf(fresh.current_version) !== versionOf(target)) return false
  if (before.instance_id) return !!fresh.instance_id && before.instance_id !== fresh.instance_id
  // Older panel versions do not report a process identity. Only a changed
  // version can establish that their normal upgrade has actually completed.
  return versionOf(before.current_version) !== versionOf(fresh.current_version)
}
