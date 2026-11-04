import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createCustomNode, listCustomNodeSubscriptions, updateCustomNode } from '../../../api'
import type { CustomNode } from '../../../api'
import { CustomNodesPanel } from './Access'

vi.mock('../../../api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../../api')>(),
  createCustomNode: vi.fn(),
  updateCustomNode: vi.fn(),
  listCustomNodeSubscriptions: vi.fn(),
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
