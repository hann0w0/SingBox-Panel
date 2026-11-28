import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { OutboundForm } from './RoutingForms'
import { createOutbound, updateOutbound } from '../../../lib/api'
import { DEFAULT_OUTBOUND_SCHEMA, validateObjectBySchema } from '../../../lib/singboxSchema'
import type { Outbound } from '../../../lib/types'

vi.mock('../../../lib/api', () => ({
  createOutbound: vi.fn(),
  updateOutbound: vi.fn(),
  createRule: vi.fn(),
  updateRule: vi.fn(),
  listSharedRuleSets: vi.fn(async () => []),
  errMsg: (error: Error) => error.message,
}))
vi.mock('../../../lib/configApply', () => ({
  applyWithToast: vi.fn(async (_key: string, action: () => Promise<unknown>) => {
    await action()
    return true
  }),
  showConfigApplyResult: vi.fn(),
}))

const originalScrollIntoView = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollIntoView')

beforeEach(() => {
  vi.stubGlobal('matchMedia', () => ({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, writable: true, value: vi.fn() })
  vi.mocked(createOutbound).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof createOutbound>>)
  vi.mocked(updateOutbound).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof updateOutbound>>)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  if (originalScrollIntoView) Object.defineProperty(Element.prototype, 'scrollIntoView', originalScrollIntoView)
  else Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
})

describe('OutboundForm JSON Schema integration', () => {
  it('preserves dial fields, grpc transport, and multiplex when editing in Schema 表单 mode', async () => {
    const complexOutbound: Outbound = {
      id: 7,
      server_id: 1,
      tag: 'relay-grpc',
      type: 'vless',
      remark: '',
      sort: 0,
      settings: {
        server: 'relay.example.com',
        server_port: 443,
        uuid: '22222222-3333-4444-5555-666666666666',
        settings: {
          transport: { type: 'grpc' },
          tls: { enabled: true, server_name: 'relay.example.com', fingerprint: 'chrome' },
          raw: {
            type: 'vless',
            tag: 'relay-grpc',
            server: 'relay.example.com',
            server_port: 443,
            detour: 'upstream',
            connect_timeout: '5s',
            tcp_fast_open: true,
            transport: { type: 'grpc', service_name: 'RelayGun' },
            multiplex: { enabled: true, protocol: 'h2mux', max_connections: 4 },
            tls: { enabled: true, server_name: 'relay.example.com', min_version: '1.3' },
          },
        },
      },
    }

    render(<OutboundForm serverId={1} outbound={complexOutbound} open onClose={vi.fn()} onSaved={vi.fn()} />)

    expect(await screen.findByDisplayValue('RelayGun')).toBeTruthy()
    expect(screen.getByDisplayValue('5s')).toBeTruthy()
    expect(screen.queryByRole('heading', { level: 3 })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(updateOutbound).toHaveBeenCalledOnce())
    const body = vi.mocked(updateOutbound).mock.calls[0][2]
    expect(body.settings?.settings?.raw).toMatchObject({
      detour: 'upstream',
      connect_timeout: '5s',
      tcp_fast_open: true,
      transport: { type: 'grpc', service_name: 'RelayGun' },
      multiplex: { enabled: true, protocol: 'h2mux', max_connections: 4 },
      tls: { min_version: '1.3' },
    })
  })

  it('omits JSON mode toggle and 认证 heading while keeping source tabs and saving manual http outbound', async () => {
    render(<OutboundForm serverId={1} outbound={null} open onClose={vi.fn()} onSaved={vi.fn()} />)

    expect(screen.getByRole('tab', { name: '手动配置' })).toBeTruthy()
    expect(screen.getByRole('tab', { name: '面板节点' })).toBeTruthy()
    expect(screen.getByRole('tab', { name: '外部节点' })).toBeTruthy()
    expect(screen.getByRole('tab', { name: 'URI 导入' })).toBeTruthy()
    expect(screen.queryByRole('radiogroup', { name: '出站编辑模式' })).toBeNull()
    expect(screen.queryByText('认证')).toBeNull()
    expect(screen.queryByRole('heading', { level: 3 })).toBeNull()
    expect(screen.queryByText('其他参数')).toBeNull()
    expect(screen.queryByRole('combobox', { name: '添加出站参数' })).toBeNull()

    const typeField = screen.getByRole('combobox', { name: /^协议/ })
    const portField = screen.getByLabelText(/^端口/)
    const serverField = screen.getByLabelText(/^地址/)
    const tagField = screen.getByLabelText(/^标签/)
    expect(typeField.compareDocumentPosition(portField) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(portField.compareDocumentPosition(serverField) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(serverField.compareDocumentPosition(tagField) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    fireEvent.click(typeField)
    fireEvent.click(await screen.findByText('http'))
    fireEvent.change(portField, { target: { value: '8443' } })
    fireEvent.change(serverField, { target: { value: 'proxy.example.com' } })
    fireEvent.change(tagField, { target: { value: 'corp-http' } })
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(createOutbound).toHaveBeenCalledOnce())
    const body = vi.mocked(createOutbound).mock.calls[0][1]
    expect(body.type).toBe('http')
    expect(body.tag).toBe('corp-http')
    expect(body.settings).toMatchObject({
      server: 'proxy.example.com',
      server_port: 8443,
      username: 'alice',
      password: 'secret',
    })
  })

  it('validates outbound schema constraints locally', () => {
    expect(validateObjectBySchema(DEFAULT_OUTBOUND_SCHEMA, { server: 'a.com' })).toContain('缺少必填字段 type')
    expect(validateObjectBySchema(DEFAULT_OUTBOUND_SCHEMA, { type: 'http', server: 'a.com', server_port: 70000 })).toContain('不能大于 65535')
    expect(validateObjectBySchema(DEFAULT_OUTBOUND_SCHEMA, { type: 'http', server: 'a.com', server_port: 8080, connect_timeout: '5s' })).toBeNull()
  })
})
