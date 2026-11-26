import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import InboundForm from './InboundForm'
import { createInbound, updateInbound } from '../../../lib/api'
import { DEFAULT_INBOUND_SCHEMA, validateObjectBySchema } from '../../../lib/singboxSchema'
import type { Inbound } from '../../../lib/types'

vi.mock('../../../lib/api', () => ({
  createInbound: vi.fn(),
  updateInbound: vi.fn(),
  errMsg: (error: Error) => error.message,
}))
vi.mock('../../../lib/configApply', () => ({
  applyWithToast: vi.fn(async (_key: string, action: () => Promise<unknown>) => {
    await action()
    return true
  }),
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
  vi.mocked(createInbound).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof createInbound>>)
  vi.mocked(updateInbound).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof updateInbound>>)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  if (originalScrollIntoView) Object.defineProperty(Element.prototype, 'scrollIntoView', originalScrollIntoView)
  else Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
})

describe('InboundForm JSON Schema integration', () => {
  it('preserves complex raw fields (grpc, tls.min_version, multiplex, tcp_fast_open) when editing in Schema 表单 mode', async () => {
    const complexInbound: Inbound = {
      id: 9,
      server_id: 1,
      type: 'vless',
      tag: 'vless-grpc',
      listen_port: 8443,
      enabled: false,
      remark: '',
      created_at: '2026-11-18T00:00:00Z',
      updated_at: '2026-11-18T00:00:00Z',
      settings: {
        uuid: '11111111-1111-1111-1111-111111111111',
        single_user: true,
        transport: { type: 'grpc' },
        tls: { enabled: true, server_name: 'edge.example.com', certificate_path: '/etc/ssl/cert.pem', key_path: '/etc/ssl/key.pem' },
        raw: {
          type: 'vless',
          tag: 'vless-grpc',
          listen: '::',
          listen_port: 8443,
          tcp_fast_open: true,
          transport: { type: 'grpc', service_name: 'GunService', idle_timeout: '15s' },
          tls: { enabled: true, server_name: 'edge.example.com', min_version: '1.3' },
          multiplex: { enabled: true, padding: true },
        },
      },
    }

    render(<InboundForm serverId={1} inbound={complexInbound} open onClose={vi.fn()} onSaved={vi.fn()} />)

    // gRPC schema field and flat populated advanced fields render directly without folding
    expect(await screen.findByDisplayValue('GunService')).toBeTruthy()
    expect(screen.queryByText('其他参数')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(updateInbound).toHaveBeenCalledOnce())
    const payload = vi.mocked(updateInbound).mock.calls[0][2]
    expect(payload).not.toHaveProperty('enabled')
    expect(payload.settings?.raw).toMatchObject({
      tcp_fast_open: true,
      transport: { type: 'grpc', service_name: 'GunService', idle_timeout: '15s' },
      tls: { min_version: '1.3' },
      multiplex: { enabled: true, padding: true },
    })
  })

  it('supports switching to JSON 编辑 mode to create a tun inbound and rejects schema violations', async () => {
    render(<InboundForm serverId={1} inbound={null} open onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.click(screen.getByRole('radio', { name: 'JSON' }))
    const editor = await screen.findByLabelText('inbound.json')

    // Invalid schema: wrong mtu type on tun
    fireEvent.change(editor, { target: { value: JSON.stringify({ type: 'tun', mtu: 'bad-mtu' }) } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(await screen.findByText(/必须是数字/)).toBeTruthy()
    expect(createInbound).not.toHaveBeenCalled()

    // Valid tun config with custom sing-box parameters
    fireEvent.change(editor, {
      target: {
        value: JSON.stringify({
          type: 'tun',
          tag: 'tun-in',
          interface_name: 'tun0',
          address: ['172.19.0.1/30'],
          mtu: 9000,
          auto_route: true,
          strict_route: true,
          stack: 'mixed',
          sniff: true,
        }),
      },
    })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(createInbound).toHaveBeenCalledOnce())
    const body = vi.mocked(createInbound).mock.calls[0][1]
    expect(body.type).toBe('tun')
    expect(body.tag).toBe('tun-in')
    expect(body.listen_port).toBe(0)
    expect(body.enabled).toBe(true)
    expect(body.settings?.raw).toMatchObject({
      type: 'tun',
      tag: 'tun-in',
      interface_name: 'tun0',
      mtu: 9000,
      auto_route: true,
      stack: 'mixed',
    })
  })

  it('omits unpopulated advanced fields from new inbounds and keeps Snell flat', async () => {
    const { rerender } = render(<InboundForm serverId={1} inbound={null} open onClose={vi.fn()} onSaved={vi.fn()} />)

    // Fresh shadowsocks creation exposes only core fields without 其他参数 or 认证 header bloat
    expect(screen.queryByText('认证')).toBeNull()
    expect(screen.queryByText('其他参数')).toBeNull()
    expect(screen.queryByText('监听地址')).toBeNull()
    expect(screen.queryByText(/legacy/i)).toBeNull()

    // Fresh VLESS creation exposes common REALITY/Flow fields without TLS version or kernel bloat
    fireEvent.click(screen.getByRole('combobox', { name: /协议/ }))
    fireEvent.click(await screen.findByText('vless'))
    expect(screen.getByLabelText('Flow')).toBeTruthy()
    expect(screen.getByLabelText(/^握手域名/)).toBeTruthy()
    expect(screen.queryByText('最低 TLS 版本')).toBeNull()
    expect(screen.queryByText('TCP Fast Open')).toBeNull()
    expect(screen.queryByText('多路复用')).toBeNull()

    // Snell renders PSK, 版本, 混淆 directly without folding into 其他参数
    const snellInbound: Inbound = {
      id: 3, server_id: 1, type: 'snell', tag: 'snell-direct', listen_port: 10923, enabled: true, remark: '',
      created_at: '2026-11-18T00:00:00Z', updated_at: '2026-11-18T00:00:00Z',
      settings: { snell_version: 5, snell_psk: 'secret-psk', snell_obfs_mode: 'http' },
    }
    rerender(<InboundForm serverId={1} inbound={snellInbound} open onClose={vi.fn()} onSaved={vi.fn()} />)
    expect(screen.getByDisplayValue('secret-psk')).toBeTruthy()
    expect(screen.getByLabelText('版本')).toBeTruthy()
    expect(screen.getByLabelText('混淆')).toBeTruthy()
    expect(screen.queryByText('其他参数')).toBeNull()
  })

  it('validates inbound schema constraints locally', () => {
    expect(validateObjectBySchema(DEFAULT_INBOUND_SCHEMA, { tag: 'x' })).toContain('缺少必填字段 type')
    expect(validateObjectBySchema(DEFAULT_INBOUND_SCHEMA, { type: 'mixed', listen_port: 70000 })).toContain('不能大于 65535')
    expect(validateObjectBySchema(DEFAULT_INBOUND_SCHEMA, { type: 'mixed', listen_port: 1080, set_system_proxy: false })).toBeNull()
  })

  it('keeps subscription address and port separate from the native inbound across JSON mode', async () => {
    render(<InboundForm serverId={1} inbound={null} open onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('连接地址'), { target: { value: '  edge.example.com  ' } })
    fireEvent.change(screen.getByLabelText('连接端口'), { target: { value: '443' } })
    fireEvent.change(screen.getByLabelText('监听端口'), { target: { value: '1443' } })

    // Switching to JSON shows native sing-box listen_port without panel-only client_port.
    fireEvent.click(screen.getByRole('radio', { name: 'JSON' }))
    const editor = screen.getByLabelText('inbound.json') as HTMLTextAreaElement
    const native = JSON.parse(editor.value)
    expect(native.listen_port).toBe(1443)
    expect(native.client_port).toBeUndefined()
    expect(native.server).toBeUndefined()
    fireEvent.change(editor, { target: { value: JSON.stringify({ ...native, listen_port: 2443 }) } })

    // Returning to form preserves client_port=443.
    fireEvent.click(screen.getByRole('radio', { name: 'Form' }))
    expect((screen.getByLabelText('连接端口') as HTMLInputElement).value).toBe('443')
    expect((screen.getByLabelText('监听端口') as HTMLInputElement).value).toBe('2443')
    expect((screen.getByLabelText('连接地址') as HTMLInputElement).value).toBe('edge.example.com')

    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(createInbound).toHaveBeenCalledOnce())
    const body = vi.mocked(createInbound).mock.calls[0][1]
    expect(body.listen_port).toBe(2443)
    expect(body.settings?.client_port).toBe(443)
    expect(body.settings?.server).toBe('edge.example.com')
    expect(body.settings?.raw?.server).toBeUndefined()
  })

  it('preloads the subscription address and saves a cleared override from JSON mode', async () => {
    const existing: Inbound = {
      id: 8, server_id: 1, tag: 'socks-in', type: 'socks', listen_port: 1080, enabled: true,
      remark: '', created_at: '', updated_at: '', settings: { server: 'edge.example.com' },
    }
    render(<InboundForm serverId={1} inbound={existing} open onClose={vi.fn()} onSaved={vi.fn()} />)

    const server = screen.getByLabelText<HTMLInputElement>('连接地址')
    expect(server.value).toBe('edge.example.com')
    fireEvent.change(server, { target: { value: '' } })
    fireEvent.click(screen.getByRole('radio', { name: 'JSON' }))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(updateInbound).toHaveBeenCalledOnce())
    expect(updateInbound).toHaveBeenCalledWith(1, 8, { settings: { server: '' } })
  })

  it.each([true, false])('saves listener edits and only sends a changed subscription address (%s)', async (changeAddress) => {
    const existing: Inbound = {
      id: 8, server_id: 1, tag: 'socks-in', type: 'socks', listen_port: 1080, enabled: true,
      remark: '', created_at: '', updated_at: '', settings: { server: 'edge.example.com' },
    }
    render(<InboundForm serverId={1} inbound={existing} open onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('监听端口'), { target: { value: '2080' } })
    if (changeAddress) fireEvent.change(screen.getByLabelText('连接地址'), { target: { value: 'new.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(updateInbound).toHaveBeenCalledOnce())
    const body = vi.mocked(updateInbound).mock.calls[0][2]
    expect(body.listen_port).toBe(2080)
    expect(body.settings?.server).toBe(changeAddress ? 'new.example.com' : undefined)
  })

  it('preserves VLESS packet_encoding=none across JSON mode and blocks mutating type on existing inbound', async () => {
    const existing: Inbound = {
      id: 7,
      server_id: 1,
      tag: 'vless-in',
      type: 'vless',
      listen_port: 443,
      enabled: false,
      remark: '',
      created_at: '',
      updated_at: '',
      settings: {
        packet_encoding: 'none',
        tls: { enabled: true, server_name: 'example.com', certificate_path: '/c.pem', key_path: '/k.pem' },
      },
    }

    render(<InboundForm serverId={1} inbound={existing} open onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.click(screen.getByRole('radio', { name: 'JSON' }))
    const editor = screen.getByLabelText('inbound.json') as HTMLTextAreaElement

    // Attempting to change type on an existing inbound must be blocked
    const mutated = { ...JSON.parse(editor.value), type: 'trojan' }
    fireEvent.change(editor, { target: { value: JSON.stringify(mutated) } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(await screen.findByText(/不能修改协议类型/)).toBeTruthy()
    expect(updateInbound).not.toHaveBeenCalled()

    // Restoring type=vless and saving in JSON mode must preserve packet_encoding="none"
    const valid = { ...JSON.parse(editor.value), type: 'vless' }
    fireEvent.change(editor, { target: { value: JSON.stringify(valid) } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(updateInbound).toHaveBeenCalledOnce())
    const payload = vi.mocked(updateInbound).mock.calls[0][2]
    expect(payload).not.toHaveProperty('enabled')
    expect(payload.settings?.packet_encoding).toBe('none')
  })
})
