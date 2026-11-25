import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DialogHost } from '../../../components/kit'
import Settings, { compareVersions } from './Settings'
import {
  getMaintenanceInfo,
  getOneDriveStatus,
  listPanelReleases,
  restoreBackup,
} from '../../../lib/api'

vi.mock('../../../lib/api', () => ({
  getMaintenanceInfo: vi.fn(),
  getOneDriveStatus: vi.fn(),
  listPanelReleases: vi.fn(),
  downloadBackup: vi.fn(),
  restoreBackup: vi.fn(),
  selfUpdate: vi.fn(),
  startOneDriveAuth: vi.fn(),
  pollOneDriveAuth: vi.fn(),
  syncOneDriveBackup: vi.fn(),
  downloadOneDriveBackup: vi.fn(),
  restoreOneDriveBackup: vi.fn(),
  deleteOneDriveBackup: vi.fn(),
  isCanceledRequest: () => false,
  errMsg: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))

beforeEach(() => {
  vi.stubGlobal('matchMedia', () => ({
    matches: false,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  vi.mocked(getMaintenanceInfo).mockResolvedValue({
    current_version: 'v1.0.17',
    latest_version: 'v1.0.17',
    has_update: false,
    update_supported: true,
    deployment: 'binary',
    db_driver: 'sqlite',
    uptime_seconds: 120,
  } as never)
  vi.mocked(getOneDriveStatus).mockResolvedValue({
    connected: false,
    files: [],
  } as never)
  vi.mocked(listPanelReleases).mockResolvedValue([])
  vi.mocked(restoreBackup).mockResolvedValue({ ok: true, message: 'ok', restarting: false } as never)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

describe('Settings', () => {
  it('selects, removes, and submits a local backup archive in the restore view', async () => {
    render(<><Settings /><DialogHost /></>)

    fireEvent.click(screen.getByRole('tab', { name: '恢复' }))
    const restoreBtn = screen.getByRole('button', { name: '恢复并重启' }) as HTMLButtonElement
    expect(restoreBtn.disabled).toBe(true)

    const file = new File(['archive-bytes'], 'panel-backup.tar.gz', { type: 'application/gzip' })
    const input = screen.getByLabelText('选择备份文件')
    fireEvent.change(input, { target: { files: [file] } })

    expect(await screen.findByText('panel-backup.tar.gz')).toBeTruthy()
    expect(restoreBtn.disabled).toBe(false)

    // Remove disables restore again
    fireEvent.click(screen.getByRole('button', { name: '移除文件' }))
    expect(screen.queryByText('panel-backup.tar.gz')).toBeNull()
    expect(restoreBtn.disabled).toBe(true)

    // Re-select via file input and confirm restore
    fireEvent.change(input, { target: { files: [file] } })
    expect(await screen.findByText('panel-backup.tar.gz')).toBeTruthy()
    fireEvent.click(restoreBtn)
    fireEvent.click(await screen.findByRole('button', { name: '确认恢复' }))

    await waitFor(() => expect(restoreBackup).toHaveBeenCalledWith(file))
  })

  it('shows error state with retry button when version info fails to load', async () => {
    vi.mocked(getMaintenanceInfo).mockRejectedValueOnce(new Error('network down'))

    render(<Settings />)

    expect(await screen.findByText('加载失败')).toBeTruthy()
    expect(screen.getByText('network down')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    expect((await screen.findAllByText('v1.0.17')).length).toBeGreaterThan(0)
  })

  it('orders pre-release versions before final releases without breaking edge tags', () => {
    expect(compareVersions('v1.0.18', 'v1.0.18-rc.1')).toBe(1)
    expect(compareVersions('v1.0.18-rc.1', 'v1.0.18')).toBe(-1)
    expect(compareVersions('v1.0.18-rc.10', 'v1.0.18-rc.2')).toBe(1)
    expect(compareVersions('v1.0.18-edge.a38152651ae5', 'v1.0.18')).toBe(0)
    expect(compareVersions('dev', 'v1.0.18')).toBe(0)
  })
})
