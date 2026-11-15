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
  // jsdom only provides localStorage when node is given --localstorage-file,
  // while CI has one. Install a real in-memory store so the address-visibility
  // toggle cannot leak between tests in either environment.
  const values = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, String(value)) },
    removeItem: (key: string) => { values.delete(key) },
    clear: () => values.clear(),
    key: (index: number) => [...values.keys()][index] ?? null,
    get length() { return values.size },
  })
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  vi.mocked(getServersMeta).mockResolvedValue(metadata([server]))
  vi.mocked(getOverview).mockResolvedValue({ users_total: 0 } as Awaited<ReturnType<typeof getOverview>>)
  vi.mocked(updateServer).mockResolvedValue({ ok: true })
  vi.mocked(createServer).mockResolvedValue({ server, install_command: 'install-agent', public_url: '' })
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('server remarks', () => {
  it('shows the reported IP, never the export-only domain, and can hide it', async () => {
    vi.mocked(getServersMeta).mockResolvedValue(metadata([{ ...server, public_ip: '203.0.113.7' }]))
    render(<MemoryRouter><Servers /></MemoryRouter>)
    const row = (await screen.findByText('Frankfurt')).closest('.srv-row')
    expect(row?.textContent).toContain('203.0.113.7')
    expect(row?.textContent).not.toContain('fra.example.com')
    fireEvent.click(screen.getByRole('button', { name: '隐藏服务器地址' }))
    expect(row?.textContent).toContain('203.*.*.7')
    expect(row?.textContent).not.toContain('203.0.113.7')
    fireEvent.click(screen.getByRole('button', { name: '显示服务器地址' }))
    expect(row?.textContent).toContain('203.0.113.7')
  })

  it('shows a placeholder when the agent has not reported an address yet', async () => {
    vi.mocked(getServersMeta).mockResolvedValue(metadata([{ ...server, address: 'node.example.com', public_ip: '', public_ipv6: '' }]))
    render(<MemoryRouter><Servers /></MemoryRouter>)
    const row = (await screen.findByText('Frankfurt')).closest('.srv-row')
    expect(row?.textContent).toContain('未设置地址')
    expect(row?.textContent).not.toContain('node.example.com')
  })

  it('shows both public addresses for a dual-stack server without a domain', async () => {
    vi.mocked(getServersMeta).mockResolvedValue(metadata([{
      ...server, address: '', public_ip: '133.200.223.98', public_ipv6: '2404:7a80:df62:8604::fde',
      public_ipv4_updated_at: new Date().toISOString(), public_ipv6_updated_at: new Date().toISOString(),
    }]))
    render(<MemoryRouter><Servers /></MemoryRouter>)
    const row = (await screen.findByText('Frankfurt')).closest('.srv-row')
    expect(row?.textContent).toContain('133.200.223.98')
    expect(row?.textContent).toContain('2404:7a80:df62:8604::fde')
    fireEvent.click(screen.getByRole('button', { name: '隐藏服务器地址' }))
    expect(row?.textContent).toContain('133.*.*.98')
    expect(row?.textContent).toContain('2404:*:*:8604::fde')
    expect(row?.textContent).not.toContain('133.200.223.98')
    expect(row?.textContent).not.toContain('2404:7a80:df62:8604::fde')
    fireEvent.click(screen.getByRole('button', { name: '显示服务器地址' }))
  })

  it('shows both real addresses even when an export domain is configured', async () => {
    vi.mocked(getServersMeta).mockResolvedValue(metadata([{
      ...server, address: 'node.example.com', public_ip: '133.200.223.98', public_ipv6: '2404:7a80:df62:8604::fde',
    }]))
    render(<MemoryRouter><Servers /></MemoryRouter>)
    const row = (await screen.findByText('Frankfurt')).closest('.srv-row')
    expect(row?.textContent).toContain('133.200.223.98')
    expect(row?.textContent).toContain('2404:7a80:df62:8604::fde')
    expect(row?.textContent).not.toContain('node.example.com')
  })

  it('offers deletion directly without a duplicate sort menu', async () => {
    render(<MemoryRouter><Servers /></MemoryRouter>)
    await screen.findByText('Frankfurt')
    expect(screen.queryByRole('button', { name: 'Frankfurt 更多操作' })).toBeNull()
    expect(screen.getByRole('button', { name: '删除 Frankfurt' })).toBeTruthy()
    expect(screen.queryByText('上移')).toBeNull()
    expect(screen.queryByText('下移')).toBeNull()
  })
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

  it('keeps server actions available without repeating the identity heading', () => {
    const props = {
      busy: '',
      onInstallSingbox: vi.fn(), onUninstallSingbox: vi.fn(),
      onInstallAgent: vi.fn(), onUninstallAgent: vi.fn(), onImport: vi.fn(),
      onEditConfig: vi.fn(), onRestart: vi.fn(), onExport: vi.fn(),
    }
    render(<ServerOverviewCard server={server} {...props} />)
    expect(screen.queryByText('Backup exit')).toBeNull()
    expect(screen.getByRole('button', { name: '导出节点' })).toBeTruthy()
    expect(screen.queryByText('导出订阅')).toBeNull()
  })
})
