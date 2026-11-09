import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createCustomNode, listCustomNodeSubscriptions, syncCustomNodeSubscription, updateCustomNode } from '../../../api'
import type { CustomNode, CustomNodeSubscription } from '../../../api'
import { message } from '../../../ui'
import { CustomNodesPanel } from './Access'

vi.mock('../../../api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../../api')>(),
  createCustomNode: vi.fn(),
  updateCustomNode: vi.fn(),
  listCustomNodeSubscriptions: vi.fn(),
  syncCustomNodeSubscription: vi.fn(),
}))

const node: CustomNode = {
  id: 1, name: 'Imported HTTP', group: '', link: '', protocol: 'http',
  address: 'proxy.example.com', port: 8443, enabled: true, sort_order: 0,
  all_users: false, user_ids: [2], excluded_user_ids: [], params: {},
}
const originalScrollIntoView = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollIntoView')

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('matchMedia', () => ({ matches: true, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, writable: true, value: vi.fn() })
  vi.mocked(listCustomNodeSubscriptions).mockResolvedValue([])
  vi.mocked(createCustomNode).mockResolvedValue(node)
  vi.mocked(updateCustomNode).mockResolvedValue(node)
})

describe('subscription sync feedback', () => {
  it('shows partial synchronization as a warning', async () => {
    const source: CustomNodeSubscription = {
      id: 1, name: 'Partial source', url: 'https://provider.example.com/sub', group: '',
      enabled: true, auto_update: true, update_interval_minutes: 60, base_sort_order: 0,
      name_rewrite_rules: [], node_count: 28, last_error: '有 1 个节点解析失败，保留 0 个仍在源中的旧节点',
      last_sync_status: 'partial', source_type: 'clash-yaml', created_at: '', updated_at: '',
    }
    vi.mocked(listCustomNodeSubscriptions).mockResolvedValue([source])
    render(<CustomNodesPanel nodes={[]} loading={false} onNodesChange={vi.fn()} />)
    const label = await screen.findByText('部分成功')
    expect(label.className).toContain('is-warn')
    expect(screen.queryByText('同步失败')).toBeNull()
    expect(label.closest('[title]')?.getAttribute('title')).toBe(source.last_error)
  })

  it.each([
    { status: 'success', error: '', lastSync: '2026-11-04T00:00:00Z', label: '正常', tone: 'is-ok' },
    { status: 'failed', error: '订阅下载失败', lastSync: '2026-11-04T00:00:00Z', label: '同步失败', tone: 'is-error' },
    { status: '', error: '旧版本错误', lastSync: '2026-11-04T00:00:00Z', label: '同步失败', tone: 'is-error' },
    { status: '', error: '', lastSync: '2026-11-04T00:00:00Z', label: '正常', tone: 'is-ok' },
    { status: '', error: '', lastSync: null, label: '待同步', tone: 'is-off' },
  ] as const)('shows $label for status=$status with legacy compatibility', async ({ status, error, lastSync, label, tone }) => {
    const source: CustomNodeSubscription = {
      id: 1, name: 'Source', url: 'https://provider.example.com/sub', group: '',
      enabled: true, auto_update: true, update_interval_minutes: 60, base_sort_order: 0,
      name_rewrite_rules: [], node_count: 28, last_error: error, last_sync_status: status,
      last_sync_at: lastSync, source_type: 'clash-yaml', created_at: '', updated_at: '',
    }
    vi.mocked(listCustomNodeSubscriptions).mockResolvedValue([source])
    render(<CustomNodesPanel nodes={[]} loading={false} onNodesChange={vi.fn()} />)
    expect((await screen.findByText(label)).className).toContain(tone)
  })

  it.each([0, 1])('reports skipped=%s without showing a false success', async (skipped) => {
    const source: CustomNodeSubscription = {
      id: 1, name: 'Source', url: 'https://provider.example.com/sub', group: '',
      enabled: true, auto_update: true, update_interval_minutes: 60, base_sort_order: 0,
      name_rewrite_rules: [], node_count: 81, last_error: '', source_type: 'clash-yaml',
      created_at: '', updated_at: '',
    }
    vi.mocked(listCustomNodeSubscriptions).mockResolvedValue([source])
    const last_error = skipped ? '有 1 个节点解析失败，保留 1 个仍在源中的旧节点' : ''
    vi.mocked(syncCustomNodeSubscription).mockResolvedValue({
      subscription: { ...source, node_count: 29, last_error },
      sync: { created: 0, updated: 28, deleted: 52, total: 29, skipped, filtered: 0 },
    })
    const warning = vi.spyOn(message, 'warning')
    const success = vi.spyOn(message, 'success')
    render(<CustomNodesPanel nodes={[]} loading={false} onNodesChange={vi.fn()} />)
    fireEvent.click(await screen.findByRole('button', { name: '同步 Source' }))
    await waitFor(() => expect(syncCustomNodeSubscription).toHaveBeenCalledWith(1))
    if (skipped) {
      await waitFor(() => expect(warning).toHaveBeenCalledWith(expect.stringContaining('部分同步完成：当前 29 个节点')))
      expect(warning).toHaveBeenCalledWith(expect.stringContaining(last_error))
      expect(success).not.toHaveBeenCalled()
    } else {
      await waitFor(() => expect(success).toHaveBeenCalledWith(expect.stringContaining('同步完成：当前 29 个节点')))
      expect(warning).not.toHaveBeenCalled()
    }
  })
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  if (originalScrollIntoView) Object.defineProperty(Element.prototype, 'scrollIntoView', originalScrollIntoView)
  else Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
})

async function showNodes(nodes: CustomNode[] = []) {
  render(<CustomNodesPanel nodes={nodes} loading={false} onNodesChange={vi.fn()} />)
  await waitFor(() => expect(listCustomNodeSubscriptions).toHaveBeenCalledOnce())
  fireEvent.click(screen.getByRole('tab', { name: /^节点/ }))
}

async function select(label: string | RegExp, option: string) {
  await act(async () => { fireEvent.click(screen.getByLabelText(label)) })
  await act(async () => { fireEvent.click(screen.getByRole('option', { name: option })) })
}

async function create(protocol: string) {
  await showNodes()
  fireEvent.click(screen.getByRole('button', { name: '新增节点' }))
  await select(/^协议/, protocol)
  await act(async () => {
    fireEvent.change(screen.getByLabelText('地址'), { target: { value: 'proxy.example.com' } })
    fireEvent.change(screen.getByLabelText('端口'), { target: { value: '8443' } })
  })
}

async function saveCreate() {
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(createCustomNode).toHaveBeenCalledOnce())
  return vi.mocked(createCustomNode).mock.calls[0][0]
}

describe('external HTTP and SOCKS proxy forms', () => {
  it('defaults HTTP to plaintext and permits a username with an empty password', async () => {
    await create('HTTP / HTTPS')
    expect(screen.getByLabelText('TLS 模式').textContent).toContain('无')
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'literal-user' } })
    expect(await saveCreate()).toMatchObject({ protocol: 'http', params: { username: 'literal-user', tls: 'none' } })
  })

  it('submits HTTPS authentication, TLS, ALPN and fingerprint settings', async () => {
    await create('HTTP / HTTPS')
    await select('TLS 模式', 'TLS')
    expect(screen.getByLabelText('TLS 模式').textContent).toContain('TLS')
    fireEvent.click(await screen.findByLabelText('跳过证书校验'))
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'user' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'secret' } })
    fireEvent.change(screen.getByLabelText('SNI'), { target: { value: 'tls.example.com' } })
    fireEvent.change(screen.getByLabelText('ALPN'), { target: { value: 'http/1.1' } })
    await select('uTLS 指纹', 'firefox')
    expect(await saveCreate()).toMatchObject({
      protocol: 'http', params: { username: 'user', password: 'secret', tls: 'tls', sni: 'tls.example.com', alpn: 'http/1.1', fingerprint: 'firefox', insecure: true },
    })
  })

  it('offers SOCKS5 with authentication and keeps TLS fields out of its payload', async () => {
    await create('SOCKS5')
    expect(screen.queryByLabelText('TLS 模式')).toBeNull()
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'user' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'secret' } })
    expect((await saveCreate()).params).toEqual({ username: 'user', password: 'secret' })
  })

  it.each([false, true])('preserves imported HTTP TLS=%s and hidden request fields on save', async (secure) => {
    const params = {
      username: 'literal-user', ...(secure ? { tls: 'tls', sni: 'tls.example.com', alpn: 'h2,http/1.1', fingerprint: 'firefox', insecure: true } : {}),
      http_path: '/connect', http_headers: { 'X-Test': ['one', 'two'] },
    }
    await showNodes([{ ...node, params }])
    fireEvent.click(screen.getByRole('button', { name: '编辑 Imported HTTP' }))
    expect(screen.getByLabelText('TLS 模式').textContent).toContain(secure ? 'TLS' : '无')
    expect(screen.queryByLabelText('HTTP Path')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(updateCustomNode).toHaveBeenCalledOnce())
    expect(vi.mocked(updateCustomNode).mock.calls[0]).toEqual([1, expect.objectContaining({
      protocol: 'http', user_ids: [2], params: expect.objectContaining({ ...params, tls: secure ? 'tls' : 'none' }),
    })])
  })
})
