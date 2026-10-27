import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createServer, getOverview, getServersMeta, updateServer } from '../../../api'
import type { Server } from '../../../types'
import Servers from './Servers'
import { ServerOverviewCard } from './ServerOverviewCard'

vi.mock('../../../api', () => ({
  createServer: vi.fn(), updateServer: vi.fn(), getServersMeta: vi.fn(), getOverview: vi.fn(),
  errMsg: (error: Error) => error.message, isCanceledRequest: () => false,
}))

const server = {
  id: 2, name: 'Frankfurt', address: 'fra.example.com', remark: 'Backup exit',
  online: false, traffic_reset_day: 1,
} as Server
const metadata = (servers: Server[]) => ({ servers, latest_agent_version: 'v1.0.7' })

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  vi.mocked(getServersMeta).mockResolvedValue(metadata([server]))
  vi.mocked(getOverview).mockResolvedValue({ users_total: 0 } as Awaited<ReturnType<typeof getOverview>>)
  vi.mocked(updateServer).mockResolvedValue({ ok: true })
  vi.mocked(createServer).mockResolvedValue({ server, install_command: 'install-agent', public_url: '' })
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('server remarks', () => {
  it('never shows an inbound count in the server list', async () => {
    const serverWithInbounds = {
      ...server,
      inbounds: [
        { id: 1, tag: 'ss-main' },
        { id: 2, tag: 'snell-main' },
        { id: 3, tag: 'trojan-main' },
      ],
    } as Server
    vi.mocked(getServersMeta).mockResolvedValue(metadata([serverWithInbounds]))

    render(<MemoryRouter><Servers /></MemoryRouter>)
    const row = (await screen.findByText('Frankfurt')).closest('.srv-row')

    expect(row).toBeTruthy()
    expect(row?.textContent).not.toContain('入站')
  })

  it('shows a saved remark in the server list and submits edits, including clearing it', async () => {
    render(<MemoryRouter><Servers /></MemoryRouter>)
    expect((await screen.findByText('Backup exit')).getAttribute('title')).toBe('Backup exit')
    fireEvent.click(screen.getByRole('button', { name: '编辑 Frankfurt' }))
    expect((screen.getByLabelText('备注') as HTMLInputElement).value).toBe('Backup exit')
    fireEvent.change(screen.getByLabelText('备注'), { target: { value: '' } })
    vi.mocked(getServersMeta).mockResolvedValue(metadata([{ ...server, remark: '' }]))
    fireEvent.click(screen.getByRole('button', { name: '确定' }))
    await waitFor(() => expect(updateServer).toHaveBeenCalledWith(2, expect.objectContaining({ remark: '' })))
    await waitFor(() => expect(screen.queryByText('Backup exit')).toBeNull())
  })

  it('sends the remark when creating a server and displays the returned list entry', async () => {
    vi.mocked(getServersMeta).mockResolvedValue(metadata([]))
    render(<MemoryRouter><Servers /></MemoryRouter>)
    await screen.findByText('暂无服务器')
    fireEvent.click(screen.getByRole('button', { name: '新增服务器' }))
    fireEvent.change(screen.getByLabelText(/名称/), { target: { value: 'Frankfurt' } })
    fireEvent.change(screen.getByLabelText('备注'), { target: { value: 'Backup exit' } })
    vi.mocked(getServersMeta).mockResolvedValue(metadata([server]))
    fireEvent.click(screen.getByRole('button', { name: '确定' }))
    await waitFor(() => expect(createServer).toHaveBeenCalledWith(expect.objectContaining({ remark: 'Backup exit' })))
    await screen.findByText('Backup exit')
  })

  it('shows the remark in the detail heading without rendering blank remarks', () => {
    const props = {
      publicURL: '', busy: '', refreshing: false,
      onRefresh: vi.fn(), onInstallSingbox: vi.fn(), onUninstallSingbox: vi.fn(),
      onInstallAgent: vi.fn(), onUninstallAgent: vi.fn(), onImport: vi.fn(),
      onEditConfig: vi.fn(), onRestart: vi.fn(), onExport: vi.fn(),
    }
    const { container, rerender } = render(<ServerOverviewCard server={server} {...props} />)
    expect(screen.getByText('Backup exit').className).toBe('sd-remark')
    expect(screen.getByRole('button', { name: '导出节点' })).toBeTruthy()
    expect(screen.queryByText('导出订阅')).toBeNull()
    rerender(<ServerOverviewCard server={{ ...server, remark: '  ' }} {...props} />)
    expect(container.querySelector('.sd-remark')).toBeNull()
  })
})
