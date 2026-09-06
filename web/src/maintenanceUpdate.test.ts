import { describe, expect, it } from 'vitest'
import type { MaintenanceInfo } from './api'
import { updateFailureMessage, updateRestartCompleted } from './maintenanceUpdate'

const before: MaintenanceInfo = { current_version: 'v1.0.0', instance_id: 'old-process', update_supported: true, db_driver: 'sqlite', uptime_seconds: 1 }

describe('updateRestartCompleted', () => {
  it('does not report same-version reinstall complete while the old process responds', () => {
    expect(updateRestartCompleted(before, { ...before, uptime_seconds: 10 }, 'v1.0.0')).toBe(false)
  })

  it('recognizes same-version restart even if polling missed the downtime', () => {
    expect(updateRestartCompleted(before, { ...before, instance_id: 'new-process' }, '1.0.0')).toBe(true)
  })

  it('requires the requested version as well as a new process', () => {
    expect(updateRestartCompleted(before, { ...before, instance_id: 'rollback-process' }, 'v1.0.1')).toBe(false)
    expect(updateRestartCompleted(before, { ...before, instance_id: 'new-process', current_version: 'v1.0.1' }, 'v1.0.1')).toBe(true)
  })

  it('supports old-panel upgrades but cannot mistake absent identity for a same-version restart', () => {
    const oldPanel = { ...before, instance_id: undefined }
    expect(updateRestartCompleted(oldPanel, oldPanel, 'v1.0.0')).toBe(false)
    expect(updateRestartCompleted(oldPanel, { ...oldPanel, current_version: 'v1.0.1' }, 'v1.0.1')).toBe(true)
  })

  it('waits for its own successful operation and rejects a same-version rollback', () => {
    const restarted = { ...before, instance_id: 'new-process' }
    expect(updateRestartCompleted(before, { ...restarted, update_operation: { id: 'ours', status: 'running' } }, 'v1.0.0', 'ours')).toBe(false)
    expect(updateRestartCompleted(before, { ...restarted, update_operation: { id: 'other', status: 'succeeded' } }, 'v1.0.0', 'ours')).toBe(false)
    expect(updateRestartCompleted(before, { ...restarted, update_operation: { id: 'ours', status: 'rolled_back' } }, 'v1.0.0', 'ours')).toBe(false)
    expect(updateRestartCompleted(before, { ...restarted, update_operation: { id: 'ours', status: 'succeeded' } }, 'v1.0.0', 'ours')).toBe(true)
  })

  it('surfaces rollback and failure only for the active update', () => {
    expect(updateFailureMessage({ ...before, update_operation: { id: 'ours', status: 'rolled_back' } }, 'ours')).toContain('已恢复')
    expect(updateFailureMessage({ ...before, update_operation: { id: 'ours', status: 'failed' } }, 'ours')).toContain('失败')
    expect(updateFailureMessage({ ...before, update_operation: { id: 'other', status: 'failed' } }, 'ours')).toBeNull()
  })
})
